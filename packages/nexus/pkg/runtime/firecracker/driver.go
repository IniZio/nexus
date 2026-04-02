package firecracker

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

var _ runtime.Driver = (*Driver)(nil)

type CommandRunner interface {
	Run(ctx context.Context, dir string, cmd string, args ...string) error
}

// ManagerInterface defines the interface for VM lifecycle management
type ManagerInterface interface {
	Spawn(ctx context.Context, spec SpawnSpec) (*Instance, error)
	Stop(ctx context.Context, workspaceID string) error
	Get(workspaceID string) (*Instance, error)
}

type Driver struct {
	runner       CommandRunner
	manager      ManagerInterface
	projectRoots map[string]string
	agents       map[string]*AgentClient
	mu           sync.RWMutex
}

type Option func(*Driver)

func WithManager(manager ManagerInterface) Option {
	return func(d *Driver) {
		d.manager = manager
	}
}

func NewDriver(runner CommandRunner, opts ...Option) *Driver {
	d := &Driver{
		runner:       runner,
		projectRoots: make(map[string]string),
		agents:       make(map[string]*AgentClient),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

func (d *Driver) Backend() string {
	return "firecracker"
}

func (d *Driver) workspaceDir(workspaceID string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if dir, ok := d.projectRoots[workspaceID]; ok {
		return dir
	}
	return ""
}

func (d *Driver) Create(ctx context.Context, req runtime.CreateRequest) error {
	if req.ProjectRoot == "" {
		return errors.New("project root is required")
	}

	if d.manager == nil {
		return errors.New("manager is required for firecracker driver")
	}

	memMiB := 1024
	if req.Options != nil {
		if memStr, ok := req.Options["mem_mib"]; ok && memStr != "" {
			if val, err := strconv.Atoi(memStr); err == nil {
				memMiB = val
			}
		}
	}

	spec := SpawnSpec{
		WorkspaceID: req.WorkspaceID,
		ProjectRoot: req.ProjectRoot,
		MemoryMiB:   memMiB,
		VCPUs:       1,
	}

	inst, err := d.manager.Spawn(ctx, spec)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.projectRoots[req.WorkspaceID] = req.ProjectRoot
	d.mu.Unlock()

	// TODO: Connect to agent via vsock when AgentClient dial is implemented
	_ = inst

	return nil
}

func (d *Driver) Start(ctx context.Context, workspaceID string) error {
	// Native firecracker VMs start immediately after Spawn
	// This is a no-op for the native implementation
	return nil
}

func (d *Driver) Stop(ctx context.Context, workspaceID string) error {
	if d.manager == nil {
		return errors.New("manager is required for firecracker driver")
	}

	d.mu.Lock()
	delete(d.agents, workspaceID)
	d.mu.Unlock()

	return d.manager.Stop(ctx, workspaceID)
}

func (d *Driver) Restore(ctx context.Context, workspaceID string) error {
	// Native firecracker doesn't support restore in this cutover
	return errors.New("restore not supported in native firecracker driver")
}

func (d *Driver) Pause(ctx context.Context, workspaceID string) error {
	// Native firecracker doesn't support pause in this cutover
	return errors.New("pause not supported in native firecracker driver")
}

func (d *Driver) Resume(ctx context.Context, workspaceID string) error {
	// Native firecracker doesn't support resume in this cutover
	return errors.New("resume not supported in native firecracker driver")
}

func (d *Driver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	// Native firecracker doesn't support fork in this cutover
	return errors.New("fork not supported in native firecracker driver")
}

func (d *Driver) Destroy(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	delete(d.projectRoots, workspaceID)
	delete(d.agents, workspaceID)
	d.mu.Unlock()

	// Stop the VM if manager is available
	if d.manager != nil {
		// Ignore error - workspace may already be stopped
		_ = d.manager.Stop(ctx, workspaceID)
	}

	return nil
}
