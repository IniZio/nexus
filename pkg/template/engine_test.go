package template

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadTemplate(t *testing.T) {
	engine := NewEngine()

	templates := []string{"node-postgres", "python-postgres", "go-postgres"}

	for _, name := range templates {
		t.Run(name, func(t *testing.T) {
			template, err := engine.LoadTemplate(name)
			require.NoError(t, err)
			assert.Equal(t, name, template.Name)
			assert.NotEmpty(t, template.Description)
			assert.Contains(t, template.Files, "docker-compose.yml")
			assert.Contains(t, template.Files, ".env.example")
			assert.Contains(t, template.Files, "README.md")
			assert.Contains(t, template.Files, "scripts/init.sh")
		})
	}
}

func TestLoadTemplate_Invalid(t *testing.T) {
	engine := NewEngine()

	_, err := engine.LoadTemplate("invalid-template")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestListTemplates(t *testing.T) {
	engine := NewEngine()

	templates := engine.ListTemplates()
	assert.Len(t, templates, 3)

	names := make([]string, len(templates))
	for i, t := range templates {
		names[i] = t.Name
	}
	assert.Contains(t, names, "node-postgres")
	assert.Contains(t, names, "python-postgres")
	assert.Contains(t, names, "go-postgres")
}

func TestApplyTemplate(t *testing.T) {
	engine := NewEngine()

	tmpDir := t.TempDir()

	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "docker-compose.yml"))
	assert.FileExists(t, filepath.Join(tmpDir, ".env.example"))
	assert.FileExists(t, filepath.Join(tmpDir, "README.md"))
	assert.FileExists(t, filepath.Join(tmpDir, "scripts", "init.sh"))
}

func TestApplyTemplate_WithVars(t *testing.T) {
	engine := NewEngine()

	tmpDir := t.TempDir()

	vars := map[string]string{
		"PROJECT_NAME": "myapp",
	}

	err := engine.ApplyTemplate("node-postgres", tmpDir, vars)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "docker-compose.yml"))
}

func TestApplyTemplate_Invalid(t *testing.T) {
	engine := NewEngine()

	tmpDir := t.TempDir()

	err := engine.ApplyTemplate("invalid", tmpDir, nil)
	assert.Error(t, err)
}

func TestApplyTemplate_CreatesDirectories(t *testing.T) {
	engine := NewEngine()

	tmpDir := t.TempDir()

	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	assert.DirExists(t, filepath.Join(tmpDir, "scripts"))
}

func TestTemplateFiles_ValidYAML(t *testing.T) {
	engine := NewEngine()

	template, err := engine.LoadTemplate("node-postgres")
	require.NoError(t, err)

	composeContent := template.Files["docker-compose.yml"]
	assert.NotEmpty(t, composeContent)
	assert.Contains(t, composeContent, "version:")
	assert.Contains(t, composeContent, "services:")
	assert.Contains(t, composeContent, "postgres:")
}

func TestTemplateFiles_NodePostgres(t *testing.T) {
	engine := NewEngine()

	template, err := engine.LoadTemplate("node-postgres")
	require.NoError(t, err)

	assert.Contains(t, template.Files, "docker-compose.yml")
	assert.Contains(t, template.Description, "React")
	assert.Contains(t, template.Description, "Node")
	assert.Contains(t, template.Description, "PostgreSQL")
}

func TestTemplateFiles_PythonPostgres(t *testing.T) {
	engine := NewEngine()

	template, err := engine.LoadTemplate("python-postgres")
	require.NoError(t, err)

	assert.Contains(t, template.Files, "docker-compose.yml")
	assert.Contains(t, template.Description, "Flask")
	assert.Contains(t, template.Description, "PostgreSQL")
}

func TestTemplateFiles_GoPostgres(t *testing.T) {
	engine := NewEngine()

	template, err := engine.LoadTemplate("go-postgres")
	require.NoError(t, err)

	assert.Contains(t, template.Files, "docker-compose.yml")
	assert.Contains(t, template.Description, "Go")
	assert.Contains(t, template.Description, "PostgreSQL")
}

func TestTemplateIdempotent(t *testing.T) {
	engine := NewEngine()

	tmpDir := t.TempDir()

	err := engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	err = engine.ApplyTemplate("node-postgres", tmpDir, nil)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "docker-compose.yml"))
}
