package transport

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransportInterface(t *testing.T) {
	// Test that Transport interface is properly defined
	var _ Transport = &MockTransport{}
}

func TestCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  *Command
		want *Command
	}{
		{
			name: "simple command",
			cmd: &Command{
				Cmd: []string{"ls", "-la"},
			},
			want: &Command{
				Cmd: []string{"ls", "-la"},
			},
		},
		{
			name: "command with environment",
			cmd: &Command{
				Cmd: []string{"echo", "$HOME"},
				Env: map[string]string{"HOME": "/tmp"},
			},
			want: &Command{
				Cmd: []string{"echo", "$HOME"},
				Env: map[string]string{"HOME": "/tmp"},
			},
		},
		{
			name: "command with working directory",
			cmd: &Command{
				Cmd:        []string{"pwd"},
				WorkingDir: "/tmp",
			},
			want: &Command{
				Cmd:        []string{"pwd"},
				WorkingDir: "/tmp",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cmd)
		})
	}
}

func TestResult(t *testing.T) {
	start := time.Now()
	end := start.Add(100 * time.Millisecond)

	result := &Result{
		ExitCode: 0,
		Output:   "success",
		Duration: 100 * time.Millisecond,
		Start:    start,
		End:      end,
	}

	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "success", result.Output)
	assert.Equal(t, 100*time.Millisecond, result.Duration)
	assert.Equal(t, start, result.Start)
	assert.Equal(t, end, result.End)
}

func TestInfo(t *testing.T) {
	now := time.Now()
	info := &Info{
		Protocol:  "ssh",
		Target:    "localhost:22",
		Connected: true,
		CreatedAt: now,
		LastUsed:  now,
		Properties: map[string]string{
			"user": "test",
		},
	}

	assert.Equal(t, "ssh", info.Protocol)
	assert.Equal(t, "localhost:22", info.Target)
	assert.True(t, info.Connected)
	assert.Equal(t, now, info.CreatedAt)
	assert.Equal(t, now, info.LastUsed)
	assert.Equal(t, "test", info.Properties["user"])
}

func TestConfig(t *testing.T) {
	cfg := &Config{
		Protocol: "ssh",
		Target:   "localhost:22",
		Auth: AuthConfig{
			Type:     "ssh_key",
			Username: "testuser",
			KeyPath:  "/tmp/test_key",
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
			IdleTimeout: 30 * time.Second,
			KeepAlive:   true,
		},
		Security: SecurityConfig{
			StrictHostKeyChecking: true,
			HostKeyAlgorithms:     []string{"rsa-sha2-256", "rsa-sha2-512"},
			Ciphers:               []string{"aes128-ctr", "aes192-ctr", "aes256-ctr"},
		},
	}

	assert.Equal(t, "ssh", cfg.Protocol)
	assert.Equal(t, "localhost:22", cfg.Target)
	assert.Equal(t, "ssh_key", cfg.Auth.Type)
	assert.Equal(t, "testuser", cfg.Auth.Username)
	assert.Equal(t, "/tmp/test_key", cfg.Auth.KeyPath)
	assert.Equal(t, 30*time.Second, cfg.Timeout)
	assert.Equal(t, 3, cfg.RetryPolicy.MaxRetries)
	assert.Equal(t, 5, cfg.Connection.MaxConns)
	assert.True(t, cfg.Security.StrictHostKeyChecking)
}

func TestTransportError(t *testing.T) {
	tests := []struct {
		name string
		err  *TransportError
		want string
	}{
		{
			name: "connection error",
			err: &TransportError{
				Type:      "connection",
				Message:   "connection failed",
				Code:      500,
				Retryable: true,
			},
			want: "connection failed",
		},
		{
			name: "auth error",
			err:  ErrAuthFailed,
			want: "authentication failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestErrorTypes(t *testing.T) {
	tests := []struct {
		name          string
		err           *TransportError
		wantType      string
		wantRetryable bool
	}{
		{
			name:          "not connected",
			err:           ErrNotConnected,
			wantType:      "connection",
			wantRetryable: false,
		},
		{
			name:          "invalid target",
			err:           ErrInvalidTarget,
			wantType:      "target",
			wantRetryable: false,
		},
		{
			name:          "auth failed",
			err:           ErrAuthFailed,
			wantType:      "auth",
			wantRetryable: false,
		},
		{
			name:          "timeout",
			err:           ErrTimeout,
			wantType:      "timeout",
			wantRetryable: true,
		},
		{
			name:          "connection failed",
			err:           ErrConnectionFailed,
			wantType:      "connection",
			wantRetryable: true,
		},
		{
			name:          "command failed",
			err:           ErrCommandFailed,
			wantType:      "command",
			wantRetryable: false,
		},
		{
			name:          "file not found",
			err:           ErrFileNotFound,
			wantType:      "file",
			wantRetryable: false,
		},
		{
			name:          "permission denied",
			err:           ErrPermissionDenied,
			wantType:      "permission",
			wantRetryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantType, tt.err.Type)
			assert.Equal(t, tt.wantRetryable, tt.err.Retryable)
		})
	}
}

// MockTransport for testing
type MockTransport struct {
	connected bool
	target    string
	protocol  string
}

func (m *MockTransport) Connect(ctx context.Context, target string) error {
	m.connected = true
	m.target = target
	m.protocol = "mock"
	return nil
}

func (m *MockTransport) Disconnect(ctx context.Context) error {
	m.connected = false
	return nil
}

func (m *MockTransport) IsConnected() bool {
	return m.connected
}

func (m *MockTransport) Execute(ctx context.Context, cmd *Command) (*Result, error) {
	if !m.connected {
		return nil, ErrNotConnected
	}
	return &Result{
		ExitCode: 0,
		Output:   "mock output",
		Duration: 10 * time.Millisecond,
		Start:    time.Now(),
		End:      time.Now().Add(10 * time.Millisecond),
	}, nil
}

func (m *MockTransport) Upload(ctx context.Context, localPath, remotePath string) error {
	if !m.connected {
		return ErrNotConnected
	}

	// Verify local file exists
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		return ErrFileNotFound
	}

	return nil
}

func (m *MockTransport) Download(ctx context.Context, remotePath, localPath string) error {
	if !m.connected {
		return ErrNotConnected
	}

	// Create a dummy file
	file, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	return nil
}

func (m *MockTransport) GetInfo() *Info {
	return &Info{
		Protocol:   m.protocol,
		Target:     m.target,
		Connected:  m.connected,
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
		Properties: map[string]string{"mock": "true"},
	}
}

func TestMockTransport(t *testing.T) {
	ctx := context.Background()
	transport := &MockTransport{}

	// Test initial state
	assert.False(t, transport.IsConnected())

	// Test connect
	err := transport.Connect(ctx, "test://localhost")
	require.NoError(t, err)
	assert.True(t, transport.IsConnected())

	// Test execute
	result, err := transport.Execute(ctx, &Command{Cmd: []string{"echo", "test"}})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "mock output", result.Output)

	// Test upload
	tmpFile := "/tmp/test_upload.txt"
	err = os.WriteFile(tmpFile, []byte("test content"), 0644)
	require.NoError(t, err)
	defer os.Remove(tmpFile)

	err = transport.Upload(ctx, tmpFile, "/tmp/remote.txt")
	require.NoError(t, err)

	// Test download
	downloadFile := "/tmp/test_download.txt"
	defer os.Remove(downloadFile)

	err = transport.Download(ctx, "/tmp/remote.txt", downloadFile)
	require.NoError(t, err)

	// Verify file was created
	_, err = os.Stat(downloadFile)
	assert.NoError(t, err)

	// Test get info
	info := transport.GetInfo()
	assert.Equal(t, "mock", info.Protocol)
	assert.Equal(t, "test://localhost", info.Target)
	assert.True(t, info.Connected)

	// Test disconnect
	err = transport.Disconnect(ctx)
	require.NoError(t, err)
	assert.False(t, transport.IsConnected())
}
