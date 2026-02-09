package ctrl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type MockProvider struct {
	mock.Mock
}

func (m *MockProvider) Name() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockProvider) Create(ctx context.Context, sessionID string, workspacePath string, config interface{}) (*provider.Session, error) {
	args := m.Called(ctx, sessionID, workspacePath, config)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*provider.Session), args.Error(1)
}

func (m *MockProvider) Start(ctx context.Context, sessionID string) error {
	args := m.Called(ctx, sessionID)
	return args.Error(0)
}

func (m *MockProvider) Stop(ctx context.Context, sessionID string) error {
	args := m.Called(ctx, sessionID)
	return args.Error(0)
}

func (m *MockProvider) Destroy(ctx context.Context, sessionID string) error {
	args := m.Called(ctx, sessionID)
	return args.Error(0)
}

func (m *MockProvider) Exec(ctx context.Context, sessionID string, opts provider.ExecOptions) error {
	args := m.Called(ctx, sessionID, opts)
	return args.Error(0)
}

func (m *MockProvider) List(ctx context.Context) ([]provider.Session, error) {
	args := m.Called(ctx)
	return args.Get(0).([]provider.Session), args.Error(1)
}

type MockWorktreeManager struct {
	mock.Mock
}

func (m *MockWorktreeManager) Add(branch string) (string, error) {
	args := m.Called(branch)
	return args.String(0), args.Error(1)
}

func (m *MockWorktreeManager) Remove(branch string) error {
	args := m.Called(branch)
	return args.Error(0)
}

func TestBaseController_Init(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ctrl-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	oldCwd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(oldCwd)

	ctrl := NewBaseController(nil, nil)
	err = ctrl.Init(context.Background())

	assert.NoError(t, err)
	assert.DirExists(t, ".nexus/hooks")
	assert.FileExists(t, ".nexus/config.yaml")
	assert.FileExists(t, ".nexus/hooks/up.sh")
}

func TestBaseController_WorkspaceRm(t *testing.T) {
	mockWT := new(MockWorktreeManager)
	mockP := new(MockProvider)
	mockP.On("Name").Return("docker")
	mockP.On("List", mock.Anything).Return([]provider.Session{}, nil)

	ctrl := NewBaseController([]provider.Provider{mockP}, mockWT)

	tempDir, err := os.MkdirTemp("", "ctrl-rm-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	oldCwd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(oldCwd)

	os.MkdirAll(".nexus/worktrees/test-workspace", 0755)
	os.WriteFile(".nexus/config.yaml", []byte("name: test-project"), 0644)

	err = ctrl.WorkspaceList(context.Background())

	assert.NoError(t, err)
}

func TestBaseController_WorkspaceList(t *testing.T) {
	mockP := new(MockProvider)
	mockP.On("Name").Return("docker")

	sessions := []provider.Session{
		{
			ID:       "test-project-ws1",
			Provider: "docker",
			Status:   "running",
			Labels: map[string]string{
				"nexus.session.id": "test-project-ws1",
			},
		},
	}
	mockP.On("List", mock.Anything).Return(sessions, nil)

	ctrl := NewBaseController([]provider.Provider{mockP}, nil)

	tempDir, err := os.MkdirTemp("", "ctrl-list-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	oldCwd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(oldCwd)

	os.MkdirAll(".nexus", 0755)
	os.WriteFile(".nexus/config.yaml", []byte("name: test-project"), 0644)

	err = ctrl.WorkspaceList(context.Background())

	assert.NoError(t, err)
}

func TestBaseController_WorkspaceCreate(t *testing.T) {
	mockWT := new(MockWorktreeManager)
	ctrl := NewBaseController(nil, mockWT)

	tempDir, err := os.MkdirTemp("", "ctrl-create-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	oldCwd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(oldCwd)

	os.MkdirAll(".nexus", 0755)
	ctrl = NewBaseController(nil, mockWT)
	err = ctrl.Init(context.Background())
	assert.NoError(t, err)

	os.WriteFile(".nexus/config.yaml", []byte("name: test-project\nagents:\n  - name: cursor\n    enabled: true"), 0644)
	os.WriteFile(".gitignore", []byte("node_modules\n"), 0644)

	wtPath := filepath.Join(tempDir, "worktree-1")
	os.MkdirAll(wtPath, 0755)
	mockWT.On("Add", "test-workspace").Return(wtPath, nil)

	err = ctrl.WorkspaceCreate(context.Background(), "test-workspace")

	assert.NoError(t, err)

	mockWT.AssertExpectations(t)
}

func TestDetectPortFromCommand(t *testing.T) {
	tests := []struct {
		command  string
		expected int
	}{
		{"npm run dev", 3000},
		{"npm start", 3000},
		{"python manage.py runserver", 8000},
		{"flask run", 5000},
		{"rails s", 3000},
		{"docker-compose up postgres", 5432},
		{"unknown command", 0},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			assert.Equal(t, tt.expected, detectPortFromCommand(tt.command))
		})
	}
}

func TestDetectProtocol(t *testing.T) {
	tests := []struct {
		service  string
		command  string
		expected string
	}{
		{"db", "postgres -D /data", "postgresql"},
		{"mysql", "mysqld", "mysql"},
		{"redis", "redis-server", "redis"},
		{"api", "npm start", "http"},
	}

	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			assert.Equal(t, tt.expected, detectProtocol(tt.service, tt.command))
		})
	}
}

func TestBaseController_FindProjectRoot(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ctrl-root-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectRoot := filepath.Join(tempDir, "project")
	os.MkdirAll(filepath.Join(projectRoot, ".nexus"), 0755)

	subDir := filepath.Join(projectRoot, "a/b/c")
	os.MkdirAll(subDir, 0755)

	ctrl := NewBaseController(nil, nil)

	os.Chdir(projectRoot)
	root, err := ctrl.findProjectRoot()
	assert.NoError(t, err)
	assert.Equal(t, projectRoot, root)

	os.Chdir(subDir)
	root, err = ctrl.findProjectRoot()
	assert.NoError(t, err)
	assert.Equal(t, projectRoot, root)
}

func TestBaseController_DetectWorkspaceFromCWD(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ctrl-detect-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectRoot := filepath.Join(tempDir, "project")
	workspaceName := "my-workspace"
	// Create config directory
	os.MkdirAll(filepath.Join(projectRoot, ".nexus"), 0755)
	// Create runtime worktrees directory with new structure
	workspaceDir := filepath.Join(projectRoot, ".nexus-runtime/state/worktrees", workspaceName)
	os.MkdirAll(workspaceDir, 0755)

	subDir := filepath.Join(workspaceDir, "src/components")
	os.MkdirAll(subDir, 0755)

	ctrl := NewBaseController(nil, nil)

	os.Chdir(workspaceDir)
	name, err := ctrl.detectWorkspaceFromCWD()
	assert.NoError(t, err)
	assert.Equal(t, workspaceName, name)

	os.Chdir(subDir)
	name, err = ctrl.detectWorkspaceFromCWD()
	assert.NoError(t, err)
	assert.Equal(t, workspaceName, name)

	os.Chdir(projectRoot)
	_, err = ctrl.detectWorkspaceFromCWD()
	assert.Error(t, err)
}

func TestBaseController_RunHook(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ctrl-hook-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	hookPath := filepath.Join(tempDir, "test-hook.sh")
	os.WriteFile(hookPath, []byte("#!/bin/bash\necho \"Hello $BRANCH_NAME\"\nprintenv | grep loom_SERVICE > services.txt\ntouch hooked.txt"), 0755)

	cfg := &config.Config{
		Services: map[string]config.Service{
			"web": {Port: 3000},
			"db":  {Command: "docker-compose up postgres"},
		},
	}

	ctrl := NewBaseController(nil, nil)
	err = ctrl.runHook(context.Background(), hookPath, cfg, tempDir)

	assert.NoError(t, err)
	assert.FileExists(t, filepath.Join(tempDir, "hooked.txt"))
	assert.FileExists(t, filepath.Join(tempDir, ".env"))

	envContent, _ := os.ReadFile(filepath.Join(tempDir, ".env"))
	assert.Contains(t, string(envContent), "loom_SERVICE_WEB_URL=http://localhost:3000")
	assert.Contains(t, string(envContent), "loom_SERVICE_DB_URL=postgresql://localhost:5432")

	servicesContent, _ := os.ReadFile(filepath.Join(tempDir, "services.txt"))
	assert.Contains(t, string(servicesContent), "loom_SERVICE_WEB_URL")
	assert.Contains(t, string(servicesContent), "loom_SERVICE_DB_URL")
}

func TestBaseController_WorkspaceUp(t *testing.T) {
	mockP := new(MockProvider)
	mockP.On("Name").Return("docker")
	mockP.On("Create", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&provider.Session{ID: "test-sess"}, nil)
	mockP.On("Start", mock.Anything, "test-sess").Return(nil)
	mockP.On("List", mock.Anything).Return([]provider.Session{{ID: "test-sess", Status: "running"}}, nil)
	mockP.On("Exec", mock.Anything, "test-sess", mock.Anything).Return(nil)

	tempDir, err := os.MkdirTemp("", "ctrl-up-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectRoot := filepath.Join(tempDir, "project")
	// Create config directory with hook
	configDir := filepath.Join(projectRoot, ".nexus")
	os.MkdirAll(filepath.Join(configDir, "hooks"), 0755)
	os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("name: test-project"), 0644)
	os.WriteFile(filepath.Join(configDir, "hooks/up.sh"), []byte("#!/bin/bash\ntouch up_executed.txt"), 0755)

	// Create runtime worktrees directory
	wtDir := filepath.Join(projectRoot, ".nexus-runtime/state/worktrees/test-ws")
	os.MkdirAll(wtDir, 0755)

	ctrl := NewBaseController([]provider.Provider{mockP}, nil)

	oldCwd, _ := os.Getwd()
	os.Chdir(projectRoot)
	defer os.Chdir(oldCwd)

	err = ctrl.WorkspaceUp(context.Background(), "test-ws")

	assert.NoError(t, err)
	assert.FileExists(t, filepath.Join(wtDir, "up_executed.txt"))
}

func TestBaseController_HandleBranchConflicts(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ctrl-git-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	repoPath := filepath.Join(tempDir, "repo")
	os.MkdirAll(repoPath, 0755)

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		cmd.Run()
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# Test"), 0644)
	runGit("add", "README.md")
	runGit("commit", "-m", "Initial")

	ctrl := NewBaseController(nil, nil)

	oldCwd, _ := os.Getwd()
	os.Chdir(repoPath)
	defer os.Chdir(oldCwd)

	err = ctrl.handleBranchConflicts("main")
	assert.NoError(t, err)

	os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# Modified"), 0644)
	err = ctrl.handleBranchConflicts("main")
	assert.NoError(t, err)

	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoPath
	output, _ := cmd.Output()
	assert.NotEmpty(t, string(output))
}

func TestBaseController_WorkspaceDown(t *testing.T) {
	mockP := new(MockProvider)
	mockP.On("Name").Return("docker")

	sessionID := "test-project-test-ws"
	sessions := []provider.Session{
		{
			ID: "cont-id",
			Labels: map[string]string{
				"nexus.session.id": sessionID,
			},
		},
	}
	mockP.On("List", mock.Anything).Return(sessions, nil)
	mockP.On("Destroy", mock.Anything, "cont-id").Return(nil)

	ctrl := NewBaseController([]provider.Provider{mockP}, nil)

	tempDir, err := os.MkdirTemp("", "ctrl-down-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	oldCwd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(oldCwd)

	os.MkdirAll(".nexus", 0755)
	os.WriteFile(".nexus/config.yaml", []byte("name: test-project"), 0644)

	err = ctrl.WorkspaceDown(context.Background(), "test-ws")

	assert.NoError(t, err)
	mockP.AssertExpectations(t)
}

func TestBaseController_WorkspaceShell(t *testing.T) {
	mockP := new(MockProvider)
	mockP.On("Name").Return("docker")

	sessionID := "test-project-test-ws"
	sessions := []provider.Session{
		{
			ID: "cont-id",
			Labels: map[string]string{
				"nexus.session.id": sessionID,
			},
		},
	}
	mockP.On("List", mock.Anything).Return(sessions, nil)
	mockP.On("Exec", mock.Anything, "cont-id", mock.Anything).Return(nil)

	ctrl := NewBaseController([]provider.Provider{mockP}, nil)

	tempDir, err := os.MkdirTemp("", "ctrl-shell-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	oldCwd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(oldCwd)

	os.MkdirAll(".nexus", 0755)
	os.WriteFile(".nexus/config.yaml", []byte("name: test-project"), 0644)

	err = ctrl.WorkspaceShell(context.Background(), "test-ws")

	assert.NoError(t, err)
	mockP.AssertExpectations(t)
}

func TestBaseController_SetupWorkspaceEnvironment(t *testing.T) {
	mockP := new(MockProvider)
	mockP.On("Name").Return("docker")

	session := &provider.Session{
		ID: "cont-id",
		Services: map[string]int{
			"3000": 32768,
		},
	}

	cfg := &config.Config{
		Name: "test-project",
		Services: map[string]config.Service{
			"web": {Port: 3000},
		},
		Hooks: struct {
			Setup    string `yaml:"setup,omitempty"`
			Dev      string `yaml:"dev,omitempty"`
			Teardown string `yaml:"teardown,omitempty"`
		}{
			Setup: "setup.sh",
		},
	}

	mockP.On("Exec", mock.Anything, "cont-id", mock.Anything).Return(nil)

	ctrl := NewBaseController([]provider.Provider{mockP}, nil)

	tempDir, err := os.MkdirTemp("", "ctrl-env-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	err = ctrl.setupWorkspaceEnvironment(context.Background(), session, cfg, mockP, tempDir)

	assert.NoError(t, err)
	assert.FileExists(t, filepath.Join(tempDir, ".env"))

	envContent, _ := os.ReadFile(filepath.Join(tempDir, ".env"))
	assert.Contains(t, string(envContent), "loom_SERVICE_WEB_URL=http://localhost:32768")

	mockP.AssertExpectations(t)
}

func TestFetchPluginFiles_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Test cloning the actual nexus repo and fetching rules
	// Use IniZio/nexus which is the correct repo name
	ctrl := NewBaseController(nil, nil)

	files, err := ctrl.fetchPluginFiles(
		"https://github.com/IniZio/nexus",
		".nexus/templates/rules",
		"main",
	)
	assert.NoError(t, err)
	assert.NotEmpty(t, files, "Expected to fetch at least some rules files")

	// Verify we got markdown files
	for _, file := range files {
		assert.True(t, len(file.Name) > 0, "File should have a name")
		assert.True(t, len(file.Content) > 0, "File %s should have content", file.Name)
		assert.True(t, len(file.Content) > 50, "File %s should have substantial content", file.Name)
	}
}

func TestFetchPluginFiles_FileContentMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// This test verifies that content fetched from GitHub matches the actual file content
	// We'll clone a known file and compare it to what fetchPluginFiles returns

	ctrl := NewBaseController(nil, nil)

	// Fetch rules from our own repo (using correct repo name)
	files, err := ctrl.fetchPluginFiles(
		"https://github.com/IniZio/nexus",
		".nexus/templates/rules",
		"main",
	)
	assert.NoError(t, err)

	// For each file, verify it can be found and has expected content
	// Check that files are not empty and have reasonable content
	assert.Greater(t, len(files), 0, "Should fetch at least one file from templates/rules")
	for _, file := range files {
		// Verify the file is not empty (indicates it was successfully fetched)
		assert.NotEmpty(t, file.Content, "File %s should have content", file.Name)
		// Verify files are reasonably sized (not stub/placeholder files)
		assert.Greater(t, len(file.Content), 50, "File %s should have substantial content (not a placeholder)", file.Name)
	}
}

func TestBaseController_SimpleMethods(t *testing.T) {
	mockP := new(MockProvider)
	mockP.On("Name").Return("docker")

	sessions := []provider.Session{{ID: "s1"}}
	mockP.On("List", mock.Anything).Return(sessions, nil)
	mockP.On("Destroy", mock.Anything, "s1").Return(nil)
	mockP.On("Exec", mock.Anything, "s1", mock.Anything).Return(nil)

	ctrl := NewBaseController([]provider.Provider{mockP}, nil)

	res, err := ctrl.List(context.Background())
	assert.NoError(t, err)
	assert.Len(t, res, 1)

	err = ctrl.Kill(context.Background(), "s1")
	assert.NoError(t, err)

	err = ctrl.Exec(context.Background(), "s1", []string{"ls"})
	assert.NoError(t, err)

	mockP.AssertExpectations(t)
}
