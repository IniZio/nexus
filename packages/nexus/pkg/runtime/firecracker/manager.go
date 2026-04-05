package firecracker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// tapSetupFunc creates a TAP device and attaches it to the bridge.
// Returns an opaque handle (unused in production, may be used in tests).
// Overridable in tests.
var tapSetupFunc func(tapName, hostIP, subnetCIDR string) (any, error) = realSetupTAP

// tapTeardownFunc tears down a TAP device.
// Overridable in tests.
var tapTeardownFunc func(tapName, subnetCIDR string) = realTeardownTAP

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
	WorkspaceID    string
	WorkDir        string
	WorkspaceImage string
	APISocket      string
	VSockPath      string
	SerialLog      string
	CID            uint32
	Process        *os.Process
	TAPName        string
	GuestIP        string
	HostIP         string
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

// networkCommandRunner runs a network-related command.
// Overridable in tests to avoid real network operations.
var networkCommandRunner = runNetworkCommand

// workspaceImageBuilderFunc creates a workspace image that is mounted at
// /workspace inside the guest. Overridable in tests.
var workspaceImageBuilderFunc = createWorkspaceImage

func runNetworkCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, strings.TrimSpace(string(out)))
	}
	return nil
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

// guestMAC returns a deterministic MAC address for a given CID.
func guestMAC(cid uint32) string {
	b0 := byte((cid >> 8) & 0xFF)
	b1 := byte(cid & 0xFF)
	return fmt.Sprintf("AA:FC:00:00:%02X:%02X", b0, b1)
}

// setupTAP delegates to tapSetupFunc (real or mock in tests).
func setupTAP(tapNameStr, hostIP, subnetCIDR string) error {
	_, err := tapSetupFunc(tapNameStr, hostIP, subnetCIDR)
	return err
}

// teardownTAP delegates to tapTeardownFunc (real or mock in tests).
func teardownTAP(tapNameStr, subnetCIDR string) {
	tapTeardownFunc(tapNameStr, subnetCIDR)
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
	workspaceImagePath := filepath.Join(workDir, "workspace.ext4")
	cleanupTap := true

	cid := m.nextCID
	m.nextCID++

	// Derive a tap name for this workspace and create it on the host bridge.
	tap := tapNameForWorkspace(spec.WorkspaceID)
	mac := guestMAC(cid)
	hostIP := bridgeGatewayIP
	subnetCIDR := guestSubnetCIDR

	if err := setupTAP(tap, hostIP, subnetCIDR); err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to setup tap %s: %w", tap, err)
	}

	if err := workspaceImageBuilderFunc(spec.ProjectRoot, workspaceImagePath); err != nil {
		if cleanupTap {
			teardownTAP(tap, subnetCIDR)
		}
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to build workspace image: %w", err)
	}

	// Launch Firecracker directly in the host network namespace.
	// The tap device was created on the host, so Firecracker opens it by name
	// without any namespace wrapper — no EBUSY.
	cmd := exec.Command(
		m.config.FirecrackerBin,
		"--api-sock", apiSocket,
		"--id", spec.WorkspaceID,
	)
	cmd.Dir = workDir
	logFile, err := os.OpenFile(serialLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		teardownTAP(tap, subnetCIDR)
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to create firecracker log file: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		teardownTAP(tap, subnetCIDR)
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}
	_ = logFile.Close()

	if err := m.waitForAPISocket(ctx, apiSocket); err != nil {
		teardownTAP(tap, subnetCIDR)
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
		teardownTAP(tap, subnetCIDR)
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure machine: %w", err)
	}

	bootSource := map[string]any{
		"kernel_image_path": m.config.KernelPath,
		"boot_args":         defaultFirecrackerBootArgs(),
	}
	if err := client.put(ctx, "/boot-source", bootSource); err != nil {
		teardownTAP(tap, subnetCIDR)
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
		teardownTAP(tap, subnetCIDR)
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure drive: %w", err)
	}

	workspaceDriveConfig := map[string]any{
		"drive_id":       "workspace",
		"path_on_host":   workspaceImagePath,
		"is_root_device": false,
		"is_read_only":   false,
	}
	if err := client.put(ctx, "/drives/workspace", workspaceDriveConfig); err != nil {
		teardownTAP(tap, subnetCIDR)
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure workspace drive: %w", err)
	}

	// Configure network interface: Firecracker opens the host tap by name.
	netIfaceConfig := map[string]any{
		"iface_id":      "eth0",
		"host_dev_name": tap,
		"guest_mac":     mac,
	}
	if err := client.put(ctx, "/network-interfaces/eth0", netIfaceConfig); err != nil {
		teardownTAP(tap, subnetCIDR)
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure network interface: %w", err)
	}

	vsockConfig := map[string]any{
		"vsock_id":  "agent",
		"guest_cid": cid,
		"uds_path":  vsockPath,
	}
	if err := client.put(ctx, "/vsock", vsockConfig); err != nil {
		teardownTAP(tap, subnetCIDR)
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to configure vsock: %w", err)
	}

	action := map[string]any{
		"action_type": "InstanceStart",
	}
	if err := client.put(ctx, "/actions", action); err != nil {
		teardownTAP(tap, subnetCIDR)
		m.cleanup(workDir, cmd.Process)
		return nil, fmt.Errorf("failed to start instance: %w", err)
	}

	inst := &Instance{
		WorkspaceID:    spec.WorkspaceID,
		WorkDir:        workDir,
		WorkspaceImage: workspaceImagePath,
		APISocket:      apiSocket,
		VSockPath:      vsockPath,
		SerialLog:      serialLog,
		CID:            cid,
		Process:        cmd.Process,
		TAPName:        tap,
		GuestIP:        "", // assigned by DHCP at boot
		HostIP:         hostIP,
	}

	m.instances[spec.WorkspaceID] = inst
	cleanupTap = false
	return inst, nil
}

// defaultFirecrackerBootArgs returns the kernel command line for a VM.
// If NEXUS_FIRECRACKER_BOOT_ARGS is set, it is returned verbatim.
// Otherwise a standard set is generated. Guest networking is configured
// by udhcpc (DHCP) inside the VM — no static ip= kernel argument.
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
		waitDone := make(chan error, 1)
		go func() {
			_, err := inst.Process.Wait()
			waitDone <- err
		}()

		select {
		case <-ctx.Done():
			_ = inst.Process.Kill()
			<-waitDone
		case <-waitDone:
		}
	}

	// Teardown the tap device after the VM exits.
	if inst.TAPName != "" {
		teardownTAP(inst.TAPName, guestSubnetCIDR)
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

func createWorkspaceImage(projectRoot, imagePath string) error {
	if strings.TrimSpace(projectRoot) == "" {
		return fmt.Errorf("project root is required for workspace image")
	}

	requiredPath := filepath.Join(projectRoot, ".nexus", "lifecycles", "start.sh")
	if _, statErr := os.Stat(requiredPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("workspace image source missing required lifecycle script: %s", requiredPath)
		}
		return fmt.Errorf("stat required lifecycle script for workspace image: %w", statErr)
	}

	projectSizeBytes, err := directorySizeBytes(projectRoot)
	if err != nil {
		return fmt.Errorf("compute project size: %w", err)
	}

	fd, err := os.OpenFile(imagePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create workspace image: %w", err)
	}
	if err := fd.Truncate(workspaceImageSizeBytes(projectSizeBytes)); err != nil {
		_ = fd.Close()
		return fmt.Errorf("truncate workspace image: %w", err)
	}
	if err := fd.Close(); err != nil {
		return fmt.Errorf("close workspace image: %w", err)
	}

	cmd := exec.Command("mkfs.ext4", "-F", "-d", projectRoot, imagePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs.ext4 workspace image: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func directorySizeBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func workspaceImageSizeBytes(projectSizeBytes int64) int64 {
	const (
		miB          = int64(1024 * 1024)
		minImageSize = int64(32*1024) * miB
		overhead     = int64(16*1024) * miB
	)

	target := projectSizeBytes + overhead
	if target < minImageSize {
		target = minImageSize
	}

	if rem := target % miB; rem != 0 {
		target += miB - rem
	}

	return target
}
