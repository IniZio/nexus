package cred_test

import (
	"slices"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func TestClaudeCodeProfile_Fields(t *testing.T) {
	p := cred.MustProfileByName(cred.ClaudeCodeProfileName)

	if p.CredentialedHost == "" {
		t.Fatal("MustProfileByName(ClaudeCodeProfileName).CredentialedHost must not be empty")
	}
	if p.Capabilities.GuestNoSelfRefresh {
		t.Fatal("MustProfileByName(ClaudeCodeProfileName).Capabilities.GuestNoSelfRefresh must be false (guest self-refreshes from live-mounted ~/.claude)")
	}
	if p.BypassConsentKey != "" {
		t.Fatalf("MustProfileByName(ClaudeCodeProfileName).BypassConsentKey must be empty for live-mount design; got %q", p.BypassConsentKey)
	}
	if p.PlaceholderEnvVar != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("MustProfileByName(ClaudeCodeProfileName).PlaceholderEnvVar = %q, want CLAUDE_CODE_OAUTH_TOKEN (broker placeholder path replaces live mount)", p.PlaceholderEnvVar)
	}
}

func TestClaudeCodeProfile_ConfigFields(t *testing.T) {
	p := cred.MustProfileByName(cred.ClaudeCodeProfileName)

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

// ── TestCursorAgentProfile_CredentialPaths ──
func TestCursorAgentProfile_CredentialPaths(t *testing.T) {
	p := cred.MustProfileByName(cred.CursorAgentProfileName)

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
	if p.CredentialFile == "" {
		t.Error("CredentialFile must be set — cursor-agent status reads auth.json for session display")
	}
}

// ── TestCursorAgentProfile_SettingsFilterRequiredRegardlessOfAuthPath ──
func TestCursorAgentProfile_SettingsFilterRequiredRegardlessOfAuthPath(t *testing.T) {
	p := cred.MustProfileByName(cred.CursorAgentProfileName)

	if p.SettingsPath != "~/.cursor/cli-config.json" {
		t.Errorf("SettingsPath = %q, want ~/.cursor/cli-config.json", p.SettingsPath)
	}
	if len(p.SettingsAllowlist) == 0 {
		t.Fatal("SettingsAllowlist must not be empty — cli-config.json can carry authInfo regardless of auth path")
	}
	if p.SettingsAllowlist["authInfo"] {
		t.Error("authInfo must NOT be in SettingsAllowlist — it carries identity and PII (email, displayName, userId, authId) this filter exists to exclude")
	}
	if p.BypassConsentKey != "" {
		t.Errorf("BypassConsentKey = %q, want empty for cursor", p.BypassConsentKey)
	}
	if !slices.Contains(p.MountAllowlist, "cli-config.json") {
		t.Errorf("MountAllowlist %v must contain cli-config.json", p.MountAllowlist)
	}
}

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

// ── TestCursorAgentProfile_CredAndSettingsDirAreDistinct ──
// XDG_CONFIG_HOME controls credential file lookup while CURSOR_CONFIG_DIR
// controls cli-config.json (settings). They cannot be collapsed into one.
func TestCursorAgentProfile_CredAndSettingsDirAreDistinct(t *testing.T) {
	p := cred.MustProfileByName(cred.CursorAgentProfileName)

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

func TestCursorAgentProfile_CredentialFileDescriptor(t *testing.T) {
	p := cred.MustProfileByName(cred.CursorAgentProfileName)

	if p.CredentialFile != "cursor/auth.json" {
		t.Errorf("CredentialFile = %q, want cursor/auth.json", p.CredentialFile)
	}
	if p.CredentialFileKey != "accessToken" {
		t.Errorf("CredentialFileKey = %q, want accessToken", p.CredentialFileKey)
	}
}

func TestAgentProfile_DeclarativeExtension(t *testing.T) {
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
	if opencode.SkillsPath != "" {
		t.Fatalf("unexpected SkillsPath: %q", opencode.SkillsPath)
	}
	codex := cred.AgentProfile{
		PlaceholderEnvVar: "CODEX_TOKEN",
		CredentialedHost:  "api.codex.example.com",
	}
	if codex.MCPConfigFormat != cred.MCPConfigFormatNone {
		t.Fatalf("zero MCPConfigFormat should be MCPConfigFormatNone, got %q", codex.MCPConfigFormat)
	}
}

// ── TestCursorAgentProfile_CredentialedHostSuffix ──
// Suffix must cause all *.cursor.sh endpoints (including
// agentn.global.api5.cursor.sh) to be treated as secret hosts.
// Must begin with "." (dot-boundary safety).
// MUTATION-PIN: change CredentialedHostSuffix → "" → RED.
func TestCursorAgentProfile_CredentialedHostSuffix(t *testing.T) {
	p := cred.MustProfileByName(cred.CursorAgentProfileName)

	if p.CredentialedHostSuffix == "" {
		t.Fatal("CredentialedHostSuffix must not be empty — inference hosts like agentn.global.api5.cursor.sh must be covered (S11)")
	}
	if p.CredentialedHostSuffix[0] != '.' {
		t.Errorf("CredentialedHostSuffix = %q: must begin with '.' for dot-boundary safety (no leading dot would match 'evilcursor.sh')", p.CredentialedHostSuffix)
	}
	if p.CredentialedHostSuffix != ".cursor.sh" {
		t.Errorf("CredentialedHostSuffix = %q, want .cursor.sh", p.CredentialedHostSuffix)
	}
	if p.CredentialedHost == "" {
		t.Error("CredentialedHost must not be empty when CredentialedHostSuffix is set")
	}
}
