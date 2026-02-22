package mocks

import (
	"context"
	"errors"
	"io"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/types"
)

type MockBackend struct {
	Workspaces   map[string]*types.Workspace
	CreateErr    error
	StartErr     error
	StopErr      error
	DeleteErr    error
	StatusErr    error
	ExecErr      error
	ExecResult   string
	NextPort     int32
	AllocatedIPs map[int32]bool
}

func NewMockBackend() *MockBackend {
	return &MockBackend{
		Workspaces:   make(map[string]*types.Workspace),
		NextPort:     32800,
		AllocatedIPs: make(map[int32]bool),
	}
}

func (m *MockBackend) CreateWorkspace(ctx context.Context, req *types.CreateWorkspaceRequest) (*types.Workspace, error) {
	if m.CreateErr != nil {
		return nil, m.CreateErr
	}
	ws := &types.Workspace{
		ID:      req.ID,
		Name:    req.Name,
		Status:  types.StatusRunning,
		Backend: types.BackendDocker,
	}
	m.Workspaces[ws.ID] = ws
	return ws, nil
}

func (m *MockBackend) CreateWorkspaceWithBridge(ctx context.Context, req *types.CreateWorkspaceRequest, bridgeSocket string) (*types.Workspace, error) {
	return m.CreateWorkspace(ctx, req)
}

func (m *MockBackend) StartWorkspace(ctx context.Context, id string) (*types.Operation, error) {
	if m.StartErr != nil {
		return nil, m.StartErr
	}
	if _, ok := m.Workspaces[id]; !ok {
		return nil, errors.New("workspace not found")
	}
	m.Workspaces[id].Status = types.StatusRunning
	return &types.Operation{ID: "op-1", Status: "running"}, nil
}

func (m *MockBackend) StopWorkspace(ctx context.Context, id string, timeout int32) (*types.Operation, error) {
	if m.StopErr != nil {
		return nil, m.StopErr
	}
	if _, ok := m.Workspaces[id]; !ok {
		return nil, errors.New("workspace not found")
	}
	m.Workspaces[id].Status = types.StatusStopped
	return &types.Operation{ID: "op-1", Status: "stopped"}, nil
}

func (m *MockBackend) DeleteWorkspace(ctx context.Context, id string) error {
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	if _, ok := m.Workspaces[id]; !ok {
		return errors.New("workspace not found")
	}
	delete(m.Workspaces, id)
	return nil
}

func (m *MockBackend) GetStatus(ctx context.Context, id string) (types.WorkspaceStatus, error) {
	if m.StatusErr != nil {
		return types.StatusError, m.StatusErr
	}
	if ws, ok := m.Workspaces[id]; ok {
		return ws.Status, nil
	}
	return types.StatusStopped, nil
}

func (m *MockBackend) GetWorkspaceStatus(ctx context.Context, id string) (types.WorkspaceStatus, error) {
	return m.GetStatus(ctx, id)
}

func (m *MockBackend) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	if m.ExecErr != nil {
		return "", m.ExecErr
	}
	if _, ok := m.Workspaces[id]; !ok {
		return "", errors.New("workspace not found")
	}
	return m.ExecResult, nil
}

func (m *MockBackend) GetLogs(ctx context.Context, id string, tail int) (string, error) {
	return "mock logs", nil
}

func (m *MockBackend) ExecViaSSH(ctx context.Context, id string, cmd []string) (string, error) {
	return "mock ssh output", nil
}

func (m *MockBackend) GetResourceStats(ctx context.Context, id string) (*types.ResourceStats, error) {
	return &types.ResourceStats{WorkspaceID: id}, nil
}

func (m *MockBackend) GetSSHPort(ctx context.Context, id string) (int32, error) {
	return 32800, nil
}

func (m *MockBackend) Shell(ctx context.Context, id string) error {
	return nil
}

func (m *MockBackend) CopyFiles(ctx context.Context, id string, src io.Reader, dst string) error {
	return nil
}

func (m *MockBackend) PauseSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (m *MockBackend) ResumeSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (m *MockBackend) FlushSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (m *MockBackend) GetSyncStatus(ctx context.Context, workspaceID string) (*types.SyncStatus, error) {
	return &types.SyncStatus{State: "connected"}, nil
}

func (m *MockBackend) AllocatePort() (int32, error) {
	port := m.NextPort
	m.NextPort++
	m.AllocatedIPs[port] = true
	return port, nil
}

func (m *MockBackend) ReleasePort(port int32) error {
	delete(m.AllocatedIPs, port)
	return nil
}

func (m *MockBackend) CommitContainer(ctx context.Context, workspaceID string, req *types.CommitContainerRequest) error {
	return nil
}

func (m *MockBackend) RemoveImage(ctx context.Context, imageName string) error {
	return nil
}

func (m *MockBackend) RestoreFromImage(ctx context.Context, workspaceID, imageName string) error {
	return nil
}
