package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLockfile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "lockfile-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	lockfileContent := `{
  "plugins": {
    "docker-support": {
      "version": "1.0.0",
      "source": "https://nexus.example.com/plugins/docker-support"
    },
    "git-integration": {
      "version": "2.1.0",
      "source": "https://nexus.example.com/plugins/git-integration"
    }
  }
}`
	lockfilePath := filepath.Join(tempDir, "nexus.lock")
	require.NoError(t, os.WriteFile(lockfilePath, []byte(lockfileContent), 0644))

	lockfile, err := ReadLockfile(lockfilePath)
	require.NoError(t, err)
	require.NotNil(t, lockfile)

	assert.Len(t, lockfile.Plugins, 2)

	dockerPlugin, exists := lockfile.Plugins["docker-support"]
	assert.True(t, exists)
	assert.Equal(t, "1.0.0", dockerPlugin.Version)
	assert.Equal(t, "https://nexus.example.com/plugins/docker-support", dockerPlugin.Source)

	gitPlugin, exists := lockfile.Plugins["git-integration"]
	assert.True(t, exists)
	assert.Equal(t, "2.1.0", gitPlugin.Version)
	assert.Equal(t, "https://nexus.example.com/plugins/git-integration", gitPlugin.Source)
}

func TestReadLockfile_FileNotFound(t *testing.T) {
	_, err := ReadLockfile("/nonexistent/path/nexus.lock")
	assert.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

func TestReadLockfileFromWorkspace(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "lockfile-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	lockfileContent := `{"plugins": {"test-plugin": {"version": "1.0.0"}}}`
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "nexus.lock"), []byte(lockfileContent), 0644))

	lockfile, err := ReadLockfileFromWorkspace(tempDir)
	require.NoError(t, err)
	require.NotNil(t, lockfile)
	assert.Len(t, lockfile.Plugins, 1)
}

func TestDiscoverPlugins(t *testing.T) {
	lockfile := &Lockfile{
		Plugins: map[string]LockedPlugin{
			"docker-support": {
				Version: "1.0.0",
				Source:  "https://nexus.example.com/plugins/docker-support",
			},
			"git-plugin": {
				Version: "2.0.0",
				Source:  "https://nexus.example.com/plugins/git",
			},
		},
	}

	plugins, err := DiscoverPlugins(lockfile)
	require.NoError(t, err)
	require.NotNil(t, plugins)
	assert.Len(t, plugins, 2)

	// Find docker-support plugin
	var dockerPlugin Plugin
	for _, p := range plugins {
		if p.Name == "docker-support" {
			dockerPlugin = p
			break
		}
	}
	assert.Equal(t, "docker-support", dockerPlugin.Name)
	assert.Equal(t, "1.0.0", dockerPlugin.Version)
	assert.Equal(t, "https://nexus.example.com/plugins/docker-support", dockerPlugin.Repository)
}

func TestDiscoverPlugins_EmptyLockfile(t *testing.T) {
	lockfile := &Lockfile{
		Plugins: map[string]LockedPlugin{},
	}

	plugins, err := DiscoverPlugins(lockfile)
	require.NoError(t, err)
	assert.Nil(t, plugins)
}

func TestDiscoverPlugins_NilLockfile(t *testing.T) {
	plugins, err := DiscoverPlugins(nil)
	require.NoError(t, err)
	assert.Nil(t, plugins)
}

func TestLoadPluginFromLockfile(t *testing.T) {
	lockfile := &Lockfile{
		Plugins: map[string]LockedPlugin{
			"docker-support": {
				Version: "1.0.0",
				Source:  "https://nexus.example.com/plugins/docker-support",
			},
		},
	}

	plugin, err := LoadPluginFromLockfile("docker-support", lockfile)
	require.NoError(t, err)
	require.NotNil(t, plugin)

	assert.Equal(t, "docker-support", plugin.Name)
	assert.Equal(t, "1.0.0", plugin.Version)
	assert.Equal(t, "https://nexus.example.com/plugins/docker-support", plugin.Repository)
}

func TestLoadPluginFromLockfile_NotFound(t *testing.T) {
	lockfile := &Lockfile{
		Plugins: map[string]LockedPlugin{
			"docker-support": {Version: "1.0.0"},
		},
	}

	_, err := LoadPluginFromLockfile("nonexistent-plugin", lockfile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found in lockfile")
}

func TestLoadPluginFromLockfile_NilLockfile(t *testing.T) {
	_, err := LoadPluginFromLockfile("test", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "lockfile is nil")
}

func TestGetPluginNames(t *testing.T) {
	lockfile := &Lockfile{
		Plugins: map[string]LockedPlugin{
			"plugin-a": {Version: "1.0.0"},
			"plugin-b": {Version: "2.0.0"},
			"plugin-c": {Version: "3.0.0"},
		},
	}

	names := GetPluginNames(lockfile)
	assert.Len(t, names, 3)
	assert.Contains(t, names, "plugin-a")
	assert.Contains(t, names, "plugin-b")
	assert.Contains(t, names, "plugin-c")
}

func TestGetPluginNames_NilLockfile(t *testing.T) {
	names := GetPluginNames(nil)
	assert.Nil(t, names)
}

func TestHasPlugin(t *testing.T) {
	lockfile := &Lockfile{
		Plugins: map[string]LockedPlugin{
			"docker-support": {Version: "1.0.0"},
		},
	}

	assert.True(t, HasPlugin(lockfile, "docker-support"))
	assert.False(t, HasPlugin(lockfile, "nonexistent"))
	assert.False(t, HasPlugin(nil, "docker-support"))
}

func TestRegistry_DiscoverPluginsWithLockfile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "plugin-registry-lockfile-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create .nexus directory
	nexusDir := filepath.Join(tempDir, ".nexus")
	require.NoError(t, os.MkdirAll(nexusDir, 0755))

	// Create nexus.lock with remote plugins
	lockfileContent := `{
  "plugins": {
    "docker-support": {
      "version": "1.0.0",
      "source": "https://nexus.example.com/plugins/docker-support"
    }
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(nexusDir, "nexus.lock"), []byte(lockfileContent), 0644))

	// Create plugins directory
	pluginsDir := filepath.Join(nexusDir, "plugins")
	require.NoError(t, os.MkdirAll(pluginsDir, 0755))

	// Create a local plugin
	localPluginDir := filepath.Join(pluginsDir, "local", "tool")
	require.NoError(t, os.MkdirAll(localPluginDir, 0755))

	localPluginManifest := `plugin:
  name: "tool"
  version: "1.0.0"
  description: "Local tool plugin"
`
	require.NoError(t, os.WriteFile(filepath.Join(localPluginDir, "plugin.yaml"), []byte(localPluginManifest), 0644))

	// Test discovery
	registry := NewRegistry()
	err = registry.DiscoverPlugins(nexusDir)
	require.NoError(t, err)

	// Verify both local and remote plugins were discovered
	plugins := registry.ListPlugins()
	assert.Len(t, plugins, 2)

	// Check local plugin
	localPlugin, exists := registry.GetPlugin("local/tool")
	assert.True(t, exists)
	assert.Equal(t, "tool", localPlugin.Name)

	// Check remote plugin from lockfile
	remotePlugin, exists := registry.GetPlugin("docker-support")
	assert.True(t, exists)
	assert.Equal(t, "docker-support", remotePlugin.Name)
	assert.Equal(t, "1.0.0", remotePlugin.Version)
	assert.Equal(t, "https://nexus.example.com/plugins/docker-support", remotePlugin.Repository)
}
