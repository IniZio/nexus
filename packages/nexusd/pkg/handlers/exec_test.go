package handlers

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/nexus/nexus/packages/nexusd/internal/interfaces"
	"github.com/nexus/nexus/packages/nexusd/internal/types"
	rpckit "github.com/nexus/nexus/packages/nexusd/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/nexusd/pkg/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockBackend struct {
	execResult string
	execErr    error
}

func (m *mockBackend) CreateWorkspace(ctx context.Context, req *types.CreateWorkspaceRequest) (*types.Workspace, error) {
	return nil, nil
}

func (m *mockBackend) CreateWorkspaceWithBridge(ctx context.Context, req *types.CreateWorkspaceRequest, bridgeSocket string) (*types.Workspace, error) {
	return nil, nil
}

func (m *mockBackend) StartWorkspace(ctx context.Context, id string) (*types.Operation, error) {
	return nil, nil
}

func (m *mockBackend) StopWorkspace(ctx context.Context, id string, timeout int32) (*types.Operation, error) {
	return nil, nil
}

func (m *mockBackend) DeleteWorkspace(ctx context.Context, id string) error {
	return nil
}

func (m *mockBackend) GetWorkspaceStatus(ctx context.Context, id string) (types.WorkspaceStatus, error) {
	return 0, nil
}

func (m *mockBackend) GetStatus(ctx context.Context, id string) (types.WorkspaceStatus, error) {
	return 0, nil
}

func (m *mockBackend) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return m.execResult, m.execErr
}

func (m *mockBackend) ExecViaSSH(ctx context.Context, id string, cmd []string) (string, error) {
	return m.execResult, m.execErr
}

func (m *mockBackend) GetLogs(ctx context.Context, id string, tail int) (string, error) {
	return "", nil
}

func (m *mockBackend) GetResourceStats(ctx context.Context, id string) (*types.ResourceStats, error) {
	return nil, nil
}

func (m *mockBackend) GetSSHPort(ctx context.Context, id string) (int32, error) {
	return 0, nil
}

func (m *mockBackend) Shell(ctx context.Context, id string) error {
	return nil
}

func (m *mockBackend) CopyFiles(ctx context.Context, id string, src io.Reader, dst string) error {
	return nil
}

func (m *mockBackend) PauseSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (m *mockBackend) ResumeSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (m *mockBackend) FlushSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (m *mockBackend) GetSyncStatus(ctx context.Context, workspaceID string) (*types.SyncStatus, error) {
	return nil, nil
}

func (m *mockBackend) AllocatePort() (int32, error) {
	return 0, nil
}

func (m *mockBackend) ReleasePort(port int32) error {
	return nil
}

func (m *mockBackend) AddPortBinding(ctx context.Context, workspaceID string, containerPort, hostPort int32) error {
	return nil
}

func (m *mockBackend) CommitContainer(ctx context.Context, workspaceID string, req *types.CommitContainerRequest) error {
	return nil
}

func (m *mockBackend) RemoveImage(ctx context.Context, imageName string) error {
	return nil
}

func (m *mockBackend) RestoreFromImage(ctx context.Context, workspaceID, imageName string) error {
	return nil
}

var _ interfaces.Backend = (*mockBackend)(nil)

func TestHandleExec_InvalidParams(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	result, rpcErr := HandleExec(context.Background(), []byte("invalid json"), ws, nil)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleExec_EmptyCommand(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"command": ""}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, nil)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleExec_Success(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"command": "echo", "args": ["hello"]}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, nil)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.Equal(t, "hello", result.Stdout)
	assert.Equal(t, 0, result.ExitCode)
}

func TestHandleExec_WithWorkDir(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"command": "pwd", "options": {"work_dir": "."}}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, nil)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
}

func TestHandleExec_WithEnv(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"command": "sh", "args": ["-c", "echo $TEST_VAR"], "options": {"env": ["TEST_VAR=testvalue"]}}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, nil)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.Equal(t, "testvalue", result.Stdout)
}

func TestHandleExec_WithTimeout(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"command": "sleep", "args": ["0.1"], "options": {"timeout": 5}}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, nil)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.Equal(t, 0, result.ExitCode)
}

func TestHandleExec_InvalidWorkDir(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"command": "echo", "args": ["hello"], "options": {"work_dir": "/etc"}}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, nil)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidPath.Code, rpcErr.Code)
}

func TestHandleExec_NonExistentCommand(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"command": "nonexistent-command-12345"}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, nil)
	assert.NotNil(t, result)
	assert.Nil(t, rpcErr)
}

func TestHandleExec_CommandWithArgs(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"command": "printf", "args": ["hello %s", "world"]}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, nil)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.Equal(t, "hello world", result.Stdout)
}

func TestHandleExec_WithBackend(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	backend := &mockBackend{
		execResult: "backend output",
		execErr:    nil,
	}

	params := `{"command": "echo", "args": ["test"]}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, backend)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.Equal(t, "backend output", result.Stdout)
	assert.Equal(t, 0, result.ExitCode)
}

func TestHandleExec_BackendError(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	backend := &mockBackend{
		execResult: "",
		execErr:    assert.AnError,
	}

	params := `{"command": "echo", "args": ["test"]}`
	result, rpcErr := HandleExec(context.Background(), []byte(params), ws, backend)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.NotEqual(t, 0, result.ExitCode)
	assert.Contains(t, result.Stderr, "exec in container failed")
}

func TestExecParams_JSONMarshaling(t *testing.T) {
	params := ExecParams{
		Command: "echo",
		Args:    []string{"hello"},
		Options: ExecOptions{
			Timeout: 30,
			WorkDir: "/tmp",
			Env:     []string{"KEY=value"},
		},
	}

	data, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"command":"echo"`)
	assert.Contains(t, string(data), `"timeout":30`)
}

func TestExecResult_JSONMarshaling(t *testing.T) {
	result := ExecResult{
		Stdout:   "output",
		Stderr:   "error",
		ExitCode: 0,
		Command:  "echo hello",
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"stdout":"output"`)
	assert.Contains(t, string(data), `"exit_code":0`)
}
