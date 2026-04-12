package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ProviderType indicates the authentication mechanism
type ProviderType string

const (
	ProviderTypeOAuth       ProviderType = "oauth"
	ProviderTypeAPIKey      ProviderType = "api_key"
	ProviderTypeSession     ProviderType = "session"
)

// ProviderConfig represents a detected OAuth/API credential from host
type ProviderConfig struct {
	Name         string       // "codex", "opencode", "claude"
	Type         ProviderType // oauth, api_key, session
	RefreshToken string       // For OAuth
	AccessToken  string       // Short-lived or API key
	ExpiresAt    time.Time
	Scopes       []string
	ConfigPath   string // Where it was found on host
}

// Discover scans host home directory for agent OAuth/API configurations
func Discover(homeDir string) ([]ProviderConfig, error) {
	var configs []ProviderConfig

	// Check each provider
	detectors := []func(string) (*ProviderConfig, error){
		detectCodex,
		detectOpenCode,
		detectClaude,
		detectOpenAI,
		detectGitHubCLI,
	}

	for _, detector := range detectors {
		if cfg, err := detector(homeDir); err == nil && cfg != nil {
			configs = append(configs, *cfg)
		}
	}

	return configs, nil
}

func detectCodex(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".config", "codex", "auth.json"),
		filepath.Join(home, ".codex", "auth.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var auth struct {
			RefreshToken string    `json:"refresh_token"`
			AccessToken  string    `json:"access_token"`
			ExpiresAt    time.Time `json:"expires_at"`
			Account      string    `json:"account"`
		}

		if err := json.Unmarshal(data, &auth); err != nil {
			continue
		}

		if auth.RefreshToken == "" {
			continue
		}

		return &ProviderConfig{
			Name:         "codex",
			Type:         ProviderTypeOAuth,
			RefreshToken: auth.RefreshToken,
			AccessToken:  auth.AccessToken,
			ExpiresAt:    auth.ExpiresAt,
			Scopes:       []string{"repo", "read:org"},
			ConfigPath:   path,
		}, nil
	}

	return nil, nil
}

func detectOpenCode(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".config", "opencode", "auth.json"),
		filepath.Join(home, ".opencode", "auth.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var auth struct {
			APIKey      string `json:"api_key"`
			AccessToken string `json:"access_token"`
			Provider    string `json:"provider"`
		}

		if err := json.Unmarshal(data, &auth); err != nil {
			continue
		}

		// OpenCode can use either API key or OAuth token
		if auth.APIKey != "" {
			return &ProviderConfig{
				Name:       "opencode",
				Type:       ProviderTypeAPIKey,
				AccessToken: auth.APIKey,
				ConfigPath: path,
			}, nil
		}

		if auth.AccessToken != "" {
			return &ProviderConfig{
				Name:        "opencode",
				Type:        ProviderTypeSession,
				AccessToken: auth.AccessToken,
				ConfigPath:  path,
			}, nil
		}
	}

	return nil, nil
}

func detectClaude(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".config", "claude", "settings.json"),
		filepath.Join(home, ".claude", "auth.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var auth struct {
			SessionToken string `json:"sessionToken"`
			APIKey       string `json:"apiKey"`
		}

		if err := json.Unmarshal(data, &auth); err != nil {
			continue
		}

		if auth.SessionToken != "" {
			return &ProviderConfig{
				Name:        "claude",
				Type:        ProviderTypeSession,
				AccessToken: auth.SessionToken,
				ConfigPath:  path,
			}, nil
		}
	}

	return nil, nil
}

func detectOpenAI(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".config", "openai", "auth.json"),
		filepath.Join(home, ".openai", "api_key"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		// Try JSON first
		var auth struct {
			APIKey string `json:"api_key"`
		}
		if err := json.Unmarshal(data, &auth); err == nil && auth.APIKey != "" {
			return &ProviderConfig{
				Name:        "openai",
				Type:        ProviderTypeAPIKey,
				AccessToken: auth.APIKey,
				ConfigPath:  path,
			}, nil
		}

		// Try plain text
		if len(data) > 0 && len(data) < 256 {
			return &ProviderConfig{
				Name:        "openai",
				Type:        ProviderTypeAPIKey,
				AccessToken: string(data),
				ConfigPath:  path,
			}, nil
		}
	}

	return nil, nil
}

func detectGitHubCLI(home string) (*ProviderConfig, error) {
	// GitHub CLI stores auth in hosts.yml, which is yaml
	// For simplicity, we'll skip this in the minimal prototype
	// Full implementation would parse hosts.yml
	return nil, nil
}

// FormatStatus returns human-readable discovery status
func FormatStatus(configs []ProviderConfig) string {
	if len(configs) == 0 {
		return "No agent credentials detected on host"
	}

	result := fmt.Sprintf("Detected %d provider(s):\n", len(configs))
	for _, cfg := range configs {
		result += fmt.Sprintf("  - %s (%s) from %s\n", cfg.Name, cfg.Type, cfg.ConfigPath)
	}
	return result
}
