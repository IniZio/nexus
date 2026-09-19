package cred

import "sort"

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
	CredDirLiveMount   bool
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

const OpencodeProfileName = "opencode"

var OpencodeProfile = AgentProfile{
	Name:              OpencodeProfileName,
	CredentialedHost:  "opencode.ai",
	EgressHosts:       []string{"opencode.ai"},
	PlaceholderEnvVar: "OPENCODE_API_KEY",
	PlaceholderIsJWT:  false,
	APIKeyEnvVar:      "OPENCODE_API_KEY",
	CACertEnvVars:     []string{"NODE_EXTRA_CA_CERTS"},
	GuestEnv: map[string]string{
		"OPENCODE_DISABLE_AUTOUPDATE":   "1",
		"OPENCODE_DISABLE_MODELS_FETCH": "1",
	},
	SettingsPath:      "~/.config/opencode/opencode.json",
	CredDirEnvVar:     "XDG_DATA_HOME",
	ConfigDirEnvVar:   "OPENCODE_CONFIG_DIR",
	CredentialFile:    "opencode/auth.json",
	CredentialFileKey: "key",
	CredentialFormat:  CredentialFormatOpencodeAPIKey,
	MCPConfigFormat:   MCPConfigFormatOpencodeJSON,
	ToolRecipe: ToolRecipe{
		BinPath: "/usr/local/bin/opencode",
		Packages: []RecipePackage{
			{
				Kind:        RecipeKindTarball,
				Name:        "node",
				Version:     "22.23.2",
				URLTemplate: "https://nodejs.org/dist/v{VERSION}/node-v{VERSION}-linux-{ARCH}.tar.gz",
				SHA256ByArch: map[string]string{
					"x64":   "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a",
					"arm64": "013b59cfd2819703a6f4a14ab891fc46fc2a4e3f5bcd92de3fb4929b43e35b30",
				},
				InstallDir: "/usr/local",
				VersionCmd: "node --version",
			},
			{
				Kind:    RecipeKindNPM,
				Name:    "opencode-ai",
				Version: "1.18.31",
			},
		},
	},
}

const OhMyPiProfileName = "oh-my-pi"

// OhMyPiProfile uses a SQLite vault (no single bearer); CheckCred must not block sandbox create. See ohmypi.go.
var OhMyPiProfile = AgentProfile{
	Name:              OhMyPiProfileName,
	CredentialFormat:  CredentialFormatOhMyPiVault,
	CredentialedHost:  "api.anthropic.com",
	EgressHosts:       []string{"api.anthropic.com", "api.openai.com", "auth.openai.com"},
	PlaceholderEnvVar: "ANTHROPIC_OAUTH_TOKEN",
	APIKeyEnvVar:      "ANTHROPIC_API_KEY",
	CACertEnvVars:     []string{"NODE_EXTRA_CA_CERTS"},
	SettingsPath:      "~/.omp/agent/config.yml",
	CredDirEnvVar:     "PI_CODING_AGENT_DIR",
	ConfigDirEnvVar:   "PI_CODING_AGENT_DIR",
	ToolRecipe: ToolRecipe{
		BinPath: "/usr/local/bin/omp",
		Packages: []RecipePackage{
			{
				Kind:        RecipeKindTarball,
				Name:        "node",
				Version:     "22.23.2",
				URLTemplate: "https://nodejs.org/dist/v{VERSION}/node-v{VERSION}-linux-{ARCH}.tar.gz",
				SHA256ByArch: map[string]string{
					"x64":   "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a",
					"arm64": "013b59cfd2819703a6f4a14ab891fc46fc2a4e3f5bcd92de3fb4929b43e35b30",
				},
				InstallDir: "/usr/local",
				VersionCmd: "node --version",
			},
			{
				Kind:    RecipeKindNPM,
				Name:    "bun",
				Version: "1.4.2",
			},
			{
				Kind:    RecipeKindNPM,
				Name:    "@oh-my-pi/pi-coding-agent",
				Version: "18.2.6",
			},
		},
	},
}

var profiles = map[string]AgentProfile{
	ClaudeCodeProfileName:  ClaudeCodeProfile,
	CursorAgentProfileName: CursorAgentProfile,
	OpencodeProfileName:    OpencodeProfile,
	OhMyPiProfileName:      OhMyPiProfile,
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
