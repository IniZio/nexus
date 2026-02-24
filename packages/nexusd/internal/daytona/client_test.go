package daytona

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name      string
		apiURL    string
		apiKey    string
		wantErr   bool
		errString string
	}{
		{
			name:    "valid with default URL",
			apiURL:  "",
			apiKey:  "dtn_testkey123",
			wantErr: false,
		},
		{
			name:    "valid with custom URL",
			apiURL:  "https://custom.daytona.io/api",
			apiKey:  "dtn_testkey123",
			wantErr: false,
		},
		{
			name:      "empty API key",
			apiURL:    "",
			apiKey:    "",
			wantErr:   true,
			errString: "API key is empty",
		},
		{
			name:      "invalid API key format",
			apiURL:    "",
			apiKey:    "invalid_key",
			wantErr:   true,
			errString: "invalid API key format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.apiURL, tt.apiKey)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errString)
				assert.Nil(t, client)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, client)
				if tt.apiURL != "" {
					assert.Equal(t, tt.apiURL, client.apiURL)
				} else {
					assert.Equal(t, DefaultAPIURL, client.apiURL)
				}
			}
		})
	}
}

func TestCreateSandbox(t *testing.T) {
	sandbox := Sandbox{
		ID:               "sb-123",
		Name:             "test-sandbox",
		State:            "started",
		Image:            "ubuntu:22.04",
		Resources:        Resources{CPU: 2, Memory: 4, Disk: 20},
		EnvVars:          map[string]string{"KEY": "value"},
		SSHInfo:          SSHInfo{Host: "host.daytona.io", Port: 22, Username: "daytona", PrivateKey: "key"},
		AutoStopInterval: 30,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "Bearer dtn_testkey", r.Header.Get("Authorization"))
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(sandbox)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		req := CreateSandboxRequest{
			Name:             "test-sandbox",
			Image:            "ubuntu:22.04",
			Resources:        &Resources{CPU: 2, Memory: 4, Disk: 20},
			AutoStopInterval: 30,
		}

		result, err := client.CreateSandbox(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, sandbox.ID, result.ID)
		assert.Equal(t, sandbox.Name, result.Name)
		assert.Equal(t, sandbox.State, result.State)
	})

	t.Run("unauthorized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "unauthorized"}`))
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		_, err = client.CreateSandbox(context.Background(), CreateSandboxRequest{Name: "test"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create sandbox failed: 401")
	})
}

func TestGetSandbox(t *testing.T) {
	sandbox := Sandbox{
		ID:               "sb-123",
		Name:             "test-sandbox",
		State:            "started",
		Image:            "ubuntu:22.04",
		Resources:        Resources{CPU: 2, Memory: 4, Disk: 20},
		SSHInfo:          SSHInfo{Host: "host.daytona.io", Port: 22, Username: "daytona"},
		AutoStopInterval: 30,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Contains(t, r.URL.Path, "/workspace/sb-123")

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(sandbox)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		result, err := client.GetSandbox(context.Background(), "sb-123")
		require.NoError(t, err)
		assert.Equal(t, sandbox.ID, result.ID)
		assert.Equal(t, sandbox.State, result.State)
	})

	t.Run("not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": "not found"}`))
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		_, err = client.GetSandbox(context.Background(), "nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get sandbox failed: 404")
	})
}

func TestStartSandbox(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "POST", r.Method)
			assert.Contains(t, r.URL.Path, "/workspace/sb-123/start")

			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		err = client.StartSandbox(context.Background(), "sb-123")
		require.NoError(t, err)
	})

	t.Run("accepted", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		err = client.StartSandbox(context.Background(), "sb-123")
		require.NoError(t, err)
	})
}

func TestStopSandbox(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "POST", r.Method)
			assert.Contains(t, r.URL.Path, "/workspace/sb-123/stop")

			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		err = client.StopSandbox(context.Background(), "sb-123")
		require.NoError(t, err)
	})
}

func TestDeleteSandbox(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			assert.Contains(t, r.URL.Path, "/sandbox/sb-123")

			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		err = client.DeleteSandbox(context.Background(), "sb-123")
		require.NoError(t, err)
	})

	t.Run("ok status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "dtn_testkey")
		require.NoError(t, err)

		err = client.DeleteSandbox(context.Background(), "sb-123")
		require.NoError(t, err)
	})
}

func TestSandbox_IsRunning(t *testing.T) {
	tests := []struct {
		state    string
		expected bool
	}{
		{"started", true},
		{"running", true},
		{"stopped", false},
		{"creating", false},
		{"error", false},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			s := Sandbox{State: tt.state}
			assert.Equal(t, tt.expected, s.IsRunning())
		})
	}
}

func TestSandbox_IsStopped(t *testing.T) {
	tests := []struct {
		state    string
		expected bool
	}{
		{"stopped", true},
		{"started", false},
		{"running", false},
		{"creating", false},
		{"error", false},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			s := Sandbox{State: tt.state}
			assert.Equal(t, tt.expected, s.IsStopped())
		})
	}
}
