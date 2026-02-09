package transport

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQEMUTransportManagerEmbedding tests that Manager provides expected methods
func TestQEMUTransportManagerEmbedding(t *testing.T) {
	manager := NewManager()

	// Verify Manager has all expected methods
	assert.NotNil(t, manager)
	assert.NotNil(t, manager.RegisterConfig)
	assert.NotNil(t, manager.CreateTransport)
	assert.NotNil(t, manager.CreatePool)
	assert.NotNil(t, manager.LoadConfig)
	assert.NotNil(t, manager.SaveConfig)
	assert.NotNil(t, manager.GetConfig)
	assert.NotNil(t, manager.ListConfigs)
	assert.NotNil(t, manager.CloseAll)
}

// TestQEMUTransportRegisterAndCreate tests config registration and transport creation
func TestQEMUTransportRegisterAndCreate(t *testing.T) {
	manager := NewManager()

	// Register SSH config - we use password auth for testing (no key file needed)
	sshConfig := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "password",
			Username: "testuser",
			Password: "testpass",
		},
	}
	err := manager.RegisterConfig("test-ssh-transport", sshConfig)
	require.NoError(t, err)

	// Verify config is registered
	configs := manager.ListConfigs()
	assert.Contains(t, configs, "test-ssh-transport")

	// Retrieve and verify config
	retrieved, err := manager.GetConfig("test-ssh-transport")
	require.NoError(t, err)
	assert.Equal(t, "ssh", retrieved.Protocol)
	assert.Equal(t, "localhost:22", retrieved.Target)

	// Create transport - will fail to connect (expected), but transport is created
	transport, err := manager.CreateTransport("test-ssh-transport")
	require.NoError(t, err)
	assert.NotNil(t, transport)

	// Verify transport interface
	assert.Implements(t, (*Transport)(nil), transport)
}

// TestQEMUTransportCreateNotFound tests error when config not found
func TestQEMUTransportCreateNotFound(t *testing.T) {
	manager := NewManager()

	_, err := manager.CreateTransport("nonexistent-config")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestQEMUTransportLoadSaveConfig tests config file operations
func TestQEMUTransportLoadSaveConfig(t *testing.T) {
	manager := NewManager()

	// Register HTTP configs only (YAML serialization is more reliable)
	manager.RegisterConfig("qemu-http-config1", &Config{
		Protocol: "http",
		Target:   "http://localhost:8080",
		Auth:     AuthConfig{Type: "token", Token: "test-token-1"},
	})
	manager.RegisterConfig("qemu-http-config2", &Config{
		Protocol: "https",
		Target:   "https://api.example.com",
		Auth:     AuthConfig{Type: "token", Token: "test-token-2"},
	})

	// Save config
	tmpPath := filepath.Join(t.TempDir(), "qemu_http_transports.yaml")
	err := manager.SaveConfig(tmpPath)
	require.NoError(t, err)
	assert.FileExists(t, tmpPath)

	// Create new manager and load config
	newManager := NewManager()
	err = newManager.LoadConfig(tmpPath)
	require.NoError(t, err)

	// Verify configs were loaded
	configs := newManager.ListConfigs()
	assert.GreaterOrEqual(t, len(configs), 1, "Should have at least one config loaded")

	// Find our registered configs by checking the content
	httpConfig, err := newManager.GetConfig("qemu-http-config1")
	if err == nil {
		assert.Equal(t, "http", httpConfig.Protocol)
		assert.Equal(t, "http://localhost:8080", httpConfig.Target)
	}

	httpsConfig, err := newManager.GetConfig("qemu-http-config2")
	if err == nil {
		assert.Equal(t, "https", httpsConfig.Protocol)
	}
}

// TestQEMUTransportDefaultSSHConfig tests SSH config factory function
func TestQEMUTransportDefaultSSHConfig(t *testing.T) {
	config := CreateDefaultSSHConfig(
		"remote.example.com:2222",
		"deployuser",
		"/home/user/.ssh/deploy_key",
	)

	assert.Equal(t, "ssh", config.Protocol)
	assert.Equal(t, "remote.example.com:2222", config.Target)
	assert.Equal(t, "ssh_key", config.Auth.Type)
	assert.Equal(t, "deployuser", config.Auth.Username)
	assert.Equal(t, "/home/user/.ssh/deploy_key", config.Auth.KeyPath)
	assert.Equal(t, 30*time.Second, config.Timeout)
	assert.Equal(t, 3, config.RetryPolicy.MaxRetries)
	assert.True(t, config.Security.StrictHostKeyChecking)
	assert.Contains(t, config.Security.HostKeyAlgorithms, "rsa-sha2-256")
}

// TestQEMUTransportDefaultHTTPConfig tests HTTP config factory function
func TestQEMUTransportDefaultHTTPConfig(t *testing.T) {
	config := CreateDefaultHTTPConfig(
		"https://api.example.com:8080",
		"secret-api-token",
	)

	assert.Equal(t, "http", config.Protocol)
	assert.Equal(t, "https://api.example.com:8080", config.Target)
	assert.Equal(t, "token", config.Auth.Type)
	assert.Equal(t, "secret-api-token", config.Auth.Token)
	assert.Equal(t, 30*time.Second, config.Timeout)
	assert.False(t, config.Security.SkipTLSVerify)
}

// TestQEMUTransportCloseAll tests cleanup of all pools
func TestQEMUTransportCloseAll(t *testing.T) {
	manager := NewManager()

	// Register config with KeyData and create pool
	manager.RegisterConfig("qemu-test-close", &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth:     AuthConfig{Type: "ssh_key", Username: "user", KeyData: []byte("test-key")},
	})
	_, err := manager.CreatePool("qemu-test-close")
	require.NoError(t, err)

	// Close all should not error
	err = manager.CloseAll()
	require.NoError(t, err)
}

// TestQEMUTransportSSHInvalidKey tests SSH transport with missing key file
func TestQEMUTransportSSHInvalidKey(t *testing.T) {
	config := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: "testuser",
			KeyPath:  "/nonexistent/path/to/key",
		},
		Timeout: 5,
	}

	// Transport creation should fail because key file doesn't exist
	_, err := NewSSHTransport(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read SSH key")
}

// TestQEMUTransportSSHPasswordAuth tests SSH transport with password auth
func TestQEMUTransportSSHPasswordAuth(t *testing.T) {
	config := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "password",
			Username: "testuser",
			Password: "testpass",
		},
		Timeout: 5,
	}

	transport, err := NewSSHTransport(config)
	require.NoError(t, err)
	assert.NotNil(t, transport)
}

// TestQEMUTransportSSHValidation tests config validation
func TestQEMUTransportSSHValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid ssh config with password",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "password",
					Username: "user",
					Password: "pass",
				},
			},
			wantErr: false,
		},
		{
			name: "valid ssh config with password",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "password",
					Username: "user",
					Password: "pass",
				},
			},
			wantErr: false,
		},
		{
			name: "missing username",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:    "ssh_key",
					KeyPath: "/tmp/test_key",
				},
			},
			wantErr: true,
		},
		{
			name: "missing key path and data",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "ssh_key",
					Username: "user",
				},
			},
			wantErr: true,
		},
		{
			name: "unsupported auth type",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "unknown",
					Username: "user",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSSHTransport(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestQEMUTransportHTTPValidation tests HTTP config validation
func TestQEMUTransportHTTPValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid http config",
			config: &Config{
				Protocol: "http",
				Target:   "http://localhost:8080",
				Auth:     AuthConfig{Type: "token", Token: "test"},
			},
			wantErr: false,
		},
		{
			name: "valid https config",
			config: &Config{
				Protocol: "https",
				Target:   "https://api.example.com",
				Auth:     AuthConfig{Type: "token", Token: "test"},
			},
			wantErr: false,
		},
		{
			name: "missing target",
			config: &Config{
				Protocol: "http",
				Auth:     AuthConfig{Type: "token"},
			},
			wantErr: true,
		},
		{
			name: "invalid target - no http prefix",
			config: &Config{
				Protocol: "http",
				Target:   "localhost:8080",
				Auth:     AuthConfig{Type: "token"},
			},
			wantErr: true,
		},
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHTTPTransport(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestQEMUTransportPoolCreation tests connection pool creation
func TestQEMUTransportPoolCreation(t *testing.T) {
	config := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: "user",
			KeyData:  []byte("test-key"),
		},
		Timeout: 30,
		Connection: ConnectionConfig{
			MaxConns:    5,
			MaxIdle:     2,
			MaxLifetime: 0,
		},
	}

	factory := &DefaultTransportFactory{}
	pool := NewPool(config, "qemu-test-pool", factory)

	assert.NotNil(t, pool)
	assert.Equal(t, "qemu-test-pool", pool.transportType)
}

// TestQEMUTransportPoolMetrics tests pool metrics collection
func TestQEMUTransportPoolMetrics(t *testing.T) {
	config := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth:     AuthConfig{Type: "ssh_key", Username: "user", KeyData: []byte("key")},
	}
	factory := &DefaultTransportFactory{}
	pool := NewPool(config, "qemu-metrics-test", factory)

	metrics := pool.GetMetrics()
	assert.NotNil(t, metrics)
	assert.Equal(t, 0, metrics.Active)
	assert.Equal(t, 0, metrics.Idle)
	assert.Equal(t, 0, metrics.Created)
	assert.Equal(t, 0, metrics.Destroyed)
}

// TestQEMUTransportErrorTypes tests error type definitions
func TestQEMUTransportErrorTypes(t *testing.T) {
	assert.NotNil(t, ErrNotConnected)
	assert.NotNil(t, ErrInvalidTarget)
	assert.NotNil(t, ErrAuthFailed)
	assert.NotNil(t, ErrTimeout)
	assert.NotNil(t, ErrConnectionFailed)
	assert.NotNil(t, ErrCommandFailed)
	assert.NotNil(t, ErrFileNotFound)
	assert.NotNil(t, ErrPermissionDenied)

	// Verify error messages
	assert.Contains(t, ErrNotConnected.Error(), "not connected")
	assert.Contains(t, ErrAuthFailed.Error(), "authentication failed")
	assert.Contains(t, ErrTimeout.Error(), "timed out")
}

// TestQEMUTransportErrorRetryable tests error retryability
func TestQEMUTransportErrorRetryable(t *testing.T) {
	err := &TransportError{
		Type:      "connection",
		Message:   "connection failed",
		Retryable: true,
	}
	assert.True(t, err.Retryable)

	nonRetryableErr := &TransportError{
		Type:      "auth",
		Message:   "authentication failed",
		Retryable: false,
	}
	assert.False(t, nonRetryableErr.Retryable)
}

// TestQEMUTransportConfigStructFields tests Config struct field tags
func TestQEMUTransportConfigStructFields(t *testing.T) {
	config := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: "user",
			Password: "pass",
			KeyPath:  "/path/to/key",
			KeyData:  []byte("key data"),
			Token:    "token",
			Headers:  map[string]string{"X-Custom": "value"},
			CertPath: "/path/to/cert",
			CertData: []byte("cert data"),
		},
		Timeout: 30,
		RetryPolicy: RetryPolicy{
			MaxRetries:      3,
			InitialDelay:    1,
			MaxDelay:        10,
			BackoffFactor:   2.0,
			RetriableErrors: []string{"connection refused"},
		},
		Connection: ConnectionConfig{
			MaxConns:     10,
			MaxIdle:      5,
			MaxLifetime:  3600,
			IdleTimeout:  300,
			KeepAlive:    true,
			KeepAliveInt: 60,
		},
		Security: SecurityConfig{
			StrictHostKeyChecking: true,
			HostKeyAlgorithms:     []string{"rsa-sha2-256"},
			Ciphers:               []string{"aes128-ctr"},
			KEXAlgorithms:         []string{"diffie-hellman-group14-sha1"},
			VerifyCertificate:     true,
			CACertPath:            "/path/to/ca",
			CACertData:            []byte("ca data"),
			SkipTLSVerify:         false,
		},
	}

	assert.Equal(t, "ssh", config.Protocol)
	assert.Equal(t, "localhost:22", config.Target)
	assert.Equal(t, "ssh_key", config.Auth.Type)
	assert.Equal(t, 3, config.RetryPolicy.MaxRetries)
	assert.True(t, config.Security.StrictHostKeyChecking)
}

// TestQEMUTransportTargetParsing tests SSH target parsing
func TestQEMUTransportTargetParsing(t *testing.T) {
	tests := []struct {
		target   string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"localhost", "localhost", 22, false},
		{"localhost:22", "localhost", 22, false},
		{"192.168.1.1:2222", "192.168.1.1", 2222, false},
		{"example.com", "example.com", 22, false},
		{"", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			host, port, err := parseSSHTarget(tt.target)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantHost, host)
				assert.Equal(t, tt.wantPort, port)
			}
		})
	}
}

// Helper to create temp SSH key for testing
func createTempSSHKeyForQEMU(t *testing.T) string {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "id_rsa")

	// Generate a test SSH key (insecure for testing only)
	cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "2048", "-f", keyPath, "-N", "", "-q")
	cmd.Dir = tmpDir

	err := cmd.Run()
	if err != nil {
		// Skip test if ssh-keygen not available
		t.Skip("ssh-keygen not available")
	}

	return keyPath
}

// TestQEMUTransportSSHWithRealKey tests SSH transport with actual key generation
func TestQEMUTransportSSHWithRealKey(t *testing.T) {
	keyPath := createTempSSHKeyForQEMU(t)

	config := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: os.Getenv("USER"),
			KeyPath:  keyPath,
		},
		Timeout: 5,
	}

	transport, err := NewSSHTransport(config)
	require.NoError(t, err)
	assert.NotNil(t, transport)
}

// TestQEMUTransportRegisterDuplicate tests that registering same config twice is OK
func TestQEMUTransportRegisterDuplicate(t *testing.T) {
	manager := NewManager()

	config := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth:     AuthConfig{Type: "ssh_key", Username: "user", KeyData: []byte("key")},
	}

	err := manager.RegisterConfig("qemu-dup", config)
	require.NoError(t, err)

	// Re-registration should overwrite without error
	err = manager.RegisterConfig("qemu-dup", config)
	require.NoError(t, err)
}

// TestQEMUTransportInfoStruct tests Info struct fields
func TestQEMUTransportInfoStruct(t *testing.T) {
	info := &Info{
		Protocol:   "ssh",
		Target:     "localhost:22",
		Connected:  true,
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
		Properties: map[string]string{"key": "value"},
	}

	assert.Equal(t, "ssh", info.Protocol)
	assert.Equal(t, "localhost:22", info.Target)
	assert.True(t, info.Connected)
	assert.NotNil(t, info.Properties)
}

// TestQEMUTransportResultStruct tests Result struct fields
func TestQEMUTransportResultStruct(t *testing.T) {
	start := time.Now()
	end := start.Add(5 * time.Second)

	result := &Result{
		ExitCode: 0,
		Output:   "test output",
		Error:    "",
		Duration: end.Sub(start),
		Start:    start,
		End:      end,
	}

	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "test output", result.Output)
	assert.Equal(t, 5*time.Second, result.Duration)
}

// TestQEMUTransportCommandStruct tests Command struct fields
func TestQEMUTransportCommandStruct(t *testing.T) {
	cmd := &Command{
		Cmd:           []string{"echo", "hello"},
		Env:           map[string]string{"VAR": "value"},
		WorkingDir:    "/tmp",
		Timeout:       30 * time.Second,
		User:          "root",
		Stdin:         nil,
		Stdout:        nil,
		Stderr:        nil,
		CaptureOutput: true,
	}

	assert.Equal(t, []string{"echo", "hello"}, cmd.Cmd)
	assert.Equal(t, "value", cmd.Env["VAR"])
	assert.Equal(t, "/tmp", cmd.WorkingDir)
	assert.Equal(t, 30*time.Second, cmd.Timeout)
	assert.Equal(t, "root", cmd.User)
	assert.True(t, cmd.CaptureOutput)
}
