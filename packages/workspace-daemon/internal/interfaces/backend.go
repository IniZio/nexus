package interfaces

import (
	"context"
	"io"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/types"
)

type Backend interface {
	CreateWorkspace(ctx context.Context, req *types.CreateWorkspaceRequest) (*types.Workspace, error)
	CreateWorkspaceWithBridge(ctx context.Context, req *types.CreateWorkspaceRequest, bridgeSocket string) (*types.Workspace, error)
	StartWorkspace(ctx context.Context, id string) (*types.Operation, error)
	StopWorkspace(ctx context.Context, id string, timeout int32) (*types.Operation, error)
	DeleteWorkspace(ctx context.Context, id string) error
	GetWorkspaceStatus(ctx context.Context, id string) (types.WorkspaceStatus, error)
	GetStatus(ctx context.Context, id string) (types.WorkspaceStatus, error)
	Exec(ctx context.Context, id string, cmd []string) (string, error)
	ExecViaSSH(ctx context.Context, id string, cmd []string) (string, error)
	GetLogs(ctx context.Context, id string, tail int) (string, error)
	GetResourceStats(ctx context.Context, id string) (*types.ResourceStats, error)
	GetSSHPort(ctx context.Context, id string) (int32, error)
	Shell(ctx context.Context, id string) error
	CopyFiles(ctx context.Context, id string, src io.Reader, dst string) error
	PauseSync(ctx context.Context, workspaceID string) error
	ResumeSync(ctx context.Context, workspaceID string) error
	FlushSync(ctx context.Context, workspaceID string) error
	GetSyncStatus(ctx context.Context, workspaceID string) (*types.SyncStatus, error)
	AllocatePort() (int32, error)
	ReleasePort(port int32) error
	AddPortBinding(ctx context.Context, workspaceID string, containerPort, hostPort int32) error
	CommitContainer(ctx context.Context, workspaceID string, req *types.CommitContainerRequest) error
	RemoveImage(ctx context.Context, imageName string) error
	RestoreFromImage(ctx context.Context, workspaceID, imageName string) error
}

type DockerSpecificBackend interface {
	Backend
	ListContainersByLabel(ctx context.Context, label string) ([]interface{}, error)
	RemoveContainer(ctx context.Context, containerID string) error
}
