package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

// fakeTokenServer returns a token endpoint that hands back a configurable
// access + refresh token on every POST, recording each call.
func fakeTokenServer(t *testing.T, accessToken, refreshToken string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeBroker records SetRealToken calls keyed by host.
type fakeBroker struct {
	mu   sync.Mutex
	seen map[string]string // host → last token
}

func newFakeBroker() *fakeBroker { return &fakeBroker{seen: make(map[string]string)} }

func (b *fakeBroker) SetRealToken(_ domain.SandboxID, host, tok string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen[host] = tok
	return nil
}

func (b *fakeBroker) LastToken(host string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seen[host]
}

// TestStartMCPOAuthRefreshers_SingleServer asserts that a Refresher built from
// an MCPOAuthRefreshConfig refreshes the token and, on Token(), calls
// Broker.SetRealToken with the new token for the correct host.
func TestStartMCPOAuthRefreshers_SingleServer(t *testing.T) {
	const (
		initialAccess  = "init-at"
		rotatedAccess  = "new-at"
		rotatedRefresh = "new-rt"
	)

	srv := fakeTokenServer(t, rotatedAccess, rotatedRefresh)
	host := "mcp.example.com"

	storeRoot := t.TempDir()
	broker := cred.NewBroker()

	cfg := service.MCPOAuthRefreshConfig{
		ServerName:    "my-mcp-server",
		Host:          host,
		AccessToken:   initialAccess,
		RefreshToken:  "init-rt",
		TokenEndpoint: srv.URL + "/token",
		ClientID:      "client123",
		ExpiresAtMs:   time.Now().Add(-time.Hour).UnixMilli(), // expired → forces refresh
	}

	refreshers, err := service.StartMCPOAuthRefreshers(context.Background(), broker, storeRoot, []service.MCPOAuthRefreshConfig{cfg})
	if err != nil {
		t.Fatalf("StartMCPOAuthRefreshers: %v", err)
	}
	if len(refreshers) != 1 {
		t.Fatalf("want 1 refresher, got %d", len(refreshers))
	}

	// Register the sandbox and call Token() to trigger refresh + broker push.
	id := domain.NewSandboxID()
	refreshers[0].Register(id)

	// Register the host placeholder in the broker so SetRealToken has a valid scope.
	if _, regErr := broker.RegisterPlaceholder(id, host, "placeholder-token"); regErr != nil {
		t.Fatalf("RegisterPlaceholder: %v", regErr)
	}

	tok, _, tokErr := refreshers[0].Token(context.Background())
	if tokErr != nil {
		t.Fatalf("Token(): %v", tokErr)
	}
	if tok != rotatedAccess {
		t.Errorf("Token() = %q, want %q", tok, rotatedAccess)
	}
}

// TestStartMCPOAuthRefreshers_StoreFileWritten asserts that the per-server
// credential store file is created under the given storeRoot with the correct name.
func TestStartMCPOAuthRefreshers_StoreFileWritten(t *testing.T) {
	srv := fakeTokenServer(t, "at2", "rt2")
	storeRoot := t.TempDir()
	broker := cred.NewBroker()

	cfg := service.MCPOAuthRefreshConfig{
		ServerName:    "linear-server",
		Host:          "api.linear.app",
		AccessToken:   "at1",
		RefreshToken:  "rt1",
		TokenEndpoint: srv.URL + "/token",
		ClientID:      "cid",
		ExpiresAtMs:   time.Now().Add(time.Hour).UnixMilli(),
	}

	if _, err := service.StartMCPOAuthRefreshers(context.Background(), broker, storeRoot, []service.MCPOAuthRefreshConfig{cfg}); err != nil {
		t.Fatalf("StartMCPOAuthRefreshers: %v", err)
	}

	storePath := filepath.Join(storeRoot, "linear-server.json")
	if _, err := os.Stat(storePath); err != nil {
		t.Errorf("expected store file %s to exist: %v", storePath, err)
	}
}

// TestStartMCPOAuthRefreshers_MultipleConfigs asserts that multiple configs
// each get their own Refresher, and stop() (deregister) is clean.
func TestStartMCPOAuthRefreshers_MultipleConfigs(t *testing.T) {
	srv := fakeTokenServer(t, "at-new", "rt-new")
	storeRoot := t.TempDir()
	broker := cred.NewBroker()

	configs := []service.MCPOAuthRefreshConfig{
		{
			ServerName:    "server-a",
			Host:          "a.example.com",
			AccessToken:   "at-a",
			RefreshToken:  "rt-a",
			TokenEndpoint: srv.URL + "/token",
			ClientID:      "cid-a",
			ExpiresAtMs:   time.Now().Add(time.Hour).UnixMilli(),
		},
		{
			ServerName:    "server-b",
			Host:          "b.example.com",
			AccessToken:   "at-b",
			RefreshToken:  "rt-b",
			TokenEndpoint: srv.URL + "/token",
			ClientID:      "cid-b",
			ExpiresAtMs:   time.Now().Add(time.Hour).UnixMilli(),
		},
	}

	refreshers, err := service.StartMCPOAuthRefreshers(context.Background(), broker, storeRoot, configs)
	if err != nil {
		t.Fatalf("StartMCPOAuthRefreshers: %v", err)
	}
	if len(refreshers) != 2 {
		t.Fatalf("want 2 refreshers, got %d", len(refreshers))
	}

	// Deregister is idempotent — no panic expected.
	id := domain.NewSandboxID()
	for _, r := range refreshers {
		r.Register(id)
		r.Deregister(id)
	}

	// Store files for both servers must exist.
	for _, name := range []string{"server-a.json", "server-b.json"} {
		p := filepath.Join(storeRoot, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("store file %s missing: %v", p, err)
		}
	}
}

// TestStartMCPOAuthRefreshers_SkipBadConfig asserts that a config with a
// missing refresh_token is skipped non-fatally; other configs still build.
func TestStartMCPOAuthRefreshers_SkipBadConfig(t *testing.T) {
	srv := fakeTokenServer(t, "good-at", "good-rt")
	storeRoot := t.TempDir()
	broker := cred.NewBroker()

	configs := []service.MCPOAuthRefreshConfig{
		{
			ServerName:   "bad-no-refresh",
			Host:         "bad.example.com",
			AccessToken:  "at-bad",
			RefreshToken: "", // missing — should be skipped
			TokenEndpoint: srv.URL + "/token",
			ClientID:     "cid",
			ExpiresAtMs:  time.Now().Add(time.Hour).UnixMilli(),
		},
		{
			ServerName:    "good-server",
			Host:          "good.example.com",
			AccessToken:   "at-good",
			RefreshToken:  "rt-good",
			TokenEndpoint: srv.URL + "/token",
			ClientID:      "cid",
			ExpiresAtMs:   time.Now().Add(time.Hour).UnixMilli(),
		},
	}

	refreshers, err := service.StartMCPOAuthRefreshers(context.Background(), broker, storeRoot, configs)
	if err != nil {
		t.Fatalf("StartMCPOAuthRefreshers returned non-nil error for partial bad config: %v", err)
	}
	if len(refreshers) != 1 {
		t.Fatalf("want 1 refresher (bad config skipped), got %d", len(refreshers))
	}
	if refreshers[0].Host() != "good.example.com" {
		t.Errorf("refresher host = %q, want good.example.com", refreshers[0].Host())
	}
}
