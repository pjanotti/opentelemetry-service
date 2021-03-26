package configsource

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/vault/api"
	"go.uber.org/zap"
)

type vaultSession struct {
	logger *zap.Logger

	client *api.Client
	secret *api.Secret
	path   string

	watcherFn         WatchForUpdate
	numWatchers       int
	coreWatcherFnOnce sync.Once
	watchersResult    chan error

	doneCh     chan struct{}
	watchersWG sync.WaitGroup
}

var _ Session = (*vaultSession)(nil)

func (v *vaultSession) Retrieve(_ context.Context, selector string, _ interface{}) (Retrieved, error) {
	if v.secret == nil {
		secret, err := v.client.Logical().Read(v.path)
		if err != nil {
			return Retrieved{}, err
		}

		// Invalid path does not return error but a nil secret.
		if secret == nil {
			return Retrieved{}, fmt.Errorf("no secret found at %q", v.path)
		}

		// Incorrect path for v2 return nil data and warnings.
		if secret.Data == nil {
			return Retrieved{}, fmt.Errorf("no data at %q warnings: %v", v.path, secret.Warnings)
		}

		v.secret = secret
	}

	watchForUpdateFn, err := v.watchForUpdateFn()
	if err != nil {
		return Retrieved{}, err
	}

	v.numWatchers++
	return Retrieved{
		Value:          traverseToKey(v.secret.Data, selector), // TODO: This can return nil, investigate.
		WatchForUpdate: watchForUpdateFn,
	}, nil
}

func (v *vaultSession) RetrieveEnd(context.Context) error {
	// No more watchers will be added, set the channel for watchers result.
	v.watchersResult = make(chan error, v.numWatchers)

	return nil
}

func (v *vaultSession) Close(context.Context) error {
	close(v.doneCh)
	v.watchersWG.Wait()

	// Vault doesn't have a close for its client, close completed.
	return nil
}

func newSession(client *api.Client, path string) (*vaultSession, error) {
	// TODO: pass from factory.
	logger, _ := zap.NewDevelopment()
	return &vaultSession{
		logger: logger,
		client: client,
		path:   path,
		doneCh: make(chan struct{}),
	}, nil
}

func (v *vaultSession) watchForUpdateFn() (WatchForUpdate, error) {
	if v.watcherFn != nil {
		return v.watcherFn, nil
	}

	switch {
	// Dynamic secrets can be either renewable or leased.
	case v.secret.Renewable:
		return v.buildRenewableWatcher()
	// TODO: leased secrets need to periodically
	default:
		// Not a dynamic secret the best that can be done is polling.
		return v.buildPollingWatcher()
	}
}

func (v *vaultSession) buildRenewableWatcher() (WatchForUpdate, error) {
	renewer, err := v.client.NewRenewer(&api.RenewerInput{
		Secret: v.secret,
	})
	if err != nil {
		return nil, err
	}

	v.watcherFn = func() error {
		v.watchersWG.Add(1)
		defer v.watchersWG.Done()

		v.coreWatcherFnOnce.Do(func() {
			go renewer.Renew()
			defer renewer.Stop()

			for {
				select {
				case <-renewer.RenewCh():
					v.logger.Debug("vault secret renewed", zap.String("path", v.path))
				case err := <-renewer.DoneCh():
					// Renewal stopped, error or not the client needs to re-fetch the configuration.
					v.watchersResult <- err
					return
				case <-v.doneCh:
					v.watchersResult <- ErrSessionClosed
					return
				}
			}
		})

		return <-v.watchersResult
	}

	return v.watcherFn, nil
}

func (v *vaultSession) buildPollingWatcher() (WatchForUpdate, error) {
	// TODO: for now return a do nothing method.
	v.watcherFn = func() error {
		v.watchersWG.Add(1)
		defer v.watchersWG.Done()

		<-v.doneCh
		return ErrSessionClosed
	}
	return v.watcherFn, nil
}
