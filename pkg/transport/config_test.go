package transport

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager(t *testing.T) {
	manager := NewManager()

	// Test initial state
	assert.NotNil(t, manager)
	assert.NotNil(t, manager.configs)
	assert.NotNil(t, manager.factory)
	assert.NotNil(t, manager.pools)

	// Test empty config list
	configs := manager.ListConfigs()
	assert.Empty(t, configs)
}

func TestManagerRegisterConfig(t *testing.T) {
	manager := NewManager()

	// Test valid SSH config
	sshConfig := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: "test",
			KeyData:  []byte("test-key"),
		},
	}

	err := manager.RegisterConfig("test-ssh", sshConfig)
	require.NoError(t, err)

	// Test valid HTTP config
	httpConfig := &Config{
		Protocol: "http",
		Target:   "http://localhost:8080",
		Auth: AuthConfig{
			Type:  "token",
			Token: "test-token",
		},
	}

	err = manager.RegisterConfig("test-http", httpConfig)
	require.NoError(t, err)

	// Verify configs are registered
	configs := manager.ListConfigs()
	assert.Len(t, configs, 2)
	assert.Contains(t, configs, "test-ssh")
	assert.Contains(t, configs, "test-http")
}

func TestManagerRegisterInvalidConfig(t *testing.T) {
	manager := NewManager()

	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
			errMsg:  "config cannot be nil",
		},
		{
			name: "missing protocol",
			config: &Config{
				Target: "localhost:22",
			},
			wantErr: true,
			errMsg:  "protocol is required",
		},
		{
			name: "missing target",
			config: &Config{
				Protocol: "ssh",
			},
			wantErr: true,
			errMsg:  "target is required",
		},
		{
			name: "unsupported protocol",
			config: &Config{
				Protocol: "invalid",
				Target:   "test://example.com",
			},
			wantErr: true,
			errMsg:  "unsupported protocol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.RegisterConfig("test", tt.config)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestManagerGetConfig(t *testing.T) {
	manager := NewManager()

	// Test non-existent config
	config, err := manager.GetConfig("nonexistent")
	assert.Nil(t, config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Test existing config
	testConfig := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "password",
			Username: "test",
			Password: "test",
		},
	}

	err = manager.RegisterConfig("test", testConfig)
	require.NoError(t, err)

	retrievedConfig, err := manager.GetConfig("test")
	require.NoError(t, err)
	assert.Equal(t, testConfig, retrievedConfig)
}

func TestManagerCreateTransport(t *testing.T) {
	manager := NewManager()

	// Register HTTP config (to avoid SSH key file issues)
	httpConfig := CreateDefaultHTTPConfig("http://localhost:8080", "test-token")
	err := manager.RegisterConfig("http-test", httpConfig)
	require.NoError(t, err)

	// Create transport
	transport, err := manager.CreateTransport("http-test")
	require.NoError(t, err)
	assert.NotNil(t, transport)

	// Test with non-existent config
	transport, err = manager.CreateTransport("nonexistent")
	assert.Nil(t, transport)
	assert.Error(t, err)
}

func TestManagerCreatePool(t *testing.T) {
	manager := NewManager()

	// Register HTTP config
	httpConfig := CreateDefaultHTTPConfig("http://localhost:8080", "test-token")
	err := manager.RegisterConfig("http-test", httpConfig)
	require.NoError(t, err)

	// Create pool
	pool, err := manager.CreatePool("http-test")
	require.NoError(t, err)
	assert.NotNil(t, pool)

	// Get same pool again (should return existing)
	pool2, err := manager.CreatePool("http-test")
	require.NoError(t, err)
	assert.Equal(t, pool, pool2)

	// Test with non-existent config
	pool, err = manager.CreatePool("nonexistent")
	assert.Nil(t, pool)
	assert.Error(t, err)
}

func TestManagerSaveLoadConfig(t *testing.T) {
	manager := NewManager()

	// Register configs
	sshConfig := CreateDefaultSSHConfig("localhost:22", "testuser", "/tmp/key")
	err := manager.RegisterConfig("ssh", sshConfig)
	require.NoError(t, err)

	httpConfig := CreateDefaultHTTPConfig("http://api.example.com", "token123")
	err = manager.RegisterConfig("http", httpConfig)
	require.NoError(t, err)

	// Save to temp file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "transport.yaml")

	err = manager.SaveConfig(configPath)
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(configPath)
	assert.NoError(t, err)

	// Load into new manager
	newManager := NewManager()
	err = newManager.LoadConfig(configPath)
	require.NoError(t, err)

	// Verify configs were loaded
	configs := newManager.ListConfigs()
	assert.Len(t, configs, 2)

	loadedSSHConfig, err := newManager.GetConfig("ssh")
	require.NoError(t, err)
	assert.Equal(t, sshConfig.Protocol, loadedSSHConfig.Protocol)
	assert.Equal(t, sshConfig.Target, loadedSSHConfig.Target)

	loadedHTTPConfig, err := newManager.GetConfig("http")
	require.NoError(t, err)
	assert.Equal(t, httpConfig.Protocol, loadedHTTPConfig.Protocol)
	assert.Equal(t, httpConfig.Target, loadedHTTPConfig.Target)
}

func TestDefaultTransportFactory(t *testing.T) {
	factory := &DefaultTransportFactory{}

	// Test HTTP transport creation
	httpConfig := CreateDefaultHTTPConfig("http://localhost:8080", "token")
	transport, err := factory.CreateTransport(httpConfig)
	assert.NoError(t, err)
	assert.NotNil(t, transport)

	// Test SSH transport creation with key data
	sshConfig := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: "test",
			KeyData:  []byte("test-key-data"),
		},
	}
	transport, err = factory.CreateTransport(sshConfig)
	assert.Error(t, err) // Will fail with invalid key data

	// Test unsupported protocol
	invalidConfig := &Config{
		Protocol: "invalid",
		Target:   "test://example.com",
	}
	transport, err = factory.CreateTransport(invalidConfig)
	assert.Nil(t, transport)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport protocol")
}

func TestCreateDefaultSSHConfig(t *testing.T) {
	config := CreateDefaultSSHConfig("test.example.com:22", "testuser", "/home/test/.ssh/id_rsa")

	assert.Equal(t, "ssh", config.Protocol)
	assert.Equal(t, "test.example.com:22", config.Target)
	assert.Equal(t, "ssh_key", config.Auth.Type)
	assert.Equal(t, "testuser", config.Auth.Username)
	assert.Equal(t, "/home/test/.ssh/id_rsa", config.Auth.KeyPath)
	assert.Equal(t, 30*time.Second, config.Timeout)
	assert.Equal(t, 3, config.RetryPolicy.MaxRetries)
	assert.Equal(t, 5, config.Connection.MaxConns)
	assert.True(t, config.Security.StrictHostKeyChecking)
}

func TestCreateDefaultHTTPConfig(t *testing.T) {
	config := CreateDefaultHTTPConfig("https://api.example.com:8080", "test-token-123")

	assert.Equal(t, "http", config.Protocol)
	assert.Equal(t, "https://api.example.com:8080", config.Target)
	assert.Equal(t, "token", config.Auth.Type)
	assert.Equal(t, "test-token-123", config.Auth.Token)
	assert.Equal(t, 30*time.Second, config.Timeout)
	assert.Equal(t, 3, config.RetryPolicy.MaxRetries)
	assert.Equal(t, 10, config.Connection.MaxConns)
	assert.False(t, config.Security.SkipTLSVerify)
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name: "valid SSH config",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "password",
					Username: "test",
					Password: "test",
				},
			},
			wantErr: false,
		},
		{
			name: "valid HTTP config",
			config: &Config{
				Protocol: "http",
				Target:   "http://localhost:8080",
				Auth: AuthConfig{
					Type:  "token",
					Token: "test-token",
				},
			},
			wantErr: false,
		},
		{
			name: "missing protocol",
			config: &Config{
				Target: "localhost:22",
			},
			wantErr: true,
		},
		{
			name: "missing target",
			config: &Config{
				Protocol: "ssh",
			},
			wantErr: true,
		},
		{
			name: "unsupported protocol",
			config: &Config{
				Protocol: "invalid",
				Target:   "test://example.com",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestManagerCloseAll(t *testing.T) {
	manager := NewManager()

	// Create some pools
	sshConfig := CreateDefaultSSHConfig("localhost:22", "test", "/tmp/key")
	err := manager.RegisterConfig("ssh", sshConfig)
	require.NoError(t, err)

	httpConfig := CreateDefaultHTTPConfig("http://localhost:8080", "token")
	err = manager.RegisterConfig("http", httpConfig)
	require.NoError(t, err)

	pool1, err := manager.CreatePool("ssh")
	require.NoError(t, err)
	pool2, err := manager.CreatePool("http")
	require.NoError(t, err)

	// Close all pools
	err = manager.CloseAll()
	require.NoError(t, err)

	// Verify pools are cleared
	assert.Empty(t, manager.pools)

	// Pools should still be functional
	assert.NotNil(t, pool1)
	assert.NotNil(t, pool2)
}
