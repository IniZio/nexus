package firecracker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/secrets/server"
	"github.com/inizio/nexus/packages/nexus/pkg/secrets/vending"
	"github.com/mdlayher/vsock"
)

var _ runtime.Driver = (*Driver)(nil)
var _ runtime.ForkSnapshotter = (*Driver)(nil)

type CommandRunner interface {
	Run(ctx context.Context, dir string, cmd string, args ...string) error
}

// ManagerInterface defines the interface for VM lifecycle management
type ManagerInterface interface {
	Spawn(ctx context.Context, spec SpawnSpec) (*Instance, error)
	Stop(ctx context.Context, workspaceID string) error
	Get(workspaceID string) (*Instance, error)
	GrowWorkspace(ctx context.Context, workspaceID string, newSizeBytes int64) error
}

type Driver struct {
	runner       CommandRunner
	manager      ManagerInterface
	projectRoots map[string]string
	agents       map[string]*AgentClient
	mu           sync.RWMutex
}

type forkSnapshotManager interface {
	CheckpointForkImage(workspaceID string, childWorkspaceID string) (string, error)
}

func (d *Driver) AgentConn(ctx context.Context, workspaceID string) (net.Conn, error) {
	if d.manager == nil {
		return nil, errors.New("manager is required for firecracker driver")
	}

	inst, err := d.manager.Get(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("workspace instance lookup failed: %w", err)
	}

	if inst == nil || inst.CID == 0 {
		return nil, errors.New("workspace instance has no guest CID")
	}

	conn, err := vsock.Dial(inst.CID, DefaultAgentVSockPort, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock dial failed: %w", err)
	}

	return conn, nil
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

func (d *Driver) GuestWorkdir(workspaceID string) string {
	_ = workspaceID
	return "/workspace"
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

	memMiB := parsePositiveIntOption(req.Options, "mem_mib", 1024)
	vcpus := parsePositiveIntOption(req.Options, "vcpus", 1)
	if vcpus <= 0 {
		vcpus = parsePositiveIntOption(req.Options, "vcpu_count", 1)
	}

	spec := SpawnSpec{
		WorkspaceID: req.WorkspaceID,
		ProjectRoot: req.ProjectRoot,
		MemoryMiB:   memMiB,
		VCPUs:       vcpus,
	}
	if req.Options != nil {
		spec.SnapshotID = strings.TrimSpace(req.Options["lineage_snapshot_id"])
	}

	inst, err := d.manager.Spawn(ctx, spec)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.projectRoots[req.WorkspaceID] = req.ProjectRoot
	d.mu.Unlock()

	// Start singleton host vending server (shared across all workspaces)
	hostServer, err := server.GetHostServer(VendingVSockPort)
	if err != nil {
		log.Printf("[secrets] Warning: failed to initialize vending: %v", err)
	} else if !hostServer.IsRunning() {
		if startErr := hostServer.Start(); startErr != nil {
			log.Printf("[secrets] Warning: failed to start vending server: %v", startErr)
		} else {
			svc, _ := vending.GetHostVendingService()
			providers := svc.ListProviders("")
			log.Printf("[secrets] Started host vending server on port %d with providers: %v", VendingVSockPort, providers)
		}
	}

	// Pass vending URL to guest via config bundle (handled by runtime)

	// TODO: Connect to agent via vsock when AgentClient dial is implemented
	_ = inst

	return nil
}

func parsePositiveIntOption(options map[string]string, key string, fallback int) int {
	if options == nil {
		return fallback
	}
	raw := strings.TrimSpace(options[key])
	if raw == "" {
		return fallback
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val <= 0 {
		return fallback
	}
	return val
}

func (d *Driver) GrowWorkspace(ctx context.Context, workspaceID string, newSizeBytes int64) error {
	if d.manager == nil {
		return errors.New("manager is required for firecracker driver")
	}

	if err := d.manager.GrowWorkspace(ctx, workspaceID, newSizeBytes); err != nil {
		return fmt.Errorf("host-side grow failed: %w", err)
	}

	conn, err := d.AgentConn(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("agent connect for disk.grow: %w", err)
	}
	defer conn.Close()

	client := NewAgentClient(conn)
	req := ExecRequest{
		ID:      fmt.Sprintf("disk-grow-%d", time.Now().UnixNano()),
		Type:    "disk.grow",
		Command: "",
	}
	result, err := client.Exec(ctx, req)
	if err != nil {
		return fmt.Errorf("disk.grow exec failed: %w", err)
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return fmt.Errorf("resize2fs failed: %s", detail)
	}

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

	// Cleanup workspace tokens from singleton vending service
	if svc, err := vending.GetHostVendingService(); err == nil {
		svc.CleanupWorkspace(workspaceID)
	}

	return d.manager.Stop(ctx, workspaceID)
}

func (d *Driver) Restore(ctx context.Context, workspaceID string) error {
	return d.Resume(ctx, workspaceID)
}

func (d *Driver) Pause(ctx context.Context, workspaceID string) error {
	return d.Stop(ctx, workspaceID)
}

func (d *Driver) Resume(ctx context.Context, workspaceID string) error {
	if d.manager == nil {
		return errors.New("manager is required for firecracker driver")
	}

	d.mu.RLock()
	projectRoot := strings.TrimSpace(d.projectRoots[workspaceID])
	d.mu.RUnlock()
	if projectRoot == "" {
		return fmt.Errorf("workspace %s has no recorded project root", workspaceID)
	}

	err := d.Create(ctx, runtime.CreateRequest{
		WorkspaceID: workspaceID,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

func (d *Driver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	parentProjectRoot := strings.TrimSpace(d.projectRoots[workspaceID])
	if parentProjectRoot == "" {
		return fmt.Errorf("parent workspace %s not found", workspaceID)
	}
	if _, exists := d.projectRoots[childWorkspaceID]; exists {
		return fmt.Errorf("workspace %s already exists", childWorkspaceID)
	}
	d.projectRoots[childWorkspaceID] = parentProjectRoot
	return nil
}

func (d *Driver) CheckpointFork(ctx context.Context, workspaceID, childWorkspaceID string) (string, error) {
	_ = ctx
	if d.manager == nil {
		return "", errors.New("manager is required for firecracker driver")
	}
	if snapshotter, ok := d.manager.(forkSnapshotManager); ok {
		return snapshotter.CheckpointForkImage(workspaceID, childWorkspaceID)
	}
	return fmt.Sprintf("fc-fork-%s-%s-%d", workspaceID, childWorkspaceID, time.Now().UTC().UnixNano()), nil
}

func (d *Driver) Destroy(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	delete(d.projectRoots, workspaceID)
	delete(d.agents, workspaceID)
	d.mu.Unlock()

	// Cleanup workspace tokens from singleton vending service
	if svc, err := vending.GetHostVendingService(); err == nil {
		svc.CleanupWorkspace(workspaceID)
	}

	// Stop the VM if manager is available
	if d.manager != nil {
		// Ignore error - workspace may already be stopped
		_ = d.manager.Stop(ctx, workspaceID)
	}

	return nil
}
