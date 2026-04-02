package firecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// initialCID is the starting CID for guest VMs.
const initialCID uint32 = 1000

// SpawnSpec defines the configuration for spawning a new Firecracker VM.
type SpawnSpec struct {
	WorkspaceID string
	ProjectRoot string
	MemoryMiB   int
	VCPUs       int
}

// Instance represents a running Firecracker VM instance.
type Instance struct {
	WorkspaceID string
	WorkDir     string
	APISocket   string
	VSockPath   string
	SerialLog   string
	CID         uint32
	Process     *os.Process
}

// ManagerConfig holds configuration for the Firecracker manager.
type ManagerConfig struct {
	FirecrackerBin string
	KernelPath     string
	RootFSPath     string
	WorkDirRoot    string
}

// APIClientFactory creates API clients for instances.
type APIClientFactory func(sockPath string) apiClientInterface

// apiClientInterface defines the methods we need from the API client.
type apiClientInterface interface {
	put(ctx context.Context, path string, body any) error
}

// Manager handles Firecracker VM lifecycle operations.
type Manager struct {
	config           ManagerConfig
	instances        map[string]*Instance
	mu               sync.RWMutex
	nextCID          uint32
	apiClientFactory APIClientFactory
}

// NewManager creates a new Firecracker manager with the given configuration.
func NewManager(cfg ManagerConfig) *Manager {
	return newManager(cfg)
}

// newManager creates a new Firecracker manager with the given configuration.
func newManager(cfg ManagerConfig) *Manager {
	return &Manager{
		config:           cfg,
		instances:        make(map[string]*Instance),
		nextCID:          initialCID,
		apiClientFactory: defaultAPIClientFactory,
	}
}

func defaultAPIClientFactory(sockPath string) apiClientInterface {
	return newAPIClient(sockPath)
}

// cleanup removes the workdir and cleans up a partially started instance.
func (m *Manager) cleanup(workDir string, proc *os.Process) {
	if proc != nil {
		proc.Kill()
		proc.Wait()
	}
	os.RemoveAll(workDir)
}

// Spawn creates and starts a new Firecracker VM instance.
func (m *Manager) Spawn(ctx context.Context, spec SpawnSpec) (*Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.instances[spec.WorkspaceID]; exists {
		return nil, fmt.Errorf("workspace already exists: %s", spec.WorkspaceID)
	}

	workDir := filepath.Join(m.config.WorkDirRoot, spec.WorkspaceID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create workdir: %w", err)
	}

	apiSocket := filepath.Join(workDir, "firecracker.sock")
	vsockPath := filepath.Join(workDir, "vsock.sock")
	serialLog := filepath.Join(workDir, "firecracker.log")

	cid := m.nextCID
	m.nextCID++

	args := []string{
		"--api-sock", apiSocket,
		"--id", spec.WorkspaceID,
	}

	cmd := exec.CommandContext(ctx, m.config.FirecrackerBin, args...)
	cmd.Dir = workDir
	logFile, err := os.OpenFile(serialLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to create firecracker log file: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}
	_ = logFile.Close()

	if err := m.waitForAPISocket(ctx, apiSocket); err != nil {
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to wait for API socket: %w", err)
	}

	client := m.apiClientFactory(apiSocket)

	machineConfig := map[string]any{
		"vcpu_count":        spec.VCPUs,
		"mem_size_mib":      spec.MemoryMiB,
		"smt":               false,
		"track_dirty_pages": false,
	}
	if err := client.put(ctx, "/machine-config", machineConfig); err != nil {
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure machine: %w", err)
	}

	bootSource := map[string]any{
		"kernel_image_path": m.config.KernelPath,
		"boot_args":         defaultFirecrackerBootArgs(),
	}
	if err := client.put(ctx, "/boot-source", bootSource); err != nil {
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure boot source: %w", err)
	}

	driveConfig := map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   m.config.RootFSPath,
		"is_root_device": true,
		"is_read_only":   false,
	}
	if err := client.put(ctx, "/drives/rootfs", driveConfig); err != nil {
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure drive: %w", err)
	}

	vsockConfig := map[string]any{
		"vsock_id":  "agent",
		"guest_cid": cid,
		"uds_path":  vsockPath,
	}
	if err := client.put(ctx, "/vsock", vsockConfig); err != nil {
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure vsock: %w", err)
	}

	action := map[string]any{
		"action_type": "InstanceStart",
	}
	if err := client.put(ctx, "/actions", action); err != nil {
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to start instance: %w", err)
	}

	inst := &Instance{
		WorkspaceID: spec.WorkspaceID,
		WorkDir:     workDir,
		APISocket:   apiSocket,
		VSockPath:   vsockPath,
		SerialLog:   serialLog,
		CID:         cid,
		Process:     cmd.Process,
	}

	m.instances[spec.WorkspaceID] = inst
	return inst, nil
}

func defaultFirecrackerBootArgs() string {
	if raw := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_BOOT_ARGS")); raw != "" {
		return raw
	}
	return "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw"
}

// waitForAPISocket polls for the API socket to exist with the given context.
func (m *Manager) waitForAPISocket(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return nil
			}
		}
	}
}

// Stop terminates a running VM instance and cleans up resources.
func (m *Manager) Stop(ctx context.Context, workspaceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, exists := m.instances[workspaceID]
	if !exists {
		return fmt.Errorf("workspace not found: %s", workspaceID)
	}

	client := m.apiClientFactory(inst.APISocket)
	action := map[string]any{
		"action_type": "SendCtrlAltDel",
	}

	if err := client.put(ctx, "/actions", action); err != nil {
		if inst.Process != nil {
			inst.Process.Kill()
		}
	}

	if inst.Process != nil {
		inst.Process.Wait()
	}

	os.RemoveAll(inst.WorkDir)
	delete(m.instances, workspaceID)

	return nil
}

// Get retrieves an instance by workspace ID.
func (m *Manager) Get(workspaceID string) (*Instance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inst, exists := m.instances[workspaceID]
	if !exists {
		return nil, fmt.Errorf("workspace not found: %s", workspaceID)
	}

	return inst, nil
}
