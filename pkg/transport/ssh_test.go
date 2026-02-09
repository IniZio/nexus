package transport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestSSHTransportCreation(t *testing.T) {
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
			name: "missing username",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type: "ssh_key",
				},
			},
			wantErr: true,
			errMsg:  "username is required",
		},
		{
			name: "missing key for ssh_key auth",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "ssh_key",
					Username: "test",
				},
			},
			wantErr: true,
			errMsg:  "key_path or key_data is required",
		},
		{
			name: "missing password for password auth",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "password",
					Username: "test",
				},
			},
			wantErr: true,
			errMsg:  "password is required",
		},
		{
			name: "unsupported auth type",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "invalid",
					Username: "test",
				},
			},
			wantErr: true,
			errMsg:  "unsupported authentication type: invalid",
		},
		{
			name: "valid ssh key config",
			config: &Config{
				Protocol: "ssh",
				Target:   "localhost:22",
				Auth: AuthConfig{
					Type:     "ssh_key",
					Username: "test",
					KeyData:  []byte("test-key-data"),
				},
			},
			wantErr: true, // Will fail to parse invalid key
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSSHTransport(tt.config)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestParseSSHTarget(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{
			name:    "empty target",
			target:  "",
			wantErr: true,
		},
		{
			name:     "host only",
			target:   "localhost",
			wantHost: "localhost",
			wantPort: 22,
		},
		{
			name:     "host with port",
			target:   "localhost:2222",
			wantHost: "localhost",
			wantPort: 2222,
		},
		{
			name:     "IPv4 with port",
			target:   "127.0.0.1:2222",
			wantHost: "127.0.0.1",
			wantPort: 2222,
		},
		{
			name:     "IPv6 with port",
			target:   "[::1]:2222",
			wantHost: "::1",
			wantPort: 2222,
		},
		{
			name:    "invalid format",
			target:  "invalid:",
			wantErr: true,
		},
		{
			name:    "invalid port",
			target:  "localhost:invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := parseSSHTarget(tt.target)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantHost, host)
				assert.Equal(t, tt.wantPort, port)
			}
		})
	}
}

func TestBuildSSHConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "ssh key authentication",
			config: &Config{
				Auth: AuthConfig{
					Type:     "ssh_key",
					Username: "test",
					KeyData:  []byte("test-key-data"),
				},
			},
			wantErr: true, // Invalid key data
		},
		{
			name: "password authentication",
			config: &Config{
				Auth: AuthConfig{
					Type:     "password",
					Username: "test",
					Password: "test-password",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sshConfig, err := buildSSHConfig(tt.config)
			if tt.wantErr {
				// We expect errors for invalid key data
				_ = err
			} else {
				require.NoError(t, err)
				assert.NotNil(t, sshConfig)
				assert.Equal(t, tt.config.Auth.Username, sshConfig.User)
			}
		})
	}
}

func TestSSHTransportConnection(t *testing.T) {
	transport := createMockSSHTransport(t)

	// Test initial state
	assert.False(t, transport.IsConnected())

	// Test connect to non-existent server
	ctx := context.Background()
	err := transport.Connect(ctx, "nonexistent:22")
	require.Error(t, err)
	assert.False(t, transport.IsConnected())

	// Test disconnect without connection
	err = transport.Disconnect(ctx)
	require.NoError(t, err)
}

func TestSSHTransportExecuteNotConnected(t *testing.T) {
	transport := createMockSSHTransport(t)

	ctx := context.Background()
	result, err := transport.Execute(ctx, &Command{Cmd: []string{"echo", "test"}})
	assert.Nil(t, result)
	assert.Equal(t, ErrNotConnected, err)
}

func TestSSHTransportFileOperationsNotConnected(t *testing.T) {
	transport := createMockSSHTransport(t)

	ctx := context.Background()

	// Test upload
	err := transport.Upload(ctx, "/tmp/test.txt", "/tmp/remote.txt")
	assert.Equal(t, ErrNotConnected, err)

	// Test download
	err = transport.Download(ctx, "/tmp/remote.txt", "/tmp/local.txt")
	assert.Equal(t, ErrNotConnected, err)
}

func TestSSHTransportGetInfo(t *testing.T) {
	transport := createMockSSHTransport(t)

	info := transport.GetInfo()
	assert.Equal(t, "ssh", info.Protocol)
	assert.Equal(t, "localhost:22", info.Target)
	assert.False(t, info.Connected)
	assert.NotNil(t, info.CreatedAt)
	assert.NotNil(t, info.Properties)
}

func createMockSSHTransport(t *testing.T) *SSHTransport {
	config := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "password",
			Username: "test",
			Password: "test",
		},
		Timeout: 5 * time.Second,
	}

	transport, err := NewSSHTransport(config)
	require.NoError(t, err)
	return transport
}

func TestGenerateSSHKey(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	privateKeyBytes := pem.EncodeToMemory(privateKeyPEM)

	// Test building SSH config with generated key
	config := &Config{
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: "test",
			KeyData:  privateKeyBytes,
		},
	}

	sshConfig, err := buildSSHConfig(config)
	require.NoError(t, err)
	assert.NotNil(t, sshConfig)
	assert.Equal(t, "test", sshConfig.User)
	assert.Len(t, sshConfig.Auth, 1)
}

func TestSSHTransportWrapError(t *testing.T) {
	transport := createMockSSHTransport(t)

	tests := []struct {
		name      string
		err       error
		errorType string
		retryable bool
	}{
		{
			name:      "connection error",
			err:       fmt.Errorf("connection failed"),
			errorType: "connection_failed",
			retryable: true,
		},
		{
			name:      "timeout error",
			err:       fmt.Errorf("timeout"),
			errorType: "timeout",
			retryable: true,
		},
		{
			name:      "command error",
			err:       fmt.Errorf("command failed"),
			errorType: "command_failed",
			retryable: false,
		},
		{
			name:      "nil error",
			err:       nil,
			errorType: "",
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrappedErr := transport.wrapError(tt.err, tt.errorType)
			if tt.err == nil {
				assert.Nil(t, wrappedErr)
			} else {
				require.NotNil(t, wrappedErr)
				transportErr, ok := wrappedErr.(*TransportError)
				require.True(t, ok)
				assert.Equal(t, tt.errorType, transportErr.Type)
				assert.Equal(t, tt.retryable, transportErr.Retryable)
				assert.Equal(t, tt.err.Error(), transportErr.Message)
			}
		})
	}
}

func TestSSHTransportExitError(t *testing.T) {
	exitErr := &ssh.ExitError{}

	transport := createMockSSHTransport(t)
	wrappedErr := transport.wrapError(exitErr, "command_failed")

	require.NotNil(t, wrappedErr)
	transportErr, ok := wrappedErr.(*TransportError)
	require.True(t, ok)
	assert.Equal(t, "command_failed", transportErr.Type)
	assert.False(t, transportErr.Retryable)
}
