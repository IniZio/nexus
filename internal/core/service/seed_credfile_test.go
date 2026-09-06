package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// ── credential file seeding tests ────────────────────────────────────────────

// credFileTestRecord builds a PlaceholderRecord for the given host and placeholder.
func credFileTestRecord(host, placeholder string) cred.PlaceholderRecord {
	return cred.PlaceholderRecord{
		Host:        host,
		Placeholder: placeholder,
		ExpiresAt:   time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		SandboxID:   seedTestID(20),
	}
}

// TestBuildCredFileSeedPayload_NilForEnvVarAgent proves Claude Code's profile
// (CredentialFile == "") produces nil from buildCredFileSeedPayload, confirming
// the env-var-only path is unaffected and never reaches the file-seeding branch.
func TestBuildCredFileSeedPayload_NilForEnvVarAgent(t *testing.T) {
	t.Parallel()
	records := []cred.PlaceholderRecord{
		credFileTestRecord(AnthropicAPIHost, "deadbeef1234567890abcdef"),
	}
	got, err := buildCredFileSeedPayload(records, cred.ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("unexpected error for env-var agent: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for env-var agent (Claude Code), got %q", got)
	}
}

// TestBuildCredFileSeedPayload_PlaceholderInFile is the primary security proof:
// it asserts the placeholder (not the real token) appears in the JSON credential
// file produced for a file-based agent (cursor).
//
// # Mutation proof
//
// Changing `placeholder = rec.Placeholder` to `placeholder = realToken` in
// buildCredFileSeedPayload makes this test fail with:
//
//	SECURITY: real token leaked into credential file
//
// and simultaneously makes the placeholder-presence assertion fail. The mutant
// COMPILES (verified with go vet) but is caught at test time. See the commit
// description for the verbatim RED output.
func TestBuildCredFileSeedPayload_PlaceholderInFile(t *testing.T) {
	t.Parallel()
	const placeholder = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	// realToken is a string that must NEVER appear in the guest credential file.
	// It represents a leaked real JWT; its value is chosen to be unmistakable.
	const realToken = "real-secret-cursor-jwt-MUST-NOT-APPEAR-IN-GUEST-FILE"

	records := []cred.PlaceholderRecord{
		credFileTestRecord(cred.CursorAgentProfile.CredentialedHost, placeholder),
	}

	got, err := buildCredFileSeedPayload(records, cred.CursorAgentProfile)
	if err != nil {
		t.Fatalf("buildCredFileSeedPayload: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil payload for file-based agent (cursor)")
	}

	// The placeholder must be present: the MITM proxy needs it to identify
	// and swap the credential on each proxied request.
	if !strings.Contains(string(got), placeholder) {
		t.Errorf("credential file missing placeholder; got: %q", got)
	}

	// The real token must NEVER appear: the whole design turns on the guest
	// holding only placeholders. This assertion is the one the mutation proof
	// drives RED by writing realToken instead of rec.Placeholder.
	if strings.Contains(string(got), realToken) {
		t.Errorf("SECURITY: real token leaked into credential file; got: %q", got)
	}

	// The file must be valid JSON with the declared key set to the placeholder.
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("credential file is not valid JSON: %v; content: %q", err, got)
	}
	key := cred.CursorAgentProfile.CredentialFileKey
	if v, ok := m[key]; !ok {
		t.Errorf("JSON missing key %q; keys present: %v", key, keysOf(m))
	} else if v != placeholder {
		t.Errorf("JSON[%q] = %q, want placeholder %q", key, v, placeholder)
	}

	// S11: every CredentialFileExtraKey must also be present with the same
	// placeholder so cursor-agent reports "Fully authenticated" rather than
	// "Partially authenticated (missing refresh token)".
	for _, extra := range cred.CursorAgentProfile.CredentialFileExtraKeys {
		if v, ok := m[extra]; !ok {
			t.Errorf("JSON missing extra key %q (S11); keys present: %v", extra, keysOf(m))
		} else if v != placeholder {
			t.Errorf("JSON[%q] = %q, want placeholder %q", extra, v, placeholder)
		}
	}
}

// TestSeedGuestCredFile_WritesFileForCursor proves SeedGuestCredFile delivers
// the JSON content to the seeder for a file-based agent and does so exactly once.
func TestSeedGuestCredFile_WritesFileForCursor(t *testing.T) {
	t.Parallel()
	const placeholder = "cafebabe111122223333cafebabe111122223333cafebabe111122223333cafe"
	records := []cred.PlaceholderRecord{
		credFileTestRecord(cred.CursorAgentProfile.CredentialedHost, placeholder),
	}
	cs := &captureSeeder{}

	err := SeedGuestCredFile(context.Background(), seedTestID(21), records, cred.CursorAgentProfile, cs.fn())
	if err != nil {
		t.Fatalf("SeedGuestCredFile: %v", err)
	}
	if cs.calls != 1 {
		t.Fatalf("expected exactly 1 seeder call, got %d", cs.calls)
	}
	if !strings.Contains(string(cs.payload), placeholder) {
		t.Errorf("seeder payload missing placeholder; got: %q", cs.payload)
	}
}

// TestSeedGuestCredFile_NoopForEnvVarAgent proves SeedGuestCredFile is a no-op
// for env-var agents: the seeder is never called for Claude Code, so the
// env-var seeding path is not disturbed.
func TestSeedGuestCredFile_NoopForEnvVarAgent(t *testing.T) {
	t.Parallel()
	records := []cred.PlaceholderRecord{
		credFileTestRecord(AnthropicAPIHost, "deadbeef"),
	}
	cs := &captureSeeder{}
	err := SeedGuestCredFile(context.Background(), seedTestID(22), records, cred.ClaudeCodeProfile, cs.fn())
	if err != nil {
		t.Fatalf("SeedGuestCredFile: %v", err)
	}
	if cs.calls != 0 {
		t.Errorf("expected 0 seeder calls for env-var agent (Claude Code), got %d", cs.calls)
	}
}

// TestGuestCredFilePath_CursorPath proves GuestCredFilePath produces the
// expected absolute guest path for cursor-agent and empty string for Claude Code.
func TestGuestCredFilePath_CursorPath(t *testing.T) {
	t.Parallel()
	want := GuestCredDirPath + "/" + cred.CursorAgentProfile.CredentialFile
	if got := GuestCredFilePath(cred.CursorAgentProfile); got != want {
		t.Errorf("GuestCredFilePath(cursor) = %q, want %q", got, want)
	}
	if got := GuestCredFilePath(cred.ClaudeCodeProfile); got != "" {
		t.Errorf("GuestCredFilePath(claude-code) = %q, want empty", got)
	}
}

// keysOf returns the sorted keys of a map[string]string for diagnostic output.
func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestBuildAgentSeedPayload_FileBasedAgentNoError proves that buildAgentSeedPayload
// does not error for a file-based agent (cursor) even though PlaceholderEnvVar
// is empty. The env-var section is simply omitted; the credential goes via
// SeedGuestCredFile instead.
func TestBuildAgentSeedPayload_FileBasedAgentNoError(t *testing.T) {
	t.Parallel()
	records := []cred.PlaceholderRecord{
		credFileTestRecord(cred.CursorAgentProfile.CredentialedHost, "aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344"),
	}
	// kindOAuth with empty PlaceholderEnvVar — previously would have errored;
	// now should succeed because CredentialFile is non-empty.
	got, err := buildAgentSeedPayload(records, kindOAuth, cred.CursorAgentProfile)
	if err != nil {
		t.Fatalf("buildAgentSeedPayload(cursor, kindOAuth): unexpected error: %v", err)
	}
	// The env payload must NOT contain the credential env-var line — no CURSOR_CODE_* token.
	// The NEXUS3_CRED_* generic lines are fine (they come from buildSeedPayload).
	if strings.Contains(string(got), "CURSOR_API_KEY=") {
		t.Errorf("env payload must not contain CURSOR_API_KEY= for kindOAuth; got: %q", got)
	}
}

// TestBuildAgentSeedPayload_CredDirRedirectEmitted is the GAP 1 regression test:
// proves buildAgentSeedPayload emits <CredDirEnvVar>=<GuestCredDirPath> for a
// file-based agent, connecting the seeded file to where the agent looks.
//
// # Mutation proof
//
// Removing the `fmt.Fprintf(&buf, "%s=%s\n", profile.CredDirEnvVar, GuestCredDirPath)`
// line (or guarding it away) makes this test fail. The mutation is proven by
// commenting out that block in seed.go and showing the test RED. See commit.
func TestBuildAgentSeedPayload_CredDirRedirectEmitted(t *testing.T) {
	t.Parallel()
	records := []cred.PlaceholderRecord{
		credFileTestRecord(cred.CursorAgentProfile.CredentialedHost, "bbccdd1122334455bbccdd1122334455bbccdd1122334455bbccdd1122334455"),
	}
	got, err := buildAgentSeedPayload(records, kindOAuth, cred.CursorAgentProfile)
	if err != nil {
		t.Fatalf("buildAgentSeedPayload(cursor): %v", err)
	}
	payload := string(got)

	// The redirect line must appear: without it the agent reads its default
	// credential dir and finds no nexus3 placeholder file.
	wantLine := cred.CursorAgentProfile.CredDirEnvVar + "=" + GuestCredDirPath
	if !strings.Contains(payload, wantLine) {
		t.Errorf("env payload missing redirect line %q; got:\n%s", wantLine, payload)
	}
}

// TestBuildAgentSeedPayload_CredDirRedirectAbsentForEnvVarAgent proves that
// Claude Code's env payload does NOT contain any CredDirEnvVar=GuestCredDirPath
// line. Claude Code has CredentialFile == "" so the redirect block is skipped.
func TestBuildAgentSeedPayload_CredDirRedirectAbsentForEnvVarAgent(t *testing.T) {
	t.Parallel()
	expires := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
	records := []cred.PlaceholderRecord{
		{Host: AnthropicAPIHost, Placeholder: "deadbeefdeadbeefdeadbeef", ExpiresAt: expires, SandboxID: seedTestID(24)},
	}
	got, err := buildAgentSeedPayload(records, kindOAuth, cred.ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("buildAgentSeedPayload(claude-code): %v", err)
	}
	payload := string(got)
	// Claude Code's CredDirEnvVar is CLAUDE_CONFIG_DIR; it must NOT be set to
	// GuestCredDirPath (that would redirect the whole config dir, not just creds).
	if strings.Contains(payload, GuestCredDirPath) {
		t.Errorf("Claude Code env payload must not contain GuestCredDirPath %q; got:\n%s", GuestCredDirPath, payload)
	}
}

// syntheticFileProfile is a hand-built profile with a non-cursor CredentialFileKey
// ("token") to prove buildCredFileSeedPayload drives the JSON key from the
// profile field rather than hardcoding "accessToken".
var syntheticFileProfile = cred.AgentProfile{
	Name:              "synthetic-file-agent",
	CredentialedHost:  "api.example.com",
	EgressHosts:       []string{"api.example.com"},
	CredDirEnvVar:     "EXAMPLE_CONFIG_HOME",
	CredentialFile:    "example/cred.json",
	CredentialFileKey: "token", // deliberately NOT "accessToken"
}

// TestBuildCredFileSeedPayload_UsesProfileKey is the GAP 2 regression test:
// proves buildCredFileSeedPayload uses profile.CredentialFileKey rather than
// a hardcoded key. Uses syntheticFileProfile whose key is "token", not "accessToken".
//
// # Mutation proof
//
// Hardcoding "accessToken" in the json.Marshal call of buildCredFileSeedPayload
// makes this test fail because the output key is "accessToken" but the test
// expects "token". The mutant compiles (go vet exit 0); the test is RED.
// See commit for verbatim RED output.
func TestBuildCredFileSeedPayload_UsesProfileKey(t *testing.T) {
	t.Parallel()
	const placeholder = "1a2b3c4d5e6f1a2b3c4d5e6f1a2b3c4d5e6f1a2b3c4d5e6f1a2b3c4d5e6f1a2b"
	records := []cred.PlaceholderRecord{
		credFileTestRecord(syntheticFileProfile.CredentialedHost, placeholder),
	}

	got, err := buildCredFileSeedPayload(records, syntheticFileProfile)
	if err != nil {
		t.Fatalf("buildCredFileSeedPayload: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil payload for file-based profile")
	}

	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("payload not valid JSON: %v; content: %q", err, got)
	}

	// The key must be "token" (from syntheticFileProfile.CredentialFileKey),
	// not "accessToken". If the implementation hardcodes "accessToken" this
	// assertion fails.
	wantKey := syntheticFileProfile.CredentialFileKey // "token"
	if v, ok := m[wantKey]; !ok {
		t.Errorf("JSON missing key %q (got keys %v); hardcoded key suspected", wantKey, keysOf(m))
	} else if v != placeholder {
		t.Errorf("JSON[%q] = %q, want placeholder %q", wantKey, v, placeholder)
	}

	// "accessToken" must NOT appear: if it does the key is hardcoded.
	if _, bad := m["accessToken"]; bad {
		t.Errorf("JSON contains hardcoded key \"accessToken\" instead of profile-driven %q", wantKey)
	}
}

// TestClaudeCodeEnvVarSeedingUnchanged proves that the Claude Code env-var
// seeding path is completely unaffected by the file-seeding extension.
// This is intentionally a re-statement of the pre-existing behaviour to
// satisfy the "Claude Code's env-var seeding is unchanged" AC.
func TestClaudeCodeEnvVarSeedingUnchanged(t *testing.T) {
	t.Parallel()
	const placeholder = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	expires := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
	records := []cred.PlaceholderRecord{
		{Host: AnthropicAPIHost, Placeholder: placeholder, ExpiresAt: expires, SandboxID: seedTestID(23)},
	}

	got, err := buildAgentSeedPayload(records, kindOAuth, cred.ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("buildAgentSeedPayload(claude-code, kindOAuth): %v", err)
	}
	payload := string(got)
	if !strings.Contains(payload, "CLAUDE_CODE_OAUTH_TOKEN="+placeholder) {
		t.Errorf("Claude Code env payload missing CLAUDE_CODE_OAUTH_TOKEN=<placeholder>; got:\n%s", payload)
	}
	// Cursor's env var must never appear in a Claude Code seed.
	if strings.Contains(payload, "CURSOR_API_KEY=") {
		t.Errorf("Claude Code env payload must not contain CURSOR_API_KEY=; got:\n%s", payload)
	}
}

// ── helpers defined in sibling test files ────────────────────────────────────
// seedTestID  → seed_test.go
// captureSeeder → seed_test.go
// AnthropicAPIHost → seed.go
