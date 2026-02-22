package interfaces

import "github.com/nexus/nexus/packages/nexusd/internal/types"

type StateStore interface {
	GetWorkspace(id string) (*types.Workspace, error)
	SaveWorkspace(ws *types.Workspace) error
	ListWorkspaces() ([]*types.Workspace, error)
	DeleteWorkspace(id string) error
	WorkspaceExists(id string) bool
	BaseDir() string
}
