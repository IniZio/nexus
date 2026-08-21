package cred_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

func TestLoadStore_AbsentFile(t *testing.T) {
	_, err := cred.LoadStore("/nonexistent/path/store.json")
	if err == nil {
		t.Fatal("expected error for absent store file, got nil")
	}
	if !errors.Is(err, cred.ErrStoreAbsent) {
		t.Fatalf("expected ErrStoreAbsent, got: %v", err)
	}
}

func TestLoadStore_ValidStore(t *testing.T) {
	expiry := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	payload := `{
		"access_token":   "tok-abc123",
		"refresh_token":  "ref-xyz789",
		"expires_at":     "2026-12-31T00:00:00Z",
		"token_type":     "Bearer",
		"client_id":      "client-id-1",
		"client_secret":  "secret-1",
		"token_endpoint": "https://auth.example.com/token"
	}`
	path := writeTemp(t, payload)

	store, err := cred.LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: unexpected error: %v", err)
	}
	if store.AccessToken != "tok-abc123" {
		t.Errorf("AccessToken = %q, want %q", store.AccessToken, "tok-abc123")
	}
	if store.RefreshToken != "ref-xyz789" {
		t.Errorf("RefreshToken = %q, want %q", store.RefreshToken, "ref-xyz789")
	}
	if !store.ExpiresAt.Equal(expiry) {
		t.Errorf("ExpiresAt = %v, want %v", store.ExpiresAt, expiry)
	}
	if store.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want %q", store.TokenType, "Bearer")
	}
	if store.ClientID != "client-id-1" {
		t.Errorf("ClientID = %q, want %q", store.ClientID, "client-id-1")
	}
	if store.TokenEndpoint != "https://auth.example.com/token" {
		t.Errorf("TokenEndpoint = %q, want %q", store.TokenEndpoint, "https://auth.example.com/token")
	}
}

func TestLoadStore_EmptyAccessToken(t *testing.T) {
	payload := `{
		"access_token":   "",
		"refresh_token":  "ref-xyz",
		"expires_at":     "2026-12-31T00:00:00Z",
		"token_type":     "Bearer",
		"client_id":      "c",
		"token_endpoint": "https://auth.example.com/token"
	}`
	path := writeTemp(t, payload)

	_, err := cred.LoadStore(path)
	if err == nil {
		t.Fatal("expected error for empty access_token, got nil")
	}
}

func TestLoadStore_MalformedJSON(t *testing.T) {
	path := writeTemp(t, `not json at all`)
	_, err := cred.LoadStore(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestSaveStoreRoundTrip(t *testing.T) {
	expiry := time.Date(2027, 6, 15, 12, 0, 0, 0, time.UTC)
	orig := &cred.DedicatedCredStore{
		AccessToken:   "at-roundtrip",
		RefreshToken:  "rt-roundtrip",
		ExpiresAt:     expiry,
		TokenType:     "Bearer",
		ClientID:      "cid-roundtrip",
		ClientSecret:  "csec-roundtrip",
		TokenEndpoint: "https://auth.example.com/token",
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")

	if err := cred.SaveStore(path, orig); err != nil {
		t.Fatalf("SaveStore: unexpected error: %v", err)
	}

	// Verify file mode is exactly 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}

	loaded, err := cred.LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: unexpected error: %v", err)
	}

	if loaded.AccessToken != orig.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, orig.AccessToken)
	}
	if loaded.RefreshToken != orig.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, orig.RefreshToken)
	}
	if !loaded.ExpiresAt.Equal(orig.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", loaded.ExpiresAt, orig.ExpiresAt)
	}
	if loaded.TokenType != orig.TokenType {
		t.Errorf("TokenType = %q, want %q", loaded.TokenType, orig.TokenType)
	}
	if loaded.ClientID != orig.ClientID {
		t.Errorf("ClientID = %q, want %q", loaded.ClientID, orig.ClientID)
	}
	if loaded.ClientSecret != orig.ClientSecret {
		t.Errorf("ClientSecret = %q, want %q", loaded.ClientSecret, orig.ClientSecret)
	}
	if loaded.TokenEndpoint != orig.TokenEndpoint {
		t.Errorf("TokenEndpoint = %q, want %q", loaded.TokenEndpoint, orig.TokenEndpoint)
	}
}

func TestSaveStoreRejectsEmptyAccessToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")

	s := &cred.DedicatedCredStore{
		AccessToken:   "", // empty — must be rejected
		RefreshToken:  "rt-xyz",
		ExpiresAt:     time.Now().Add(time.Hour),
		TokenType:     "Bearer",
		ClientID:      "cid",
		ClientSecret:  "csec",
		TokenEndpoint: "https://auth.example.com/token",
	}

	err := cred.SaveStore(path, s)
	if err == nil {
		t.Fatal("expected error for empty AccessToken, got nil")
	}

	// File must not have been created.
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("store file should not exist after rejected save, Stat error = %v", statErr)
	}
}

// writeTemp writes content to a temporary file and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return path
}

// TestWithStoreLock_ConcurrentWritersNoUpdateLost is the goroutine-level
// concurrency proof for [cred.WithStoreLock].
//
// N goroutines each perform a load-modify-save cycle under the lock: they read a
// counter encoded in the AccessToken field, increment it, and write it back.
// After all goroutines finish the counter must equal N — every increment must
// survive, none lost to last-write-wins overwrite.
//
// # Mutation proof
//
// Temporarily removing the TryExclusive call from WithStoreLock so that all
// goroutines entered the critical section simultaneously gives counter < N
// (typically 1–4), confirming last-write-wins overwrites.
func TestWithStoreLock_ConcurrentWritersNoUpdateLost(t *testing.T) {
	const N = 20

	dir := t.TempDir()
	storePath := filepath.Join(dir, "creds.json")
	expiry := time.Now().Add(time.Hour)

	if err := cred.SaveStore(storePath, &cred.DedicatedCredStore{
		AccessToken:   "counter=0",
		RefreshToken:  "rt-init",
		ExpiresAt:     expiry,
		TokenType:     "Bearer",
		TokenEndpoint: "https://auth.example.com/token",
		ClientID:      "test-client",
	}); err != nil {
		t.Fatalf("initial SaveStore: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			err := cred.WithStoreLock(context.Background(), storePath, func(s *cred.DedicatedCredStore) (*cred.DedicatedCredStore, error) {
				if s == nil {
					return nil, fmt.Errorf("store unexpectedly absent")
				}
				var count int
				fmt.Sscanf(s.AccessToken, "counter=%d", &count)
				count++
				return &cred.DedicatedCredStore{
					AccessToken:   fmt.Sprintf("counter=%d", count),
					RefreshToken:  s.RefreshToken,
					ExpiresAt:     s.ExpiresAt,
					TokenType:     s.TokenType,
					TokenEndpoint: s.TokenEndpoint,
					ClientID:      s.ClientID,
				}, nil
			})
			if err != nil {
				t.Errorf("WithStoreLock goroutine error: %v", err)
			}
		}()
	}
	wg.Wait()

	final, err := cred.LoadStore(storePath)
	if err != nil {
		t.Fatalf("LoadStore final: %v", err)
	}
	var got int
	fmt.Sscanf(final.AccessToken, "counter=%d", &got)
	if got != N {
		t.Errorf("counter = %d, want %d (some updates were lost)", got, N)
	}
}
