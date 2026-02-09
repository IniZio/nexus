package ctrl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceGroupManager(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()

	// Create a mock config path
	configPath := filepath.Join(tmpDir, "workspace-groups.yaml")

	// Create manager with custom config path
	manager := &WorkspaceGroupManager{
		configPath: configPath,
	}

	t.Run("AddGroup", func(t *testing.T) {
		err := manager.AddGroup("frontend", "Frontend workspaces", []string{"feat/login", "feat/dashboard"})
		require.NoError(t, err)

		// Verify the file was created
		data, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.Contains(t, string(data), "frontend")
	})

	t.Run("AddGroup duplicates", func(t *testing.T) {
		err := manager.AddGroup("frontend", "Another description", []string{"feat/new"})
		assert.Error(t, err) // Group already exists
	})

	t.Run("GetGroup", func(t *testing.T) {
		group := manager.GetGroup("frontend")
		require.NotNil(t, group)
		assert.Equal(t, "frontend", group.Name)
		assert.Equal(t, 2, len(group.Workspaces))
	})

	t.Run("ListGroups", func(t *testing.T) {
		// Add another group
		err := manager.AddGroup("backend", "Backend workspaces", []string{"feat/api", "feat/db"})
		require.NoError(t, err)

		groups := manager.ListGroups()
		assert.Equal(t, 2, len(groups))
	})

	t.Run("SetAlias", func(t *testing.T) {
		err := manager.SetAlias("f1", "feat/login")
		require.NoError(t, err)

		// Reload and check
		manager2 := &WorkspaceGroupManager{configPath: configPath}
		err = manager2.Load()
		require.NoError(t, err)

		assert.Equal(t, "feat/login", manager2.ResolveAlias("f1"))
		assert.Equal(t, "unknown", manager2.ResolveAlias("unknown"))
	})

	t.Run("GetWorkspacesForGroup", func(t *testing.T) {
		workspaces, err := manager.GetWorkspacesForGroup("frontend")
		require.NoError(t, err)
		assert.Equal(t, 2, len(workspaces))
	})

	t.Run("GetWorkspacesForGroup single workspace", func(t *testing.T) {
		workspaces, err := manager.GetWorkspacesForGroup("nonexistent")
		require.NoError(t, err)
		assert.Equal(t, 1, len(workspaces))
		assert.Equal(t, "nonexistent", workspaces[0])
	})

	t.Run("GetWorkspacesForGroup alias", func(t *testing.T) {
		workspaces, err := manager.GetWorkspacesForGroup("f1")
		require.NoError(t, err)
		assert.Equal(t, 1, len(workspaces))
		assert.Equal(t, "feat/login", workspaces[0])
	})
}

func TestBatchResult(t *testing.T) {
	result := BatchResult{
		Workspace: "test-ws",
		Success:   true,
		Error:     nil,
	}

	assert.Equal(t, "test-ws", result.Workspace)
	assert.True(t, result.Success)
	assert.Nil(t, result.Error)

	result.Error = assert.AnError
	assert.Error(t, result.Error)
}
