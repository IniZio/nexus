package authrelay

import "strings"

func RelayEnv(binding, value string) map[string]string {
	out := map[string]string{
		"NEXUS_AUTH_BINDING": binding,
		"NEXUS_AUTH_VALUE":   value,
	}
	switch strings.ToLower(strings.TrimSpace(binding)) {
	case "github", "gh", "copilot", "github-copilot":
		out["GITHUB_TOKEN"] = value
		out["GH_TOKEN"] = value
	case "opencode":
		out["OPENCODE_API_KEY"] = value
	case "codex":
		if looksLikeOpenAIAPIKey(value) {
			out["OPENAI_API_KEY"] = value
		}
	case "openai", "openai_api_key":
		out["OPENAI_API_KEY"] = value
	case "openrouter":
		out["OPENROUTER_API_KEY"] = value
	case "minimax":
		out["MINIMAX_API_KEY"] = value
	case "claude", "anthropic":
		out["ANTHROPIC_API_KEY"] = value
	}
	return out
}

func looksLikeOpenAIAPIKey(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "sk-")
}
