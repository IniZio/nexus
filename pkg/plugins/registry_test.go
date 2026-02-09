package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_DiscoverPlugins(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "plugin-registry-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create plugin directory structure
	pluginsDir := filepath.Join(tempDir, ".nexus", "plugins")
	require.NoError(t, os.MkdirAll(pluginsDir, 0755))

	// Create a local plugin
	localPluginDir := filepath.Join(pluginsDir, "myorg", "git")
	require.NoError(t, os.MkdirAll(localPluginDir, 0755))

	localPluginManifest := `plugin:
  name: "git"
  version: "1.0.0"
  description: "Git integration plugin"
  dependencies:
    - "core/base"
`
	require.NoError(t, os.WriteFile(filepath.Join(localPluginDir, "plugin.yaml"), []byte(localPluginManifest), 0644))

	// Create dependency plugin
	corePluginDir := filepath.Join(pluginsDir, "core", "base")
	require.NoError(t, os.MkdirAll(corePluginDir, 0755))

	corePluginManifest := `plugin:
  name: "base"
  version: "1.0.0"
  description: "Core base plugin"
`
	require.NoError(t, os.WriteFile(filepath.Join(corePluginDir, "plugin.yaml"), []byte(corePluginManifest), 0644))

	// Debug: check if files exist
	pluginYaml := filepath.Join(tempDir, ".nexus", "plugins", "myorg", "git", "plugin.yaml")
	if _, err := os.Stat(pluginYaml); err == nil {
		t.Logf("Plugin YAML exists")
	} else {
		t.Logf("Plugin YAML does not exist: %v", err)
	}

	// Test discovery
	registry := NewRegistry()
	err = registry.DiscoverPlugins(tempDir + "/.nexus")
	require.NoError(t, err)

	// Debug: list all plugins
	t.Logf("Discovered plugins: %+v", registry.ListPlugins())

	// Verify plugins were discovered
	gitPlugin, exists := registry.GetPlugin("myorg/git")
	assert.True(t, exists)
	if exists {
		assert.Equal(t, "git", gitPlugin.Name)
		assert.Equal(t, "1.0.0", gitPlugin.Version)
		assert.Contains(t, gitPlugin.Dependencies, "core/base")
	}

	basePlugin, exists := registry.GetPlugin("core/base")
	assert.True(t, exists)
	assert.Equal(t, "base", basePlugin.Name)
	assert.Empty(t, basePlugin.Dependencies)
}

func TestRegistry_ResolveDependencies(t *testing.T) {
	registry := NewRegistry()

	// Setup mock plugins
	registry.plugins["core/base"] = &Plugin{
		Name:    "base",
		Version: "1.0.0",
	}
	registry.plugins["myorg/git"] = &Plugin{
		Name:         "git",
		Version:      "1.0.0",
		Dependencies: []string{"core/base"},
	}
	registry.plugins["myorg/verification"] = &Plugin{
		Name:         "verification",
		Version:      "1.0.0",
		Dependencies: []string{"myorg/git"},
	}

	tests := []struct {
		name        string
		pluginNames []string
		expected    []string
		wantErr     bool
	}{
		{
			name:        "simple dependency",
			pluginNames: []string{"myorg/git"},
			expected:    []string{"core/base", "myorg/git"},
			wantErr:     false,
		},
		{
			name:        "transitive dependencies",
			pluginNames: []string{"myorg/verification"},
			expected:    []string{"core/base", "myorg/git", "myorg/verification"},
			wantErr:     false,
		},
		{
			name:        "multiple plugins",
			pluginNames: []string{"core/base", "myorg/git"},
			expected:    []string{"core/base", "myorg/git"},
			wantErr:     false,
		},
		{
			name:        "non-existent plugin",
			pluginNames: []string{"nonexistent/plugin"},
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := registry.ResolveDependencies(tt.pluginNames)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRegistry_ResolveDependencies_Circular(t *testing.T) {
	registry := NewRegistry()

	// Setup circular dependency
	registry.plugins["a"] = &Plugin{Name: "a", Dependencies: []string{"b"}}
	registry.plugins["b"] = &Plugin{Name: "b", Dependencies: []string{"a"}}

	_, err := registry.ResolveDependencies([]string{"a"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestRegistry_ListPlugins(t *testing.T) {
	registry := NewRegistry()
	plugin := &Plugin{Name: "test", Version: "1.0.0"}
	registry.plugins["test/plugin"] = plugin

	plugins := registry.ListPlugins()
	assert.Len(t, plugins, 1)
	assert.Equal(t, plugin, plugins["test/plugin"])
}
