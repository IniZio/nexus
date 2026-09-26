// Package vaulttest provides fakes and contract suites for the vault interfaces.
package vaulttest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/vault"
)

// MemStore is an in-memory implementation of vault.Store.
type MemStore struct {
	mu      sync.RWMutex
	records map[vault.Key]vault.Record
}

// NewMemStore creates an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{records: make(map[vault.Key]vault.Record)}
}

func (s *MemStore) Get(_ context.Context, key vault.Key) (vault.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[key]
	if !ok {
		return vault.Record{}, vault.ErrUnlinked
	}
	return r, nil
}

func (s *MemStore) Put(_ context.Context, key vault.Key, record vault.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[key] = record
	return nil
}

func (s *MemStore) Delete(_ context.Context, key vault.Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, key)
	return nil
}

func (s *MemStore) List(_ context.Context) ([]vault.Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]vault.Key, 0, len(s.records))
	for k := range s.records {
		keys = append(keys, k)
	}
	return keys, nil
}

// fakeSource is a cred.CredentialSource backed by a fixed Record.
type fakeSource struct {
	rec vault.Record
}

func (f *fakeSource) Token(_ context.Context) (string, time.Time, error) {
	return f.rec.AccessToken, f.rec.Expiry, nil
}

// Fake is an in-memory implementation of vault.Vault backed by a MemStore.
type Fake struct {
	store *MemStore
}

// NewFake creates a Fake vault backed by a new MemStore.
func NewFake() *Fake {
	return &Fake{store: NewMemStore()}
}

func (f *Fake) Get(ctx context.Context, key vault.Key) (vault.Record, error) {
	return f.store.Get(ctx, key)
}

func (f *Fake) Put(ctx context.Context, key vault.Key, record vault.Record) error {
	return f.store.Put(ctx, key, record)
}

func (f *Fake) Delete(ctx context.Context, key vault.Key) error {
	return f.store.Delete(ctx, key)
}

func (f *Fake) List(ctx context.Context) ([]vault.Key, error) {
	return f.store.List(ctx)
}

func (f *Fake) Source(key vault.Key, project string) (cred.CredentialSource, error) {
	rec, err := f.store.Get(context.Background(), key)
	if err != nil {
		if errors.Is(err, vault.ErrUnlinked) {
			return nil, vault.ErrUnlinked
		}
		return nil, err
	}
	allowed := false
	for _, p := range rec.AllowedProjects {
		if p == "*" || p == project {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, vault.ErrProjectNotAllowed
	}
	return &fakeSource{rec: rec}, nil
}

func (f *Fake) ForceRefresh(ctx context.Context, key vault.Key) (vault.Record, error) {
	rec, err := f.store.Get(ctx, key)
	if err != nil {
		return vault.Record{}, err
	}
	rec.AccessToken = "refreshed-" + rec.AccessToken
	_ = f.store.Put(ctx, key, rec)
	return rec, nil
}

// FakeConnector is a test double for vault.Connector using the device flow.
type FakeConnector struct {
	id string
}

// NewFakeConnector creates a FakeConnector with the given id.
func NewFakeConnector(id string) *FakeConnector {
	return &FakeConnector{id: id}
}

func (c *FakeConnector) ID() string { return c.id }

func (c *FakeConnector) LinkFlow() vault.LinkFlow { return vault.LinkFlowDevice }

func (c *FakeConnector) StartDevice(_ context.Context) (vault.DeviceAuth, error) {
	return vault.DeviceAuth{
		DeviceCode:      "dev-code",
		UserCode:        "USER-CODE",
		VerificationURI: "https://example.com/device",
		ExpiresIn:       300,
		Interval:        5,
	}, nil
}

func (c *FakeConnector) PollDevice(_ context.Context, _ string) (vault.Record, error) {
	return vault.Record{
		AccessToken:     "fake-access-token",
		RefreshToken:    "fake-refresh-token",
		Expiry:          time.Now().Add(8 * time.Hour),
		Scopes:          []string{"repo", "read:user"},
		AllowedProjects: []string{"*"},
	}, nil
}

func (c *FakeConnector) AuthURL(_ string) (string, string, error) {
	return "https://example.com/auth", "verifier", nil
}

func (c *FakeConnector) Exchange(_ context.Context, _, _ string) (vault.Record, error) {
	return vault.Record{
		AccessToken:     "fake-access-token",
		Expiry:          time.Now().Add(8 * time.Hour),
		AllowedProjects: []string{"*"},
	}, nil
}

func (c *FakeConnector) Refresh(_ context.Context, rec vault.Record) (vault.Record, error) {
	rec.AccessToken = "refreshed-token"
	rec.Expiry = time.Now().Add(8 * time.Hour)
	return rec, nil
}

func (c *FakeConnector) AllowedHosts() []string { return nil }

// NewTestRegistry returns a vault.Registry for use in tests.
func NewTestRegistry() *vault.Registry {
	return vault.NewRegistry()
}
