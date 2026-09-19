package cred

import "sort"

type CredentialFormat string

const (
	CredentialFormatNone CredentialFormat = ""

	CredentialFormatCursorJWT CredentialFormat = "cursor-jwt"
)

type MCPConfigFormat string

const (
	MCPConfigFormatNone MCPConfigFormat = ""

	MCPConfigFormatClaudeJSON MCPConfigFormat = "claude-json"

	MCPConfigFormatOpencodeJSON MCPConfigFormat = "opencode-json"

	MCPConfigFormatCursorJSON MCPConfigFormat = "cursor-json"
)

type AgentCapabilities struct {
	GuestNoSelfRefresh bool
	CredDirLiveMount   bool
}

type AgentProfile struct {
	Name string

	PlaceholderEnvVar string

	CredentialedHostSuffix string

	// PlaceholderIsJWT: cursor JWT-parses its token; hex placeholder triggers refresh POST (not MITM-intercepted → fails). JWT-shaped placeholder with exp=2099 prevents that.
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

	SettingsAllowlist map[string]bool

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

var ClaudeCodeProfile = AgentProfile{
	Name:             ClaudeCodeProfileName,
	CredentialedHost: "api.anthropic.com",
	EgressHosts:      []string{"api.anthropic.com", "platform.claude.com"},
	APIKeyEnvVar:     "ANTHROPIC_AUTH_TOKEN",
	CACertEnvVars:    []string{"NODE_EXTRA_CA_CERTS"},
	GuestEnv: map[string]string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	},
	Capabilities: AgentCapabilities{
		GuestNoSelfRefresh: false,
		CredDirLiveMount:   true,
	},
	SettingsPath:    "~/.claude/settings.json",
	CredDirEnvVar:   "CLAUDE_CONFIG_DIR",
	ConfigDirEnvVar: "CLAUDE_CONFIG_DIR",
	SkillsPath:      "~/.claude/skills",
	MCPConfigFormat: MCPConfigFormatClaudeJSON,
	MountAllowlist: []string{
		"CLAUDE.md",
		"skills/**",
		"settings.json",
	},
	SettingsAllowlist: map[string]bool{
		"model":                             true,
		"advisorModel":                      true,
		"availableModels":                   true,
		"theme":                             true,
		"tui":                               true,
		"defaultShell":                      true,
		"attribution":                       true,
		"enabledPlugins":                    true,
		"extraKnownMarketplaces":            true,
		"autoMode":                          true,
		"effortLevel":                       true,
		"autoUpdatesChannel":                true,
		"enableWorkflows":                   true,
		"skipDangerousModePermissionPrompt": true,
	},
	ToolRecipe: ToolRecipe{
		BinPath: "/usr/local/bin/claude",
		Packages: []RecipePackage{
			{
				// Version floats: resolved to a concrete digest against the image
				// tag on the host at create time, upstream of the image-cache key.
				Kind:       RecipeKindOCI,
				Name:       "claude-code",
				Version:    FloatingVersion,
				Image:      "docker/sandbox-templates:claude-code-minimal-nightly",
				SrcPath:    "/home/agent/.local/share/claude/versions/",
				InstallDir: "/usr/local/share/claude/versions",
				BinRel:     "",
				Symlinks: []RecipeSymlink{
					{LinkPath: "/usr/local/bin/claude"},
				},
			},
		},
	},
}

const ClaudeCodeProfileName = "claude-code"

const DefaultProfileName = ClaudeCodeProfileName

const CursorAgentProfileName = "cursor"

var CursorAgentProfile = AgentProfile{
	Name:                    CursorAgentProfileName,
	CredentialedHost:        "api2.cursor.sh",
	CredentialedHostSuffix:  ".cursor.sh",
	EgressHosts:             []string{"api2.cursor.sh"},
	PlaceholderEnvVar:       "CURSOR_AUTH_TOKEN",
	PlaceholderIsJWT:        true,
	APIKeyEnvVar:            "CURSOR_API_KEY",
	CACertEnvVars:           []string{"NODE_EXTRA_CA_CERTS"},
	SettingsPath:            "~/.cursor/cli-config.json",
	CredDirEnvVar:           "XDG_CONFIG_HOME",
	ConfigDirEnvVar:         "CURSOR_CONFIG_DIR",
	CredentialFile:          "cursor/auth.json",
	CredentialFileKey:       "accessToken",
	CredentialFileExtraKeys: []string{"refreshToken"},
	CredentialFormat:        CredentialFormatCursorJWT,
	MountAllowlist: []string{
		"cli-config.json",
	},
	SettingsAllowlist: map[string]bool{
		"version":                           true,
		"editor":                            true,
		"display":                           true,
		"notifications":                     true,
		"hints":                             true,
		"modelSlashCommands":                true,
		"rewind":                            true,
		"hasChangedDefaultModel":            true,
		"exploreSubagentModel":              true,
		"permissions":                       true,
		"approvalMode":                      true,
		"autoAcceptWebSearch":               true,
		"attribution":                       true,
		"model":                             true,
		"maxMode":                           true,
		"selectedModel":                     true,
		"modelParameters":                   true,
		"runEverythingSettingsPromptStreak": true,
	},
	MCPConfigFormat: MCPConfigFormatCursorJSON,
	ToolRecipe: ToolRecipe{
		BinPath: "/usr/local/bin/cursor-agent",
		Packages: []RecipePackage{
			{
				// Version floats: resolved to a concrete digest against the image
				// tag on the host at create time, upstream of the image-cache key.
				Kind:       RecipeKindOCI,
				Name:       "cursor-agent",
				Version:    FloatingVersion,
				Image:      "docker/sandbox-templates:cursor-agent-nightly",
				SrcPath:    "/home/agent/.local/share/cursor-agent/versions/",
				InstallDir: "/usr/local/share/cursor-agent/versions",
				BinRel:     "cursor-agent",
				Symlinks: []RecipeSymlink{
					{LinkPath: "/usr/local/bin/cursor-agent"},
				},
			},
		},
	},
}

var profiles = map[string]AgentProfile{
	ClaudeCodeProfileName:  ClaudeCodeProfile,
	CursorAgentProfileName: CursorAgentProfile,
}

func ProfileByName(name string) (AgentProfile, bool) {
	p, ok := profiles[name]
	return p, ok
}

func ProfileNames() []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
