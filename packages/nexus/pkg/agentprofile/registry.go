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
		EnvVars:    []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
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
			".config/opencode/opencode.json",
			".config/opencode/ocx.jsonc",
			".config/opencode/dcp.jsonc",
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
