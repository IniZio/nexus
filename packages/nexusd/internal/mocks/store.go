package mocks

import (
	"errors"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

type MockStateStore struct {
	Workspaces map[string]*types.Workspace
	GetErr     error
	SaveErr    error
	DeleteErr  error
	ListErr    error
}

func NewMockStateStore() *MockStateStore {
	return &MockStateStore{
		Workspaces: make(map[string]*types.Workspace),
	}
}

func (m *MockStateStore) GetWorkspace(id string) (*types.Workspace, error) {
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	ws, ok := m.Workspaces[id]
	if !ok {
		return nil, errors.New("workspace not found")
	}
	return ws, nil
}

func (m *MockStateStore) SaveWorkspace(ws *types.Workspace) error {
	if m.SaveErr != nil {
		return m.SaveErr
	}
	if ws == nil {
		return errors.New("workspace is nil")
	}
	m.Workspaces[ws.ID] = ws
	return nil
}

func (m *MockStateStore) ListWorkspaces() ([]*types.Workspace, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	result := make([]*types.Workspace, 0, len(m.Workspaces))
	for _, ws := range m.Workspaces {
		result = append(result, ws)
	}
	return result, nil
}

func (m *MockStateStore) DeleteWorkspace(id string) error {
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	if _, ok := m.Workspaces[id]; !ok {
		return errors.New("workspace not found")
	}
	delete(m.Workspaces, id)
	return nil
}

func (m *MockStateStore) WorkspaceExists(id string) bool {
	_, ok := m.Workspaces[id]
	return ok
}

func (m *MockStateStore) BaseDir() string {
	return "/tmp/mock"
}
