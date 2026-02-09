package coordination

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds the coordination server configuration
type Config struct {
	Server struct {
		Host         string `yaml:"host,omitempty"`
		Port         int    `yaml:"port,omitempty"`
		AuthToken    string `yaml:"auth_token,omitempty"`
		JWTSecret    string `yaml:"jwt_secret,omitempty"`
		ReadTimeout  string `yaml:"read_timeout,omitempty"`
		WriteTimeout string `yaml:"write_timeout,omitempty"`
		IdleTimeout  string `yaml:"idle_timeout,omitempty"`
	} `yaml:"server,omitempty"`

	Registry struct {
		Provider            string        `yaml:"provider,omitempty"`
		SyncInterval        string        `yaml:"sync_interval,omitempty"`
		HealthCheckInterval string        `yaml:"health_check_interval,omitempty"`
		NodeTimeout         string        `yaml:"node_timeout,omitempty"`
		MaxRetries          int           `yaml:"max_retries,omitempty"`
		Storage             StorageConfig `yaml:"storage,omitempty"`
	} `yaml:"registry,omitempty"`

	WebSocket struct {
		Enabled    bool     `yaml:"enabled,omitempty"`
		Path       string   `yaml:"path,omitempty"`
		Origins    []string `yaml:"origins,omitempty"`
		PingPeriod string   `yaml:"ping_period,omitempty"`
	} `yaml:"websocket,omitempty"`

	Auth struct {
		Enabled     bool     `yaml:"enabled,omitempty"`
		JWTSecret   string   `yaml:"jwt_secret,omitempty"`
		TokenExpiry string   `yaml:"token_expiry,omitempty"`
		AllowedIPs  []string `yaml:"allowed_ips,omitempty"`
	} `yaml:"auth,omitempty"`

	Logging struct {
		Level      string `yaml:"level,omitempty"`
		Format     string `yaml:"format,omitempty"`
		Output     string `yaml:"output,omitempty"`
		MaxSize    int    `yaml:"max_size,omitempty"`
		MaxBackups int    `yaml:"max_backups,omitempty"`
		MaxAge     int    `yaml:"max_age,omitempty"`
	} `yaml:"logging,omitempty"`

	Provider struct {
		Type string `yaml:"type,omitempty"`
		LXC  struct {
			Remote struct {
				Node string `yaml:"node,omitempty"`
				User string `yaml:"user,omitempty"`
				Port int    `yaml:"port,omitempty"`
			} `yaml:"remote,omitempty"`
		} `yaml:"lxc,omitempty"`
	} `yaml:"provider,omitempty"`
}

type StorageConfig struct {
	Type   string                 `yaml:"type,omitempty"`
	Path   string                 `yaml:"path,omitempty"`
	Config map[string]interface{} `yaml:"config,omitempty"`
}

// LoadConfig loads coordination configuration from file
func LoadConfig(path string) (*Config, error) {
	// Set defaults
	cfg := &Config{}
	cfg.Server.Host = "0.0.0.0"
	cfg.Server.Port = 3001
	cfg.Server.ReadTimeout = "30s"
	cfg.Server.WriteTimeout = "30s"
	cfg.Server.IdleTimeout = "120s"

	cfg.Registry.Provider = "memory"
	cfg.Registry.SyncInterval = "30s"
	cfg.Registry.HealthCheckInterval = "10s"
	cfg.Registry.NodeTimeout = "60s"
	cfg.Registry.MaxRetries = 3
	cfg.Registry.Storage.Type = "sqlite"
	cfg.Registry.Storage.Path = ""

	cfg.WebSocket.Enabled = true
	cfg.WebSocket.Path = "/ws"
	cfg.WebSocket.PingPeriod = "30s"

	cfg.Auth.Enabled = false
	cfg.Auth.TokenExpiry = "24h"

	cfg.Logging.Level = "info"
	cfg.Logging.Format = "json"
	cfg.Logging.Output = "stdout"
	cfg.Logging.MaxSize = 100
	cfg.Logging.MaxBackups = 3
	cfg.Logging.MaxAge = 28

	cfg.Provider.Type = "lxc"
	cfg.Provider.LXC.Remote.User = "root"
	cfg.Provider.LXC.Remote.Port = 22

	// Load from file if exists
	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	// Environment variable overrides
	if host := os.Getenv("VENDETTA_COORD_HOST"); host != "" {
		cfg.Server.Host = host
	}
	if port := os.Getenv("VENDETTA_COORD_PORT"); port != "" {
		var p int
		if _, err := fmt.Sscanf(port, "%d", &p); err == nil {
			cfg.Server.Port = p
		}
	}
	if jwtSecret := os.Getenv("VENDETTA_JWT_SECRET"); jwtSecret != "" {
		cfg.Auth.JWTSecret = jwtSecret
		cfg.Server.JWTSecret = jwtSecret
	}

	return cfg, nil
}

// GetConfigPath returns the default config path
func GetConfigPath() string {
	// Fallback to user config
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "nexus", "coordination.yaml")
	}

	// Current directory fallback
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Join(cwd, ".nexus", "coordination.yaml")
	}

	return ".nexus/coordination.yaml"
}

// SaveConfig saves configuration to file
func SaveConfig(cfg *Config, path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
