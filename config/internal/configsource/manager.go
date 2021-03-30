// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package configsource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/viper"

	"go.opentelemetry.io/collector/config"
	"go.opentelemetry.io/collector/consumer/consumererror"
)

// Manager is used to inject data from config sources into a configuration and also
// to monitor for updates on the items injected into the configuration.  All methods
// of a Manager must be called only once and have a expected sequence:
//
// 1. NewManager to create a new instance;
// 2. Resolve to inject the data from config sources into a configuration;
// 3. WatchForUpdate in a goroutine to wait for configuration updates;
// 4. WaitForWatcher to wait until the watchers are in place;
// 5. Close to close the instance;
//
// The current syntax to reference a config source in a YAML is provisional. Currently
// only now-wrapped single line is supported:
//
//    $<cfgSrcName>:<selector>[?<params>]
//
// The <cfgSrcName> is a name string used to indentify the config source instance to be used
// to retrieve the value.
//
// The <selector> is the mandatory parameter required when retrieving data from a config source.
//
// <params> is an optional parameter in syntax similar to single line YAML but removing the spaces
// to ensure that it is a string value. Not all config sources need these params, they are used to
// provide extra control when retrieving the data.
//
// Assuming a config source named "env" that retrieve environment variables and one named "file" that
// retrieves contents from individual files, here are some examples:
//
//    component:
//      # Retrieves the value of the environment variable LOGS_DIR.
//      logs_dir: $env:LOGS_DIR
//
//      # Retrieves the value from the file /etc/secret.bin and injects its contents as a []byte.
//      bytes_from_file: $file:/etc/secret.bin?{binary:true}
//
//      # Retrieves the value from the file /etc/text.txt and injects its contents as a string.
//      # Hypothetically the "file" config source by default tries to inject the file contents
//      # as a string if params doesn't specify that "binary" is true.
//      text_from_file: $file:/etc/text.txt
//
type Manager struct {
	// configSources is map from ConfigSource names (as defined in the configuration)
	// and the respective instances.
	configSources map[string]ConfigSource
	// sessions track all the Session objects used to retrieve values to be injected
	// into the configuration.
	sessions map[string]Session
	// watchers keeps track of all WatchForUpdate functions for retrieved values.
	watchers []func() error
	// watchersWG is used to ensure that Close waits for all WatchForUpdate calls
	// to complete.
	watchersWG sync.WaitGroup
	// watchingCh is used to notify users of the Manager that the WatchForUpdate function
	// is ready and waiting for notifications.
	watchingCh chan struct{}
	// closeCh is used to notify the Manager WatchForUpdate function that the manager
	// is being closed.
	closeCh chan struct{}
}

// NewManager creates a new instance of a Manager to be used to inject data from
// ConfigSource objects into a configuration and watch for updates on the injected
// data.
func NewManager(*config.Parser) (*Manager, error) {
	// TODO: Config sources should be extracted for the config itself, need Factories for that.

	return &Manager{
		// TODO: Temporarily tests should set their config sources per their needs.
		sessions:   make(map[string]Session),
		watchingCh: make(chan struct{}),
		closeCh:    make(chan struct{}),
	}, nil
}

// Resolve inspects the given config.Parser and resolves all config sources referenced
// in the configuration, returning a config.Parser fully resolved. This must be called only
// once per lifetime of a Manager object.
func (m *Manager) Resolve(ctx context.Context, parser *config.Parser) (*config.Parser, error) {
	res := config.NewParser()
	allKeys := parser.AllKeys()
	for _, k := range allKeys {
		value, err := m.expandStringValues(ctx, parser.Get(k))
		if err != nil {
			// Call RetrieveEnd for all sessions used so far but don't record any errors.
			_ = m.retrieveEndAllSessions(ctx)
			return nil, err
		}
		res.Set(k, value)
	}

	if errs := m.retrieveEndAllSessions(ctx); len(errs) > 0 {
		return nil, consumererror.Combine(errs)
	}

	return res, nil
}

// WatchForUpdate must watch for updates on any of the values retrieved from config sources
// and injected into the configuration. Typically this method is launched in a goroutine, the
// method WaitForWatcher blocks until the WatchForUpdate goroutine is running and ready.
func (m *Manager) WatchForUpdate() error {
	// Use a channel to capture the first error returned by any watcher and another one
	// to ensure completion of any remaining watcher also trying to report an error.
	errChannel := make(chan error, 1)
	doneCh := make(chan struct{})
	defer close(doneCh)

	for _, watcher := range m.watchers {
		m.watchersWG.Add(1)
		watcherFn := watcher
		go func() {
			defer m.watchersWG.Done()

			err := watcherFn()
			switch {
			case errors.Is(err, ErrWatcherNotSupported):
				// The watcher for the retrieved value is not supported, nothing to
				// do, just exit from the goroutine.
				return
			case errors.Is(err, ErrSessionClosed):
				// The Session from which this watcher was retrieved is being closed.
				// There is no error to report, just exit from the goroutine.
				return
			default:
				select {
				case errChannel <- err:
					// Try to report any other error.
				case <-doneCh:
					// There was either one error published or the watcher was closed.
					// This channel was closed and any goroutines waiting on these
					// should simply close.
				}
			}
		}()
	}

	// All goroutines were created, they may not be running yet, but the manager WatchForUpdate
	// is only waiting for any of the watchers to terminate.
	close(m.watchingCh)

	select {
	case err := <-errChannel:
		// Return the first error that reaches the channel and ignore any other error.
		return err
	case <-m.closeCh:
		// This covers the case that all watchers returned ErrWatcherNotSupported.
		return ErrSessionClosed
	}
}

// WaitForWatcher blocks until the watchers used by WatchForUpdate are all ready.
// This is used to ensure that the watchers are in place before proceeding.
func (m *Manager) WaitForWatcher() {
	<-m.watchingCh
}

// Close terminates the WatchForUpdate function and closes all Session objects used
// in the configuration. It should be called
func (m *Manager) Close(ctx context.Context) error {
	var errs []error
	for _, session := range m.sessions {
		if err := session.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	close(m.closeCh)
	m.watchersWG.Wait()

	return consumererror.Combine(errs)
}

func (m *Manager) retrieveEndAllSessions(ctx context.Context) []error {
	var errs []error
	for _, session := range m.sessions {
		if err := session.RetrieveEnd(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func (m *Manager) expandStringValues(ctx context.Context, value interface{}) (interface{}, error) {
	switch v := value.(type) {
	case string:
		return m.expandConfigSources(ctx, v)
	case []interface{}:
		nslice := make([]interface{}, 0, len(v))
		for _, vint := range v {
			value, err := m.expandStringValues(ctx, vint)
			if err != nil {
				return nil, err
			}
			nslice = append(nslice, value)
		}
		return nslice, nil
	case map[interface{}]interface{}:
		nmap := make(map[interface{}]interface{}, len(v))
		for k, vint := range v {
			value, err := m.expandStringValues(ctx, vint)
			if err != nil {
				return nil, err
			}
			nmap[k] = value
		}
		return nmap, nil
	default:
		return v, nil
	}
}

// expandConfigSources retrieve data from the specified config sources and injects them into
// the configuration. The Manager tracks sessions and watcher objects as needed.
func (m *Manager) expandConfigSources(ctx context.Context, s string) (interface{}, error) {
	// Provisional implementation: only strings prefixed with the first character '$'
	// are checked for config sources.
	//
	// TODO: Handle different ways to express config sources:
	//
	// 1. Concatenated with other strings (needs delimiter syntax);
	// 2. Using spaces in its declaration;
	// 3. Multiline;
	//
	if len(s) == 0 || s[0] != '$' {
		// TODO: handle escaped $.
		return s, nil
	}

	cfgSrcName, selector, params, err := parseCfgSrc(s[1:])
	if err != nil {
		return nil, err
	}

	session, ok := m.sessions[cfgSrcName]
	if !ok {
		// The session for this config source was not created yet.
		cfgSrc, ok := m.configSources[cfgSrcName]
		if !ok {
			return nil, fmt.Errorf("config source %q not found", cfgSrcName)
		}

		session, err = cfgSrc.NewSession(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create session for config source %q: %w", cfgSrcName, err)
		}
		m.sessions[cfgSrcName] = session
	}

	retrieved, err := session.Retrieve(ctx, selector, params)
	if err != nil {
		return nil, fmt.Errorf("config source %q failed to retrieve value: %w", cfgSrcName, err)
	}

	m.watchers = append(m.watchers, retrieved.WatchForUpdate)

	return retrieved.Value(), nil
}

// parseCfgSrc extracts the reference to a config source from a string value.
// The current syntax is provisional: <cfgSrcName>:<selector>[?<params>]
// The caller should check for error explicitly since it is possible for the
// other values to have been partially set.
// TODO: Improve parameter resolution.
func parseCfgSrc(s string) (cfgSrcName, selector string, params interface{}, err error) {
	const cfgSrcDelim string = ":"
	parts := strings.SplitN(s, cfgSrcDelim, 2)
	if len(parts) != 2 {
		err = fmt.Errorf("inproper config source syntax at %q, it must have at least the config source name and a selector", s)
		return
	}
	cfgSrcName = strings.Trim(parts[0], " ")

	const selectorDelim string = "?"
	parts = strings.SplitN(parts[1], selectorDelim, 2)
	selector = strings.Trim(parts[0], " ")

	if len(parts) == 2 {
		// There are parameters, for now simply transform it to an YAML and parse it
		params, err = parseParams(parts[1])
		if err != nil {
			err = fmt.Errorf("invalid parameters syntax at %q: %w", s, err)
			return
		}
	}

	return cfgSrcName, selector, params, err
}

func parseParams(s string) (interface{}, error) {
	// Build a single-line valid yaml text to be parsed.
	// The delimiter chars are all ASCII '{', '}', ',', ':', looping over string
	//  as bytes is fine.
	yamlBuf := make([]byte, 0, 2*len(s))
	yamlBuf = append(yamlBuf, []byte("params: ")...)

	for i := 0; i < len(s); i++ {
		yamlBuf = append(yamlBuf, s[i])
		if yamlDelimiter(s[i]) {
			// Make it a legal YAML by adding a single space after the delimiters.
			yamlBuf = append(yamlBuf, ' ')
		}
	}

	// yamlBuf now it is a single line representing the params, parse it.
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(bytes.NewReader(yamlBuf)); err != nil {
		return nil, err
	}

	return v.Get("params"), nil
}

func yamlDelimiter(c byte) bool {
	return c == '{' || c == '}' || c == ':' || c == ','
}
