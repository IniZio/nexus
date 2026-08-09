package cred_test

import (
	"errors"
	"os"
	"path/filepath"
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
