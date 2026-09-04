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
