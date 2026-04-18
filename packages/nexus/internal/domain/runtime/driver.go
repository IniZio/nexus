package runtime

import (
	"context"

	"github.com/inizio/nexus/packages/nexus/internal/domain/workspace"
)

// Driver defines the interface for workspace runtime backends.
type Driver interface {
	Backend() string
	Create(ctx context.Context, req *CreateRequest) error
	Start(ctx context.Context, ws *workspace.Workspace) error
	Stop(ctx context.Context, ws *workspace.Workspace) error
	Pause(ctx context.Context, ws *workspace.Workspace) error
	Resume(ctx context.Context, ws *workspace.Workspace) error
	Snapshot(ctx context.Context, ws *workspace.Workspace) (*Snapshot, error)
	Restore(ctx context.Context, ws *workspace.Workspace, snap *Snapshot) error
	Fork(ctx context.Context, parent *workspace.Workspace, child *workspace.Workspace) error
	Destroy(ctx context.Context, ws *workspace.Workspace) error
}
