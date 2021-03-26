package configsource

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/vault/api"
	"go.uber.org/zap"
)

type vaultSession struct {
	logger     *zap.Logger
	client     *api.Client
	doneCh     chan struct{}
	watchersWG sync.WaitGroup
}

var _ Session = (*vaultSession)(nil)

func (v *vaultSession) Retrieve(_ context.Context, selector string, _ interface{}) (Retrieved, error) {
	vaultPath, key, err := splitConfigPath(selector) // TODO: now same parsing as SA, evaluate alternatives.
	if err != nil {
		return Retrieved{}, err
	}

	secret, err := v.client.Logical().Read(selector) // TODO: this could be cached by vaultPath
	if err != nil {
		return Retrieved{}, err
	}

	// Invalid path does not return error but a nil secret.
	if secret == nil {
		return Retrieved{}, fmt.Errorf("no secret found at %q", vaultPath)
	}

	// Incorrect path for v2 return nil data and warnings.
	if secret.Data == nil {
		return Retrieved{}, fmt.Errorf("no data at %q warnings: %v", vaultPath, secret.Warnings)
	}

	watchForUpdate, err := v.buildWatchForUpdate(secret, vaultPath)
	if err != nil {
		return Retrieved{}, fmt.Errorf("failed to build the watcher: %w", err)
	}

	return Retrieved{
		Value:          traverseToKey(secret.Data, key), // TODO: This can return nil, investigate.
		WatchForUpdate: watchForUpdate,
	}, nil
}

func (v *vaultSession) buildWatchForUpdate(secret *api.Secret, vaultPath string) (WatchForUpdate, error) {
	switch {
	case secret.Renewable:
		return v.buildRenewableWatcher(secret, vaultPath)
	}
	return nil, errors.New("only renewable secrets are supported")
}

func (v *vaultSession) RetrieveEnd(context.Context) error {
	// Nothing for Vault at retrieve end.
	return nil
}

func (v *vaultSession) Close(context.Context) error {
	close(v.doneCh)
	v.watchersWG.Wait()

	// Vault doesn't have a close for its client, close completed.
	return nil
}

func (v *vaultSession) buildRenewableWatcher(secret *api.Secret, vaultPath string) (WatchForUpdate, error) {
	renewer, err := v.client.NewRenewer(&api.RenewerInput{
		Secret: secret,
	})
	if err != nil {
		return nil, err
	}

	// TODO: Renewal is per path this doesn't work well for multiple keys... this suggests that the config source
	// itself should be tied to one path however this requires that the config have separate entries for different
	// paths that may actually be using the same set of credentials...
	watchForUpdate := func() error {
		v.watchersWG.Add(1)
		defer v.watchersWG.Done()

		go renewer.Renew()
		defer renewer.Stop()

		for {
			select {
			case <-renewer.RenewCh():
				v.logger.Debug("vault secret renewed", zap.String("path", vaultPath))
			case err := <-renewer.DoneCh():
				// Renewal stopped, error or not the client needs to refetch the configuration.
				return err
			case <-v.doneCh:
				return ErrSessionClosed
			}
		}
	}

	return watchForUpdate, nil
}

func newSession(url, token string) (*vaultSession, error) {
	client, err := api.NewClient(&api.Config{
		Address: url,
	})
	if err != nil {
		return nil, err
	}

	client.SetToken(token)

	// TODO: pass from factory.
	logger, _ := zap.NewDevelopment()
	return &vaultSession{
		logger: logger,
		client: client,
	}, nil
}
