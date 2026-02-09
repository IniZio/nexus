package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPTransportCreation(t *testing.T) {
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
			name: "missing target",
			config: &Config{
				Protocol: "http",
			},
			wantErr: true,
			errMsg:  "target is required",
		},
		{
			name: "invalid target format",
			config: &Config{
				Protocol: "http",
				Target:   "invalid-url",
			},
			wantErr: true,
			errMsg:  "must start with http:// or https://",
		},
		{
			name: "valid http config",
			config: &Config{
				Protocol: "http",
				Target:   "http://localhost:8080",
			},
			wantErr: false,
		},
		{
			name: "valid https config",
			config: &Config{
				Protocol: "http",
				Target:   "https://api.example.com",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHTTPTransport(tt.config)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHTTPTransportConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status": "ok"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(&Config{
		Target: server.URL,
	})
	require.NoError(t, err)

	// Test initial state
	assert.False(t, transport.IsConnected())

	// Test connect
	ctx := context.Background()
	err = transport.Connect(ctx, "")
	require.NoError(t, err)
	assert.True(t, transport.IsConnected())

	// Test disconnect
	err = transport.Disconnect(ctx)
	require.NoError(t, err)
	assert.False(t, transport.IsConnected())
}

func TestHTTPTransportConnectionFailed(t *testing.T) {
	transport, err := NewHTTPTransport(&Config{
		Target: "http://nonexistent:12345",
	})
	require.NoError(t, err)

	ctx := context.Background()
	err = transport.Connect(ctx, "")
	require.Error(t, err)
	assert.False(t, transport.IsConnected())
}

func TestHTTPTransportExecute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/execute":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			var req struct {
				Cmd []string `json:"cmd"`
			}
			json.NewDecoder(r.Body).Decode(&req)

			result := Result{
				ExitCode: 0,
				Output:   "command output",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(&Config{
		Target: server.URL,
	})
	require.NoError(t, err)

	ctx := context.Background()
	err = transport.Connect(ctx, "")
	require.NoError(t, err)

	result, err := transport.Execute(ctx, &Command{
		Cmd:           []string{"echo", "test"},
		CaptureOutput: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "command output", result.Output)
}

func TestHTTPTransportExecuteNotConnected(t *testing.T) {
	transport, err := NewHTTPTransport(&Config{
		Target: "http://localhost:8080",
	})
	require.NoError(t, err)

	ctx := context.Background()
	result, err := transport.Execute(ctx, &Command{Cmd: []string{"echo", "test"}})
	assert.Nil(t, result)
	assert.Equal(t, ErrNotConnected, err)
}

func TestHTTPTransportExecuteServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/execute":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Internal server error"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(&Config{
		Target: server.URL,
	})
	require.NoError(t, err)

	ctx := context.Background()
	err = transport.Connect(ctx, "")
	require.NoError(t, err)

	result, err := transport.Execute(ctx, &Command{
		Cmd: []string{"echo", "test"},
	})
	assert.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestHTTPTransportFileOperations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/upload":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/api/v1/download":
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("file content"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(&Config{
		Target: server.URL,
	})
	require.NoError(t, err)

	ctx := context.Background()
	err = transport.Connect(ctx, "")
	require.NoError(t, err)

	// Create test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	require.NoError(t, err)

	// Test upload
	err = transport.Upload(ctx, testFile, "/tmp/remote.txt")
	require.NoError(t, err)

	// Test download
	downloadFile := filepath.Join(tmpDir, "local.txt")
	err = transport.Download(ctx, "/tmp/remote.txt", downloadFile)
	require.NoError(t, err)
}

func TestHTTPTransportFileOperationsNotConnected(t *testing.T) {
	transport, err := NewHTTPTransport(&Config{
		Target: "http://localhost:8080",
	})
	require.NoError(t, err)

	ctx := context.Background()

	// Test upload
	err = transport.Upload(ctx, "/tmp/test.txt", "/tmp/remote.txt")
	assert.Equal(t, ErrNotConnected, err)

	// Test download
	err = transport.Download(ctx, "/tmp/remote.txt", "/tmp/local.txt")
	assert.Equal(t, ErrNotConnected, err)
}

func TestHTTPTransportAuthHeaders(t *testing.T) {
	var authHeader string
	var tokenHeader string
	var customHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			authHeader = r.Header.Get("Authorization")
			tokenHeader = r.Header.Get("X-Token")
			customHeader = r.Header.Get("X-Custom")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tests := []struct {
		name       string
		authConfig AuthConfig
		checkFunc  func()
	}{
		{
			name: "token auth",
			authConfig: AuthConfig{
				Type:  "token",
				Token: "test-token",
			},
			checkFunc: func() {
				assert.Equal(t, "Bearer test-token", authHeader)
			},
		},
		{
			name: "header auth",
			authConfig: AuthConfig{
				Type: "header",
				Headers: map[string]string{
					"X-Token":  "header-token",
					"X-Custom": "custom-value",
				},
			},
			checkFunc: func() {
				assert.Equal(t, "header-token", tokenHeader)
				assert.Equal(t, "custom-value", customHeader)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, err := NewHTTPTransport(&Config{
				Target: server.URL,
				Auth:   tt.authConfig,
			})
			require.NoError(t, err)

			ctx := context.Background()
			err = transport.Connect(ctx, "")
			require.NoError(t, err)

			tt.checkFunc()
		})
	}
}

func TestHTTPTransportGetInfo(t *testing.T) {
	transport, err := NewHTTPTransport(&Config{
		Target: "https://api.example.com:8080",
	})
	require.NoError(t, err)

	info := transport.GetInfo()
	assert.Equal(t, "http", info.Protocol)
	assert.Equal(t, "https://api.example.com:8080", info.Target)
	assert.False(t, info.Connected)
	assert.NotNil(t, info.CreatedAt)
	assert.NotNil(t, info.Properties)
}

func TestBuildHTTPClient(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "basic config",
			config: &Config{
				Timeout: 10 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "with TLS skip verify",
			config: &Config{
				Security: SecurityConfig{
					SkipTLSVerify: true,
				},
			},
			wantErr: false,
		},
		{
			name: "with custom timeout",
			config: &Config{
				Timeout: 5 * time.Second,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := buildHTTPClient(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

func TestHTTPTransportWrapError(t *testing.T) {
	transport, err := NewHTTPTransport(&Config{
		Target: "http://localhost:8080",
	})
	require.NoError(t, err)

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
