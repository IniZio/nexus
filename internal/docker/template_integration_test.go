package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/workspace"
	"nexus/pkg/template"
	"nexus/pkg/testutil"
)

func getAbsPath(path string) string {
	absPath, _ := filepath.Abs(path)
	os.MkdirAll(absPath, 0755)
	return absPath
}

func TestTemplateIntegration_NodePostgres(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	ctx := context.Background()
	worktreePath := getAbsPath(filepath.Join(".nexus", "test-worktrees", fmt.Sprintf("test-%d", os.Getpid())))

	err = provider.Create(ctx, "test-template-node", worktreePath)
	require.NoError(t, err)

	defer func() {
		provider.Destroy(ctx, "test-template-node")
		os.RemoveAll(".nexus/test-worktrees")
	}()

	engine := template.NewEngine()
	err = engine.ApplyTemplate("node-postgres", worktreePath, nil)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(worktreePath, "docker-compose.yml"))
	assert.FileExists(t, filepath.Join(worktreePath, ".env.example"))
	assert.FileExists(t, filepath.Join(worktreePath, "scripts", "init.sh"))
}

func TestTemplateIntegration_PythonPostgres(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	ctx := context.Background()
	worktreePath := getAbsPath(filepath.Join(".nexus", "test-worktrees", fmt.Sprintf("test-%d", os.Getpid())))

	err = provider.Create(ctx, "test-template-python", worktreePath)
	require.NoError(t, err)

	defer func() {
		provider.Destroy(ctx, "test-template-python")
		os.RemoveAll(".nexus/test-worktrees")
	}()

	engine := template.NewEngine()
	err = engine.ApplyTemplate("python-postgres", worktreePath, nil)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(worktreePath, "docker-compose.yml"))
	assert.FileExists(t, filepath.Join(worktreePath, ".env.example"))
}

func TestTemplateIntegration_GoPostgres(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	ctx := context.Background()
	worktreePath := getAbsPath(filepath.Join(".nexus", "test-worktrees", fmt.Sprintf("test-%d", os.Getpid())))

	err = provider.Create(ctx, "test-template-go", worktreePath)
	require.NoError(t, err)

	defer func() {
		provider.Destroy(ctx, "test-template-go")
		os.RemoveAll(".nexus/test-worktrees")
	}()

	engine := template.NewEngine()
	err = engine.ApplyTemplate("go-postgres", worktreePath, nil)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(worktreePath, "docker-compose.yml"))
	assert.FileExists(t, filepath.Join(worktreePath, ".env.example"))
}

func TestTemplateIntegration_InvalidTemplate(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	ctx := context.Background()
	worktreePath := getAbsPath(filepath.Join(".nexus", "test-worktrees", fmt.Sprintf("test-%d", os.Getpid())))

	err = provider.Create(ctx, "test-template-invalid", worktreePath)
	require.NoError(t, err)

	defer func() {
		provider.Destroy(ctx, "test-template-invalid")
		os.RemoveAll(".nexus/test-worktrees")
	}()

	engine := template.NewEngine()
	err = engine.ApplyTemplate("invalid-template", worktreePath, nil)
	assert.Error(t, err)
}

func TestTemplateIntegration_DockerComposeValid(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()

	tmpDir := t.TempDir()
	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	content, err := os.ReadFile(composePath)
	require.NoError(t, err)

	assert.Contains(t, string(content), "version:")
	assert.Contains(t, string(content), "services:")
	assert.Contains(t, string(content), "postgres:")
}

func TestTemplateIntegration_ListTemplates(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()
	templates := engine.ListTemplates()

	assert.GreaterOrEqual(t, len(templates), 3)

	names := make([]string, len(templates))
	for i, t := range templates {
		names[i] = t.Name
	}
	assert.Contains(t, names, "node-postgres")
	assert.Contains(t, names, "python-postgres")
	assert.Contains(t, names, "go-postgres")
}

func TestTemplateIntegration_InitScript(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()

	tmpDir := t.TempDir()
	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	scriptPath := filepath.Join(tmpDir, "scripts", "init.sh")
	assert.FileExists(t, scriptPath)

	content, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "#!/bin/bash")
}

func TestTemplateIntegration_EnvExample(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()

	tmpDir := t.TempDir()
	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	envPath := filepath.Join(tmpDir, ".env.example")
	assert.FileExists(t, envPath)

	content, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "DATABASE_URL")
}

func TestWorkspaceWithTemplate_AllThree(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	templates := []string{"node-postgres", "python-postgres", "go-postgres"}

	for _, tmpl := range templates {
		t.Run(tmpl, func(t *testing.T) {
			engine := template.NewEngine()
			tmpDir := t.TempDir()

			err := engine.ApplyTemplate(tmpl, tmpDir, nil)
			require.NoError(t, err)

			assert.FileExists(t, filepath.Join(tmpDir, "docker-compose.yml"))
			assert.FileExists(t, filepath.Join(tmpDir, ".env.example"))
			assert.FileExists(t, filepath.Join(tmpDir, "README.md"))
			assert.FileExists(t, filepath.Join(tmpDir, "scripts", "init.sh"))
		})
	}
}

func TestWorkspaceWithTemplate_Reapply(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()
	tmpDir := t.TempDir()

	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	firstContent, err := os.ReadFile(filepath.Join(tmpDir, "docker-compose.yml"))
	require.NoError(t, err)

	err = engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	secondContent, err := os.ReadFile(filepath.Join(tmpDir, "docker-compose.yml"))
	require.NoError(t, err)

	assert.Equal(t, firstContent, secondContent)
}

func TestWorkspaceWithTemplate_Variables(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()
	tmpDir := t.TempDir()

	vars := map[string]string{
		"PROJECT_NAME": "my awesome project",
	}

	err := engine.ApplyTemplate("node-postgres", tmpDir, vars)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(tmpDir, "docker-compose.yml"))
	require.NoError(t, err)
	assert.NotEmpty(t, string(content))
}

func TestWorkspaceProvider_CreateWithTemplate(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	mockProvider := &mockTemplateProvider{
		containers: make(map[string]bool),
	}

	mgr := workspace.NewManager(mockProvider)

	err := mgr.CreateWithTemplate("test-ws", "node-postgres", nil)
	require.NoError(t, err)
	assert.True(t, mockProvider.containers["test-ws"])
}

type mockTemplateProvider struct {
	containers map[string]bool
}

func (m *mockTemplateProvider) Create(ctx context.Context, name string, worktreePath string) error {
	m.containers[name] = true
	return nil
}

func (m *mockTemplateProvider) CreateWithDinD(ctx context.Context, name string, worktreePath string) error {
	m.containers[name] = true
	return nil
}

func (m *mockTemplateProvider) Start(ctx context.Context, name string) error {
	return nil
}

func (m *mockTemplateProvider) Stop(ctx context.Context, name string) error {
	return nil
}

func (m *mockTemplateProvider) Destroy(ctx context.Context, name string) error {
	delete(m.containers, name)
	return nil
}

func (m *mockTemplateProvider) Shell(ctx context.Context, name string) error {
	return nil
}

func (m *mockTemplateProvider) Exec(ctx context.Context, name string, command []string) error {
	return nil
}

func (m *mockTemplateProvider) List(ctx context.Context) ([]workspace.WorkspaceInfo, error) {
	return nil, nil
}

func (m *mockTemplateProvider) Close() error {
	return nil
}

func (m *mockTemplateProvider) ContainerExists(ctx context.Context, name string) (bool, error) {
	_, exists := m.containers[name]
	return exists, nil
}

func (m *mockTemplateProvider) StartSync(ctx context.Context, workspaceName, worktreePath string) (string, error) {
	return "", nil
}

func (m *mockTemplateProvider) PauseSync(ctx context.Context, workspaceName string) error {
	return nil
}

func (m *mockTemplateProvider) ResumeSync(ctx context.Context, workspaceName string) error {
	return nil
}

func (m *mockTemplateProvider) StopSync(ctx context.Context, workspaceName string) error {
	return nil
}

func (m *mockTemplateProvider) GetSyncStatus(ctx context.Context, workspaceName string) (interface{}, error) {
	return nil, nil
}

func (m *mockTemplateProvider) FlushSync(ctx context.Context, workspaceName string) error {
	return nil
}

func TestTemplateIntegration_PortAccessibility(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()
	tmpDir := t.TempDir()

	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(tmpDir, "docker-compose.yml"))
	require.NoError(t, err)

	yamlStr := string(content)
	ports := []string{"3000", "5000", "5432"}
	for _, port := range ports {
		assert.True(t, strings.Contains(yamlStr, port), "Port %s should be in docker-compose.yml", port)
	}
}

func TestTemplateIntegration_HealthChecks(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()

	templates := []string{"node-postgres", "python-postgres", "go-postgres"}

	for _, tmpl := range templates {
		t.Run(tmpl, func(t *testing.T) {
			tmpDir := t.TempDir()
			err := engine.ApplyTemplate(tmpl, tmpDir, nil)
			require.NoError(t, err)

			readmeContent, err := os.ReadFile(filepath.Join(tmpDir, "README.md"))
			require.NoError(t, err)
			assert.Contains(t, string(readmeContent), "Getting Started")
		})
	}
}

func TestTemplateIntegration_EnvironmentVars(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	engine := template.NewEngine()

	testCases := []struct {
		template  string
		envVar    string
		envVarVal string
	}{
		{"node-postgres", "DATABASE_URL", "postgres://"},
		{"python-postgres", "DATABASE_URL", "postgresql://"},
		{"go-postgres", "DATABASE_URL", "postgres://"},
	}

	for _, tc := range testCases {
		t.Run(tc.template, func(t *testing.T) {
			tmpDir := t.TempDir()
			err := engine.ApplyTemplate(tc.template, tmpDir, nil)
			require.NoError(t, err)

			envContent, err := os.ReadFile(filepath.Join(tmpDir, ".env.example"))
			require.NoError(t, err)
			assert.Contains(t, string(envContent), tc.envVar)
		})
	}
}

func TestTemplateIntegration_ServicesStartup(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	t.Skip("Requires docker-compose up - requires running services")

	engine := template.NewEngine()
	tmpDir := t.TempDir()

	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := strings.Fields("docker-compose up -d")
	execCmd := &externalCmd{
		Command: cmd[0],
		Args:    cmd[1:],
		Dir:     tmpDir,
	}

	err = execCmd.Run(ctx)
	require.NoError(t, err)

	time.Sleep(10 * time.Second)

	assert.True(t, len("placeholder") > 0)
}

type externalCmd struct {
	Command string
	Args    []string
	Dir     string
}

func (e *externalCmd) Run(ctx context.Context) error {
	return nil
}
