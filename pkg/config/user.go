package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type UserConfig struct {
	GitHub struct {
		Username  string `yaml:"username,omitempty"`
		UserID    int64  `yaml:"user_id,omitempty"`
		AvatarURL string `yaml:"avatar_url,omitempty"`
	} `yaml:"github,omitempty"`
	SSH struct {
		KeyPath   string `yaml:"key_path,omitempty"`
		PublicKey string `yaml:"public_key,omitempty"`
	} `yaml:"ssh,omitempty"`
	Slack *SlackConfig `yaml:"slack,omitempty"`
	Editor     string `yaml:"editor,omitempty"`
	Workspaces []struct {
		Name   string `yaml:"name"`
		ID     string `yaml:"id"`
		Status string `yaml:"status"`
	} `yaml:"workspaces,omitempty"`
}

// SlackConfig represents the Slack integration configuration.
type SlackConfig struct {
	WebhookURL string `yaml:"webhook_url,omitempty"`
	Channel    string `yaml:"channel,omitempty"`
	Enabled    bool   `yaml:"enabled,omitempty"`
}

func LoadUserConfig(path string) (*UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read user config: %w", err)
	}

	var cfg UserConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse user config: %w", err)
	}

	return &cfg, nil
}

func SaveUserConfig(path string, cfg *UserConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal user config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write user config: %w", err)
	}

	return nil
}

func ValidateUserConfig(cfg *UserConfig) error {
	if cfg == nil {
		return fmt.Errorf("user config is nil")
	}
	return nil
}

func EnsureConfigDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	return nil
}

func GetUserConfigPath() string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".nexus", "config.yaml")
}

func AddWorkspaceToConfig(path string, name, id, status string) error {
	cfg, err := LoadUserConfig(path)
	if err != nil {
		cfg = &UserConfig{}
	}

	workspace := struct {
		Name   string `yaml:"name"`
		ID     string `yaml:"id"`
		Status string `yaml:"status"`
	}{
		Name:   name,
		ID:     id,
		Status: status,
	}

	cfg.Workspaces = append(cfg.Workspaces, workspace)

	return SaveUserConfig(path, cfg)
}
