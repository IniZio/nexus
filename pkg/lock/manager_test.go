package lock

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexus/nexus/pkg/plugins"
)

func TestManager_GenerateLockfile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "lock-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Setup mock registry
	registry := plugins.NewRegistry()
	basePlugin := &plugins.Plugin{
		Name:    "base",
		Version: "1.0.0",
	}
	gitPlugin := &plugins.Plugin{
		Name:         "git",
		Version:      "2.0.0",
		Dependencies: []string{"core/base"},
	}
	registry.AddPlugin("core/base", basePlugin)
	registry.AddPlugin("myorg/git", gitPlugin)

	manager := NewManager(tempDir)

	lockfile, err := manager.GenerateLockfile(registry, []string{"myorg/git"})
	require.NoError(t, err)

	// Verify lockfile structure
	assert.Equal(t, "1.0", lockfile.Version)
	assert.Len(t, lockfile.Plugins, 2)

	// Check base plugin entry
	baseEntry, exists := lockfile.Plugins["core/base"]
	assert.True(t, exists)
	assert.Equal(t, "base", baseEntry.Name)
	assert.Equal(t, "1.0.0", baseEntry.Version)
	assert.NotEmpty(t, baseEntry.SHA)

	// Check git plugin entry
	gitEntry, exists := lockfile.Plugins["myorg/git"]
	assert.True(t, exists)
	assert.Equal(t, "git", gitEntry.Name)
	assert.Equal(t, "2.0.0", gitEntry.Version)
	assert.Contains(t, gitEntry.Dependencies, "core/base")
	assert.NotEmpty(t, gitEntry.SHA)

	// Verify content hash is generated
	assert.NotEmpty(t, lockfile.Metadata.ContentHash)
}

func TestManager_SaveLoadLockfile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "lock-save-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create a lockfile
	original := &Lockfile{
		Version: "1.0",
		Plugins: map[string]*LockEntry{
			"test/plugin": {
				Name:    "plugin",
				Version: "1.0.0",
				SHA:     "abc123",
			},
		},
		Metadata: LockMetadata{
			ContentHash: "hash123",
			Timestamp:   time.Now().Format(time.RFC3339),
			Generator:   "nexus",
		},
	}

	manager := NewManager(tempDir)

	// Save lockfile
	err = manager.SaveLockfile(original)
	require.NoError(t, err)

	// Load lockfile
	loaded, err := manager.LoadLockfile()
	require.NoError(t, err)

	// Verify contents match
	assert.Equal(t, original.Version, loaded.Version)
	assert.Equal(t, original.Plugins["test/plugin"].Name, loaded.Plugins["test/plugin"].Name)
	assert.Equal(t, original.Metadata.ContentHash, loaded.Metadata.ContentHash)
}

func TestManager_VerifyIntegrity(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "lock-integrity-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Setup registry and generate lockfile
	registry := plugins.NewRegistry()
	plugin := &plugins.Plugin{Name: "test", Version: "1.0.0"}
	registry.AddPlugin("test/plugin", plugin)

	manager := NewManager(tempDir)
	lockfile, err := manager.GenerateLockfile(registry, []string{"test/plugin"})
	require.NoError(t, err)

	// Verify integrity of generated lockfile
	err = manager.VerifyIntegrity(lockfile)
	assert.NoError(t, err)

	// Tamper with the lockfile
	lockfile.Metadata.ContentHash = "tampered"
	err = manager.VerifyIntegrity(lockfile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "integrity check failed")
}

func TestManager_IsUpToDate(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "lock-uptodate-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	registry := plugins.NewRegistry()
	plugin := &plugins.Plugin{Name: "test", Version: "1.0.0"}
	registry.AddPlugin("test/plugin", plugin)

	manager := NewManager(tempDir)

	// No lockfile exists yet
	upToDate, err := manager.IsUpToDate(registry, []string{"test/plugin"})
	assert.NoError(t, err)
	assert.False(t, upToDate)

	// Generate and save lockfile
	lockfile, err := manager.GenerateLockfile(registry, []string{"test/plugin"})
	require.NoError(t, err)
	err = manager.SaveLockfile(lockfile)
	require.NoError(t, err)

	// Now it should be up to date
	upToDate, err = manager.IsUpToDate(registry, []string{"test/plugin"})
	assert.NoError(t, err)
	assert.True(t, upToDate)

	// Change plugin version - should not be up to date
	plugin.Version = "2.0.0"
	upToDate, err = manager.IsUpToDate(registry, []string{"test/plugin"})
	assert.NoError(t, err)
	assert.False(t, upToDate)
}

func TestManager_LoadLockfile_NotFound(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "lock-notfound-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	manager := NewManager(tempDir)
	_, err = manager.LoadLockfile()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "lockfile not found")
}
