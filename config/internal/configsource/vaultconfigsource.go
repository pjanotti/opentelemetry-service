package configsource

import (
	"context"

	"github.com/hashicorp/vault/api"
)

type vaultConfigSource struct {
	client *api.Client
}

var _ ConfigSource = (*vaultConfigSource)(nil)

func (v *vaultConfigSource) NewSession(context.Context) (Session, error) {
	panic("implement me")
}

func newConfigSource(address, token string) (*vaultConfigSource, error) {
	client, err := api.NewClient(&api.Config{
		Address: address,
	})
	if err != nil {
		return nil, err
	}

	client.SetToken(token)
	return &vaultConfigSource{
		client: client,
	}, nil
}
