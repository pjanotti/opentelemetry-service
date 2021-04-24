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
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/testutil"
	"go.uber.org/zap"
)

func TestZPagesRegisterZPages_RecoverMuxPanic(t *testing.T) {
	handleFunc := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Ok"))
	}

	fstMux := http.NewServeMux()
	fstMux.HandleFunc("/pathz", handleFunc)
	require.NoError(t, RegisterZPages(fstMux))
	defer UnregisterAllZPages()

	// Another Mux but using the same path
	sndMux := http.NewServeMux()
	sndMux.HandleFunc("/pathz", handleFunc)
	assert.IsType(t, &errServerMuxPanic{}, RegisterZPages(sndMux))
}

func TestZPagesRegisterZPages_AfterStart(t *testing.T) {
	config := Config{
		TCPAddr: confignet.TCPAddr{
			Endpoint: testutil.GetAvailableLocalAddress(t),
		},
	}

	ext := newServer(config, zap.NewNop())
	require.NotNil(t, ext)

	require.NoError(t, ext.Start(context.Background(), componenttest.NewNopHost()))
	t.Cleanup(func() { require.NoError(t, ext.Shutdown(context.Background())) })

	mux := http.NewServeMux()
	mux.HandleFunc("/pathz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Ok"))
	})
	assert.Equal(t, errExtensionAlreadyStarted, RegisterZPages(mux))
}
