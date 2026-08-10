package cred

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testCredJSON = `{
	"claudeAiOauth": {
		"accessToken":  "test-access-token",
		"refreshToken": "test-refresh-token",
		"expiresAt":    1786322465598,
		"scopes":       ["user:profile", "user:inference"],
		"subscriptionType": "pro",
		"rateLimitTier": "standard"
	}
}`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writeFixture: %v", err)
	}
	return p
}

// TestImportClaudeCredentials_RoundTrip verifies that a valid .credentials.json
// is correctly mapped to a DedicatedCredStore.
func TestImportClaudeCredentials_RoundTrip(t *testing.T) {
	path := writeFixture(t, testCredJSON)

	store, err := ImportClaudeCredentials(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.AccessToken != "test-access-token" {
		t.Errorf("AccessToken = %q; want %q", store.AccessToken, "test-access-token")
	}
	if store.RefreshToken != "test-refresh-token" {
		t.Errorf("RefreshToken = %q; want %q", store.RefreshToken, "test-refresh-token")
	}

	wantExpiry := time.UnixMilli(1786322465598)
	if !store.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v; want %v", store.ExpiresAt, wantExpiry)
	}

	if store.TokenType != "Bearer" {
		t.Errorf("TokenType = %q; want \"Bearer\"", store.TokenType)
	}
	if store.ClientID != ClaudeCodeClientID {
		t.Errorf("ClientID = %q; want %q", store.ClientID, ClaudeCodeClientID)
	}
	if store.ClientSecret != "" {
		t.Errorf("ClientSecret = %q; want empty (public client)", store.ClientSecret)
	}
	if store.TokenEndpoint != ClaudeCodeTokenEndpoint {
		t.Errorf("TokenEndpoint = %q; want %q", store.TokenEndpoint, ClaudeCodeTokenEndpoint)
	}
}

// TestImportClaudeCredentials_MissingFile verifies that a nonexistent path
// returns an error wrapping os.ErrNotExist.
func TestImportClaudeCredentials_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	_, err := ImportClaudeCredentials(path)
	if err == nil {
		t.Fatal("expected error for missing file; got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error does not wrap os.ErrNotExist: %v", err)
	}
}

// TestImportClaudeCredentials_EmptyTokens verifies that empty accessToken and
// empty refreshToken each produce a descriptive error with no store returned.
func TestImportClaudeCredentials_EmptyTokens(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantSub string
	}{
		{
			name: "empty_accessToken",
			content: `{"claudeAiOauth": {"accessToken": "", "refreshToken": "r", "expiresAt": 0}}`,
			wantSub: "accessToken is empty",
		},
		{
			name: "empty_refreshToken",
			content: `{"claudeAiOauth": {"accessToken": "a", "refreshToken": "", "expiresAt": 0}}`,
			wantSub: "refreshToken is empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFixture(t, tc.content)
			store, err := ImportClaudeCredentials(path)
			if err == nil {
				t.Fatalf("expected error; got nil (store=%+v)", store)
			}
			if store != nil {
				t.Errorf("expected nil store on error; got %+v", store)
			}
			// The error message should be descriptive.
			errMsg := err.Error()
			found := false
			for _, needle := range []string{tc.wantSub} {
				if containsStr(errMsg, needle) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("error %q does not mention %q", errMsg, tc.wantSub)
			}
		})
	}
}

// TestImportClaudeCredentials_SaveLoadRoundTrip verifies that an imported store
// survives a SaveStore → LoadStore cycle with all 7 fields intact.
func TestImportClaudeCredentials_SaveLoadRoundTrip(t *testing.T) {
	src := writeFixture(t, testCredJSON)

	store, err := ImportClaudeCredentials(src)
	if err != nil {
		t.Fatalf("ImportClaudeCredentials: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "store.json")
	if err := SaveStore(dst, store); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	loaded, err := LoadStore(dst)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	if loaded.AccessToken != store.AccessToken {
		t.Errorf("AccessToken mismatch: %q vs %q", loaded.AccessToken, store.AccessToken)
	}
	if loaded.RefreshToken != store.RefreshToken {
		t.Errorf("RefreshToken mismatch: %q vs %q", loaded.RefreshToken, store.RefreshToken)
	}
	if !loaded.ExpiresAt.Equal(store.ExpiresAt) {
		t.Errorf("ExpiresAt mismatch: %v vs %v", loaded.ExpiresAt, store.ExpiresAt)
	}
	if loaded.TokenType != store.TokenType {
		t.Errorf("TokenType mismatch: %q vs %q", loaded.TokenType, store.TokenType)
	}
	if loaded.ClientID != store.ClientID {
		t.Errorf("ClientID mismatch: %q vs %q", loaded.ClientID, store.ClientID)
	}
	if loaded.ClientSecret != store.ClientSecret {
		t.Errorf("ClientSecret mismatch: %q vs %q", loaded.ClientSecret, store.ClientSecret)
	}
	if loaded.TokenEndpoint != store.TokenEndpoint {
		t.Errorf("TokenEndpoint mismatch: %q vs %q", loaded.TokenEndpoint, store.TokenEndpoint)
	}
}

// containsStr is a simple substring check to avoid importing strings in tests.
func containsStr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
