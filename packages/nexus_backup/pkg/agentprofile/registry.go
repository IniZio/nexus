package agentprofile

import "strings"

type Profile struct {
	Name         string
	Aliases      []string
	Binary       string
	EnvVars      []string
	APIKeyPrefix string
	CredFiles    []string
	InstallPkg   string
}

var registry = []Profile{
	{
		Name:       "claude",
		Aliases:    []string{"anthropic", "claude-code"},
		Binary:     "claude",
		EnvVars:    []string{"ANTHROPIC_API_KEY"},
		CredFiles:  []string{".claude/.credentials.json", ".claude.json"},
		InstallPkg: "@anthropic-ai/claude-code",
	},
	{
		Name:         "codex",
		Binary:       "codex",
		EnvVars:      []string{"OPENAI_API_KEY"},
		APIKeyPrefix: "sk-",
		CredFiles: []string{
			".codex/auth.json",
			".codex/version.json",
			".codex/.codex-global-state.json",
			".codex/config.toml",
			".codex/AGENTS.md",
			".codex/skills",
			".codex/agents",
			".codex/rules",
			".codex/prompts",
			".config/openai/auth.json",
		},
		InstallPkg: "@openai/codex",
	},
	{
		Name:    "openai",
		Aliases: []string{"openai_api_key"},
		EnvVars: []string{"OPENAI_API_KEY"},
	},
	{
		Name:    "opencode",
		Binary:  "opencode",
		EnvVars: []string{"OPENCODE_API_KEY"},
		CredFiles: []string{
			".local/share/opencode/auth.json",
			".local/share/opencode/mcp-auth.json",
			".config/opencode/opencode.json",
			".config/opencode/ocx.jsonc",
			".config/opencode/dcp.jsonc",
			".config/opencode/opencode-mem.jsonc",
			".config/opencode/skills",
			".config/opencode/plugin",
			".config/opencode/plugins",
			".config/opencode/profiles",
		},
		InstallPkg: "opencode-ai",
	},
	{
		Name:    "github",
		Aliases: []string{"gh", "copilot", "github-copilot"},
		Binary:  "gh",
		EnvVars: []string{"GITHUB_TOKEN", "GH_TOKEN"},
		CredFiles: []string{
			".config/github-copilot/hosts.json",
			".config/github-copilot/apps.json",
		},
	},
	{
		Name:    "openrouter",
		EnvVars: []string{"OPENROUTER_API_KEY"},
	},
	{
		Name:    "minimax",
		EnvVars: []string{"MINIMAX_API_KEY"},
	},
	{
		Name:       "gemini",
		Aliases:    []string{"gemini-cli", "google-gemini"},
		Binary:     "gemini",
		EnvVars:    []string{"GEMINI_API_KEY"},
		CredFiles:  []string{".gemini/settings.json", ".gemini/.env"},
		InstallPkg: "@google/gemini-cli",
	},
	{
		Name:      "continue",
		Aliases:   []string{"continue-cli", "cn"},
		Binary:    "cn",
		EnvVars:   []string{"CONTINUE_API_KEY"},
		CredFiles: []string{".continue/config.yaml", ".continue/permissions.yaml", ".continue/.env"},
	},
	{
		Name:       "kiro",
		Aliases:    []string{"kiro-cli"},
		Binary:     "kiro-cli",
		EnvVars:    []string{"KIRO_API_KEY"},
		CredFiles:  []string{".kiro/settings/cli.json", "Library/Application Support/kiro-cli/data.sqlite3", ".local/share/kiro-cli/data.sqlite3"},
		InstallPkg: "kiro-cli",
	},
	{
		Name:       "pi",
		Aliases:    []string{"pi-agent", "pi-coding-agent"},
		Binary:     "pi",
		EnvVars:    []string{"PI_API_KEY"},
		CredFiles:  []string{".pi/agent/auth.json", ".pi/agent/settings.json"},
		InstallPkg: "@mariozechner/pi-coding-agent",
	},
	{
		Name:      "aider",
		Aliases:   []string{"aider-chat"},
		Binary:    "aider",
		EnvVars:   []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"},
		CredFiles: []string{".aider.conf.yml", ".env"},
	},
	{
		Name:      "goose",
		Aliases:   []string{"block-goose", "goose-cli"},
		Binary:    "goose",
		EnvVars:   []string{"GOOSE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"},
		CredFiles: []string{".config/goose/config.yaml", ".config/goose/profiles.yaml", ".config/goose/secrets.yaml"},
	},
	{
		Name:      "copilot-cli",
		Aliases:   []string{"github-copilot-cli", "copilot"},
		Binary:    "copilot",
		EnvVars:   []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"},
		CredFiles: []string{".copilot/config.json", ".config/github-copilot/hosts.json"},
	},
}

func Lookup(name string) *Profile {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return nil
	}
	for i := range registry {
		p := &registry[i]
		if strings.ToLower(p.Name) == normalized {
			return p
		}
		for _, a := range p.Aliases {
			if strings.ToLower(a) == normalized {
				return p
			}
		}
	}
	return nil
}

func AllBinaries() []string {
	out := make([]string, 0, len(registry))
	for _, p := range registry {
		if p.Binary != "" {
			out = append(out, p.Binary)
		}
	}
	return out
}

func AllCredFiles() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range registry {
		for _, f := range p.CredFiles {
			if _, ok := seen[f]; !ok {
				seen[f] = struct{}{}
				out = append(out, f)
			}
		}
	}
	return out
}

func AllInstallPkgs() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range registry {
		if p.InstallPkg != "" {
			if _, ok := seen[p.InstallPkg]; !ok {
				seen[p.InstallPkg] = struct{}{}
				out = append(out, p.InstallPkg)
			}
		}
	}
	return out
}
