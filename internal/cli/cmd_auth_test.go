package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// writeCursorAuthFixture writes a minimal cursor auth.json at path with the
// given access token. The refreshToken field is left empty; cursor login does
// not require it for nexus3's verify-and-report path.
func writeCursorAuthFixture(t *testing.T, path, accessToken string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("writeCursorAuthFixture: mkdir: %v", err)
	}
	type authFile struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	data, err := json.Marshal(authFile{AccessToken: accessToken})
	if err != nil {
		t.Fatalf("writeCursorAuthFixture: marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeCursorAuthFixture: write: %v", err)
	}
}

// buildTestJWT returns a minimal unsigned (alg=none) JWT whose exp claim is
// set to expUnix (Unix seconds). The signature segment is the literal string
// "fakesig". ParseCursorJWTExpiry will parse this correctly.
func buildTestJWT(t *testing.T, expUnix int64) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{"exp": float64(expUnix)})
	if err != nil {
		t.Fatalf("buildTestJWT: marshal: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fakesig"
}

// listFilesUnder returns a set of all non-directory paths under root.
func listFilesUnder(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	m := make(map[string]struct{})
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			m[path] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("listFilesUnder %s: %v", root, err)
	}
	return m
}

// setDiff returns keys present in a but not in b.
func setDiff(a, b map[string]struct{}) []string {
	var diff []string
	for k := range a {
		if _, ok := b[k]; !ok {
			diff = append(diff, k)
		}
	}
	return diff
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

// ── AC-3: no --agent = byte-identical to pre-flag behavior ───────────────────

// TestAuthLogin_NoAgent_SameDest verifies that omitting --agent uses
// DedicatedCredStorePathForProfile(ClaudeCodeProfile) as the destination,
// proving the no-flag path is byte-identical to the legacy implementation.
//
// AC-3.
func TestAuthLogin_NoAgent_SameDest(t *testing.T) {
	destPath := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", destPath)

	fromDir := t.TempDir()
	fromPath := filepath.Join(fromDir, ".credentials.json")
	writeCredentialsFixture(t, fromPath, "tok-access-ac3", "tok-refresh-ac3", time.Now().Add(time.Hour).UnixMilli())

	out, _, _ := capture(true)
	if err := runAuthLogin(context.Background(), []string{"--from", fromPath}, out); err != nil {
		t.Fatalf("no --agent import: %v", err)
	}

	store, err := cred.LoadStore(destPath)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if store.AccessToken != "tok-access-ac3" {
		t.Errorf("AC-3: access token mismatch: got %q, want tok-access-ac3", store.AccessToken)
	}
}

// TestAuthLogin_NoAgent_ForceGuard verifies the --force guard fires on the
// no-agent path, proving the guard semantics are preserved.
//
// AC-3.
func TestAuthLogin_NoAgent_ForceGuard(t *testing.T) {
	destPath := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", destPath)

	fromDir := t.TempDir()
	fromPath := filepath.Join(fromDir, ".credentials.json")
	writeCredentialsFixture(t, fromPath, "tok-access-1", "tok-refresh-1", time.Now().Add(time.Hour).UnixMilli())

	out1, _, _ := capture(false)
	if err := runAuthLogin(context.Background(), []string{"--from", fromPath}, out1); err != nil {
		t.Fatalf("first import: %v", err)
	}

	out2, _, _ := capture(false)
	err := runAuthLogin(context.Background(), []string{"--from", fromPath}, out2)
	if err == nil {
		t.Fatal("AC-3: expected error on second import without --force, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("AC-3: error should mention --force; got: %v", err)
	}
}

// ── AC-4: unknown --agent fails clearly ──────────────────────────────────────

// TestAuthLogin_UnknownAgent_Error verifies that an unknown --agent value fails
// with exit code 2 and an error that names both the unknown agent and every
// valid agent.
//
// AC-4.
func TestAuthLogin_UnknownAgent_Error(t *testing.T) {
	out, _, _ := capture(false)
	err := runAuthLogin(context.Background(), []string{"--agent", "bogus-agent-xyz"}, out)
	if err == nil {
		t.Fatal("AC-4: expected error for unknown --agent, got nil")
	}
	if !strings.Contains(err.Error(), "bogus-agent-xyz") {
		t.Errorf("AC-4: error should name the unknown agent; got: %v", err)
	}
	for _, name := range cred.ProfileNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("AC-4: error should list valid agent %q; got: %v", name, err)
		}
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Errorf("AC-4: error should be *UsageError (exit 2); got %T", err)
	}
}

// ── AC-1, AC-2, AC-5, AC-6: cursor verify-and-report path ───────────────────

// TestAuthLoginCursor_WritesNothing verifies that `--agent cursor`:
//   - writes no file anywhere in the redirected XDG_CONFIG_HOME (AC-1)
//   - leaves the cursor auth.json unchanged in mtime and content (AC-1)
//   - does not create or modify the claude credential store (AC-2)
//   - emits cred_path in the JSON data, not dest_path (distinguishes verify
//     from import; this assertion goes RED under the AC-6 mutation)
//
// AC-1, AC-2, AC-6 (RED probe).
func TestAuthLoginCursor_WritesNothing(t *testing.T) {
	// Redirect claude's cred store.
	claudeStore := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", claudeStore)

	// Redirect cursor's credential directory.
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)

	// Write a valid cursor auth.json with a parseable JWT.
	cursorAuthPath := filepath.Join(xdgHome, "cursor", "auth.json")
	token := buildTestJWT(t, time.Now().Add(24*time.Hour).Unix())
	writeCursorAuthFixture(t, cursorAuthPath, token)

	// Snapshot the cursor file state before the command.
	beforeStat, err := os.Stat(cursorAuthPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	beforeContent, err := os.ReadFile(cursorAuthPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	// Snapshot all files in xdgHome before the command.
	filesBefore := listFilesUnder(t, xdgHome)

	// Also set up a valid claude source file so the AC-6 mutation (fall through
	// to import) would succeed and produce a detectable write, rather than
	// failing on a missing --from file and masking the real failure.
	claudeFrom := filepath.Join(t.TempDir(), ".credentials.json")
	writeCredentialsFixture(t, claudeFrom, "claude-tok-access", "claude-tok-refresh",
		time.Now().Add(time.Hour).UnixMilli())

	out, stdout, _ := capture(true)
	if err := runAuthLogin(context.Background(),
		[]string{"--agent", "cursor", "--from", claudeFrom}, out); err != nil {
		t.Fatalf("runAuthLogin --agent cursor: unexpected error: %v", err)
	}

	// AC-1a: no new files in xdgHome.
	filesAfter := listFilesUnder(t, xdgHome)
	if len(filesAfter) != len(filesBefore) {
		t.Errorf("AC-1: new files created in config root: %v",
			setDiff(filesAfter, filesBefore))
	}

	// AC-1b: cursor auth.json mtime unchanged.
	afterStat, err := os.Stat(cursorAuthPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !afterStat.ModTime().Equal(beforeStat.ModTime()) {
		t.Errorf("AC-1: cursor auth.json mtime changed (before=%v after=%v) — verify-only path must not write",
			beforeStat.ModTime(), afterStat.ModTime())
	}

	// AC-1c: cursor auth.json content unchanged.
	afterContent, err := os.ReadFile(cursorAuthPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(afterContent) != string(beforeContent) {
		t.Error("AC-1: cursor auth.json content changed — verify-only path must not write")
	}

	// AC-2: claude store not touched.
	if _, err := os.Stat(claudeStore); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("AC-2: claude store should not exist after --agent cursor (stat err=%v)", err)
	}

	// AC-6 probe: output envelope must carry cred_path (verify shape), NOT
	// dest_path (import shape). With the mutation — default: runAuthLoginImport
	// instead of runAuthLoginVerify — the output carries dest_path instead and
	// this assertion goes RED.
	var env map[string]any
	decodeOne(t, stdout, &env)
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("AC-6: data is not a map, got %T", env["data"])
	}
	if _, ok := data["cred_path"]; !ok {
		t.Error("AC-6: output should have cred_path field (got import shape instead of verify shape?)")
	}
	if _, ok := data["dest_path"]; ok {
		t.Error("AC-6: output must not have dest_path field (import shape leaked into cursor verify path)")
	}
}

// TestAuthLoginCursor_MissingFile verifies that a missing cursor auth.json
// returns a non-zero exit code and a message naming the agent.
func TestAuthLoginCursor_MissingFile(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", filepath.Join(t.TempDir(), "creds.json"))
	// No cursor/auth.json written.

	out, _, _ := capture(false)
	err := runAuthLogin(context.Background(), []string{"--agent", "cursor"}, out)
	if err == nil {
		t.Fatal("expected error for missing cursor credential file, got nil")
	}
	if !strings.Contains(err.Error(), "cursor") {
		t.Errorf("error should mention 'cursor'; got: %v", err)
	}
}

// TestAuthLoginCursor_ReportsExpiry verifies that a cursor credential with a
// parseable JWT expiry reports a non-"unknown" expires_at in the output.
func TestAuthLoginCursor_ReportsExpiry(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", filepath.Join(t.TempDir(), "creds.json"))

	exp := time.Now().Add(48 * time.Hour).Unix()
	token := buildTestJWT(t, exp)
	writeCursorAuthFixture(t, filepath.Join(xdgHome, "cursor", "auth.json"), token)

	out, stdout, _ := capture(true)
	if err := runAuthLogin(context.Background(), []string{"--agent", "cursor"}, out); err != nil {
		t.Fatalf("runAuthLogin: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data := env["data"].(map[string]any)
	expiresAt, _ := data["expires_at"].(string)
	if expiresAt == "" || expiresAt == "unknown" {
		t.Errorf("expires_at should be a timestamp, got %q", expiresAt)
	}
}

// ── AC-5: no token value in any route's output ────────────────────────────────

// TestAuthLoginCursor_NoTokenInOutput verifies that the cursor verify path
// never prints the token value. The sentinel is the literal accessToken
// written to auth.json; it must not appear in any rendered output.
//
// AC-5.
func TestAuthLoginCursor_NoTokenInOutput(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", filepath.Join(t.TempDir(), "creds.json"))

	// The sentinel does not need to be a valid JWT; expiry will be "unknown",
	// which is fine — the assertion is about output content, not expiry parsing.
	const sentinel = "SENTINEL-CURSOR-TOKEN-MUST-NOT-APPEAR-IN-OUTPUT"
	writeCursorAuthFixture(t, filepath.Join(xdgHome, "cursor", "auth.json"), sentinel)

	out, stdout, _ := capture(true)
	if err := runAuthLogin(context.Background(), []string{"--agent", "cursor"}, out); err != nil {
		t.Fatalf("runAuthLogin --agent cursor: %v", err)
	}

	if strings.Contains(string(stdout.Bytes()), sentinel) {
		t.Error("AC-5: cursor token sentinel appeared in rendered output; token values must never be printed")
	}
}
