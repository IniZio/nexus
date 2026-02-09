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

func TestHandleGetNodeStatus(t *testing.T) {
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

	req := httptest.NewRequest("GET", "/api/v1/nodes/test-node/status", nil)
	w := httptest.NewRecorder()

	srv.handleGetNodeStatus(w, req, "test-node")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleUpdateNode(t *testing.T) {
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

	updates := map[string]interface{}{"status": "inactive"}
	body, _ := json.Marshal(updates)
	req := httptest.NewRequest("PUT", "/api/v1/nodes/test-node", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleUpdateNode(w, req, "test-node")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleUnregisterNode(t *testing.T) {
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

	req := httptest.NewRequest("DELETE", "/api/v1/nodes/test-node", nil)
	w := httptest.NewRecorder()

	srv.handleUnregisterNode(w, req, "test-node")
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandleSendCommand(t *testing.T) {
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

	cmd := map[string]interface{}{
		"id":   "cmd-1",
		"type": "shell",
	}
	body, _ := json.Marshal(cmd)
	req := httptest.NewRequest("POST", "/api/v1/nodes/nonexistent/command", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleSendCommand(w, req, "nonexistent")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleCommandResult(t *testing.T) {
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

	result := CommandResult{
		ID:     "cmd-1",
		Status: "success",
	}
	body, _ := json.Marshal(result)
	req := httptest.NewRequest("POST", "/api/v1/commands/cmd-1/result", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleCommandResult(w, req, "cmd-1")
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestHandleCommandResultMismatch(t *testing.T) {
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

	result := CommandResult{
		ID:     "cmd-2",
		Status: "success",
	}
	body, _ := json.Marshal(result)
	req := httptest.NewRequest("POST", "/api/v1/commands/cmd-1/result", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleCommandResult(w, req, "cmd-1")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleRegisterUser(t *testing.T) {
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

	user := User{
		Username:  "test-user",
		PublicKey: "ssh-ed25519 AAAA...",
	}
	body, _ := json.Marshal(user)
	req := httptest.NewRequest("POST", "/api/v1/users", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleRegisterUser(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleListUsers(t *testing.T) {
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

	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	w := httptest.NewRecorder()

	srv.handleListUsers(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Contains(t, resp, "users")
}

func TestHandleGetUser(t *testing.T) {
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

	req := httptest.NewRequest("GET", "/api/v1/users/nonexistent", nil)
	w := httptest.NewRecorder()

	srv.handleGetUser(w, req, "nonexistent")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleDeleteUser(t *testing.T) {
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

	req := httptest.NewRequest("DELETE", "/api/v1/users/test-user", nil)
	w := httptest.NewRecorder()

	srv.handleDeleteUser(w, req, "test-user")
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandleGetWorkspaceServices(t *testing.T) {
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

	req := httptest.NewRequest("GET", "/api/v1/workspaces/ws-1/services", nil)
	w := httptest.NewRecorder()

	srv.handleGetWorkspaceServices(w, req, "ws-1")
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Contains(t, resp, "services")
}

func TestHandleGetWorkspaceUsers(t *testing.T) {
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

	req := httptest.NewRequest("GET", "/api/v1/workspaces/ws-1/users", nil)
	w := httptest.NewRecorder()

	srv.handleGetWorkspaceUsers(w, req, "ws-1")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestLoggingMiddleware(t *testing.T) {
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

	handler := srv.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
