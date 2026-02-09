package coordination

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			cfg: &Config{
				Server: struct {
					Host         string `yaml:"host,omitempty"`
					Port         int    `yaml:"port,omitempty"`
					AuthToken    string `yaml:"auth_token,omitempty"`
					JWTSecret    string `yaml:"jwt_secret,omitempty"`
					ReadTimeout  string `yaml:"read_timeout,omitempty"`
					WriteTimeout string `yaml:"write_timeout,omitempty"`
					IdleTimeout  string `yaml:"idle_timeout,omitempty"`
				}{Host: "localhost", Port: 3001},
				Auth: struct {
					Enabled     bool     `yaml:"enabled,omitempty"`
					JWTSecret   string   `yaml:"jwt_secret,omitempty"`
					TokenExpiry string   `yaml:"token_expiry,omitempty"`
					AllowedIPs  []string `yaml:"allowed_ips,omitempty"`
				}{Enabled: false},
			},
			wantErr: false,
		},
		{
			name: "invalid port too low",
			cfg: &Config{
				Server: struct {
					Host         string `yaml:"host,omitempty"`
					Port         int    `yaml:"port,omitempty"`
					AuthToken    string `yaml:"auth_token,omitempty"`
					JWTSecret    string `yaml:"jwt_secret,omitempty"`
					ReadTimeout  string `yaml:"read_timeout,omitempty"`
					WriteTimeout string `yaml:"write_timeout,omitempty"`
					IdleTimeout  string `yaml:"idle_timeout,omitempty"`
				}{Host: "localhost", Port: 0},
			},
			wantErr: true,
			errMsg:  "port must be between 1 and 65535",
		},
		{
			name: "empty host",
			cfg: &Config{
				Server: struct {
					Host         string `yaml:"host,omitempty"`
					Port         int    `yaml:"port,omitempty"`
					AuthToken    string `yaml:"auth_token,omitempty"`
					JWTSecret    string `yaml:"jwt_secret,omitempty"`
					ReadTimeout  string `yaml:"read_timeout,omitempty"`
					WriteTimeout string `yaml:"write_timeout,omitempty"`
					IdleTimeout  string `yaml:"idle_timeout,omitempty"`
				}{Host: "", Port: 3001},
			},
			wantErr: true,
			errMsg:  "server host cannot be empty",
		},
		{
			name: "negative max retries",
			cfg: &Config{
				Server: struct {
					Host         string `yaml:"host,omitempty"`
					Port         int    `yaml:"port,omitempty"`
					AuthToken    string `yaml:"auth_token,omitempty"`
					JWTSecret    string `yaml:"jwt_secret,omitempty"`
					ReadTimeout  string `yaml:"read_timeout,omitempty"`
					WriteTimeout string `yaml:"write_timeout,omitempty"`
					IdleTimeout  string `yaml:"idle_timeout,omitempty"`
				}{Host: "localhost", Port: 3001},
				Registry: struct {
					Provider            string        `yaml:"provider,omitempty"`
					SyncInterval        string        `yaml:"sync_interval,omitempty"`
					HealthCheckInterval string        `yaml:"health_check_interval,omitempty"`
					NodeTimeout         string        `yaml:"node_timeout,omitempty"`
					MaxRetries          int           `yaml:"max_retries,omitempty"`
					Storage             StorageConfig `yaml:"storage,omitempty"`
				}{MaxRetries: -1},
			},
			wantErr: true,
			errMsg:  "max_retries must be non-negative",
		},
		{
			name: "auth enabled without secret",
			cfg: &Config{
				Server: struct {
					Host         string `yaml:"host,omitempty"`
					Port         int    `yaml:"port,omitempty"`
					AuthToken    string `yaml:"auth_token,omitempty"`
					JWTSecret    string `yaml:"jwt_secret,omitempty"`
					ReadTimeout  string `yaml:"read_timeout,omitempty"`
					WriteTimeout string `yaml:"write_timeout,omitempty"`
					IdleTimeout  string `yaml:"idle_timeout,omitempty"`
				}{Host: "localhost", Port: 3001},
				Auth: struct {
					Enabled     bool     `yaml:"enabled,omitempty"`
					JWTSecret   string   `yaml:"jwt_secret,omitempty"`
					TokenExpiry string   `yaml:"token_expiry,omitempty"`
					AllowedIPs  []string `yaml:"allowed_ips,omitempty"`
				}{Enabled: true, JWTSecret: ""},
			},
			wantErr: true,
			errMsg:  "JWT secret is required",
		},
		{
			name: "jwt secret too short",
			cfg: &Config{
				Server: struct {
					Host         string `yaml:"host,omitempty"`
					Port         int    `yaml:"port,omitempty"`
					AuthToken    string `yaml:"auth_token,omitempty"`
					JWTSecret    string `yaml:"jwt_secret,omitempty"`
					ReadTimeout  string `yaml:"read_timeout,omitempty"`
					WriteTimeout string `yaml:"write_timeout,omitempty"`
					IdleTimeout  string `yaml:"idle_timeout,omitempty"`
				}{Host: "localhost", Port: 3001},
				Auth: struct {
					Enabled     bool     `yaml:"enabled,omitempty"`
					JWTSecret   string   `yaml:"jwt_secret,omitempty"`
					TokenExpiry string   `yaml:"token_expiry,omitempty"`
					AllowedIPs  []string `yaml:"allowed_ips,omitempty"`
				}{Enabled: true, JWTSecret: "short"},
			},
			wantErr: true,
			errMsg:  "must be at least 16 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCheckPortAvailable(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		port    int
		wantErr bool
	}{
		{
			name:    "available port",
			host:    "127.0.0.1",
			port:    9999,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPortAvailable(tt.host, 9999)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGenerateDefaultConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := tmpDir + "/config.yaml"

	err := GenerateDefaultConfig(configPath)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 3001, cfg.Server.Port)
	assert.Equal(t, "memory", cfg.Registry.Provider)
	assert.True(t, cfg.WebSocket.Enabled)
	assert.False(t, cfg.Auth.Enabled)
}

func TestServerGetServerInfo(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	info, err := srv.GetServerInfo()
	require.NoError(t, err)

	assert.NotNil(t, info)
	assert.Equal(t, "localhost", info.Host)
	assert.Equal(t, 3001, info.Port)
	assert.Greater(t, info.PID, 0)
}

func TestServerBackupRegistry(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	backup, err := srv.BackupRegistry()
	require.NoError(t, err)

	var data map[string]interface{}
	err = json.Unmarshal(backup, &data)
	require.NoError(t, err)

	assert.Contains(t, data, "timestamp")
	assert.Contains(t, data, "version")
	assert.Contains(t, data, "nodes")
}

func TestServerRestoreRegistry(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	backup := []byte(`{"timestamp":"2024-01-01T00:00:00Z","version":"1.0","nodes":[]}`)
	err := srv.RestoreRegistry(backup)
	require.NoError(t, err)
}

func TestServerGetStats(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	stats := srv.GetStats()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "total_nodes")
	assert.Contains(t, stats, "connected_clients")
	assert.Contains(t, stats, "nodes_by_status")
}

func TestServerHealthCheck(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
		WebSocket: struct {
			Enabled    bool     `yaml:"enabled,omitempty"`
			Path       string   `yaml:"path,omitempty"`
			Origins    []string `yaml:"origins,omitempty"`
			PingPeriod string   `yaml:"ping_period,omitempty"`
		}{Enabled: true},
	})

	health := srv.HealthCheck()
	assert.NotNil(t, health)
	assert.Contains(t, health, "status")
	assert.Contains(t, health, "checks")
}

func TestHandleListNodes(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	req := httptest.NewRequest("GET", "/api/v1/nodes", nil)
	w := httptest.NewRecorder()

	srv.handleListNodes(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Contains(t, resp, "nodes")
	assert.Contains(t, resp, "count")
}

func TestHandleGetNode(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	req := httptest.NewRequest("GET", "/api/v1/nodes/nonexistent", nil)
	w := httptest.NewRecorder()

	srv.handleGetNode(w, req, "nonexistent")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleHealth(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()

	srv.handleHealth(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "healthy", resp["status"])
}

func TestHandleMetrics(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	req := httptest.NewRequest("GET", "/api/v1/metrics", nil)
	w := httptest.NewRecorder()

	srv.handleMetrics(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Contains(t, resp, "nodes")
	assert.Contains(t, resp, "services")
}

func TestHandleListServices(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	req := httptest.NewRequest("GET", "/api/v1/services", nil)
	w := httptest.NewRecorder()

	srv.handleListServices(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleRegisterNode(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	nodeData := map[string]interface{}{
		"id":       "node1",
		"name":     "test-node",
		"status":   "active",
		"provider": "docker",
	}
	body, _ := json.Marshal(nodeData)
	req := httptest.NewRequest("POST", "/api/v1/nodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleRegisterNode(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuthMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		authEnabled    bool
		authHeader     string
		expectedStatus int
	}{
		{
			name:           "auth disabled",
			authEnabled:    false,
			authHeader:     "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid token",
			authEnabled:    true,
			authHeader:     "Bearer test-token",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "missing auth header",
			authEnabled:    true,
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Server: struct {
					Host         string `yaml:"host,omitempty"`
					Port         int    `yaml:"port,omitempty"`
					AuthToken    string `yaml:"auth_token,omitempty"`
					JWTSecret    string `yaml:"jwt_secret,omitempty"`
					ReadTimeout  string `yaml:"read_timeout,omitempty"`
					WriteTimeout string `yaml:"write_timeout,omitempty"`
					IdleTimeout  string `yaml:"idle_timeout,omitempty"`
				}{Host: "localhost", Port: 3001, AuthToken: "test-token"},
				Auth: struct {
					Enabled     bool     `yaml:"enabled,omitempty"`
					JWTSecret   string   `yaml:"jwt_secret,omitempty"`
					TokenExpiry string   `yaml:"token_expiry,omitempty"`
					AllowedIPs  []string `yaml:"allowed_ips,omitempty"`
				}{Enabled: tt.authEnabled},
			}
			srv := NewServer(cfg)

			handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest("GET", "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)
		})
	}
}

func TestCORSMiddleware(t *testing.T) {
	srv := NewServer(&Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{Host: "localhost", Port: 3001},
	})

	handler := srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("OPTIONS", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}
