package cred

import (
	"sort"
	"strings"
)

type CredentialFormat string

const (
	CredentialFormatNone           CredentialFormat = ""
	CredentialFormatCursorJWT      CredentialFormat = "cursor-jwt"
	CredentialFormatOpencodeAPIKey CredentialFormat = "opencode-api-key"
	CredentialFormatOhMyPiVault    CredentialFormat = "ohmypi-vault"
)

type MCPConfigFormat string

const (
	MCPConfigFormatNone         MCPConfigFormat = ""
	MCPConfigFormatClaudeJSON   MCPConfigFormat = "claude-json"
	MCPConfigFormatOpencodeJSON MCPConfigFormat = "opencode-json"
	MCPConfigFormatCursorJSON   MCPConfigFormat = "cursor-json"
)

type AgentCapabilities struct {
	GuestNoSelfRefresh bool
}

type AgentProfile struct {
	Name string

	PlaceholderEnvVar string

	CredentialedHostSuffix string

	// PlaceholderIsJWT: cursor JWT-parses its token; hex placeholder triggers refresh POST. JWT-shaped placeholder with exp=2099 prevents that.
	PlaceholderIsJWT bool

	CredentialedHost string

	EgressHosts []string

	APIKeyEnvVar string

	CACertEnvVars []string

	GuestEnv map[string]string

	Capabilities AgentCapabilities

	SettingsPath string

	CredDirEnvVar string

	ConfigDirEnvVar string

	CredentialFile string

	CredentialFileKey string

	CredentialFileExtraKeys []string

	CredentialFormat CredentialFormat

	SkillsPath string

	MCPConfigFormat MCPConfigFormat

	MountAllowlist []string

	SymlinkPreservePrefixes []string

	// SettingsAllowlist: only these settings keys are staged into the guest.
	// Used when the settings file can carry PII under arbitrary keys (cursor's
	// authInfo). Mutually exclusive with SettingsDenylist.
	SettingsAllowlist map[string]bool

	// SettingsDenylist: every settings key is staged EXCEPT these (credential
	// helpers, host-bound hooks/permissions). A top-level "env" object is
	// additionally filtered by [SettingsEnvDropped]. Mutually exclusive with
	// SettingsAllowlist.
	SettingsDenylist map[string]bool

	BypassConsentKey string

	ToolRecipe ToolRecipe
}

func (p AgentProfile) Egress() []string {
	out := make([]string, len(p.EgressHosts))
	copy(out, p.EgressHosts)
	return out
}

func (p AgentProfile) Recipe() ToolRecipe {
	out := ToolRecipe{
		BinPath:  p.ToolRecipe.BinPath,
		Packages: make([]RecipePackage, len(p.ToolRecipe.Packages)),
	}
	for i, pkg := range p.ToolRecipe.Packages {
		cp := pkg
		if pkg.SHA256ByArch != nil {
			cp.SHA256ByArch = make(map[string]string, len(pkg.SHA256ByArch))
			for k, v := range pkg.SHA256ByArch {
				cp.SHA256ByArch[k] = v
			}
		}
		if pkg.Symlinks != nil {
			cp.Symlinks = make([]RecipeSymlink, len(pkg.Symlinks))
			copy(cp.Symlinks, pkg.Symlinks)
		}
		out.Packages[i] = cp
	}
	return out
}

const ClaudeCodeProfileName = "claude-code"

const DefaultProfileName = ClaudeCodeProfileName

const CursorAgentProfileName = "cursor"

const OpencodeProfileName = "opencode"

const OhMyPiProfileName = "oh-my-pi"

const KiroProfileName = "kiro"

const CodexProfileName = "codex"

var profiles = map[string]AgentProfile{}

func ProfileByName(name string) (AgentProfile, bool) {
	ensureProfiles()
	p, ok := profiles[name]
	return p, ok
}

// MustProfileByName returns the named profile or panics. Use only for
// built-in profiles that are guaranteed to be registered at init time.
func MustProfileByName(name string) AgentProfile {
	ensureProfiles()
	p, ok := profiles[name]
	if !ok {
		panic("cred: profile not registered: " + name)
	}
	return p
}

// settingsEnvSecretMarkers are case-insensitive substrings of an env var name
// that mark it as credential-bearing (ANTHROPIC_API_KEY, GITHUB_TOKEN,
// AWS_SECRET_ACCESS_KEY, DB_PASSWORD, ANTHROPIC_AUTH_TOKEN, ...).
var settingsEnvSecretMarkers = []string{"KEY", "TOKEN", "SECRET", "PASS", "AUTH", "CREDENTIAL", "COOKIE", "SESSION"}

// SettingsEnvDropped reports whether a settings.json "env" entry must not be
// staged into the guest: its name looks credential-bearing (the real token
// never enters the guest; brokered credentials arrive as placeholders), or its
// value is a host filesystem path (e.g. TMPDIR=/dev/shm/...), which does not
// exist in the guest.
func SettingsEnvDropped(name, value string) bool {
	upper := strings.ToUpper(name)
	for _, m := range settingsEnvSecretMarkers {
		if strings.Contains(upper, m) {
			return true
		}
	}
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~")
}

func ProfileNames() []string {
	ensureProfiles()
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
