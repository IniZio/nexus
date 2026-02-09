package coordination

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryRegistry(t *testing.T) {
	registry := NewInMemoryRegistry()

	node := &Node{
		ID:       "test-node-1",
		Name:     "Test Node",
		Provider: "test",
		Status:   "active",
		Address:  "localhost",
		Port:     8080,
		Labels: map[string]string{
			"env": "test",
		},
		Capabilities: map[string]interface{}{
			"docker": true,
		},
	}

	t.Run("Register and Get", func(t *testing.T) {
		err := registry.Register(node)
		assert.NoError(t, err)

		retrieved, err := registry.Get("test-node-1")
		require.NoError(t, err)
		assert.Equal(t, node.ID, retrieved.ID)
		assert.Equal(t, node.Name, retrieved.Name)
		assert.Equal(t, node.Provider, retrieved.Provider)
		assert.Equal(t, node.Status, retrieved.Status)
		assert.Equal(t, node.Address, retrieved.Address)
		assert.Equal(t, node.Port, retrieved.Port)
		assert.NotZero(t, retrieved.CreatedAt)
		assert.NotZero(t, retrieved.UpdatedAt)
		assert.NotZero(t, retrieved.LastSeen)
	})

	t.Run("List", func(t *testing.T) {
		nodes, err := registry.List()
		require.NoError(t, err)
		assert.Len(t, nodes, 1)
		assert.Equal(t, "test-node-1", nodes[0].ID)
	})

	t.Run("Update", func(t *testing.T) {
		updates := map[string]interface{}{
			"status": "inactive",
			"port":   8081,
		}

		err := registry.Update("test-node-1", updates)
		require.NoError(t, err)

		retrieved, err := registry.Get("test-node-1")
		require.NoError(t, err)
		assert.Equal(t, "inactive", retrieved.Status)
		assert.Equal(t, 8081, retrieved.Port)
	})

	t.Run("SetStatus", func(t *testing.T) {
		err := registry.SetStatus("test-node-1", "active")
		require.NoError(t, err)

		retrieved, err := registry.Get("test-node-1")
		require.NoError(t, err)
		assert.Equal(t, "active", retrieved.Status)
	})

	t.Run("GetByLabel", func(t *testing.T) {
		// Add another node with same label
		node2 := *node
		node2.ID = "test-node-2"
		node2.Name = "Test Node 2"
		err := registry.Register(&node2)
		require.NoError(t, err)

		nodes, err := registry.GetByLabel("env", "test")
		require.NoError(t, err)
		assert.Len(t, nodes, 2)
	})

	t.Run("GetByCapability", func(t *testing.T) {
		nodes, err := registry.GetByCapability("docker")
		require.NoError(t, err)
		assert.Len(t, nodes, 2)
	})

	t.Run("Unregister", func(t *testing.T) {
		err := registry.Unregister("test-node-1")
		require.NoError(t, err)

		_, err = registry.Get("test-node-1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "node not found")
	})
}

func TestServerCreation(t *testing.T) {
	config := &Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{
			Host:      "localhost",
			Port:      3001,
			AuthToken: "test-token",
		},
	}

	server := NewServer(config)
	assert.NotNil(t, server)
	assert.Equal(t, config, server.config)
	assert.NotNil(t, server.registry)
	assert.NotNil(t, server.router)
	assert.NotNil(t, server.commandCh)
}

func TestServerHandlers(t *testing.T) {
	config := &Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{
			Host:      "localhost",
			Port:      0, // Use random port for testing
			AuthToken: "test-token",
		},
	}

	server := NewServer(config)
	require.NotNil(t, server)

	t.Run("Register Node", func(t *testing.T) {
		node := Node{
			ID:       "test-node",
			Name:     "Test Node",
			Provider: "test",
			Status:   "active",
			Address:  "localhost",
			Port:     8080,
		}

		nodeJSON, _ := json.Marshal(node)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(string(nodeJSON)))
		w := httptest.NewRecorder()

		server.handleRegisterNode(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var responseNode Node
		err := json.NewDecoder(w.Body).Decode(&responseNode)
		require.NoError(t, err)
		assert.Equal(t, node.ID, responseNode.ID)
		assert.Equal(t, node.Name, responseNode.Name)
	})

	t.Run("List Nodes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
		w := httptest.NewRecorder()

		server.handleListNodes(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.NewDecoder(w.Body).Decode(&response)
		require.NoError(t, err)
		assert.Contains(t, response, "nodes")
		assert.Contains(t, response, "count")
	})

	t.Run("Get Node", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/test-node", nil)
		w := httptest.NewRecorder()

		server.handleGetNode(w, req, "test-node")

		assert.Equal(t, http.StatusOK, w.Code)

		var responseNode Node
		err := json.NewDecoder(w.Body).Decode(&responseNode)
		require.NoError(t, err)
		assert.Equal(t, "test-node", responseNode.ID)
	})

	t.Run("Health Check", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		w := httptest.NewRecorder()

		server.handleHealth(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var health map[string]interface{}
		err := json.NewDecoder(w.Body).Decode(&health)
		require.NoError(t, err)
		assert.Equal(t, "healthy", health["status"])
	})

	t.Run("Metrics", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		w := httptest.NewRecorder()

		server.handleMetrics(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var metrics map[string]interface{}
		err := json.NewDecoder(w.Body).Decode(&metrics)
		require.NoError(t, err)
		assert.Contains(t, metrics, "timestamp")
		assert.Contains(t, metrics, "nodes")
		assert.Contains(t, metrics, "websocket")
	})
}

func TestCommandHandling(t *testing.T) {
	config := &Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{
			Host:      "localhost",
			Port:      0,
			AuthToken: "test-token",
		},
	}

	server := NewServer(config)
	require.NotNil(t, server)

	// Register a test node first
	node := Node{
		ID:       "test-node",
		Name:     "Test Node",
		Provider: "test",
		Status:   "active",
		Address:  "localhost",
		Port:     8080,
	}
	err := server.registry.Register(&node)
	require.NoError(t, err)

	t.Run("Send Command", func(t *testing.T) {
		command := Command{
			Type:   "exec",
			Action: "echo hello",
			Params: map[string]interface{}{
				"timeout": "30s",
			},
		}

		commandJSON, _ := json.Marshal(command)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/test-node/commands", strings.NewReader(string(commandJSON)))
		w := httptest.NewRecorder()

		server.handleSendCommand(w, req, "test-node")

		assert.Equal(t, http.StatusOK, w.Code)

		var result CommandResult
		err := json.NewDecoder(w.Body).Decode(&result)
		require.NoError(t, err)
		assert.NotEmpty(t, result.ID)
		assert.Equal(t, "test-node", result.NodeID)
		assert.Equal(t, command.Type, result.Command.Type)
		assert.Equal(t, command.Action, result.Command.Action)
		assert.Equal(t, "success", result.Status)
		assert.NotEmpty(t, result.Output)
	})

	t.Run("Command Result", func(t *testing.T) {
		result := CommandResult{
			ID:       "test-cmd-123",
			NodeID:   "test-node",
			Status:   "success",
			Output:   "Command completed",
			Duration: time.Millisecond * 100,
			Finished: time.Now(),
		}

		resultJSON, _ := json.Marshal(result)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/commands/test-cmd-123/result", strings.NewReader(string(resultJSON)))
		w := httptest.NewRecorder()

		server.handleCommandResult(w, req, "test-cmd-123")

		assert.Equal(t, http.StatusAccepted, w.Code)
	})
}

func TestConfig(t *testing.T) {
	t.Run("ValidateConfig", func(t *testing.T) {
		validConfig := &Config{
			Server: struct {
				Host         string `yaml:"host,omitempty"`
				Port         int    `yaml:"port,omitempty"`
				AuthToken    string `yaml:"auth_token,omitempty"`
				JWTSecret    string `yaml:"jwt_secret,omitempty"`
				ReadTimeout  string `yaml:"read_timeout,omitempty"`
				WriteTimeout string `yaml:"write_timeout,omitempty"`
				IdleTimeout  string `yaml:"idle_timeout,omitempty"`
			}{
				Host: "localhost",
				Port: 3001,
			},
			Registry: struct {
				Provider            string        `yaml:"provider,omitempty"`
				SyncInterval        string        `yaml:"sync_interval,omitempty"`
				HealthCheckInterval string        `yaml:"health_check_interval,omitempty"`
				NodeTimeout         string        `yaml:"node_timeout,omitempty"`
				MaxRetries          int           `yaml:"max_retries,omitempty"`
				Storage             StorageConfig `yaml:"storage,omitempty"`
			}{
				MaxRetries: 3,
			},
		}

		err := ValidateConfig(validConfig)
		assert.NoError(t, err)
	})

	t.Run("ValidateConfig - Invalid Port", func(t *testing.T) {
		invalidConfig := &Config{
			Server: struct {
				Host         string `yaml:"host,omitempty"`
				Port         int    `yaml:"port,omitempty"`
				AuthToken    string `yaml:"auth_token,omitempty"`
				JWTSecret    string `yaml:"jwt_secret,omitempty"`
				ReadTimeout  string `yaml:"read_timeout,omitempty"`
				WriteTimeout string `yaml:"write_timeout,omitempty"`
				IdleTimeout  string `yaml:"idle_timeout,omitempty"`
			}{
				Host: "localhost",
				Port: 0, // Invalid port
			},
		}

		err := ValidateConfig(invalidConfig)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "port must be between 1 and 65535")
	})

	t.Run("ValidateConfig - Auth Enabled without Secret", func(t *testing.T) {
		invalidConfig := &Config{
			Server: struct {
				Host         string `yaml:"host,omitempty"`
				Port         int    `yaml:"port,omitempty"`
				AuthToken    string `yaml:"auth_token,omitempty"`
				JWTSecret    string `yaml:"jwt_secret,omitempty"`
				ReadTimeout  string `yaml:"read_timeout,omitempty"`
				WriteTimeout string `yaml:"write_timeout,omitempty"`
				IdleTimeout  string `yaml:"idle_timeout,omitempty"`
			}{
				Host: "localhost",
				Port: 3001,
			},
			Auth: struct {
				Enabled     bool     `yaml:"enabled,omitempty"`
				JWTSecret   string   `yaml:"jwt_secret,omitempty"`
				TokenExpiry string   `yaml:"token_expiry,omitempty"`
				AllowedIPs  []string `yaml:"allowed_ips,omitempty"`
			}{
				Enabled: true,
				// JWTSecret is empty
			},
		}

		err := ValidateConfig(invalidConfig)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "JWT secret is required")
	})
}

func TestTimeoutParsing(t *testing.T) {
	config := &Config{
		Server: struct {
			Host         string `yaml:"host,omitempty"`
			Port         int    `yaml:"port,omitempty"`
			AuthToken    string `yaml:"auth_token,omitempty"`
			JWTSecret    string `yaml:"jwt_secret,omitempty"`
			ReadTimeout  string `yaml:"read_timeout,omitempty"`
			WriteTimeout string `yaml:"write_timeout,omitempty"`
			IdleTimeout  string `yaml:"idle_timeout,omitempty"`
		}{
			Host: "localhost",
			Port: 3001,
		},
	}

	server := NewServer(config)

	t.Run("Valid Timeout", func(t *testing.T) {
		duration := server.parseTimeout("30s")
		assert.Equal(t, 30*time.Second, duration)
	})

	t.Run("Invalid Timeout", func(t *testing.T) {
		duration := server.parseTimeout("invalid")
		assert.Equal(t, 30*time.Second, duration) // Should default to 30s
	})
}
