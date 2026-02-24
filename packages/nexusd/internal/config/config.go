package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

type Config struct {
	Version   string          `yaml:"version"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Boulder   BoulderConfig   `yaml:"boulder"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	Daemon    DaemonConfig    `yaml:"daemon"`
	CLI       CLIConfig       `yaml:"cli"`
	Backends  BackendConfigs  `yaml:"backends"`
}

type BackendConfigs struct {
	Docker  DockerConfig        `yaml:"docker"`
	Daytona types.DaytonaConfig `yaml:"daytona"`
}

type DockerConfig struct {
	Enabled bool `yaml:"enabled"`
}

type WorkspaceConfig struct {
	DefaultBackend types.BackendType `yaml:"default_backend"`
	Default        string            `yaml:"default"`
	AutoStart      bool              `yaml:"auto_start"`
	StoragePath    string            `yaml:"storage_path"`
}

type BoulderConfig struct {
	EnforcementLevel string `yaml:"enforcement_level"`
	IdleThreshold    int    `yaml:"idle_threshold"`
}

type TelemetryConfig struct {
	Enabled       bool `yaml:"enabled"`
	Sampling      int  `yaml:"sampling"`
	RetentionDays int  `yaml:"retention_days"`
}

type DaemonConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type CLIConfig struct {
	Update UpdateConfig `yaml:"update"`
}

type UpdateConfig struct {
	AutoInstall bool   `yaml:"auto_install"`
	Channel     string `yaml:"channel"`
}

func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	return &Config{
		Version: "1",
		Workspace: WorkspaceConfig{
			DefaultBackend: types.BackendDocker,
			Default:        "",
			AutoStart:      true,
			StoragePath:    filepath.Join(homeDir, ".nexus", "workspaces"),
		},
		Boulder: BoulderConfig{
			EnforcementLevel: "normal",
			IdleThreshold:    30,
		},
		Telemetry: TelemetryConfig{
			Enabled:       true,
			Sampling:      100,
			RetentionDays: 30,
		},
		Daemon: DaemonConfig{
			Host: "localhost",
			Port: 9847,
		},
		CLI: CLIConfig{
			Update: UpdateConfig{
				AutoInstall: true,
				Channel:     "stable",
			},
		},
		Backends: BackendConfigs{
			Docker: DockerConfig{
				Enabled: true,
			},
			Daytona: types.DaytonaConfig{
				Enabled: false,
				APIURL:  "https://app.daytona.io/api",
			},
		},
	}
}

func ConfigPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".nexus", "config.yaml")
}

func DirPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".nexus")
}

func Load() (*Config, error) {
	path := ConfigPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if err := cfg.Save(); err != nil {
				return nil, fmt.Errorf("failed to create default config: %w", err)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Save() error {
	dir := DirPath()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(ConfigPath(), data, 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

func (c *Config) Get(key string) (string, error) {
	switch key {
	case "version":
		return c.Version, nil
	case "workspace.default":
		return c.Workspace.Default, nil
	case "workspace.default_backend":
		return c.Workspace.DefaultBackend.String(), nil
	case "workspace.auto_start":
		return fmt.Sprintf("%t", c.Workspace.AutoStart), nil
	case "workspace.storage_path":
		return c.Workspace.StoragePath, nil
	case "boulder.enforcement_level":
		return c.Boulder.EnforcementLevel, nil
	case "boulder.idle_threshold":
		return fmt.Sprintf("%d", c.Boulder.IdleThreshold), nil
	case "telemetry.enabled":
		return fmt.Sprintf("%t", c.Telemetry.Enabled), nil
	case "telemetry.sampling":
		return fmt.Sprintf("%d", c.Telemetry.Sampling), nil
	case "telemetry.retention_days":
		return fmt.Sprintf("%d", c.Telemetry.RetentionDays), nil
	case "daemon.host":
		return c.Daemon.Host, nil
	case "daemon.port":
		return fmt.Sprintf("%d", c.Daemon.Port), nil
	case "cli.update.auto_install":
		return fmt.Sprintf("%t", c.CLI.Update.AutoInstall), nil
	case "cli.update.channel":
		return c.CLI.Update.Channel, nil
	default:
		return "", fmt.Errorf("unknown key: %s", key)
	}
}

func (c *Config) Set(key, value string) error {
	switch key {
	case "version":
		c.Version = value
	case "workspace.default":
		c.Workspace.Default = value
	case "workspace.default_backend":
		c.Workspace.DefaultBackend = types.BackendTypeFromString(value)
	case "workspace.auto_start":
		c.Workspace.AutoStart = value == "true"
	case "workspace.storage_path":
		c.Workspace.StoragePath = value
	case "boulder.enforcement_level":
		c.Boulder.EnforcementLevel = value
	case "boulder.idle_threshold":
		if _, err := fmt.Sscanf(value, "%d", &c.Boulder.IdleThreshold); err != nil {
			return fmt.Errorf("invalid idle_threshold value: %w", err)
		}
	case "telemetry.enabled":
		c.Telemetry.Enabled = value == "true"
	case "telemetry.sampling":
		if _, err := fmt.Sscanf(value, "%d", &c.Telemetry.Sampling); err != nil {
			return fmt.Errorf("invalid sampling value: %w", err)
		}
	case "telemetry.retention_days":
		if _, err := fmt.Sscanf(value, "%d", &c.Telemetry.RetentionDays); err != nil {
			return fmt.Errorf("invalid retention_days value: %w", err)
		}
	case "daemon.host":
		c.Daemon.Host = value
	case "daemon.port":
		if _, err := fmt.Sscanf(value, "%d", &c.Daemon.Port); err != nil {
			return fmt.Errorf("invalid port value: %w", err)
		}
	case "cli.update.auto_install":
		c.CLI.Update.AutoInstall = value == "true"
	case "cli.update.channel":
		c.CLI.Update.Channel = value
	default:
		return fmt.Errorf("unknown key: %s", key)
	}
	return c.Save()
}
