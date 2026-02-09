package transport

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Manager struct {
	configs map[string]*Config
	factory TransportFactory
	pools   map[string]*Pool
}

func NewManager() *Manager {
	return &Manager{
		configs: make(map[string]*Config),
		factory: &DefaultTransportFactory{},
		pools:   make(map[string]*Pool),
	}
}

func (m *Manager) LoadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read transport config: %w", err)
	}

	var configs map[string]*Config
	if err := yaml.Unmarshal(data, &configs); err != nil {
		return fmt.Errorf("failed to parse transport config: %w", err)
	}

	for name, config := range configs {
		if err := m.RegisterConfig(name, config); err != nil {
			return fmt.Errorf("failed to register config %s: %w", name, err)
		}
	}

	return nil
}

func (m *Manager) SaveConfig(path string) error {
	data, err := yaml.Marshal(m.configs)
	if err != nil {
		return fmt.Errorf("failed to marshal transport config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write transport config: %w", err)
	}

	return nil
}

func (m *Manager) RegisterConfig(name string, config *Config) error {
	if err := validateConfig(config); err != nil {
		return fmt.Errorf("invalid config for %s: %w", name, err)
	}

	m.configs[name] = config
	return nil
}

func (m *Manager) GetConfig(name string) (*Config, error) {
	config, exists := m.configs[name]
	if !exists {
		return nil, fmt.Errorf("transport config not found: %s", name)
	}
	return config, nil
}

func (m *Manager) ListConfigs() []string {
	names := make([]string, 0, len(m.configs))
	for name := range m.configs {
		names = append(names, name)
	}
	return names
}

func (m *Manager) CreateTransport(name string) (Transport, error) {
	config, err := m.GetConfig(name)
	if err != nil {
		return nil, err
	}

	return m.factory.CreateTransport(config)
}

func (m *Manager) CreatePool(name string) (*Pool, error) {
	config, err := m.GetConfig(name)
	if err != nil {
		return nil, err
	}

	if pool, exists := m.pools[name]; exists {
		return pool, nil
	}

	pool := NewPool(config, name, m.factory)
	m.pools[name] = pool
	return pool, nil
}

func (m *Manager) CloseAll() error {
	for _, pool := range m.pools {
		if err := pool.Close(); err != nil {
			return fmt.Errorf("failed to close pool: %w", err)
		}
	}
	m.pools = make(map[string]*Pool)
	return nil
}

func validateConfig(config *Config) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	if config.Protocol == "" {
		return fmt.Errorf("protocol is required")
	}

	if config.Target == "" {
		return fmt.Errorf("target is required")
	}

	switch config.Protocol {
	case "ssh":
		return validateSSHConfig(config)
	case "http", "https":
		return validateHTTPConfig(config)
	default:
		return fmt.Errorf("unsupported protocol: %s", config.Protocol)
	}
}

type DefaultTransportFactory struct{}

func (f *DefaultTransportFactory) CreateTransport(config *Config) (Transport, error) {
	switch config.Protocol {
	case "ssh":
		return NewSSHTransport(config)
	case "http", "https":
		return NewHTTPTransport(config)
	default:
		return nil, fmt.Errorf("unsupported transport protocol: %s", config.Protocol)
	}
}

func CreateDefaultSSHConfig(target, username, keyPath string) *Config {
	return &Config{
		Protocol: "ssh",
		Target:   target,
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: username,
			KeyPath:  keyPath,
		},
		Timeout: 30 * time.Second,
		RetryPolicy: RetryPolicy{
			MaxRetries:    3,
			InitialDelay:  1 * time.Second,
			MaxDelay:      10 * time.Second,
			BackoffFactor: 2.0,
		},
		Connection: ConnectionConfig{
			MaxConns:    5,
			MaxIdle:     2,
			MaxLifetime: 1 * time.Hour,
			IdleTimeout: 30 * time.Minute,
			KeepAlive:   true,
		},
		Security: SecurityConfig{
			StrictHostKeyChecking: true,
			HostKeyAlgorithms:     []string{"rsa-sha2-256", "rsa-sha2-512", "ssh-rsa"},
			Ciphers:               []string{"aes128-ctr", "aes192-ctr", "aes256-ctr"},
		},
	}
}

func CreateDefaultHTTPConfig(target, token string) *Config {
	return &Config{
		Protocol: "http",
		Target:   target,
		Auth: AuthConfig{
			Type:  "token",
			Token: token,
		},
		Timeout: 30 * time.Second,
		RetryPolicy: RetryPolicy{
			MaxRetries:    3,
			InitialDelay:  1 * time.Second,
			MaxDelay:      10 * time.Second,
			BackoffFactor: 2.0,
		},
		Connection: ConnectionConfig{
			MaxConns:    10,
			MaxIdle:     5,
			MaxLifetime: 30 * time.Minute,
			IdleTimeout: 5 * time.Minute,
		},
		Security: SecurityConfig{
			SkipTLSVerify:     false,
			VerifyCertificate: true,
		},
	}
}
