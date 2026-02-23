package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStateStore(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)
	assert.NotNil(t, store)
	assert.Equal(t, tmpDir, store.BaseDir())
}

func TestBaseDir(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	absPath, _ := filepath.Abs(tmpDir)
	assert.Equal(t, absPath, store.BaseDir())
}

func TestNewStateStore_InvalidPath(t *testing.T) {
	store, err := NewStateStore("/invalid/path/that/does/not/exist")
	assert.Error(t, err)
	assert.Nil(t, store)
}

func TestSaveAndGetWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	ws := &types.Workspace{
		ID:          "test-workspace",
		Name:        "test-workspace",
		DisplayName: "Test Workspace",
		Status:      types.StatusRunning,
		Backend:     types.BackendDocker,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	err = store.SaveWorkspace(ws)
	require.NoError(t, err)

	got, err := store.GetWorkspace("test-workspace")
	require.NoError(t, err)
	assert.Equal(t, ws.ID, got.ID)
	assert.Equal(t, ws.Name, got.Name)
	assert.Equal(t, ws.Status, got.Status)
}

func TestSaveWorkspace_Nil(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	err = store.SaveWorkspace(nil)
	assert.ErrorIs(t, err, ErrInvalidState)
}

func TestGetWorkspace_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	_, err = store.GetWorkspace("nonexistent")
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
}

func TestGetWorkspace_CorruptedData(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	wsDir := filepath.Join(tmpDir, "corrupted-workspace")
	err = os.MkdirAll(wsDir, 0755)
	require.NoError(t, err)

	wsPath := filepath.Join(wsDir, "workspace.json")
	err = os.WriteFile(wsPath, []byte("invalid json{{{"), 0644)
	require.NoError(t, err)

	_, err = store.GetWorkspace("corrupted-workspace")
	assert.Error(t, err)
}

func TestDeleteWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	ws := &types.Workspace{
		ID:     "to-delete",
		Name:   "to-delete",
		Status: types.StatusRunning,
	}
	err = store.SaveWorkspace(ws)
	require.NoError(t, err)

	err = store.DeleteWorkspace("to-delete")
	require.NoError(t, err)

	_, err = store.GetWorkspace("to-delete")
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
}

func TestDeleteWorkspace_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	err = store.DeleteWorkspace("nonexistent")
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
}

func TestListWorkspaces(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	workspaces := []*types.Workspace{
		{ID: "ws1", Name: "ws1", Status: types.StatusRunning},
		{ID: "ws2", Name: "ws2", Status: types.StatusStopped},
		{ID: "ws3", Name: "ws3", Status: types.StatusSleeping},
	}

	for _, ws := range workspaces {
		err = store.SaveWorkspace(ws)
		require.NoError(t, err)
	}

	list, err := store.ListWorkspaces()
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

func TestListWorkspaces_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	list, err := store.ListWorkspaces()
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestListWorkspaces_SkipsInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	err = store.SaveWorkspace(&types.Workspace{ID: "valid", Name: "valid", Status: types.StatusRunning})
	require.NoError(t, err)

	invalidDir := filepath.Join(tmpDir, "invalid-workspace")
	err = os.MkdirAll(invalidDir, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(invalidDir, "workspace.json"), []byte("bad"), 0644)
	require.NoError(t, err)

	list, err := store.ListWorkspaces()
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "valid", list[0].ID)
}

func TestWorkspaceExists(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	err = store.SaveWorkspace(&types.Workspace{ID: "exists", Name: "exists", Status: types.StatusRunning})
	require.NoError(t, err)

	assert.True(t, store.WorkspaceExists("exists"))
	assert.False(t, store.WorkspaceExists("nonexistent"))
}

func TestSaveWorkspace_UpdatesTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStateStore(tmpDir)
	require.NoError(t, err)

	ws := &types.Workspace{
		ID:     "timestamp-test",
		Name:   "timestamp-test",
		Status: types.StatusRunning,
	}
	err = store.SaveWorkspace(ws)
	require.NoError(t, err)

	firstSave := ws.UpdatedAt

	time.Sleep(10 * time.Millisecond)

	ws.UpdatedAt = time.Now().Add(-time.Hour)
	err = store.SaveWorkspace(ws)
	require.NoError(t, err)

	got, err := store.GetWorkspace("timestamp-test")
	require.NoError(t, err)
	assert.True(t, got.UpdatedAt.After(firstSave), "expected %v to be after %v", got.UpdatedAt, firstSave)
}
