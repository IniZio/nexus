package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nexus/nexus/pkg/templates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		expected *Config
		wantErr  bool
	}{
		{
			name: "valid config",
			yaml: `name: test-project
provider: docker
services:
  web:
    command: "npm run dev"
  api:
    command: "go run main.go"
agents:
  - name: cursor
    enabled: true`,
			expected: &Config{
				Name:     "test-project",
				Provider: "docker",
				Services: map[string]Service{
					"web": {Command: "npm run dev"},
					"api": {Command: "go run main.go"},
				},
			},
			wantErr: false,
		},
		{
			name:    "file not found",
			yaml:    "",
			wantErr: true,
		},
		{
			name: "valid qemu config",
			yaml: `name: qemu-test
provider: qemu
qemu:
  image: ubuntu:22.04
  cpu: 2
  memory: 4G
  disk: 20G
  ssh_port: 2222
  forward_ports:
    - 8080:80
    - 3000:3000
services:
  web:
    port: 8080
  api:
    port: 3000`,
			expected: &Config{
				Name:     "qemu-test",
				Provider: "qemu",
				Services: map[string]Service{
					"web": {Port: 8080},
					"api": {Port: 3000},
				},
				QEMU: struct {
					Image        string   `yaml:"image,omitempty"`
					CPU          int      `yaml:"cpu,omitempty"`
					Memory       string   `yaml:"memory,omitempty"`
					Disk         string   `yaml:"disk,omitempty"`
					SSHPort      int      `yaml:"ssh_port,omitempty"`
					ForwardPorts []string `yaml:"forward_ports,omitempty"`
					CacheMode    string   `yaml:"cache_mode,omitempty"`
					IoThread     bool     `yaml:"io_thread,omitempty"`
					VirtIO       bool     `yaml:"virtio,omitempty"`
					SELinux      bool     `yaml:"selinux,omitempty"`
					Firewall     bool     `yaml:"firewall,omitempty"`
				}{
					Image:        "ubuntu:22.04",
					CPU:          2,
					Memory:       "4G",
					Disk:         "20G",
					SSHPort:      2222,
					ForwardPorts: []string{"8080:80", "3000:3000"},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tempFile string
			if tt.yaml != "" {
				temp, err := os.CreateTemp("", "config-*.yaml")
				require.NoError(t, err)
				defer os.Remove(temp.Name())

				_, err = temp.WriteString(tt.yaml)
				require.NoError(t, err)
				temp.Close()

				tempFile = temp.Name()
			} else {
				tempFile = "nonexistent.yaml"
			}

			config, err := LoadConfig(tempFile)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, config)
				assert.Equal(t, tt.expected.Name, config.Name)
				assert.Equal(t, tt.expected.Provider, config.Provider)
				assert.Equal(t, len(tt.expected.Services), len(config.Services))
			}
		})
	}
}

func TestConfig_GetMergedTemplates(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "config-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	templatesDir := filepath.Join(tempDir, ".nexus", "templates")
	require.NoError(t, os.MkdirAll(templatesDir, 0755))

	templateContent := `skills:
  - name: "test-skill"
    description: "A test skill"
rules:
  - name: "test-rule"
    content: "Test rule content"`

	templateFile := filepath.Join(templatesDir, "test.yaml")
	require.NoError(t, os.WriteFile(templateFile, []byte(templateContent), 0644))

	config := &Config{}

	_, _ = config.GetMergedTemplates(tempDir)
}

func TestConfig_GenerateAgentConfigs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "agent-config-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	worktreeDir := filepath.Join(tempDir, "worktree")
	require.NoError(t, os.MkdirAll(worktreeDir, 0755))

	config := &Config{
		Name: "test-project",
	}

	mergedTemplates := &templates.TemplateData{}

	err = config.GenerateAgentConfigs(worktreeDir, mergedTemplates)
	_ = err
}

func TestUpdateGitignore(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "gitignore-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	oldDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldDir)

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	err = updateGitignore([]string{".cursor/"})
	assert.NoError(t, err)

	content, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	assert.Contains(t, string(content), ".cursor/")

	require.NoError(t, os.WriteFile(".gitignore", []byte("node_modules/\n"), 0644))
	err = updateGitignore([]string{".opencode/"})
	assert.NoError(t, err)

	content, err = os.ReadFile(".gitignore")
	require.NoError(t, err)
	assert.Contains(t, string(content), "node_modules/")
	assert.Contains(t, string(content), ".opencode/")
}

func TestConfigYAMLMarshalling(t *testing.T) {
	config := &Config{
		Name:     "test-project",
		Provider: "docker",
		Services: map[string]Service{
			"web": {
				Command: "npm run dev",
				Port:    3000,
			},
			"api": {
				Command:   "go run main.go",
				DependsOn: []string{"db"},
				Env: map[string]string{
					"DEBUG": "true",
				},
			},
		},
	}

	data, err := yaml.Marshal(config)
	require.NoError(t, err)

	yamlStr := string(data)
	assert.Contains(t, yamlStr, "test-project")
	assert.Contains(t, yamlStr, "npm run dev")
	assert.Contains(t, yamlStr, "3000")

	var unmarshalled Config
	err = yaml.Unmarshal(data, &unmarshalled)
	require.NoError(t, err)

	assert.Equal(t, config.Name, unmarshalled.Name)
	assert.Equal(t, config.Provider, unmarshalled.Provider)
	assert.Equal(t, len(config.Services), len(unmarshalled.Services))
}

func TestIsPluginEnabled(t *testing.T) {
	tempDir1 := t.TempDir()
	tempDir2 := t.TempDir()
	tempDir3 := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tempDir1, "go.mod"), []byte("module test"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir2, "package.json"), []byte("{}"), 0644))

	config := &Config{}

	assert.True(t, config.isPluginEnabled(tempDir1, "golang"))
	assert.True(t, config.isPluginEnabled(tempDir2, "node"))
	assert.False(t, config.isPluginEnabled(tempDir3, "golang"))
	assert.False(t, config.isPluginEnabled(tempDir3, "node"))
	assert.True(t, config.isPluginEnabled(tempDir3, "unknown"))
}

func TestFileExists(t *testing.T) {
	tempDir := t.TempDir()

	existingFile := filepath.Join(tempDir, "existing.txt")
	require.NoError(t, os.WriteFile(existingFile, []byte("content"), 0644))
	assert.True(t, fileExists(existingFile))

	nonExistingFile := filepath.Join(tempDir, "nonexistent.txt")
	assert.False(t, fileExists(nonExistingFile))
}

func TestGenerateAgentRules(t *testing.T) {
	worktreePath := t.TempDir()

	config := &Config{}

	templates := &templates.TemplateData{
		Plugins: map[string]*templates.PluginData{
			"test-plugin": {
				Rules: map[string]interface{}{
					"rule1": "content1",
				},
			},
		},
	}

	err := config.generateAgentRules(worktreePath, "cursor", ".cursor/rules", templates)
	require.NoError(t, err)
}

func TestStripFrontmatter(t *testing.T) {
	content := "---\ntitle: Test\n---\n# Main Content"
	result := stripFrontmatter(content)
	assert.NotEqual(t, content, result, "Function should modify content")
	assert.Contains(t, result, "# Main Content", "Function should preserve main content")
}

func TestGenerateJSONSchema(t *testing.T) {
	schema, err := GenerateJSONSchema()
	assert.NoError(t, err)
	assert.NotNil(t, schema)

	schemaStr := string(schema)
	assert.Contains(t, schemaStr, "name")
	assert.Contains(t, schemaStr, "provider")
	assert.Contains(t, schemaStr, "services")
}
