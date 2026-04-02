package firecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type SpawnSpec struct {
	WorkspaceID string
	ProjectRoot string
	MemoryMiB   int
	VCPUs       int
}

type Instance struct {
	WorkspaceID string
	WorkDir     string
	APISocket   string
	VSockPath   string
	CID         uint32
	Process     *os.Process
}

type ManagerConfig struct {
	FirecrackerBin string
	KernelPath     string
	RootFSPath     string
	WorkDirRoot    string
}

// FirecrackerProcess defines the interface for managing a firecracker process
type FirecrackerProcess interface {
	Kill() error
	Wait() (*os.ProcessState, error)
}

// APIClientFactory creates API clients for instances
type APIClientFactory func(sockPath string) apiClientInterface

// apiClientInterface defines the methods we need from the API client
type apiClientInterface interface {
	put(ctx context.Context, path string, body any) error
}

type Manager struct {
	config         ManagerConfig
	instances      map[string]*Instance
	mu             sync.RWMutex
	nextCID        uint32
	apiClientFactory APIClientFactory
}

func newManager(cfg ManagerConfig) *Manager {
	return &Manager{
		config:           cfg,
		instances:        make(map[string]*Instance),
		nextCID:          1000,
		apiClientFactory: defaultAPIClientFactory,
	}
}

func defaultAPIClientFactory(sockPath string) apiClientInterface {
	return newAPIClient(sockPath)
}

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
	
	cid := m.nextCID
	m.nextCID++
	
	args := []string{
		"--api-sock", apiSocket,
		"--id", spec.WorkspaceID,
	}
	
	cmd := exec.CommandContext(ctx, m.config.FirecrackerBin, args...)
	cmd.Dir = workDir
	
	if err := cmd.Start(); err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}
	
	if err := m.waitForAPISocket(ctx, apiSocket); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to wait for API socket: %w", err)
	}
	
	client := m.apiClientFactory(apiSocket)
	
	machineConfig := map[string]any{
		"vcpu_count":       spec.VCPUs,
		"mem_size_mib":     spec.MemoryMiB,
		"ht_enabled":       false,
		"track_dirty_pages": false,
	}
	if err := client.put(ctx, "/machine-config", machineConfig); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to configure machine: %w", err)
	}
	
	bootSource := map[string]any{
		"kernel_image_path": m.config.KernelPath,
		"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off",
	}
	if err := client.put(ctx, "/boot-source", bootSource); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to configure boot source: %w", err)
	}
	
	driveConfig := map[string]any{
		"drive_id":      "rootfs",
		"path_on_host":  m.config.RootFSPath,
		"is_root_device": true,
		"is_read_only":  false,
	}
	if err := client.put(ctx, "/drives/rootfs", driveConfig); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to configure drive: %w", err)
	}
	
	vsockConfig := map[string]any{
		"vsock_id":   "agent",
		"guest_cid":  cid,
		"uds_path":   vsockPath,
	}
	if err := client.put(ctx, "/vsocks/agent", vsockConfig); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to configure vsock: %w", err)
	}
	
	action := map[string]any{
		"action_type": "InstanceStart",
	}
	if err := client.put(ctx, "/actions", action); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to start instance: %w", err)
	}
	
	inst := &Instance{
		WorkspaceID: spec.WorkspaceID,
		WorkDir:     workDir,
		APISocket:   apiSocket,
		VSockPath:   vsockPath,
		CID:         cid,
		Process:     cmd.Process,
	}
	
	m.instances[spec.WorkspaceID] = inst
	return inst, nil
}

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

func (m *Manager) Get(workspaceID string) (*Instance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	inst, exists := m.instances[workspaceID]
	if !exists {
		return nil, fmt.Errorf("workspace not found: %s", workspaceID)
	}
	
	return inst, nil
}
