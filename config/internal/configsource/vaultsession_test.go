package configsource

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func Test_newSession(t *testing.T) {
	tests := []struct {
		name    string
		address string
		token   string
	}{
		{
			name:    "basic",
			address: "http://localhost:8200",
			token:   "s.OM1vXlWP5Ixa4mJbYg83DDJ6",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs, err := newConfigSource(tt.address, tt.token, "secret/data/hello")
			require.NoError(t, err)
			require.NotNil(t, cs)

			s, err := cs.NewSession(context.Background())
			require.NoError(t, err)
			require.NotNil(t, s)

			retrieved, err := s.Retrieve(context.Background(), "data.foo", nil)
			require.NoError(t, err)
			require.Equal(t, "world", retrieved.Value.(string))
			require.NoError(t, s.RetrieveEnd(context.Background()))

			var watcherErr error
			doneCh := make(chan struct{})
			go func() {
				watcherErr = retrieved.WatchForUpdate()
				close(doneCh)
			}()

			require.NoError(t, s.Close(context.Background()))
			<-doneCh
			require.Equal(t, ErrSessionClosed, watcherErr)
		})
	}
}
