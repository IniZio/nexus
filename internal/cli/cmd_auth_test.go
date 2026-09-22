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

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// ── fixture helpers ───────────────────────────────────────────────────────────

// writeCredentialsFixture writes a minimal .credentials.json with OAuth tokens.
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

// writeCursorAuthFixture writes a minimal cursor auth.json with access token.
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

// buildTestJWT returns a minimal unsigned JWT with exp claim set to expUnix.
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

// TestAuth_MissingAction_UsageError verifies auth with no action returns exit 2.
func TestAuth_MissingAction_UsageError(t *testing.T) {
	code := Run([]string{"auth"})
	if code != 2 {
		t.Errorf("auth (no action): exit code = %d, want 2", code)
	}
}

// TestAuth_UnknownAction_UsageError verifies unknown auth action returns exit 2.
func TestAuth_UnknownAction_UsageError(t *testing.T) {
	code := Run([]string{"auth", "frobnicate"})
	if code != 2 {
		t.Errorf("auth frobnicate: exit code = %d, want 2", code)
	}
}

// TestAuthLogin_ClaudeCodeDefault verifies bare auth login defaults to claude-code import path.
func TestAuthLogin_ClaudeCodeDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NEXUS_DEDICATED_CRED_STORE", filepath.Join(home, ".config", "nexus", "creds.json"))
	out, _, _ := capture(false)
	err := runAuthLogin(context.Background(), []string{}, out)
	if err == nil {
		t.Fatal("runAuthLogin (no args): expected source-not-found error, got nil")
	}
	if !strings.Contains(err.Error(), "source credentials file not found") {
		t.Errorf("expected source-not-found error; got: %v", err)
	}
	if !strings.Contains(err.Error(), "claude-dedicated") {
		t.Errorf("error should reference claude-dedicated path; got: %v", err)
	}
}

// TestAuthLogin_ClaudeCodeExplicit verifies --agent claude-code also hits the import path.
func TestAuthLogin_ClaudeCodeExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NEXUS_DEDICATED_CRED_STORE", filepath.Join(home, ".config", "nexus", "creds.json"))
	out, _, _ := capture(false)
	err := runAuthLogin(context.Background(), []string{"--agent", "claude-code"}, out)
	if err == nil {
		t.Fatal("runAuthLogin --agent claude-code: expected source-not-found error, got nil")
	}
	if !strings.Contains(err.Error(), "source credentials file not found") {
		t.Errorf("expected source-not-found error; got: %v", err)
	}
}

// ── AC-4: unknown --agent fails clearly ──────────────────────────────────────

// TestAuthLogin_UnknownAgent_Error verifies unknown --agent fails with valid agents listed.
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

// TestAuthLoginCursor_WritesNothing verifies cursor auth doesn't write files.
// AC-1, AC-2, AC-6 (RED probe).
func TestAuthLoginCursor_WritesNothing(t *testing.T) {
	claudeStore := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("NEXUS_DEDICATED_CRED_STORE", claudeStore)

	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)

	cursorAuthPath := filepath.Join(xdgHome, "cursor", "auth.json")
	token := buildTestJWT(t, time.Now().Add(24*time.Hour).Unix())
	writeCursorAuthFixture(t, cursorAuthPath, token)

	beforeStat, err := os.Stat(cursorAuthPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	beforeContent, err := os.ReadFile(cursorAuthPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	filesBefore := listFilesUnder(t, xdgHome)

	claudeFrom := filepath.Join(t.TempDir(), ".credentials.json")
	writeCredentialsFixture(t, claudeFrom, "claude-tok-access", "claude-tok-refresh",
		time.Now().Add(time.Hour).UnixMilli())

	out, stdout, _ := capture(true)
	if err := runAuthLogin(context.Background(),
		[]string{"--agent", "cursor", "--from", claudeFrom}, out); err != nil {
		t.Fatalf("runAuthLogin --agent cursor: unexpected error: %v", err)
	}

	filesAfter := listFilesUnder(t, xdgHome)
	if len(filesAfter) != len(filesBefore) {
		t.Errorf("AC-1: new files created in config root: %v",
			setDiff(filesAfter, filesBefore))
	}

	afterStat, err := os.Stat(cursorAuthPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !afterStat.ModTime().Equal(beforeStat.ModTime()) {
		t.Errorf("AC-1: cursor auth.json mtime changed (before=%v after=%v) — verify-only path must not write",
			beforeStat.ModTime(), afterStat.ModTime())
	}

	afterContent, err := os.ReadFile(cursorAuthPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(afterContent) != string(beforeContent) {
		t.Error("AC-1: cursor auth.json content changed — verify-only path must not write")
	}

	if _, err := os.Stat(claudeStore); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("AC-2: claude store should not exist after --agent cursor (stat err=%v)", err)
	}
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

// TestAuthLoginCursor_MissingFile verifies missing cursor auth returns error.
func TestAuthLoginCursor_MissingFile(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	t.Setenv("NEXUS_DEDICATED_CRED_STORE", filepath.Join(t.TempDir(), "creds.json"))

	out, _, _ := capture(false)
	err := runAuthLogin(context.Background(), []string{"--agent", "cursor"}, out)
	if err == nil {
		t.Fatal("expected error for missing cursor credential file, got nil")
	}
	if !strings.Contains(err.Error(), "cursor") {
		t.Errorf("error should mention 'cursor'; got: %v", err)
	}
}

// TestAuthLoginCursor_ReportsExpiry verifies cursor JWT expiry is reported.
func TestAuthLoginCursor_ReportsExpiry(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	t.Setenv("NEXUS_DEDICATED_CRED_STORE", filepath.Join(t.TempDir(), "creds.json"))

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

// TestAuthLoginCursor_NoTokenInOutput verifies token value never appears in output.
// AC-5.
func TestAuthLoginCursor_NoTokenInOutput(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	t.Setenv("NEXUS_DEDICATED_CRED_STORE", filepath.Join(t.TempDir(), "creds.json"))

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

// TestAuthLoginImport_ProfileDriven_MutationProof verifies dispatch through registry ImportFn.
// S20-AC-5, S20-AC-7: mutation probe for hardcoded importFn.
func TestAuthLoginImport_ProfileDriven_MutationProof(t *testing.T) {
	const syntheticFormat cred.CredentialFormat = "s20-oauth-synthetic-v1"
	const sentinelClientID = "SENTINEL-CLIENT-ID-MUST-APPEAR-UNDER-PROFILE-DISPATCH"

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NEXUS_DEDICATED_CRED_STORE", filepath.Join(home, "creds.json"))

	fromDir := t.TempDir()
	fromPath := filepath.Join(fromDir, "agent-creds.json")
	writeCredentialsFixture(t, fromPath, "tok-access-s20", "tok-refresh-s20", time.Now().Add(time.Hour).UnixMilli())

	unregisterFormat := cred.RegisterOAuthFormatForTest(syntheticFormat,
		func(_ cred.AgentProfile) string { return "" },
		func(path string) (*cred.DedicatedCredStore, error) {
			store, err := cred.ImportClaudeCredentials(path)
			if err != nil {
				return nil, err
			}
			store.ClientID = sentinelClientID
			return store, nil
		},
	)
	t.Cleanup(unregisterFormat)

	syntheticProfile := cred.AgentProfile{
		Name:             "s20-synthetic-agent",
		CredentialFormat: syntheticFormat,
	}
	unregisterProfile, err := cred.RegisterProfileForTest(syntheticProfile)
	if err != nil {
		t.Fatalf("register synthetic profile: %v", err)
	}
	t.Cleanup(unregisterProfile)

	agentCredsDir := filepath.Join(home, ".config", "nexus", "agent-creds")
	if err := os.MkdirAll(agentCredsDir, 0o700); err != nil {
		t.Fatalf("create agent-creds dir: %v", err)
	}

	out, stdout, _ := capture(true)
	if err := runAuthLogin(context.Background(),
		[]string{"--agent", "s20-synthetic-agent", "--from", fromPath}, out); err != nil {
		t.Fatalf("runAuthLogin --agent s20-synthetic-agent: %v", err)
	}

	syntheticDest := filepath.Join(home, ".config", "nexus", "agent-creds", "s20-synthetic-agent.json")
	store, err := cred.LoadStore(syntheticDest)
	if err != nil {
		t.Fatalf("LoadStore after import: %v", err)
	}
	if store.ClientID != sentinelClientID {
		t.Errorf("S20-AC-5: store.ClientID = %q, want sentinel %q\n"+
			"(mutation: dispatch used hardcoded importer instead of registry ImportFromPathFn)",
			store.ClientID, sentinelClientID)
	}

	raw := stdout.Bytes()
	if !strings.Contains(string(raw), sentinelClientID) {
		t.Errorf("S20-AC-5: sentinel client_id %q not found in output; got: %s",
			sentinelClientID, raw)
	}
}
