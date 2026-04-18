package firecracker

import (
	"context"
	"errors"

	domainruntime "github.com/inizio/nexus/packages/nexus/internal/domain/runtime"
	"github.com/inizio/nexus/packages/nexus/internal/domain/workspace"
)

// Adapter wraps FCDriver to satisfy domain/runtime.Driver.
type Adapter struct {
	d *FCDriver
}

// NewAdapter returns an Adapter that wraps the given FCDriver.
func NewAdapter(d *FCDriver) *Adapter {
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

func (a *Adapter) Snapshot(ctx context.Context, ws *workspace.Workspace) (*domainruntime.Snapshot, error) {
	snapshotID, err := a.d.CheckpointFork(ctx, ws.ID, ws.ID+"-snap")
	if err != nil {
		return nil, err
	}
	return &domainruntime.Snapshot{
		ID:          snapshotID,
		WorkspaceID: ws.ID,
	}, nil
}

func (a *Adapter) Restore(ctx context.Context, ws *workspace.Workspace, snap *domainruntime.Snapshot) error {
	if snap == nil {
		return errors.New("snapshot is required for restore")
	}
	req := CreateRequest{
		WorkspaceID: ws.ID,
		ProjectRoot: ws.Repo,
		Options:     map[string]string{"lineage_snapshot_id": snap.ID},
	}
	return a.d.Create(ctx, req)
}

func (a *Adapter) Fork(ctx context.Context, parent *workspace.Workspace, child *workspace.Workspace) error {
	return a.d.Fork(ctx, parent.ID, child.ID)
}

func (a *Adapter) Destroy(ctx context.Context, ws *workspace.Workspace) error {
	return a.d.Destroy(ctx, ws.ID)
}
