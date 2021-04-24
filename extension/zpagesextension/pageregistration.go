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

package zpagesextension

import (
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"go.opencensus.io/zpages"
)

const zPagesPathPrefix = "/debug"

// zPagesMux combines all zpages registered to be exposed by the extension.
var zPagesMux *http.ServeMux

var errExtensionAlreadyStarted = errors.New("cannot register zPages when the extension is already started")

// Private error type to help with testability but with dynamic message.
type errServerMuxPanic struct{ error }

func init() {
	UnregisterAllZPages()
}

// RegisterZPages allow other components to also add zPages to the extension.
// This needs to be called when components are created and before the extension
// is started. Since the callers don't have the extension instance this is a
// static function making the registration of zPages global to the whole package
// which forces the extension to support only a single running instance.
//
// Components adding zPages must use path starting with "/" any prefix added by
// the zPages extension will be removed before reaching the component's mux handlers.
func RegisterZPages(mux *http.ServeMux) (err error) {
	if atomic.LoadPointer(activeInstancePtr) != nil {
		return errExtensionAlreadyStarted
	}

	// http.ServerMux panics instead of returning an error. Add a deferred func
	// to recover from any panic and return it as an error to the caller.
	defer func() {
		if r := recover(); r != nil {
			err = &errServerMuxPanic{fmt.Errorf("%v", r)}
		}
	}()

	zPagesMux.Handle(zPagesPathPrefix+"/", http.StripPrefix(zPagesPathPrefix, mux))
	return
}

// UnregisterAllZPages resets the internal registration of zPages. This is used to
// ensure that any previous registrations are cleaned.
func UnregisterAllZPages() {
	zPagesMux = http.NewServeMux()
	zpages.Handle(zPagesMux, zPagesPathPrefix)
}
