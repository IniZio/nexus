package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Sync SyncConfig `yaml:"sync"`
}

type SyncConfig struct {
	Provider string   `yaml:"provider"`
	Mode     string   `yaml:"mode"`
	Exclude  []string `yaml:"exclude"`
}

func (s SyncConfig) ToSyncConfig() *syncConfig {
	return &syncConfig{
		Mode:     s.Mode,
		Exclude:  s.Exclude,
	}
}

type syncConfig struct {
	Mode          string        `yaml:"mode"`
	Exclude       []string      `yaml:"exclude"`
	WatchInterval time.Duration `yaml:"-"`
}

func DefaultConfigDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".nexus"
	}
	return filepath.Join(homeDir, ".nexus")
}

func DefaultConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "config.yaml")
}

func LoadConfig(configPath string) (*Config, error) {
	if configPath == "" {
		configPath = DefaultConfigPath()
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if cfg.Sync.Mode == "" {
		cfg.Sync.Mode = "two-way-safe"
	}

	if cfg.Sync.Exclude == nil {
		cfg.Sync.Exclude = []string{"node_modules", ".git"}
	}

	return &cfg, nil
}

func DefaultConfig() *Config {
	return &Config{
		Sync: SyncConfig{
			Provider: "mutagen",
			Mode:     "two-way-safe",
			Exclude:  []string{"node_modules", ".git"},
		},
	}
}

func EnsureConfigDir() error {
	configDir := DefaultConfigDir()
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	return nil
}

func WriteDefaultConfig() error {
	if err := EnsureConfigDir(); err != nil {
		return err
	}

	configPath := DefaultConfigPath()
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}

	cfg := DefaultConfig()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(configPath, data, 0644)
}
