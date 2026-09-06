package cred_test

import (
	"slices"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

func TestClaudeCodeProfile_Fields(t *testing.T) {
	p := cred.ClaudeCodeProfile

	if p.PlaceholderEnvVar == "" {
		t.Fatal("ClaudeCodeProfile.PlaceholderEnvVar must not be empty")
	}
	if p.CredentialedHost == "" {
		t.Fatal("ClaudeCodeProfile.CredentialedHost must not be empty")
	}
	if !p.Capabilities.GuestNoSelfRefresh {
		t.Fatal("ClaudeCodeProfile.Capabilities.GuestNoSelfRefresh must be true for Claude Code agents")
	}
}

// TestClaudeCodeProfile_ConfigFields pins the config-sharing descriptor
// values that future mount/share slices will consume.
func TestClaudeCodeProfile_ConfigFields(t *testing.T) {
	p := cred.ClaudeCodeProfile

	if p.SettingsPath != "~/.claude/settings.json" {
		t.Errorf("SettingsPath = %q, want ~/.claude/settings.json", p.SettingsPath)
	}
	if p.CredDirEnvVar != "CLAUDE_CONFIG_DIR" {
		t.Errorf("CredDirEnvVar = %q, want CLAUDE_CONFIG_DIR", p.CredDirEnvVar)
	}
	if p.ConfigDirEnvVar != "CLAUDE_CONFIG_DIR" {
		t.Errorf("ConfigDirEnvVar = %q, want CLAUDE_CONFIG_DIR", p.ConfigDirEnvVar)
	}
	if p.SkillsPath != "~/.claude/skills" {
		t.Errorf("SkillsPath = %q, want ~/.claude/skills", p.SkillsPath)
	}
	if p.MCPConfigFormat != cred.MCPConfigFormatClaudeJSON {
		t.Errorf("MCPConfigFormat = %q, want %q", p.MCPConfigFormat, cred.MCPConfigFormatClaudeJSON)
	}
	// MountAllowlist must contain the three safe globs and must NOT contain
	// any known secrets file pattern.
	for _, want := range []string{"CLAUDE.md", "skills/**", "settings.json"} {
		if !slices.Contains(p.MountAllowlist, want) {
			t.Errorf("MountAllowlist missing %q; got %v", want, p.MountAllowlist)
		}
	}
	for _, secret := range []string{".credentials.json", ".claude.json", "settings.local.json"} {
		if slices.Contains(p.MountAllowlist, secret) {
			t.Errorf("MountAllowlist must not contain secret %q", secret)
		}
	}
}

// TestCursorAgentProfile_CredentialPaths pins cursor's dual credential delivery:
//   - File path: auth.json gets {accessToken, refreshToken} = placeholder (S11).
//     cursor-agent status reads this file; both keys must be present.
//   - Env var path: CURSOR_AUTH_TOKEN=placeholder (S11).
//     cursor-agent -p (one-shot) checks this env var for its local auth check
//     via its internal r.D function; without it -p returns "Authentication required"
//     even when auth.json is fully populated.
//
// The placeholder is JWT-shaped (PlaceholderIsJWT=true) so cursor's JWT parser
// sees exp=2099 and does not trigger a refresh grant (which would send the
// refresh_token in a POST body — not intercepted by the MITM proxy).
func TestCursorAgentProfile_CredentialPaths(t *testing.T) {
	p := cred.CursorAgentProfile

	if p.APIKeyEnvVar != "CURSOR_API_KEY" {
		t.Errorf("APIKeyEnvVar = %q, want CURSOR_API_KEY", p.APIKeyEnvVar)
	}
	if p.PlaceholderEnvVar != "CURSOR_AUTH_TOKEN" {
		t.Errorf("PlaceholderEnvVar = %q, want CURSOR_AUTH_TOKEN — required for cursor-agent -p one-shot mode (S11)", p.PlaceholderEnvVar)
	}
	if !p.PlaceholderIsJWT {
		t.Error("PlaceholderIsJWT must be true — cursor JWT-parses its token; hex placeholder triggers refresh grant via POST body (not swapped by MITM)")
	}
	if p.CredentialedHost != "api2.cursor.sh" {
		t.Errorf("CredentialedHost = %q, want api2.cursor.sh", p.CredentialedHost)
	}
	if !slices.Contains(p.EgressHosts, p.CredentialedHost) {
		t.Errorf("EgressHosts %v must contain CredentialedHost %q", p.EgressHosts, p.CredentialedHost)
	}
	// Both env var and file-based credential must be declared: neither alone is
	// sufficient for all cursor-agent invocation patterns.
	if p.CredentialFile == "" {
		t.Error("CredentialFile must be set — cursor-agent status reads auth.json for session display")
	}
}

// TestCursorAgentProfile_SettingsFilterRequiredRegardlessOfAuthPath is the
// mutation-relevant invariant test: cursor's settings file (cli-config.json)
// must be filtered even though nexus3 never brokers cursor's credential.
// The filter protects an operator's OWN interactive `cursor-agent login`
// session: authInfo in cli-config.json carries identity and PII (email,
// displayName, userId, authId — not a token), and must not be shared into
// a sandbox regardless of which credential path nexus3 itself uses.
func TestCursorAgentProfile_SettingsFilterRequiredRegardlessOfAuthPath(t *testing.T) {
	p := cred.CursorAgentProfile

	if p.SettingsPath != "~/.cursor/cli-config.json" {
		t.Errorf("SettingsPath = %q, want ~/.cursor/cli-config.json", p.SettingsPath)
	}
	if len(p.SettingsAllowlist) == 0 {
		t.Fatal("SettingsAllowlist must not be empty — cli-config.json can carry authInfo regardless of auth path")
	}
	if p.SettingsAllowlist["authInfo"] {
		t.Error("authInfo must NOT be in SettingsAllowlist — it carries identity and PII (email, displayName, userId, authId) this filter exists to exclude")
	}
	// cursor has no settings-key bypass-consent mechanism; skip-permissions is
	// a launch-time flag (--force/--yolo), not a persisted setting.
	if p.BypassConsentKey != "" {
		t.Errorf("BypassConsentKey = %q, want empty for cursor", p.BypassConsentKey)
	}
	if !slices.Contains(p.MountAllowlist, "cli-config.json") {
		t.Errorf("MountAllowlist %v must contain cli-config.json", p.MountAllowlist)
	}
}

// TestCursorAgentProfile_Registered pins the registry entry so a typo'd
// --agent cursor cannot silently resolve to no profile.
func TestCursorAgentProfile_Registered(t *testing.T) {
	p, ok := cred.ProfileByName(cred.CursorAgentProfileName)
	if !ok {
		t.Fatal("cred.CursorAgentProfileName is not registered in ProfileByName")
	}
	if p.Name != cred.CursorAgentProfileName {
		t.Errorf("registered profile Name = %q, want %q", p.Name, cred.CursorAgentProfileName)
	}
	if !slices.Contains(cred.ProfileNames(), cred.CursorAgentProfileName) {
		t.Errorf("ProfileNames() = %v, missing %q", cred.ProfileNames(), cred.CursorAgentProfileName)
	}
}

// TestCursorAgentProfile_CredAndSettingsDirAreDistinct asserts that cursor's
// credential-directory redirect (CredDirEnvVar) and settings-directory
// redirect (ConfigDirEnvVar) are DIFFERENT env vars. This is the empirically
// verified fact: XDG_CONFIG_HOME controls the credential file lookup while
// CURSOR_CONFIG_DIR controls cli-config.json (settings). They cannot be
// collapsed into one variable — see the profile's CredDirEnvVar doc comment.
// This test fails if either field is set to the other's value.
func TestCursorAgentProfile_CredAndSettingsDirAreDistinct(t *testing.T) {
	p := cred.CursorAgentProfile

	if p.CredDirEnvVar == "" {
		t.Fatal("CredDirEnvVar must not be empty — cursor credential dir is redirected via XDG_CONFIG_HOME")
	}
	if p.ConfigDirEnvVar == "" {
		t.Fatal("ConfigDirEnvVar must not be empty — cursor settings dir is redirected via CURSOR_CONFIG_DIR")
	}
	if p.CredDirEnvVar == p.ConfigDirEnvVar {
		t.Errorf("CredDirEnvVar and ConfigDirEnvVar must differ for cursor: both are %q — "+
			"XDG_CONFIG_HOME governs the credential, CURSOR_CONFIG_DIR governs settings", p.CredDirEnvVar)
	}
	if p.CredDirEnvVar != "XDG_CONFIG_HOME" {
		t.Errorf("CredDirEnvVar = %q, want XDG_CONFIG_HOME", p.CredDirEnvVar)
	}
	if p.ConfigDirEnvVar != "CURSOR_CONFIG_DIR" {
		t.Errorf("ConfigDirEnvVar = %q, want CURSOR_CONFIG_DIR", p.ConfigDirEnvVar)
	}
}

// TestCursorAgentProfile_CredentialFileDescriptor pins the file-based
// credential descriptor fields added in S3. These are consumed by the S8
// seeding slice; this test asserts the correct values are present in the
// profile declaration.
func TestCursorAgentProfile_CredentialFileDescriptor(t *testing.T) {
	p := cred.CursorAgentProfile

	if p.CredentialFile != "cursor/auth.json" {
		t.Errorf("CredentialFile = %q, want cursor/auth.json", p.CredentialFile)
	}
	if p.CredentialFileKey != "accessToken" {
		t.Errorf("CredentialFileKey = %q, want accessToken", p.CredentialFileKey)
	}
}

func TestAgentProfile_DeclarativeExtension(t *testing.T) {
	// Adding a new agent type must require only a new value, not new code.
	// This test demonstrates that by constructing two hypothetical profiles
	// inline — including the new config-sharing descriptor fields — without
	// any code branching on agent type.
	opencode := cred.AgentProfile{
		PlaceholderEnvVar: "OPENCODE_TOKEN",
		CredentialedHost:  "api.opencode.example.com",
		MCPConfigFormat:   cred.MCPConfigFormatOpencodeJSON,
		SettingsPath:      "~/.opencode/settings.json",
		ConfigDirEnvVar:   "OPENCODE_CONFIG_DIR",
		SkillsPath:        "",
		MountAllowlist:    []string{"settings.json"},
		Capabilities: cred.AgentCapabilities{
			GuestNoSelfRefresh: false,
		},
	}
	if opencode.PlaceholderEnvVar != "OPENCODE_TOKEN" {
		t.Fatalf("unexpected PlaceholderEnvVar: %q", opencode.PlaceholderEnvVar)
	}
	if opencode.CredentialedHost != "api.opencode.example.com" {
		t.Fatalf("unexpected CredentialedHost: %q", opencode.CredentialedHost)
	}
	if opencode.MCPConfigFormat != cred.MCPConfigFormatOpencodeJSON {
		t.Fatalf("unexpected MCPConfigFormat: %q", opencode.MCPConfigFormat)
	}
	// An agent with no skills concept must leave SkillsPath empty (zero value).
	if opencode.SkillsPath != "" {
		t.Fatalf("unexpected SkillsPath: %q", opencode.SkillsPath)
	}

	// A no-MCP agent uses the zero value.
	codex := cred.AgentProfile{
		PlaceholderEnvVar: "CODEX_TOKEN",
		CredentialedHost:  "api.codex.example.com",
		// MCPConfigFormat zero value = MCPConfigFormatNone; no explicit set needed.
	}
	if codex.MCPConfigFormat != cred.MCPConfigFormatNone {
		t.Fatalf("zero MCPConfigFormat should be MCPConfigFormatNone, got %q", codex.MCPConfigFormat)
	}
}

// TestCursorAgentProfile_CredentialedHostSuffix pins the suffix that causes all
// *.cursor.sh endpoints (including sharded inference nodes like
// agentn.global.api5.cursor.sh) to be treated as secret hosts by the MITM
// proxy. It also checks dot-boundary safety: the suffix must begin with "." so
// that "evilcursor.sh" is NOT matched.
//
// Inference hosts use h2 only (no HTTP/1.1 ALPN fallback). The proxy handles
// them via ConnectHijack (h2SuffixHijack in proxy.go) which advertises h2 in
// NextProtos and serves via an http2-configured http.Server — not via the
// built-in ConnectMitm which only speaks HTTP/1.1.
//
// Mutation guard: change CredentialedHostSuffix → "" → test fails RED.
func TestCursorAgentProfile_CredentialedHostSuffix(t *testing.T) {
	p := cred.CursorAgentProfile

	if p.CredentialedHostSuffix == "" {
		t.Fatal("CredentialedHostSuffix must not be empty — inference hosts like agentn.global.api5.cursor.sh must be covered (S11)")
	}
	if p.CredentialedHostSuffix[0] != '.' {
		t.Errorf("CredentialedHostSuffix = %q: must begin with '.' for dot-boundary safety (no leading dot would match 'evilcursor.sh')", p.CredentialedHostSuffix)
	}
	if p.CredentialedHostSuffix != ".cursor.sh" {
		t.Errorf("CredentialedHostSuffix = %q, want .cursor.sh", p.CredentialedHostSuffix)
	}
	// CredentialedHost must still be set for credential file lookup.
	if p.CredentialedHost == "" {
		t.Error("CredentialedHost must not be empty when CredentialedHostSuffix is set")
	}
}
