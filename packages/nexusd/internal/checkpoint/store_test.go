package checkpoint

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileCheckpointStore_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	cp := &Checkpoint{
		ID:          "test-cp-1",
		WorkspaceID: "test-ws",
		Name:        "baseline",
		CreatedAt:   time.Now(),
		ImageName:   "test-image",
		Size:        1024 * 1024,
	}

	err = store.SaveCheckpoint(cp)
	require.NoError(t, err)

	loaded, err := store.GetCheckpoint("test-ws", "test-cp-1")
	require.NoError(t, err)
	assert.Equal(t, cp.ID, loaded.ID)
	assert.Equal(t, cp.Name, loaded.Name)
	assert.Equal(t, cp.WorkspaceID, loaded.WorkspaceID)
}

func TestFileCheckpointStore_List(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		cp := &Checkpoint{
			ID:          fmt.Sprintf("cp-%d", i),
			WorkspaceID: "test-ws",
			Name:        fmt.Sprintf("checkpoint-%d", i),
			CreatedAt:   time.Now(),
		}
		require.NoError(t, store.SaveCheckpoint(cp))
	}

	checkpoints, err := store.ListCheckpoints("test-ws")
	require.NoError(t, err)
	assert.Len(t, checkpoints, 3)
}

func TestFileCheckpointStore_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	cp := &Checkpoint{
		ID:          "to-delete",
		WorkspaceID: "test-ws",
		Name:        "temp",
	}

	require.NoError(t, store.SaveCheckpoint(cp))

	err = store.DeleteCheckpoint("test-ws", "to-delete")
	require.NoError(t, err)

	_, err = store.GetCheckpoint("test-ws", "to-delete")
	assert.Error(t, err)
}

func TestFileCheckpointStore_LoadWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	cp := &Checkpoint{
		ID:          "cp-1",
		WorkspaceID: "workspace-1",
		Name:        "test",
		CreatedAt:   time.Now(),
	}

	require.NoError(t, store.SaveCheckpoint(cp))

	loaded, err := store.GetCheckpoint("workspace-1", "cp-1")
	require.NoError(t, err)
	assert.Equal(t, "cp-1", loaded.ID)
}

func TestFileCheckpointStore_LoadAll(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		cp := &Checkpoint{
			ID:          fmt.Sprintf("cp-%d", i),
			WorkspaceID: "ws-all",
			Name:        fmt.Sprintf("checkpoint-%d", i),
			CreatedAt:   time.Now(),
		}
		require.NoError(t, store.SaveCheckpoint(cp))
	}

	err = store.LoadAll()
	require.NoError(t, err)

	checkpoints, err := store.ListCheckpoints("ws-all")
	require.NoError(t, err)
	assert.Len(t, checkpoints, 2)
}

func TestFileCheckpointStore_BaseDir(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	absPath, err := filepath.Abs(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, absPath, store.BaseDir())
}

func TestFileCheckpointStore_CheckpointPersistence(t *testing.T) {
	tmpDir := t.TempDir()

	store1, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	cp := &Checkpoint{
		ID:          "persistent-cp",
		WorkspaceID: "persistent-ws",
		Name:        "persistent",
		CreatedAt:   time.Now(),
		Size:        2048,
	}

	require.NoError(t, store1.SaveCheckpoint(cp))

	store2, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	loaded, err := store2.GetCheckpoint("persistent-ws", "persistent-cp")
	require.NoError(t, err)
	assert.Equal(t, cp.ID, loaded.ID)
	assert.Equal(t, cp.Name, loaded.Name)
	assert.Equal(t, cp.Size, loaded.Size)
}

func TestFileCheckpointStore_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	_, err = store.GetCheckpoint("nonexistent-ws", "nonexistent-cp")
	assert.Error(t, err)
}

func TestFileCheckpointStore_DeleteNonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileCheckpointStore(tmpDir)
	require.NoError(t, err)

	err = store.DeleteCheckpoint("nonexistent-ws", "nonexistent-cp")
	assert.Error(t, err)
}
