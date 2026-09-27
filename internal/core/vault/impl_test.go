package vault_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func newTestVault(t *testing.T) *vault.VaultImpl {
	t.Helper()
	reg := vault.NewRegistry()
	_ = reg.Register(vaulttest.NewFakeConnector("github"))
	_ = reg.Register(vaulttest.NewFakeConnector("linear"))
	return vault.NewVaultImpl(vaulttest.NewMemStore(), reg)
}

func TestVaultImplPassesVaultContract(t *testing.T) {
	vaulttest.RunVaultContract(t, newTestVault(t))
}

func TestRefreshWhenExpiryUnder5Min(t *testing.T) {
	reg := vault.NewRegistry()
	_ = reg.Register(vaulttest.NewFakeConnector("github"))
	store := vaulttest.NewMemStore()
	v := vault.NewVaultImpl(store, reg)

	ctx := context.Background()
	k := vault.Key{Principal: "local:alice", Integration: "github"}
	rec := vault.Record{
		AccessToken:     "old-token",
		RefreshToken:    "old-refresh",
		Expiry:          time.Now().Add(2 * time.Minute),
		AllowedProjects: []string{"*"},
	}
	_ = store.Put(ctx, k, rec)

	src, err := v.Source(k, "proj")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := src.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok == "old-token" {
		t.Error("expected token to be refreshed")
	}
}

func TestNoRefreshWhenFresh(t *testing.T) {
	reg := vault.NewRegistry()
	_ = reg.Register(vaulttest.NewFakeConnector("github"))
	store := vaulttest.NewMemStore()
	v := vault.NewVaultImpl(store, reg)

	ctx := context.Background()
	k := vault.Key{Principal: "local:alice", Integration: "github"}
	rec := vault.Record{
		AccessToken:     "fresh-token",
		RefreshToken:    "refresh",
		Expiry:          time.Now().Add(time.Hour),
		AllowedProjects: []string{"*"},
	}
	_ = store.Put(ctx, k, rec)

	src, err := v.Source(k, "proj")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := src.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "fresh-token" {
		t.Errorf("fresh token should not be refreshed, got %q", tok)
	}
}

func TestRefreshSingleflight(t *testing.T) {
	var refreshCount atomic.Int64
	conn := &countingConnector{id: "github", count: &refreshCount}
	reg := vault.NewRegistry()
	_ = reg.Register(conn)
	store := vaulttest.NewMemStore()
	v := vault.NewVaultImpl(store, reg)

	ctx := context.Background()
	k := vault.Key{Principal: "local:alice", Integration: "github"}
	rec := vault.Record{
		AccessToken:     "expiring",
		RefreshToken:    "ref",
		Expiry:          time.Now().Add(2 * time.Minute),
		AllowedProjects: []string{"*"},
	}
	_ = store.Put(ctx, k, rec)

	src, _ := v.Source(k, "proj")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src.Token(ctx)
		}()
	}
	wg.Wait()

	if n := refreshCount.Load(); n > 1 {
		t.Errorf("singleflight should collapse concurrent refreshes, got %d calls", n)
	}
}

func TestForceRefreshOn401RetriesOnce(t *testing.T) {
	reg := vault.NewRegistry()
	_ = reg.Register(vaulttest.NewFakeConnector("github"))
	store := vaulttest.NewMemStore()
	v := vault.NewVaultImpl(store, reg)

	ctx := context.Background()
	k := vault.Key{Principal: "local:alice", Integration: "github"}
	rec := vault.Record{
		AccessToken:     "stale-401-token",
		RefreshToken:    "ref",
		Expiry:          time.Now().Add(time.Hour),
		AllowedProjects: []string{"*"},
	}
	_ = store.Put(ctx, k, rec)

	refreshed, err := v.ForceRefresh(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken == "stale-401-token" {
		t.Error("ForceRefresh should return a new token")
	}

	stored, err := store.Get(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != refreshed.AccessToken {
		t.Error("ForceRefresh should persist the new token")
	}
}

func TestRotatedRefreshTokenPersisted(t *testing.T) {
	reg := vault.NewRegistry()
	_ = reg.Register(vaulttest.NewFakeConnector("github"))
	store := vaulttest.NewMemStore()
	v := vault.NewVaultImpl(store, reg)

	ctx := context.Background()
	k := vault.Key{Principal: "local:alice", Integration: "github"}
	rec := vault.Record{
		AccessToken:     "old",
		RefreshToken:    "old-refresh",
		Expiry:          time.Now().Add(2 * time.Minute),
		AllowedProjects: []string{"*"},
	}
	_ = store.Put(ctx, k, rec)

	src, _ := v.Source(k, "proj")
	tok, _, err := src.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != tok {
		t.Errorf("persisted token %q != returned token %q", stored.AccessToken, tok)
	}
}

type countingConnector struct {
	id    string
	count *atomic.Int64
}

func (c *countingConnector) ID() string               { return c.id }
func (c *countingConnector) LinkFlow() vault.LinkFlow { return vault.LinkFlowDevice }
func (c *countingConnector) StartDevice(_ context.Context) (vault.DeviceAuth, error) {
	return vault.DeviceAuth{DeviceCode: "d", UserCode: "u", VerificationURI: "https://x"}, nil
}
func (c *countingConnector) PollDevice(_ context.Context, _ string) (vault.Record, error) {
	return vault.Record{}, nil
}
func (c *countingConnector) AuthURL(_ string) (string, string, error) {
	return "https://x", "v", nil
}
func (c *countingConnector) Exchange(_ context.Context, _, _ string) (vault.Record, error) {
	return vault.Record{}, nil
}
func (c *countingConnector) Refresh(_ context.Context, rec vault.Record) (vault.Record, error) {
	c.count.Add(1)
	time.Sleep(5 * time.Millisecond)
	rec.AccessToken = "refreshed"
	rec.Expiry = time.Now().Add(time.Hour)
	return rec, nil
}
func (c *countingConnector) AllowedHosts() []string { return nil }
