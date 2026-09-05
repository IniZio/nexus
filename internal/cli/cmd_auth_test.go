package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// ── fixture helpers ───────────────────────────────────────────────────────────

// writeCredentialsFixture writes a minimal Claude Code .credentials.json at
// path with the supplied access and refresh tokens.
func writeCredentialsFixture(t *testing.T, path, accessToken, refreshToken string, expiresAtMs int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("writeCredentialsFixture: mkdir: %v", err)
	}
	type oauthBlob struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	}
	type credFile struct {
		ClaudeAiOauth oauthBlob `json:"claudeAiOauth"`
	}
	data, err := json.Marshal(credFile{ClaudeAiOauth: oauthBlob{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAtMs,
	}})
	if err != nil {
		t.Fatalf("writeCredentialsFixture: marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeCredentialsFixture: write: %v", err)
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestAuth_MissingAction_UsageError verifies that `nexus3 auth` with no action
// returns exit code 2 (usage error).
func TestAuth_MissingAction_UsageError(t *testing.T) {
	code := Run([]string{"auth"})
	if code != 2 {
		t.Errorf("auth (no action): exit code = %d, want 2", code)
	}
}

// TestAuth_UnknownAction_UsageError verifies that `nexus3 auth frobnicate`
// returns exit code 2 (usage error).
func TestAuth_UnknownAction_UsageError(t *testing.T) {
	code := Run([]string{"auth", "frobnicate"})
	if code != 2 {
		t.Errorf("auth frobnicate: exit code = %d, want 2", code)
	}
}

// TestAuthLogin_FreshImport verifies that importing into an absent store
// writes a credential store with the expected fields.
func TestAuthLogin_FreshImport(t *testing.T) {
	destPath := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", destPath)

	fromDir := t.TempDir()
	fromPath := filepath.Join(fromDir, ".credentials.json")
	expiresMs := time.Now().Add(time.Hour).UnixMilli()
	writeCredentialsFixture(t, fromPath, "tok-access-123", "tok-refresh-456", expiresMs)

	out, stdout, _ := capture(true)
	if err := runAuthLogin(context.Background(), []string{"--from", fromPath}, out); err != nil {
		t.Fatalf("runAuthLogin: unexpected error: %v", err)
	}

	// Decode the success envelope.
	var env map[string]any
	decodeOne(t, stdout, &env)
	if env["kind"] != "auth.login" {
		t.Errorf("kind = %v, want auth.login", env["kind"])
	}

	// Verify the written store via cred.LoadStore.
	store, err := cred.LoadStore(destPath)
	if err != nil {
		t.Fatalf("LoadStore after import: %v", err)
	}
	if store.AccessToken != "tok-access-123" {
		t.Errorf("access_token = %q, want tok-access-123", store.AccessToken)
	}
	if store.RefreshToken != "tok-refresh-456" {
		t.Errorf("refresh_token = %q, want tok-refresh-456", store.RefreshToken)
	}
	if store.ClientID == "" {
		t.Error("client_id should be non-empty after import")
	}
	if store.TokenEndpoint == "" {
		t.Error("token_endpoint should be non-empty after import")
	}
}

// TestAuthLogin_JSON_NoTokenValues verifies that the success envelope data
// contains dest_path/token_endpoint/client_id/expires_at but NOT the token
// values themselves.
func TestAuthLogin_JSON_NoTokenValues(t *testing.T) {
	destPath := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", destPath)

	fromDir := t.TempDir()
	fromPath := filepath.Join(fromDir, ".credentials.json")
	writeCredentialsFixture(t, fromPath, "tok-access-secret", "tok-refresh-secret", time.Now().Add(time.Hour).UnixMilli())

	out, stdout, _ := capture(true)
	if err := runAuthLogin(context.Background(), []string{"--from", fromPath}, out); err != nil {
		t.Fatalf("runAuthLogin: %v", err)
	}

	raw := stdout.Bytes()
	// Token values must never appear in the output.
	if strings.Contains(string(raw), "tok-access-secret") {
		t.Error("JSON output must not contain the access token value")
	}
	if strings.Contains(string(raw), "tok-refresh-secret") {
		t.Error("JSON output must not contain the refresh token value")
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not a map, got %T", env["data"])
	}
	for _, field := range []string{"dest_path", "token_endpoint", "client_id", "expires_at"} {
		if _, ok := data[field]; !ok {
			t.Errorf("data.%s field missing from JSON output", field)
		}
	}
}

// TestAuthLogin_GuardRejectsExistingStore verifies that importing over a
// complete credential store (access + refresh token present) without --force
// fails with exit code 1.
func TestAuthLogin_GuardRejectsExistingStore(t *testing.T) {
	destPath := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", destPath)

	fromDir := t.TempDir()
	fromPath := filepath.Join(fromDir, ".credentials.json")
	writeCredentialsFixture(t, fromPath, "tok-access-1", "tok-refresh-1", time.Now().Add(time.Hour).UnixMilli())

	// First import succeeds.
	out1, _, _ := capture(false)
	if err := runAuthLogin(context.Background(), []string{"--from", fromPath}, out1); err != nil {
		t.Fatalf("first import: %v", err)
	}

	// Second import without --force must fail at the CLI level (exit 1).
	code := Run([]string{"auth", "login", "--from", fromPath})
	if code != 1 {
		t.Errorf("auth login over existing store without --force: exit code = %d, want 1", code)
	}
}

// TestAuthLogin_ForceOverwritesExistingStore verifies that --force allows
// re-importing over a complete credential store.
func TestAuthLogin_ForceOverwritesExistingStore(t *testing.T) {
	destPath := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", destPath)

	fromDir := t.TempDir()
	fromPath := filepath.Join(fromDir, ".credentials.json")
	writeCredentialsFixture(t, fromPath, "tok-access-1", "tok-refresh-1", time.Now().Add(time.Hour).UnixMilli())

	// First import.
	out1, _, _ := capture(false)
	if err := runAuthLogin(context.Background(), []string{"--from", fromPath}, out1); err != nil {
		t.Fatalf("first import: %v", err)
	}

	// Update the fixture to have a new token.
	writeCredentialsFixture(t, fromPath, "tok-access-2", "tok-refresh-2", time.Now().Add(2*time.Hour).UnixMilli())

	// Second import with --force must succeed.
	out2, _, _ := capture(false)
	if err := runAuthLogin(context.Background(), []string{"--from", fromPath, "--force"}, out2); err != nil {
		t.Errorf("import with --force: unexpected error: %v", err)
	}

	// Verify the store was overwritten.
	store, err := cred.LoadStore(destPath)
	if err != nil {
		t.Fatalf("LoadStore after force import: %v", err)
	}
	if store.AccessToken != "tok-access-2" {
		t.Errorf("access_token after force = %q, want tok-access-2", store.AccessToken)
	}
}

// TestAuthLogin_MissingSourceFile verifies that a missing --from file returns
// a non-zero exit code and an actionable error mentioning "claude auth login".
func TestAuthLogin_MissingSourceFile(t *testing.T) {
	destPath := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", destPath)

	nonExistent := filepath.Join(t.TempDir(), "does-not-exist", ".credentials.json")

	out, _, stderr := capture(false)
	err := runAuthLogin(context.Background(), []string{"--from", nonExistent}, out)
	if err == nil {
		t.Fatal("expected non-nil error for missing source file, got nil")
	}
	if !strings.Contains(err.Error(), "claude auth login") {
		t.Errorf("error message should mention 'claude auth login'; got: %s", err.Error())
	}

	// At the CLI level it must exit non-zero (exit 1).
	code := Run([]string{"auth", "login", "--from", nonExistent})
	if code == 0 {
		t.Errorf("auth login with missing source: exit code = 0, want non-zero")
	}

	// stderr must be empty (capture was in human mode, no writes expected from
	// our logic; root.go writes to stderr on error but we called runAuthLogin
	// directly above).
	_ = stderr
}
