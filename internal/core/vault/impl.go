package vault

import (
	"context"
	"time"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// VaultImpl is the real Vault built over a Store and a Registry.
type VaultImpl struct {
	store    Store
	registry *Registry
	refresh  *refresher
}

// NewVaultImpl creates a VaultImpl. The Registry is used to find connectors for lazy refresh.
func NewVaultImpl(store Store, registry *Registry) *VaultImpl {
	v := &VaultImpl{store: store, registry: registry}
	v.refresh = newRefresher(store, registry)
	return v
}

func (v *VaultImpl) Get(ctx context.Context, key Key) (Record, error) {
	return v.store.Get(ctx, key)
}

func (v *VaultImpl) Put(ctx context.Context, key Key, record Record) error {
	return v.store.Put(ctx, key, record)
}

func (v *VaultImpl) Delete(ctx context.Context, key Key) error {
	return v.store.Delete(ctx, key)
}

func (v *VaultImpl) List(ctx context.Context) ([]Key, error) {
	return v.store.List(ctx)
}

func (v *VaultImpl) Source(key Key, project string) (cred.CredentialSource, error) {
	ctx := context.Background()
	rec, err := v.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if !rec.projectAllowed(project) {
		return nil, ErrProjectNotAllowed
	}
	return &vaultSource{key: key, project: project, vault: v}, nil
}

// ForceRefresh evicts the singleflight state and forces a connector Refresh for the given key.
func (v *VaultImpl) ForceRefresh(ctx context.Context, key Key) (Record, error) {
	v.refresh.forget(key)
	return v.refresh.doRefresh(ctx, key)
}

type vaultSource struct {
	key     Key
	project string
	vault   *VaultImpl
}

func (s *vaultSource) Token(ctx context.Context) (string, time.Time, error) {
	rec, err := s.vault.store.Get(ctx, s.key)
	if err != nil {
		return "", time.Time{}, err
	}
	if time.Until(rec.Expiry) < 5*time.Minute {
		rec, err = s.vault.refresh.refresh(ctx, s.key)
		if err != nil {
			return "", time.Time{}, err
		}
	}
	return rec.AccessToken, rec.Expiry, nil
}
