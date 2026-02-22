package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/checkpoint"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/mocks"
	wsTypes "github.com/nexus/nexus/packages/workspace-daemon/internal/types"
)

func TestWorkspaceLifecycle(t *testing.T) {
	mockStore := mocks.NewMockStateStore()
	mockBackend := mocks.NewMockBackend()

	cpDir := t.TempDir()
	cpStore, err := checkpoint.NewFileCheckpointStore(cpDir)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = cpStore
	})

	ctx := context.Background()

	ws := &wsTypes.Workspace{
		ID:        "test-ws-1",
		Name:      "test-lifecycle",
		Status:    wsTypes.StatusStopped,
		Backend:   wsTypes.BackendDocker,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err = mockStore.SaveWorkspace(ws)
	require.NoError(t, err)

	createReq := &wsTypes.CreateWorkspaceRequest{
		ID:   "test-ws-1",
		Name: "test-lifecycle",
	}

	createdWS, err := mockBackend.CreateWorkspace(ctx, createReq)
	require.NoError(t, err)
	assert.NotEmpty(t, createdWS.ID)

	status, err := mockBackend.GetStatus(ctx, "test-ws-1")
	require.NoError(t, err)
	assert.Equal(t, wsTypes.StatusRunning, status)

	_, err = mockBackend.StartWorkspace(ctx, "test-ws-1")
	require.NoError(t, err)

	status, err = mockBackend.GetStatus(ctx, "test-ws-1")
	require.NoError(t, err)
	assert.Equal(t, wsTypes.StatusRunning, status)

	_, err = mockBackend.StopWorkspace(ctx, "test-ws-1", 30)
	require.NoError(t, err)

	status, err = mockBackend.GetStatus(ctx, "test-ws-1")
	require.NoError(t, err)
	assert.Equal(t, wsTypes.StatusStopped, status)

	err = mockBackend.DeleteWorkspace(ctx, "test-ws-1")
	require.NoError(t, err)

	err = mockStore.DeleteWorkspace("test-ws-1")
	require.NoError(t, err)
}

func TestMockStateStore(t *testing.T) {
	store := mocks.NewMockStateStore()

	ws := &wsTypes.Workspace{
		ID:   "ws-1",
		Name: "test-ws",
	}

	err := store.SaveWorkspace(ws)
	require.NoError(t, err)

	loaded, err := store.GetWorkspace("ws-1")
	require.NoError(t, err)
	assert.Equal(t, "ws-1", loaded.ID)

	workspaces, err := store.ListWorkspaces()
	require.NoError(t, err)
	assert.Len(t, workspaces, 1)

	err = store.DeleteWorkspace("ws-1")
	require.NoError(t, err)

	_, err = store.GetWorkspace("ws-1")
	assert.Error(t, err)
}

func TestMockBackend_AllocatePort(t *testing.T) {
	backend := mocks.NewMockBackend()

	port1, err := backend.AllocatePort()
	require.NoError(t, err)
	assert.Equal(t, int32(32800), port1)

	port2, err := backend.AllocatePort()
	require.NoError(t, err)
	assert.Equal(t, int32(32801), port2)

	err = backend.ReleasePort(port1)
	require.NoError(t, err)

	port3, err := backend.AllocatePort()
	require.NoError(t, err)
	assert.Equal(t, int32(32802), port3)
}

func TestCheckpointIntegration(t *testing.T) {
	cpDir := t.TempDir()
	store, err := checkpoint.NewFileCheckpointStore(cpDir)
	require.NoError(t, err)

	cp := &checkpoint.Checkpoint{
		ID:          "cp-1",
		WorkspaceID: "ws-1",
		Name:        "test-checkpoint",
		CreatedAt:   time.Now(),
		ImageName:   "test-image",
		Size:        1024,
	}

	err = store.SaveCheckpoint(cp)
	require.NoError(t, err)

	checkpoints, err := store.ListCheckpoints("ws-1")
	require.NoError(t, err)
	assert.Len(t, checkpoints, 1)

	err = store.DeleteCheckpoint("ws-1", "cp-1")
	require.NoError(t, err)

	checkpoints, err = store.ListCheckpoints("ws-1")
	require.NoError(t, err)
	assert.Len(t, checkpoints, 0)
}

func TestMockBackend_SyncStatus(t *testing.T) {
	backend := mocks.NewMockBackend()

	status, err := backend.GetSyncStatus(context.Background(), "test-ws")
	require.NoError(t, err)
	assert.Equal(t, "connected", status.State)
}

func TestMockBackend_ResourceStats(t *testing.T) {
	backend := mocks.NewMockBackend()

	stats, err := backend.GetResourceStats(context.Background(), "test-ws")
	require.NoError(t, err)
	assert.Equal(t, "test-ws", stats.WorkspaceID)
}

func TestMockBackend_Exec(t *testing.T) {
	backend := mocks.NewMockBackend()
	backend.ExecResult = "test output"

	createReq := &wsTypes.CreateWorkspaceRequest{
		ID:   "test-ws",
		Name: "test-ws",
	}
	_, err := backend.CreateWorkspace(context.Background(), createReq)
	require.NoError(t, err)

	output, err := backend.Exec(context.Background(), "test-ws", []string{"echo", "hello"})
	require.NoError(t, err)
	assert.Equal(t, "test output", output)
}

func TestMockStateStore_NotFound(t *testing.T) {
	store := mocks.NewMockStateStore()

	_, err := store.GetWorkspace("nonexistent")
	assert.Error(t, err)

	err = store.DeleteWorkspace("nonexistent")
	assert.Error(t, err)
}
