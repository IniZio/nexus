package docker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_NewProvider(t *testing.T) {
	t.Skip("Requires Docker running")

	provider, err := NewProvider()
	require.NoError(t, err)
	require.NotNil(t, provider)
	defer provider.Close()
}

func TestProvider_CreateWorkspace(t *testing.T) {
	t.Skip("Requires Docker running")

	ctx := context.Background()
	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	worktreePath := "/tmp/test-workspace"
	err = provider.Create(ctx, "test-workspace", worktreePath)
	require.NoError(t, err)

	exists, err := provider.ContainerExists(ctx, "test-workspace")
	require.NoError(t, err)
	assert.True(t, exists)

	err = provider.Destroy(ctx, "test-workspace")
	require.NoError(t, err)
}

func TestProvider_StartStop(t *testing.T) {
	t.Skip("Requires Docker running")

	ctx := context.Background()
	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	worktreePath := "/tmp/test-workspace-start-stop"
	err = provider.Create(ctx, "test-workspace-start-stop", worktreePath)
	require.NoError(t, err)
	defer provider.Destroy(ctx, "test-workspace-start-stop")

	err = provider.Start(ctx, "test-workspace-start-stop")
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	err = provider.Stop(ctx, "test-workspace-start-stop")
	require.NoError(t, err)
}

func TestProvider_Exec(t *testing.T) {
	t.Skip("Requires Docker running")

	ctx := context.Background()
	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	worktreePath := "/tmp/test-workspace-exec"
	err = provider.Create(ctx, "test-workspace-exec", worktreePath)
	require.NoError(t, err)
	defer provider.Destroy(ctx, "test-workspace-exec")

	err = provider.Start(ctx, "test-workspace-exec")
	require.NoError(t, err)
	defer provider.Stop(ctx, "test-workspace-exec")

	time.Sleep(2 * time.Second)

	err = provider.Exec(ctx, "test-workspace-exec", []string{"echo", "hello"})
	require.NoError(t, err)
}

func TestProvider_List(t *testing.T) {
	t.Skip("Requires Docker running")

	ctx := context.Background()
	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	workspaces, err := provider.List(ctx)
	require.NoError(t, err)
	assert.NotNil(t, workspaces)
}

func TestProvider_Close(t *testing.T) {
	t.Skip("Requires Docker running")

	provider, err := NewProvider()
	require.NoError(t, err)

	err = provider.Close()
	require.NoError(t, err)
}

func TestProvider_PortAllocation(t *testing.T) {
	t.Skip("Requires Docker running")

	ctx := context.Background()
	provider, err := NewProvider()
	require.NoError(t, err)
	defer provider.Close()

	worktreePath := "/tmp/test-workspace-port"
	err = provider.Create(ctx, "test-workspace-port", worktreePath)
	require.NoError(t, err)
	defer provider.Destroy(ctx, "test-workspace-port")

	workspaces, err := provider.List(ctx)
	require.NoError(t, err)

	var found bool
	for _, ws := range workspaces {
		if ws.Name == "test-workspace-port" {
			found = true
			assert.NotEmpty(t, ws.Port)
			break
		}
	}
	assert.True(t, found, "Workspace should be in list")
}
