package cred_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// TestS0_ProfileStoreToken is the end-to-end S0 tracer test: it loads a
// profile, loads a store from disk, constructs a StaticCredentialSource, and
// resolves a token — proving the profile→store→token path works.
func TestS0_ProfileStoreToken(t *testing.T) {
	// 1. Profile — use the canonical ClaudeCode profile.
	profile := cred.ClaudeCodeProfile
	if profile.PlaceholderEnvVar == "" || profile.CredentialedHost == "" {
		t.Fatal("ClaudeCodeProfile is incomplete")
	}

	// 2. Store — write a fixture to disk and load it.
	expiry := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	storePath := filepath.Join(dir, "nexus3_cred.json")
	payload := `{
		"access_token":   "real-token-s0",
		"refresh_token":  "real-refresh-s0",
		"expires_at":     "2027-06-01T00:00:00Z",
		"token_type":     "Bearer",
		"client_id":      "nexus3-client",
		"token_endpoint": "https://auth.anthropic.com/oauth/token"
	}`
	if err := os.WriteFile(storePath, []byte(payload), 0600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	store, err := cred.LoadStore(storePath)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	// 3. Source — wrap the store.
	src := cred.NewStaticCredentialSource(store)

	// 4. Resolve — the payoff: Token() must return the real token.
	tok, expiresAt, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: unexpected error: %v", err)
	}
	if tok != "real-token-s0" {
		t.Errorf("Token = %q, want %q", tok, "real-token-s0")
	}
	if !expiresAt.Equal(expiry) {
		t.Errorf("expiresAt = %v, want %v", expiresAt, expiry)
	}

	// Sanity: the interface is satisfied (compile-time proof via assignment).
	var _ cred.CredentialSource = src
}

func TestStaticCredentialSource_NilStorePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil store, got none")
		}
	}()
	cred.NewStaticCredentialSource(nil)
}
