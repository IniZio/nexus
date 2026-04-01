package local

import (
	"context"
	"fmt"
	"sync"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

// Driver implements runtime.Driver for local execution backend.
// It manages workspace metadata without actual VM/container lifecycle.
type Driver struct {
	mu         sync.RWMutex
	workspaces map[string]*workspaceState
}

type workspaceState struct {
	id        string
	projectID string
	state     string
}

// Ensure Driver implements runtime.Driver
var _ runtime.Driver = (*Driver)(nil)

// NewDriver creates a new local runtime driver.
func NewDriver() *Driver {
	return &Driver{
		workspaces: make(map[string]*workspaceState),
	}
}

// Backend returns the backend identifier.
func (d *Driver) Backend() string {
	return "local"
}

// Create registers a new workspace in the local driver.
func (d *Driver) Create(ctx context.Context, req runtime.CreateRequest) error {
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

// Start transitions a workspace to running state.
func (d *Driver) Start(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	ws.state = "running"
	return nil
}

// Stop transitions a workspace to stopped state.
func (d *Driver) Stop(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	ws.state = "stopped"
	return nil
}

// Restore transitions a workspace to running state from stopped.
func (d *Driver) Restore(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	ws.state = "running"
	return nil
}

// Pause is a no-op for local driver (success).
func (d *Driver) Pause(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workspaces[workspaceID]; !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	// No-op: local workspaces don't support true pause/resume
	return nil
}

// Resume is a no-op for local driver (success).
func (d *Driver) Resume(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workspaces[workspaceID]; !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	// No-op: local workspaces don't support true pause/resume
	return nil
}

// Fork creates a child workspace linked to the parent.
func (d *Driver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	parent, exists := d.workspaces[workspaceID]
	if !exists {
		return fmt.Errorf("parent workspace %s not found", workspaceID)
	}

	if _, exists := d.workspaces[childWorkspaceID]; exists {
		return fmt.Errorf("child workspace %s already exists", childWorkspaceID)
	}

	// Child inherits parent's project ID
	d.workspaces[childWorkspaceID] = &workspaceState{
		id:        childWorkspaceID,
		projectID: parent.projectID,
		state:     "created",
	}

	return nil
}

// Destroy removes a workspace from the driver.
func (d *Driver) Destroy(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workspaces[workspaceID]; !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	delete(d.workspaces, workspaceID)
	return nil
}

// GetState returns the current state of a workspace (for testing/inspection).
func (d *Driver) GetState(workspaceID string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return "", false
	}
	return ws.state, true
}

// GetProjectID returns the project ID for a workspace (for testing/inspection).
func (d *Driver) GetProjectID(workspaceID string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return "", false
	}
	return ws.projectID, true
}