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
			v, err := newSession(tt.address, tt.token)
			require.NoError(t, err)
			require.NotNil(t, v)

			retrieved, err := v.Retrieve(context.Background(), "secret/hello[foo]", nil)
			require.NoError(t, err)
			require.Equal(t, "world", retrieved.Value.(string))
		})
	}
}
