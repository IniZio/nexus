package process

import (
	"context"
	"fmt"
	"sync"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

// Driver implements runtime.Driver for host process sandbox execution.
type Driver struct {
	mu         sync.RWMutex
	workspaces map[string]*workspaceState
}

type workspaceState struct {
	id        string
	projectID string
	state     string
}

var _ runtime.Driver = (*Driver)(nil)

func NewDriver() *Driver {
	return &Driver{
		workspaces: make(map[string]*workspaceState),
	}
}

func (d *Driver) Backend() string {
	return "process"
}

func (d *Driver) Create(_ context.Context, req runtime.CreateRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workspaces[req.WorkspaceID]; exists {
		return fmt.Errorf("workspace %s already exists", req.WorkspaceID)
	}

	d.workspaces[req.WorkspaceID] = &workspaceState{
		id:        req.WorkspaceID,
		projectID: req.ProjectRoot,
		state:     "created",
	}
	return nil
}

func (d *Driver) Start(_ context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	ws.state = "running"
	return nil
}

func (d *Driver) Stop(_ context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	ws.state = "stopped"
	return nil
}

func (d *Driver) Restore(_ context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	ws.state = "running"
	return nil
}

func (d *Driver) Pause(_ context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.workspaces[workspaceID]; !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	return nil
}

func (d *Driver) Resume(_ context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.workspaces[workspaceID]; !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	return nil
}

func (d *Driver) Fork(_ context.Context, workspaceID, childWorkspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	parent, exists := d.workspaces[workspaceID]
	if !exists {
		return fmt.Errorf("parent workspace %s not found", workspaceID)
	}
	if _, exists := d.workspaces[childWorkspaceID]; exists {
		return fmt.Errorf("child workspace %s already exists", childWorkspaceID)
	}

	d.workspaces[childWorkspaceID] = &workspaceState{
		id:        childWorkspaceID,
		projectID: parent.projectID,
		state:     "created",
	}
	return nil
}

func (d *Driver) Destroy(_ context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.workspaces[workspaceID]; !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	delete(d.workspaces, workspaceID)
	return nil
}
