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
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"unsafe"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/component"
)

// Tracks that only a single instance is active per process.
// See comment on RegisterZPages function for the reasons for that.
var activeInstance *zpagesExtension

// #nosec G103
var activeInstancePtr = (*unsafe.Pointer)(unsafe.Pointer(&activeInstance))

var errInstanceAlreadyRunning = errors.New("only a single zPages extension instance can be running per process")

type zpagesExtension struct {
	config Config
	logger *zap.Logger
	server http.Server
	stopCh chan struct{}
}

func (zpe *zpagesExtension) Start(_ context.Context, host component.Host) error {
	if !atomic.CompareAndSwapPointer(activeInstancePtr, nil, unsafe.Pointer(zpe)) {
		return errInstanceAlreadyRunning
	}

	// Start the listener here so we can have earlier failure if port is
	// already in use.
	ln, err := zpe.config.TCPAddr.Listen()
	if err != nil {
		atomic.StorePointer(activeInstancePtr, nil)
		return err
	}

	zpe.logger.Info("Starting zPages extension", zap.Any("config", zpe.config))
	zpe.server = http.Server{Handler: zPagesMux}
	zpe.stopCh = make(chan struct{})
	go func() {
		defer close(zpe.stopCh)
		defer atomic.StorePointer(activeInstancePtr, nil)

		if err := zpe.server.Serve(ln); err != http.ErrServerClosed {
			host.ReportFatalError(err)
		}
	}()

	return nil
}

func (zpe *zpagesExtension) Shutdown(context.Context) error {
	err := zpe.server.Close()
	if zpe.stopCh != nil {
		<-zpe.stopCh
	}
	return err
}

func newServer(config Config, logger *zap.Logger) *zpagesExtension {
	return &zpagesExtension{
		config: config,
		logger: logger,
	}
}
