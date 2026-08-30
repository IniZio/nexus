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

// TestCursorAgentProfile_APIKeyOnlyPath pins the deliberate scope narrowing:
// cursor brokers only the API-key path (CURSOR_API_KEY), never the OAuth
// device-flow path, because that token lands inline in cli-config.json with
// no by-omission exclusion mechanism the way Claude's separate
// .credentials.json has.
func TestCursorAgentProfile_APIKeyOnlyPath(t *testing.T) {
	p := cred.CursorAgentProfile

	if p.APIKeyEnvVar != "CURSOR_API_KEY" {
		t.Errorf("APIKeyEnvVar = %q, want CURSOR_API_KEY", p.APIKeyEnvVar)
	}
	if p.PlaceholderEnvVar != "" {
		t.Errorf("PlaceholderEnvVar = %q, want empty — cursor's OAuth path is not brokered", p.PlaceholderEnvVar)
	}
	if p.CredentialedHost != "api2.cursor.sh" {
		t.Errorf("CredentialedHost = %q, want api2.cursor.sh", p.CredentialedHost)
	}
	if !slices.Contains(p.EgressHosts, p.CredentialedHost) {
		t.Errorf("EgressHosts %v must contain CredentialedHost %q", p.EgressHosts, p.CredentialedHost)
	}
}

// TestCursorAgentProfile_SettingsFilterRequiredRegardlessOfAuthPath is the
// mutation-relevant invariant test: cursor's settings file (cli-config.json)
// must be filtered even though nexus3 never brokers cursor's OAuth token. The
// filter protects an operator's OWN interactive `cursor-agent login` session,
// which is orthogonal to which credential path nexus3 itself uses.
func TestCursorAgentProfile_SettingsFilterRequiredRegardlessOfAuthPath(t *testing.T) {
	p := cred.CursorAgentProfile

	if p.SettingsPath != "~/.cursor/cli-config.json" {
		t.Errorf("SettingsPath = %q, want ~/.cursor/cli-config.json", p.SettingsPath)
	}
	if len(p.SettingsAllowlist) == 0 {
		t.Fatal("SettingsAllowlist must not be empty — cli-config.json can carry authInfo regardless of auth path")
	}
	if p.SettingsAllowlist["authInfo"] {
		t.Error("authInfo must NOT be in SettingsAllowlist — it is the credential blob this filter exists to strip")
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
