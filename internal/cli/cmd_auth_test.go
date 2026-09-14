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

// TestAuthLogin_ClaudeCodeNoOp verifies that `nexus3 auth login` for claude-code
// (the default — no --agent flag) prints a no-op message and returns nil,
// since credentials come from the live-mounted ~/.claude directory.
func TestAuthLogin_ClaudeCodeNoOp(t *testing.T) {
	out, stdout, _ := capture(false)
	err := runAuthLogin(context.Background(), []string{}, out)
	if err != nil {
		t.Fatalf("runAuthLogin: unexpected error: %v", err)
	}
	msg := stdout.String()
	if !strings.Contains(msg, "no longer needed") {
		t.Errorf("expected no-op message containing 'no longer needed'; got: %q", msg)
	}
	if !strings.Contains(msg, "claude login") {
		t.Errorf("expected no-op message containing 'claude login'; got: %q", msg)
	}
}

// TestAuthLogin_ClaudeCodeExplicit_NoOp verifies the same no-op for an explicit
// --agent claude-code flag.
func TestAuthLogin_ClaudeCodeExplicit_NoOp(t *testing.T) {
	out, stdout, _ := capture(false)
	err := runAuthLogin(context.Background(), []string{"--agent", "claude-code"}, out)
	if err != nil {
		t.Fatalf("runAuthLogin --agent claude-code: unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "no longer needed") {
		t.Errorf("expected no-op message; got: %q", stdout.String())
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

// ── AC-5 S20: profile-driven import dispatch — mutation proof ─────────────────

// TestAuthLoginImport_ProfileDriven_MutationProof verifies that the import
// route for CredentialFormatNone agents dispatches through the cred registry's
// ImportFromPathFn, not through a hardcoded per-agent importer.
//
// The mutation being probed is: "replace the registry-derived importFn with a
// hardcoded call to cred.ImportClaudeCredentials inside runAuthLogin or
// runAuthLoginImport."  Under the mutation the registered importFn is bypassed;
// the test goes RED because the registry's sentinel ClientID never appears.
//
// S20-AC-5, S20-AC-7.
func TestAuthLoginImport_ProfileDriven_MutationProof(t *testing.T) {
	const syntheticFormat cred.CredentialFormat = "s20-oauth-synthetic-v1"
	const sentinelClientID = "SENTINEL-CLIENT-ID-MUST-APPEAR-UNDER-PROFILE-DISPATCH"

	// Redirect HOME so DedicatedCredStorePathForProfile resolves to a temp dir.
	// The synthetic profile's dest path is $HOME/.config/nexus3/agent-creds/<name>.json.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Also redirect the claude-code path (NEXUS3_DEDICATED_CRED_STORE) to
	// avoid any accidental write to the real store.
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", filepath.Join(home, "creds.json"))

	// Write a minimal credential fixture.  The synthetic importFn reads it.
	fromDir := t.TempDir()
	fromPath := filepath.Join(fromDir, "agent-creds.json")
	writeCredentialsFixture(t, fromPath, "tok-access-s20", "tok-refresh-s20", time.Now().Add(time.Hour).UnixMilli())

	// Register a synthetic CredentialFormatNone-like format in the cred registry.
	// DefaultFromPathFn returns an empty string (the test always passes --from).
	// ImportFromPathFn returns a store whose ClientID carries the sentinel.
	unregisterFormat := cred.RegisterOAuthFormatForTest(syntheticFormat,
		func(_ cred.AgentProfile) string { return "" },
		func(path string) (*cred.DedicatedCredStore, error) {
			// Delegate to the real claude importer for the credential shape,
			// then stamp the sentinel ClientID so the test can verify dispatch.
			store, err := cred.ImportClaudeCredentials(path)
			if err != nil {
				return nil, err
			}
			store.ClientID = sentinelClientID
			return store, nil
		},
	)
	t.Cleanup(unregisterFormat)

	// Register a synthetic agent profile that uses this format.
	syntheticProfile := cred.AgentProfile{
		Name:             "s20-synthetic-agent",
		CredentialFormat: syntheticFormat,
	}
	unregisterProfile, err := cred.RegisterProfileForTest(syntheticProfile)
	if err != nil {
		t.Fatalf("register synthetic profile: %v", err)
	}
	t.Cleanup(unregisterProfile)

	// Pre-create the parent directory that DedicatedCredStorePathForProfile
	// will write into: $HOME/.config/nexus3/agent-creds/.
	agentCredsDir := filepath.Join(home, ".config", "nexus3", "agent-creds")
	if err := os.MkdirAll(agentCredsDir, 0o700); err != nil {
		t.Fatalf("create agent-creds dir: %v", err)
	}

	out, stdout, _ := capture(true)
	if err := runAuthLogin(context.Background(),
		[]string{"--agent", "s20-synthetic-agent", "--from", fromPath}, out); err != nil {
		t.Fatalf("runAuthLogin --agent s20-synthetic-agent: %v", err)
	}

	// The written store must carry the sentinel ClientID — proving the registry's
	// ImportFromPathFn was called, not any hardcoded importer.
	// The dest path is profile-derived: $HOME/.config/nexus3/agent-creds/s20-synthetic-agent.json.
	syntheticDest := filepath.Join(home, ".config", "nexus3", "agent-creds", "s20-synthetic-agent.json")
	store, err := cred.LoadStore(syntheticDest)
	if err != nil {
		t.Fatalf("LoadStore after import: %v", err)
	}
	if store.ClientID != sentinelClientID {
		t.Errorf("S20-AC-5: store.ClientID = %q, want sentinel %q\n"+
			"(mutation: dispatch used hardcoded importer instead of registry ImportFromPathFn)",
			store.ClientID, sentinelClientID)
	}

	// The JSON output must also reflect the sentinel (via token_endpoint or
	// client_id in the success envelope).
	raw := stdout.Bytes()
	if !strings.Contains(string(raw), sentinelClientID) {
		t.Errorf("S20-AC-5: sentinel client_id %q not found in output; got: %s",
			sentinelClientID, raw)
	}
}

