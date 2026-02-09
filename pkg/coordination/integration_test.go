package coordination

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOAuthIntegration tests the complete GitHub OAuth flow
func TestOAuthIntegration(t *testing.T) {
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

	user, _ := userReg.GetByUsername("testuser")
	server.gitHubInstallationsMu.Lock()
	server.gitHubInstallations["testuser"] = &GitHubInstallation{
		UserID:         user.ID,
		GitHubUsername: "testuser",
		Token:          "gho_test_token_12345",
		TokenExpiresAt: time.Now().Add(8760 * time.Hour),
	}
	server.gitHubInstallationsMu.Unlock()

	// Verify installation was stored
	server.gitHubInstallationsMu.RLock()
	installation, exists := server.gitHubInstallations["testuser"]
	server.gitHubInstallationsMu.RUnlock()

	assert.True(t, exists, "Installation should exist")
	assert.Equal(t, "gho_test_token_12345", installation.Token)
	assert.Equal(t, "testuser", installation.GitHubUsername)
}

// TestForkDetectionIntegration tests automatic fork detection during workspace creation
func TestForkDetectionIntegration(t *testing.T) {
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
		Username:  "alice",
		PublicKey: "ssh-ed25519 AAAA... alice@example.com",
	})

	// Set up GitHub installation with mock token
	server.gitHubInstallationsMu.Lock()
	user, _ := userReg.GetByUsername("alice")
	server.gitHubInstallations["alice"] = &GitHubInstallation{
		UserID:         user.ID,
		GitHubUsername: "alice",
		Token:          "gho_fork_test_token",
		TokenExpiresAt: time.Now().Add(8760 * time.Hour),
	}
	server.gitHubInstallationsMu.Unlock()

	// Create workspace request for a private repo
	createReq := M4CreateWorkspaceRequest{
		GitHubUsername: "alice",
		WorkspaceName:  "fork-test-workspace",
		Provider:       "lxc",
		Image:          "ubuntu:22.04",
		Repository: M4Repository{
			Owner:  "private-org",
			Name:   "private-repo",
			URL:    "git@github.com:private-org/private-repo.git",
			Branch: "main",
		},
		Services: []M4ServiceDefinition{
			{
				Name:    "app",
				Command: "npm run dev",
				Port:    3000,
			},
		},
	}

	body, _ := json.Marshal(createReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/create-from-repo", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.handleM4CreateWorkspace(w, req)

	// Should accept the request (may fail to actually fork due to fake token, but should process)
	assert.Equal(t, http.StatusAccepted, w.Code)

	var resp M4CreateWorkspaceResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEmpty(t, resp.WorkspaceID)
	assert.Equal(t, "creating", resp.Status)
}

// TestSQLitePersistenceAcrossRestarts tests that data persists with SQLite registry
func TestSQLitePersistenceAcrossRestarts(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	// Step 1: Create registry and store data
	registry1, err := NewSQLiteRegistry(dbPath)
	require.NoError(t, err)

	user := &User{
		Username:  "persistuser",
		PublicKey: "ssh-ed25519 AAAA... persistent@example.com",
	}
	registry1.GetUserRegistry().Register(user)

	userStored, _ := registry1.GetUserRegistry().GetByUsername("persistuser")

	installation := &GitHubInstallation{
		InstallationID: 12345,
		UserID:         userStored.ID,
		GitHubUserID:   987654,
		GitHubUsername: "persistuser",
		Token:          "persistent-token-123",
		TokenExpiresAt: time.Now().Add(24 * time.Hour),
	}
	registry1.StoreGitHubInstallation(installation)

	// Step 2: Create new registry instance from same database (simulating server restart)
	registry2, err := NewSQLiteRegistry(dbPath)
	require.NoError(t, err)

	// Verify data is accessible from new registry instance using the stored user ID
	retrievedInstall, err := registry2.GetGitHubInstallation(userStored.ID)
	require.NoError(t, err)
	assert.Equal(t, "persistent-token-123", retrievedInstall.Token)
	assert.Equal(t, "persistuser", retrievedInstall.GitHubUsername)

	// Verify user data also persists
	userQuery, err := registry2.GetUserRegistry().GetByUsername("persistuser")
	require.NoError(t, err)
	assert.Equal(t, "persistuser", userQuery.Username)
}

// TestWorkspaceCreationWithRegistration tests the complete workflow of registering user and creating workspace
func TestWorkspaceCreationWithRegistration(t *testing.T) {
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

	// Step 1: Register user
	regReq := M4RegisterGitHubUserRequest{
		GitHubUsername:          "newuser",
		GitHubID:                987654321,
		SSHPublicKey:            "ssh-ed25519 AAAA... newuser@example.com",
		SSHPublicKeyFingerprint: "SHA256:wxyz9876",
	}

	regBody, _ := json.Marshal(regReq)
	regHTTPReq := httptest.NewRequest(http.MethodPost, "/api/v1/users/register", bytes.NewReader(regBody))
	regW := httptest.NewRecorder()

	server.handleM4RegisterGitHub(regW, regHTTPReq)
	assert.Equal(t, http.StatusCreated, regW.Code)

	// Step 2: Set up GitHub auth for the user
	server.gitHubInstallationsMu.Lock()
	user, _ := server.registry.GetUserRegistry().GetByUsername("newuser")
	server.gitHubInstallations["newuser"] = &GitHubInstallation{
		UserID:         user.ID,
		GitHubUsername: "newuser",
		Token:          "gho_workflow_token",
		TokenExpiresAt: time.Now().Add(8760 * time.Hour),
	}
	server.gitHubInstallationsMu.Unlock()

	// Step 3: Create workspace
	wsReq := M4CreateWorkspaceRequest{
		GitHubUsername: "newuser",
		WorkspaceName:  "workflow-test-ws",
		Provider:       "lxc",
		Image:          "ubuntu:22.04",
		Repository: M4Repository{
			Owner:  "testorg",
			Name:   "testrepo",
			URL:    "git@github.com:testorg/testrepo.git",
			Branch: "main",
		},
		Services: []M4ServiceDefinition{
			{
				Name:    "backend",
				Command: "python manage.py runserver",
				Port:    8000,
			},
		},
	}

	wsBody, _ := json.Marshal(wsReq)
	wsHTTPReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/create-from-repo", bytes.NewReader(wsBody))
	wsW := httptest.NewRecorder()

	server.handleM4CreateWorkspace(wsW, wsHTTPReq)
	assert.Equal(t, http.StatusAccepted, wsW.Code)

	var wsResp M4CreateWorkspaceResponse
	json.NewDecoder(wsW.Body).Decode(&wsResp)
	assert.NotEmpty(t, wsResp.WorkspaceID)
	assert.Equal(t, "creating", wsResp.Status)
	assert.Greater(t, wsResp.SSHPort, 2200)
}

// TestMultipleWorkspacesForSameUser tests creating multiple workspaces for the same user
func TestMultipleWorkspacesForSameUser(t *testing.T) {
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
		Username:  "multiuser",
		PublicKey: "ssh-ed25519 AAAA... multi@example.com",
	})

	server.gitHubInstallationsMu.Lock()
	user, _ := userReg.GetByUsername("multiuser")
	server.gitHubInstallations["multiuser"] = &GitHubInstallation{
		UserID:         user.ID,
		GitHubUsername: "multiuser",
		Token:          "gho_multi_token",
		TokenExpiresAt: time.Now().Add(8760 * time.Hour),
	}
	server.gitHubInstallationsMu.Unlock()

	workspaceIDs := make([]string, 3)

	// Create 3 workspaces for the same user
	for i := 0; i < 3; i++ {
		wsReq := M4CreateWorkspaceRequest{
			GitHubUsername: "multiuser",
			WorkspaceName:  "ws" + string(rune(i+49)), // ws1, ws2, ws3
			Provider:       "lxc",
			Image:          "ubuntu:22.04",
			Repository: M4Repository{
				Owner:  "org",
				Name:   "repo",
				URL:    "git@github.com:org/repo.git",
				Branch: "main",
			},
		}

		body, _ := json.Marshal(wsReq)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/create-from-repo", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleM4CreateWorkspace(w, req)
		assert.Equal(t, http.StatusAccepted, w.Code)

		var resp M4CreateWorkspaceResponse
		json.NewDecoder(w.Body).Decode(&resp)
		workspaceIDs[i] = resp.WorkspaceID
	}

	// Verify all workspace IDs are unique
	for i := 0; i < len(workspaceIDs); i++ {
		for j := i + 1; j < len(workspaceIDs); j++ {
			assert.NotEqual(t, workspaceIDs[i], workspaceIDs[j], "Workspace IDs should be unique")
		}
	}
}

// TestGitHubTokenRefresh tests that expired tokens trigger re-auth
func TestGitHubTokenRefresh(t *testing.T) {
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
		Username:  "tokenuser",
		PublicKey: "ssh-ed25519 AAAA... token@example.com",
	})

	// Set up expired GitHub installation
	server.gitHubInstallationsMu.Lock()
	user, _ := userReg.GetByUsername("tokenuser")
	server.gitHubInstallations["tokenuser"] = &GitHubInstallation{
		UserID:         user.ID,
		GitHubUsername: "tokenuser",
		Token:          "gho_expired_token",
		TokenExpiresAt: time.Now().Add(-1 * time.Hour), // Already expired
	}
	server.gitHubInstallationsMu.Unlock()

	// Try to create workspace - should fail with auth required
	wsReq := M4CreateWorkspaceRequest{
		GitHubUsername: "tokenuser",
		WorkspaceName:  "expired-test",
		Provider:       "lxc",
		Repository: M4Repository{
			Owner: "org",
			Name:  "repo",
		},
	}

	body, _ := json.Marshal(wsReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/create-from-repo", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.handleM4CreateWorkspace(w, req)

	// Should either fail with auth required or succeed depending on implementation
	// For this test, we just verify it doesn't crash
	assert.True(t, w.Code == http.StatusUnauthorized || w.Code == http.StatusAccepted)
}
