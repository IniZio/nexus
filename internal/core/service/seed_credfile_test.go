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

func credFileTestRecord(host, placeholder string) cred.PlaceholderRecord {
	return cred.PlaceholderRecord{
		Host:        host,
		Placeholder: placeholder,
		ExpiresAt:   time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		SandboxID:   seedTestID(20),
	}
}

// TestBuildCredFileSeedPayload_NilForEnvVarAgent proves Claude Code (CredentialFile=="") yields nil.
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

// TestBuildCredFileSeedPayload_PlaceholderInFile proves placeholder (not real token) in file.
// MUTATION-PIN: changing placeholder=rec.Placeholder to placeholder=realToken fails this test.
func TestBuildCredFileSeedPayload_PlaceholderInFile(t *testing.T) {
	t.Parallel()
	const placeholder = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	/** realToken: leaked real JWT (must NEVER appear in guest file). Unmistakable value for detection. */
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

	if !strings.Contains(string(got), placeholder) {
		t.Errorf("credential file missing placeholder; got: %q", got)
	}
	if strings.Contains(string(got), realToken) {
		t.Errorf("SECURITY: real token leaked into credential file; got: %q", got)
	}
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

	for _, extra := range cred.CursorAgentProfile.CredentialFileExtraKeys {
		if v, ok := m[extra]; !ok {
			t.Errorf("JSON missing extra key %q (S11); keys present: %v", extra, keysOf(m))
		} else if v != placeholder {
			t.Errorf("JSON[%q] = %q, want placeholder %q", extra, v, placeholder)
		}
	}
}

// TestSeedGuestCredFile_WritesFileForCursor proves SeedGuestCredFile delivers JSON to seeder once.
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

// TestSeedGuestCredFile_NoopForEnvVarAgent proves SeedGuestCredFile is a no-op for Claude Code.
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

// TestGuestCredFilePath_CursorPath proves GuestCredFilePath produces expected paths.
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

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestBuildAgentSeedPayload_FileBasedAgentNoError proves buildAgentSeedPayload succeeds for file-based agents.
func TestBuildAgentSeedPayload_FileBasedAgentNoError(t *testing.T) {
	t.Parallel()
	records := []cred.PlaceholderRecord{
		credFileTestRecord(cred.CursorAgentProfile.CredentialedHost, "aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344"),
	}
	got, err := buildAgentSeedPayload(records, kindOAuth, cred.CursorAgentProfile)
	if err != nil {
		t.Fatalf("buildAgentSeedPayload(cursor, kindOAuth): unexpected error: %v", err)
	}
	if strings.Contains(string(got), "CURSOR_API_KEY=") {
		t.Errorf("env payload must not contain CURSOR_API_KEY= for kindOAuth; got: %q", got)
	}
}

// TestBuildAgentSeedPayload_CredDirRedirectEmitted: GAP 1 regression (MUTATION-PIN: CredDirEnvVar line required).
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
	wantLine := cred.CursorAgentProfile.CredDirEnvVar + "=" + GuestCredDirPath
	if !strings.Contains(payload, wantLine) {
		t.Errorf("env payload missing redirect line %q; got:\n%s", wantLine, payload)
	}
}

// TestBuildAgentSeedPayload_CredDirRedirectAbsentForEnvVarAgent proves Claude Code (CredentialFile=="") has no redirect.
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
	if strings.Contains(payload, GuestCredDirPath) {
		t.Errorf("Claude Code env payload must not contain GuestCredDirPath %q; got:\n%s", GuestCredDirPath, payload)
	}
}

// syntheticFileProfile tests that JSON key comes from profile, not hardcoded.
var syntheticFileProfile = cred.AgentProfile{
	Name:              "synthetic-file-agent",
	CredentialedHost:  "api.example.com",
	EgressHosts:       []string{"api.example.com"},
	CredDirEnvVar:     "EXAMPLE_CONFIG_HOME",
	CredentialFile:    "example/cred.json",
	CredentialFileKey: "token", // deliberately NOT "accessToken"
}

// TestBuildCredFileSeedPayload_UsesProfileKey: GAP 2 regression (MUTATION-PIN: hardcoding "accessToken" fails).
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

	wantKey := syntheticFileProfile.CredentialFileKey
	if v, ok := m[wantKey]; !ok {
		t.Errorf("JSON missing key %q (got keys %v); hardcoded key suspected", wantKey, keysOf(m))
	} else if v != placeholder {
		t.Errorf("JSON[%q] = %q, want placeholder %q", wantKey, v, placeholder)
	}
	if _, bad := m["accessToken"]; bad {
		t.Errorf("JSON contains hardcoded key \"accessToken\" instead of profile-driven %q", wantKey)
	}
}

// TestClaudeCodeEnvVarSeedingUnchanged: CredDirLiveMount (MUTATION-PIN: NODE_EXTRA_CA_CERTS present, others absent).
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
	if strings.Contains(payload, "CLAUDE_CODE_OAUTH_TOKEN=") {
		t.Errorf("Claude Code env payload must NOT contain CLAUDE_CODE_OAUTH_TOKEN (CredDirLiveMount); got:\n%s", payload)
	}
	if !strings.Contains(payload, "NODE_EXTRA_CA_CERTS=") {
		t.Errorf("Claude Code env payload missing NODE_EXTRA_CA_CERTS; got:\n%s", payload)
	}
	if strings.Contains(payload, "CURSOR_API_KEY=") {
		t.Errorf("Claude Code env payload must not contain CURSOR_API_KEY=; got:\n%s", payload)
	}
}
