package coordination

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestM4RegisterGitHubUser(t *testing.T) {
	server := NewServer(&Config{
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
	})

	tests := []struct {
		name           string
		request        M4RegisterGitHubUserRequest
		expectedStatus int
		expectError    bool
	}{
		{
			name: "valid_registration",
			request: M4RegisterGitHubUserRequest{
				GitHubUsername:          "alice",
				GitHubID:                123456789,
				SSHPublicKey:            "ssh-ed25519 AAAA... alice@example.com",
				SSHPublicKeyFingerprint: "SHA256:abcd1234",
			},
			expectedStatus: http.StatusCreated,
			expectError:    false,
		},
		{
			name: "missing_github_username",
			request: M4RegisterGitHubUserRequest{
				GitHubID:                123456789,
				SSHPublicKey:            "ssh-ed25519 AAAA... alice@example.com",
				SSHPublicKeyFingerprint: "SHA256:abcd1234",
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
		{
			name: "missing_ssh_key",
			request: M4RegisterGitHubUserRequest{
				GitHubUsername:          "bob",
				GitHubID:                987654321,
				SSHPublicKeyFingerprint: "SHA256:1234",
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
		{
			name: "invalid_ssh_key_format",
			request: M4RegisterGitHubUserRequest{
				GitHubUsername:          "charlie",
				GitHubID:                111111111,
				SSHPublicKey:            "invalid-key-format",
				SSHPublicKeyFingerprint: "SHA256:5678",
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
		{
			name: "duplicate_user",
			request: M4RegisterGitHubUserRequest{
				GitHubUsername:          "alice",
				GitHubID:                999999999,
				SSHPublicKey:            "ssh-ed25519 BBBB... alice2@example.com",
				SSHPublicKeyFingerprint: "SHA256:efgh5678",
			},
			expectedStatus: http.StatusConflict,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/users/register-github", bytes.NewReader(body))
			w := httptest.NewRecorder()

			server.handleM4RegisterGitHub(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if !tt.expectError {
				var resp M4RegisterGitHubUserResponse
				err := json.NewDecoder(w.Body).Decode(&resp)
				require.NoError(t, err)
				assert.Equal(t, tt.request.GitHubUsername, resp.GitHubUsername)
				assert.Equal(t, tt.request.SSHPublicKeyFingerprint, resp.SSHPublicKeyFingerprint)
				assert.NotEmpty(t, resp.UserID)
				assert.NotEmpty(t, resp.RegisteredAt)
				assert.Empty(t, resp.Workspaces)
			} else {
				var errResp M4ErrorResponse
				err := json.NewDecoder(w.Body).Decode(&errResp)
				require.NoError(t, err)
				assert.NotEmpty(t, errResp.Error)
				assert.NotEmpty(t, errResp.Message)
				assert.NotEmpty(t, errResp.RequestID)
			}
		})
	}
}

func TestM4CreateWorkspace(t *testing.T) {
	server := NewServer(&Config{
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
	})

	userReg := server.registry.GetUserRegistry()
	userReg.Register(&User{
		Username:  "testuser",
		PublicKey: "ssh-ed25519 AAAA... test@example.com",
	})

	// Set up GitHub installation for the user
	user, _ := userReg.GetByUsername("testuser")
	server.gitHubInstallationsMu.Lock()
	server.gitHubInstallations["testuser"] = &GitHubInstallation{
		UserID:         user.ID,
		GitHubUsername: "testuser",
		Token:          "test-token-12345",
		TokenExpiresAt: time.Now().Add(24 * time.Hour),
	}
	server.gitHubInstallationsMu.Unlock()

	tests := []struct {
		name           string
		request        M4CreateWorkspaceRequest
		expectedStatus int
		expectError    bool
	}{
		{
			name: "valid_create",
			request: M4CreateWorkspaceRequest{
				GitHubUsername: "testuser",
				WorkspaceName:  "feature-branch",
				Provider:       "lxc",
				Image:          "ubuntu:22.04",
				Repository: M4Repository{
					Owner:  "org",
					Name:   "project",
					URL:    "git@github.com:org/project.git",
					Branch: "main",
				},
				Services: []M4ServiceDefinition{
					{
						Name:    "web",
						Command: "npm run dev",
						Port:    3000,
					},
				},
			},
			expectedStatus: http.StatusAccepted,
			expectError:    false,
		},
		{
			name: "missing_github_username",
			request: M4CreateWorkspaceRequest{
				WorkspaceName: "test-ws",
				Provider:      "lxc",
				Repository: M4Repository{
					Owner: "org",
					Name:  "project",
				},
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
		{
			name: "invalid_repo",
			request: M4CreateWorkspaceRequest{
				GitHubUsername: "testuser",
				WorkspaceName:  "test-ws",
				Provider:       "lxc",
				Repository:     M4Repository{},
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
		{
			name: "user_not_found",
			request: M4CreateWorkspaceRequest{
				GitHubUsername: "nonexistent",
				WorkspaceName:  "test-ws",
				Provider:       "lxc",
				Repository: M4Repository{
					Owner: "org",
					Name:  "project",
				},
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/create-from-repo", bytes.NewReader(body))
			w := httptest.NewRecorder()

			server.handleM4CreateWorkspace(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if !tt.expectError {
				var resp M4CreateWorkspaceResponse
				err := json.NewDecoder(w.Body).Decode(&resp)
				require.NoError(t, err)
				assert.NotEmpty(t, resp.WorkspaceID)
				assert.Equal(t, "creating", resp.Status)
				assert.Greater(t, resp.SSHPort, 2200)
				assert.Greater(t, resp.EstimatedTimeSecs, 0)
				assert.Contains(t, resp.PollingURL, resp.WorkspaceID)
			}
		})
	}
}

func TestM4GetWorkspaceStatus(t *testing.T) {
	server := NewServer(&Config{
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
	})

	ws1 := &DBWorkspace{
		WorkspaceID:   "ws-123",
		UserID:        "user-1",
		WorkspaceName: "test-workspace-1",
		Status:        "running",
		Provider:      "lxc",
		Image:         "ubuntu:22.04",
		RepoOwner:     "oursky",
		RepoName:      "epson-eshop",
		RepoURL:       "git@github.com:oursky/epson-eshop.git",
		RepoBranch:    "main",
	}
	ws2 := &DBWorkspace{
		WorkspaceID:   "ws-999",
		UserID:        "user-2",
		WorkspaceName: "test-workspace-2",
		Status:        "running",
		Provider:      "lxc",
		Image:         "ubuntu:22.04",
		RepoOwner:     "oursky",
		RepoName:      "oms",
		RepoURL:       "git@github.com:oursky/oms.git",
		RepoBranch:    "develop",
	}
	server.workspaceRegistry.Create(ws1)
	server.workspaceRegistry.Create(ws2)

	tests := []struct {
		name           string
		workspaceID    string
		expectedStatus int
	}{
		{
			name:           "get_status_success",
			workspaceID:    "ws-123",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "get_status_different_id",
			workspaceID:    "ws-999",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/workspaces/" + tt.workspaceID + "/status"
			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()

			server.handleM4GetWorkspaceStatus(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var resp M4WorkspaceStatusResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, tt.workspaceID, resp.WorkspaceID)
			assert.NotEmpty(t, resp.Owner)
			assert.NotEmpty(t, resp.Status)
			assert.NotEmpty(t, resp.SSH.Host)
			assert.Greater(t, resp.SSH.Port, 0)
		})
	}
}

func TestM4ListWorkspaces(t *testing.T) {
	server := NewServer(&Config{
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
	})

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
		expectedLimit  int
		expectedOffset int
	}{
		{
			name:           "list_default",
			queryParams:    "",
			expectedStatus: http.StatusOK,
			expectedLimit:  50,
			expectedOffset: 0,
		},
		{
			name:           "list_with_limit",
			queryParams:    "?limit=10",
			expectedStatus: http.StatusOK,
			expectedLimit:  10,
			expectedOffset: 0,
		},
		{
			name:           "list_with_offset",
			queryParams:    "?limit=20&offset=5",
			expectedStatus: http.StatusOK,
			expectedLimit:  20,
			expectedOffset: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/workspaces" + tt.queryParams
			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()

			server.handleM4ListWorkspacesRouter(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var resp M4ListWorkspacesResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedLimit, resp.Limit)
			assert.Equal(t, tt.expectedOffset, resp.Offset)
			assert.GreaterOrEqual(t, resp.Total, 0)
		})
	}
}

func TestM4StopWorkspace(t *testing.T) {
	server := NewServer(&Config{
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
	})

	ws1 := &DBWorkspace{
		WorkspaceID:   "ws-123",
		UserID:        "user-1",
		WorkspaceName: "test-workspace-1",
		Status:        "running",
		Provider:      "lxc",
		Image:         "ubuntu:22.04",
		RepoOwner:     "oursky",
		RepoName:      "epson-eshop",
		RepoURL:       "git@github.com:oursky/epson-eshop.git",
		RepoBranch:    "main",
	}
	ws2 := &DBWorkspace{
		WorkspaceID:   "ws-999",
		UserID:        "user-2",
		WorkspaceName: "test-workspace-2",
		Status:        "running",
		Provider:      "lxc",
		Image:         "ubuntu:22.04",
		RepoOwner:     "oursky",
		RepoName:      "oms",
		RepoURL:       "git@github.com:oursky/oms.git",
		RepoBranch:    "develop",
	}
	server.workspaceRegistry.Create(ws1)
	server.workspaceRegistry.Create(ws2)

	tests := []struct {
		name           string
		workspaceID    string
		expectedStatus int
	}{
		{
			name:           "stop_workspace",
			workspaceID:    "ws-123",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "stop_another_workspace",
			workspaceID:    "ws-999",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/workspaces/" + tt.workspaceID + "/stop"
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(`{"force":false}`)))
			w := httptest.NewRecorder()

			server.handleM4StopWorkspace(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var resp M4StopWorkspaceResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, tt.workspaceID, resp.WorkspaceID)
			assert.Equal(t, "stopped", resp.Status)
			assert.NotEmpty(t, resp.StoppedAt)
		})
	}
}

func TestM4DeleteWorkspace(t *testing.T) {
	server := NewServer(&Config{
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
	})

	ws := &DBWorkspace{
		WorkspaceID:   "ws-123",
		UserID:        "user-1",
		WorkspaceName: "test-workspace",
		Status:        "running",
		Provider:      "lxc",
		Image:         "ubuntu:22.04",
		RepoOwner:     "oursky",
		RepoName:      "epson-eshop",
		RepoURL:       "git@github.com:oursky/epson-eshop.git",
		RepoBranch:    "main",
	}
	server.workspaceRegistry.Create(ws)

	tests := []struct {
		name           string
		workspaceID    string
		expectedStatus int
	}{
		{
			name:           "delete_workspace",
			workspaceID:    "ws-123",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/workspaces/" + tt.workspaceID
			req := httptest.NewRequest(http.MethodDelete, url, nil)
			w := httptest.NewRecorder()

			server.handleM4DeleteWorkspace(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var resp M4DeleteWorkspaceResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, tt.workspaceID, resp.WorkspaceID)
			assert.NotEmpty(t, resp.Message)
		})
	}
}

func TestM4ErrorResponses(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		errorCode      string
		message        string
		expectedStatus int
	}{
		{
			name:           "bad_request",
			statusCode:     http.StatusBadRequest,
			errorCode:      "invalid_request",
			message:        "Invalid request",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "conflict",
			statusCode:     http.StatusConflict,
			errorCode:      "resource_exists",
			message:        "Resource already exists",
			expectedStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			sendM4JSONError(w, tt.statusCode, tt.errorCode, tt.message, nil)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var resp M4ErrorResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, tt.errorCode, resp.Error)
			assert.Equal(t, tt.message, resp.Message)
			assert.NotEmpty(t, resp.RequestID)
		})
	}
}

func TestM4HTTPMethods(t *testing.T) {
	server := NewServer(&Config{
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
	})

	tests := []struct {
		name           string
		method         string
		handler        http.HandlerFunc
		expectedStatus int
	}{
		{
			name:           "register_with_get_should_fail",
			method:         http.MethodGet,
			handler:        server.handleM4RegisterGitHub,
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "list_workspaces_with_post_should_fail",
			method:         http.MethodPost,
			handler:        server.handleM4ListWorkspacesRouter,
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:   "get_status_with_post_should_fail",
			method: http.MethodPost,
			handler: func(w http.ResponseWriter, r *http.Request) {
				server.handleM4GetWorkspaceStatus(w, r)
			},
			expectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/test", nil)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

func TestM4ValidationErrorDetails(t *testing.T) {
	server := NewServer(&Config{
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
	})

	req := M4RegisterGitHubUserRequest{
		GitHubUsername: "test",
	}

	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/users/register-github", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.handleM4RegisterGitHub(w, httpReq)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var errResp M4ErrorResponse
	err := json.NewDecoder(w.Body).Decode(&errResp)
	require.NoError(t, err)
	assert.NotNil(t, errResp.Details)
}

func TestM4InvalidJSONRequest(t *testing.T) {
	server := NewServer(&Config{
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
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/register-github", strings.NewReader("invalid json"))
	w := httptest.NewRecorder()

	server.handleM4RegisterGitHub(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var errResp M4ErrorResponse
	err := json.NewDecoder(w.Body).Decode(&errResp)
	require.NoError(t, err)
	assert.Equal(t, "invalid_request", errResp.Error)
}
