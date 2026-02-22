package server

import (
	"context"
	"testing"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/mocks"
	wsTypes "github.com/nexus/nexus/packages/workspace-daemon/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServerWithDeps(t *testing.T) {
	mockStore := mocks.NewMockStateStore()
	mockBackend := mocks.NewMockBackend()
	mockLifecycle := mocks.NewMockLifecycleManager()
	mockMutagen := mocks.NewMockMutagenClient()

	srv, err := NewServerWithDeps(
		8080,
		"/tmp/test-workspace",
		"test-token",
		mockStore,
		mockBackend,
		mockLifecycle,
		mockMutagen,
	)

	require.NoError(t, err)
	assert.NotNil(t, srv)
	assert.Equal(t, 8080, srv.port)
	assert.Equal(t, "test-token", srv.tokenSecret)
}

func TestServerWithMockBackend(t *testing.T) {
	mockStore := mocks.NewMockStateStore()
	mockBackend := mocks.NewMockBackend()
	mockBackend.Workspaces["test-ws"] = &wsTypes.Workspace{
		ID:     "test-ws",
		Name:   "test",
		Status: wsTypes.StatusRunning,
	}

	srv, err := NewServerWithDeps(
		8080,
		"/tmp/test-workspace",
		"test-token",
		mockStore,
		mockBackend,
		nil,
		nil,
	)

	require.NoError(t, err)
	assert.NotNil(t, srv)

	status, err := mockBackend.GetStatus(context.Background(), "test-ws")
	require.NoError(t, err)
	assert.Equal(t, wsTypes.StatusRunning, status)
}

func TestServerCreateAndListWorkspaces(t *testing.T) {
	mockStore := mocks.NewMockStateStore()
	mockBackend := mocks.NewMockBackend()

	_, err := NewServerWithDeps(
		8080,
		"/tmp/test-workspace",
		"test-token",
		mockStore,
		mockBackend,
		nil,
		nil,
	)

	require.NoError(t, err)

	err = mockStore.SaveWorkspace(&wsTypes.Workspace{
		ID:     "ws-1",
		Name:   "workspace-1",
		Status: wsTypes.StatusRunning,
	})
	require.NoError(t, err)

	err = mockStore.SaveWorkspace(&wsTypes.Workspace{
		ID:     "ws-2",
		Name:   "workspace-2",
		Status: wsTypes.StatusStopped,
	})
	require.NoError(t, err)

	workspaces, err := mockStore.ListWorkspaces()
	require.NoError(t, err)
	assert.Len(t, workspaces, 2)
}
