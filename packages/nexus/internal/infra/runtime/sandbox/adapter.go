package sandbox

import (
	"context"

	domainruntime "github.com/inizio/nexus/packages/nexus/internal/domain/runtime"
	"github.com/inizio/nexus/packages/nexus/internal/domain/workspace"
)

// Adapter wraps Driver to satisfy domain/runtime.Driver.
type Adapter struct {
	d *Driver
}

// NewAdapter returns an Adapter that wraps the given sandbox Driver.
func NewAdapter(d *Driver) *Adapter {
	return &Adapter{d: d}
}

var _ domainruntime.Driver = (*Adapter)(nil)

func (a *Adapter) Backend() string {
	return a.d.Backend()
}

func (a *Adapter) Create(ctx context.Context, req *domainruntime.CreateRequest) error {
	return a.d.Create(ctx, CreateRequest{
		WorkspaceID: req.WorkspaceID,
		ProjectRoot: req.ProjectRoot,
		Options:     req.Options,
	})
}

func (a *Adapter) Start(ctx context.Context, ws *workspace.Workspace) error {
	return a.d.Start(ctx, ws.ID)
}

func (a *Adapter) Stop(ctx context.Context, ws *workspace.Workspace) error {
	return a.d.Stop(ctx, ws.ID)
}

func (a *Adapter) Pause(ctx context.Context, ws *workspace.Workspace) error {
	return a.d.Pause(ctx, ws.ID)
}

func (a *Adapter) Resume(ctx context.Context, ws *workspace.Workspace) error {
	return a.d.Resume(ctx, ws.ID)
}

func (a *Adapter) Snapshot(_ context.Context, ws *workspace.Workspace) (*domainruntime.Snapshot, error) {
	return &domainruntime.Snapshot{
		ID:          ws.ID + "-snap",
		WorkspaceID: ws.ID,
	}, nil
}

func (a *Adapter) Restore(ctx context.Context, ws *workspace.Workspace, _ *domainruntime.Snapshot) error {
	return a.d.Restore(ctx, ws.ID)
}

func (a *Adapter) Fork(ctx context.Context, parent *workspace.Workspace, child *workspace.Workspace) error {
	return a.d.Fork(ctx, parent.ID, child.ID)
}

func (a *Adapter) Destroy(ctx context.Context, ws *workspace.Workspace) error {
	return a.d.Destroy(ctx, ws.ID)
}
