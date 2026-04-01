package firecracker

import (
	"context"
	"errors"
	goRuntime "runtime"
	"sync"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

type CommandRunner interface {
	Run(ctx context.Context, dir string, cmd string, args ...string) error
}

type Driver struct {
	runner       CommandRunner
	projectRoots map[string]string
	hostOS       string
	bridge       *LimaBridge
	mu           sync.RWMutex
}

type Option func(*Driver)

func WithHostOS(hostOS string) Option {
	return func(d *Driver) {
		d.hostOS = hostOS
	}
}

func WithLimaInstance(instance string) Option {
	return func(d *Driver) {
		d.bridge = NewLimaBridge(instance)
	}
}

func NewDriver(runner CommandRunner, opts ...Option) *Driver {
	d := &Driver{
		runner:       runner,
		projectRoots: make(map[string]string),
		hostOS:       goRuntime.GOOS,
		bridge:       NewLimaBridge("nexus-firecracker"),
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
	d.mu.Lock()
	d.projectRoots[req.WorkspaceID] = req.ProjectRoot
	d.mu.Unlock()
	args := []string{"create", "--id", req.WorkspaceID}
	if req.Options != nil {
		if memMiB, ok := req.Options["mem_mib"]; ok && memMiB != "" {
			args = append(args, "--mem-mib", memMiB)
		}
	}
	args = append(args, "--balloon", "off")
	return d.runVMCommand(ctx, req.ProjectRoot, args...)
}

func (d *Driver) Start(ctx context.Context, workspaceID string) error {
	return d.runWorkspaceCommand(ctx, workspaceID, "start")
}

func (d *Driver) Stop(ctx context.Context, workspaceID string) error {
	return d.runWorkspaceCommand(ctx, workspaceID, "stop")
}

func (d *Driver) Restore(ctx context.Context, workspaceID string) error {
	return d.runWorkspaceCommand(ctx, workspaceID, "restore")
}

func (d *Driver) Pause(ctx context.Context, workspaceID string) error {
	return d.runWorkspaceCommand(ctx, workspaceID, "pause")
}

func (d *Driver) Resume(ctx context.Context, workspaceID string) error {
	return d.runWorkspaceCommand(ctx, workspaceID, "resume")
}

func (d *Driver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	dir := d.workspaceDir(workspaceID)
	if dir == "" {
		return errors.New("workspace not created or project root unknown")
	}

	d.mu.Lock()
	d.projectRoots[childWorkspaceID] = dir
	d.mu.Unlock()

	return d.runVMCommand(ctx, dir, "fork", "--id", workspaceID, "--child-id", childWorkspaceID)
}

func (d *Driver) Destroy(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	dir, ok := d.projectRoots[workspaceID]
	if ok {
		delete(d.projectRoots, workspaceID)
	}
	d.mu.Unlock()
	if !ok {
		return nil
	}
	return d.runVMCommand(ctx, dir, "destroy", "--id", workspaceID)
}

func (d *Driver) runWorkspaceCommand(ctx context.Context, workspaceID string, action string) error {
	dir := d.workspaceDir(workspaceID)
	if dir == "" {
		return errors.New("workspace not created or project root unknown")
	}
	return d.runVMCommand(ctx, dir, action, "--id", workspaceID)
}

func (d *Driver) runVMCommand(ctx context.Context, dir string, args ...string) error {
	cmd := "vmctl-firecracker"
	cmdArgs := args
	if d.hostOS == "darwin" {
		cmd, cmdArgs = d.bridge.Wrap(cmd, args...)
	}
	return d.runner.Run(ctx, dir, cmd, cmdArgs...)
}
