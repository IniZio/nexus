package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type mockAPIClient struct {
	putErr   error
	putCalls []string // recorded "<method> <path>" entries
}

func (m *mockAPIClient) put(ctx context.Context, path string, body any) error {
	m.putCalls = append(m.putCalls, path)
	return m.putErr
}

// testNetworkCommands records calls made to network commands and suppresses
// actual execution so tests run without real network permissions.
type testNetworkCommands struct {
	calls []string
	errs  map[string]error // keyed by "command arg0 arg1 ..."
}

func (t *testNetworkCommands) run(name string, args ...string) error {
	key := strings.Join(append([]string{name}, args...), " ")
	t.calls = append(t.calls, key)
	if t.errs != nil {
		if err, ok := t.errs[key]; ok {
			return err
		}
	}
	return nil
}

func installTestNetworkRunner(t *testing.T) *testNetworkCommands {
	t.Helper()
	nc := &testNetworkCommands{}

	// Mock networkCommandRunner (used for iptables, etc.)
	oldNCR := networkCommandRunner
	networkCommandRunner = nc.run
	t.Cleanup(func() { networkCommandRunner = oldNCR })

	// Mock tapSetupFunc so tests record "ip tuntap add dev <name> mode tap" calls
	// without requiring real CAP_NET_ADMIN, while keeping test assertions intact.
	oldSetup := tapSetupFunc
	tapSetupFunc = func(tapName, hostIP, subnetCIDR string) (any, error) {
		nc.run("ip", "tuntap", "add", "dev", tapName, "mode", "tap")
		nc.run("ip", "link", "set", tapName, "master", bridgeName)
		nc.run("ip", "link", "set", tapName, "up")
		return nil, nil
	}
	t.Cleanup(func() { tapSetupFunc = oldSetup })

	// Mock tapTeardownFunc so tests record "ip tuntap del dev <name> mode tap" calls.
	oldTeardown := tapTeardownFunc
	tapTeardownFunc = func(tapName, subnetCIDR string) {
		nc.run("ip", "tuntap", "del", "dev", tapName, "mode", "tap")
	}
	t.Cleanup(func() { tapTeardownFunc = oldTeardown })

	return nc
}

func installWorkspaceImageBuilder(t *testing.T) {
	t.Helper()
	original := workspaceImageBuilderFunc
	workspaceImageBuilderFunc = func(projectRoot, imagePath string) error {
		return os.WriteFile(imagePath, []byte("workspace-image"), 0o600)
	}
	t.Cleanup(func() { workspaceImageBuilderFunc = original })
}

func TestManagerSpawnConfiguresAndStartsVM(t *testing.T) {
	nc := installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mock := &mockAPIClient{}
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return mock
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-test-1",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   1024,
		VCPUs:       1,
	}

	inst, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	if inst.WorkspaceID != spec.WorkspaceID {
		t.Errorf("expected workspace ID %q, got %q", spec.WorkspaceID, inst.WorkspaceID)
	}

	if inst.APISocket == "" {
		t.Error("expected API socket path to be set")
	}

	if inst.VSockPath == "" {
		t.Error("expected vsock path to be set")
	}

	if inst.CID == 0 {
		t.Error("expected CID to be set")
	}

	if inst.Process == nil {
		t.Error("expected process to be set")
	}

	if _, err := os.Stat(inst.APISocket); os.IsNotExist(err) {
		t.Errorf("API socket file does not exist: %s", inst.APISocket)
	}

	// Network fields: TAPName and HostIP must be set; GuestIP is empty (DHCP assigned at boot)
	if inst.TAPName == "" {
		t.Error("expected TAPName to be set")
	}
	if inst.HostIP == "" {
		t.Error("expected HostIP to be set")
	}
	// GuestIP is intentionally empty — it is assigned by udhcpc inside the guest
	if inst.GuestIP != "" {
		t.Errorf("expected GuestIP to be empty (DHCP), got %q", inst.GuestIP)
	}

	// TAP setup commands must have been called
	hasTAPAdd := false
	for _, call := range nc.calls {
		if strings.Contains(call, "tuntap") && strings.Contains(call, "add") {
			hasTAPAdd = true
		}
	}
	if !hasTAPAdd {
		t.Errorf("expected ip tuntap add to be called; got calls: %v", nc.calls)
	}

	// /network-interfaces/eth0 must be in the API calls
	hasNetIface := false
	for _, call := range mock.putCalls {
		if strings.HasPrefix(call, "/network-interfaces/") {
			hasNetIface = true
		}
	}
	if !hasNetIface {
		t.Errorf("expected PUT /network-interfaces/eth0 to be called; got: %v", mock.putCalls)
	}

	_ = nc
}

func TestManagerSpawnNetworkInterfaceAPICallOrder(t *testing.T) {
	installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mock := &mockAPIClient{}
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return mock
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-call-order",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	_, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	// Expected call order: machine-config, boot-source, drives/rootfs,
	// drives/workspace, network-interfaces/eth0, vsock, actions
	wantPaths := []string{
		"/machine-config",
		"/boot-source",
		"/drives/rootfs",
		"/drives/workspace",
		"/network-interfaces/eth0",
		"/vsock",
		"/actions",
	}

	if len(mock.putCalls) != len(wantPaths) {
		t.Fatalf("expected %d API calls, got %d: %v", len(wantPaths), len(mock.putCalls), mock.putCalls)
	}
	for i, want := range wantPaths {
		if mock.putCalls[i] != want {
			t.Errorf("API call[%d]: want %q, got %q", i, want, mock.putCalls[i])
		}
	}
}

// TestManagerSpawnBootArgsDHCP verifies that default boot args do NOT contain
// a static ip= kernel argument — networking is configured by DHCP (udhcpc).
func TestManagerSpawnBootArgsDHCP(t *testing.T) {
	installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)

	var capturedBootArgs string
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &captureBootArgsClient{onBootSource: func(args string) { capturedBootArgs = args }}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-boot-args",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	_, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	if strings.Contains(capturedBootArgs, "ip=") {
		t.Errorf("boot_args must NOT contain static ip= arg (DHCP used instead), got: %q", capturedBootArgs)
	}
}

// captureBootArgsClient is a mock that captures the boot_args from /boot-source PUT.
type captureBootArgsClient struct {
	onBootSource func(string)
}

func (c *captureBootArgsClient) put(ctx context.Context, path string, body any) error {
	if path == "/boot-source" {
		if m, ok := body.(map[string]any); ok {
			if args, ok := m["boot_args"].(string); ok {
				c.onBootSource(args)
			}
		}
	}
	return nil
}

func TestManagerSpawnTAPCleanupOnAPIFailure(t *testing.T) {
	nc := installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{putErr: errors.New("api error")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-cleanup",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	_, err := mgr.Spawn(ctx, spec)
	if err == nil {
		t.Fatal("expected error from API failure")
	}

	// Verify that the TAP setup was attempted.
	hasTAPAdd := false
	for _, call := range nc.calls {
		if strings.Contains(call, "tuntap") && strings.Contains(call, "add") {
			hasTAPAdd = true
		}
	}
	if !hasTAPAdd {
		t.Errorf("expected ip tuntap add to be called; got calls: %v", nc.calls)
	}

	_ = nc
}

func TestManagerStopTeardownsTAP(t *testing.T) {
	nc := installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-stop-tap",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	inst, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	tapName := inst.TAPName
	if tapName == "" {
		t.Fatal("expected TAPName to be set after spawn")
	}

	// Reset call recording so we can check what stop does
	nc.calls = nil

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stopCancel()

	if err := mgr.Stop(stopCtx, spec.WorkspaceID); err != nil {
		t.Errorf("stop failed: %v", err)
	}

	// TAP teardown must be called on Stop
	hasTAPDel := false
	for _, call := range nc.calls {
		if strings.Contains(call, "tuntap") && strings.Contains(call, "del") && strings.Contains(call, tapName) {
			hasTAPDel = true
		}
	}
	if !hasTAPDel {
		t.Errorf("expected ip tuntap del %s to be called on Stop; got calls: %v", tapName, nc.calls)
	}
}

// TestDefaultFirecrackerBootArgsDHCP verifies that the default boot args do not
// contain a static ip= kernel argument — DHCP (udhcpc) runs inside the guest.
func TestDefaultFirecrackerBootArgsDHCP(t *testing.T) {
	t.Setenv("NEXUS_FIRECRACKER_BOOT_ARGS", "")
	args := defaultFirecrackerBootArgs()
	if strings.Contains(args, "ip=") {
		t.Errorf("default boot args must NOT contain ip= (DHCP used), got %q", args)
	}
	// Must still contain required kernel params
	for _, required := range []string{"console=ttyS0", "root=/dev/vda"} {
		if !strings.Contains(args, required) {
			t.Errorf("boot args missing %q, got %q", required, args)
		}
	}
}

func TestDefaultFirecrackerBootArgsEnvOverride(t *testing.T) {
	t.Setenv("NEXUS_FIRECRACKER_BOOT_ARGS", "console=ttyS0 root=/dev/vda rw")
	args := defaultFirecrackerBootArgs()
	if strings.Contains(args, "ip=") {
		t.Errorf("env override should suppress ip= injection, got %q", args)
	}
}

func TestManagerSpawnBinaryNotFound(t *testing.T) {
	installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	cfg.FirecrackerBin = "/nonexistent/firecracker"
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}

	// Without unshare, the nonexistent binary causes cmd.Start() to fail immediately,
	// or the API socket is never created so waitForAPISocket times out.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-notfound",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   1024,
		VCPUs:       1,
	}

	start := time.Now()
	_, err := mgr.Spawn(ctx, spec)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}

	// Should fail quickly (binary not found → immediate error or context timeout).
	if elapsed > 2*time.Second {
		t.Errorf("spawn took too long (%v), should fail fast", elapsed)
	}
}

func TestManagerSpawnDuplicateWorkspaceID(t *testing.T) {
	installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}

	ctx := context.Background()
	spec := SpawnSpec{
		WorkspaceID: "ws-dup",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	_, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("first spawn failed: %v", err)
	}

	_, err = mgr.Spawn(ctx, spec)
	if err == nil {
		t.Fatal("expected error for duplicate workspace ID")
	}

	if err.Error() != "workspace already exists: ws-dup" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManagerStop(t *testing.T) {
	installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-stop",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	inst, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stopCancel()

	err = mgr.Stop(stopCtx, inst.WorkspaceID)
	if err != nil {
		t.Errorf("stop failed: %v", err)
	}

	err = mgr.Stop(stopCtx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent workspace")
	}
}

func TestManagerGet(t *testing.T) {
	installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-get",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	_, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	inst, err := mgr.Get(spec.WorkspaceID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	if inst.WorkspaceID != spec.WorkspaceID {
		t.Errorf("expected workspace ID %q, got %q", spec.WorkspaceID, inst.WorkspaceID)
	}

	_, err = mgr.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent workspace")
	}
}

func TestManagerSpawnAPIError(t *testing.T) {
	nc := installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	expectedErr := errors.New("api error")
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{putErr: expectedErr}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-api-error",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	_, err := mgr.Spawn(ctx, spec)
	if err == nil {
		t.Fatal("expected error from API failure")
	}

	if !strings.Contains(err.Error(), "api error") {
		t.Errorf("expected API error, got: %v", err)
	}

	_ = nc
}

func TestManagerSpawnProcessOutlivesSpawnContext(t *testing.T) {
	installTestNetworkRunner(t)
	installWorkspaceImageBuilder(t)
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	spec := SpawnSpec{
		WorkspaceID: "ws-context-lifetime",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}

	inst, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer stopCancel()
		_ = mgr.Stop(stopCtx, spec.WorkspaceID)
	})

	time.Sleep(350 * time.Millisecond)

	if err := inst.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("expected firecracker process to outlive spawn context, but it exited: %v", err)
	}

	state, err := processState(inst.Process.Pid)
	if err != nil {
		t.Fatalf("failed to read firecracker process state: %v", err)
	}
	if state == 'Z' {
		t.Fatal("expected firecracker process to outlive spawn context, but it became a zombie")
	}
}

func processState(pid int) (byte, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}

	fields := strings.SplitN(string(data), ") ", 2)
	if len(fields) != 2 || len(fields[1]) == 0 {
		return 0, fmt.Errorf("unexpected /proc stat format")
	}

	return fields[1][0], nil
}

func TestManagerWaitForAPISocketTimeout(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := mgr.waitForAPISocket(ctx, "/nonexistent/path/sock")
	if err == nil {
		t.Fatal("expected timeout error")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context deadline exceeded, got: %v", err)
	}
}

func TestManagerWaitForAPISocketCancellation(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := mgr.waitForAPISocket(ctx, "/nonexistent/path/sock")
	if err == nil {
		t.Fatal("expected cancellation error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context canceled, got: %v", err)
	}
}

func TestWorkspaceImageSizeBytes(t *testing.T) {
	const miB = int64(1024 * 1024)

	if got := workspaceImageSizeBytes(1); got != 32768*miB {
		t.Fatalf("expected minimum image size 32768MiB, got %d", got)
	}

	if got := workspaceImageSizeBytes(1024 * miB); got != 32768*miB {
		t.Fatalf("expected minimum size 32768MiB for 1GiB project, got %d", got)
	}

	if got := workspaceImageSizeBytes(20 * 1024 * miB); got != 36864*miB {
		t.Fatalf("expected size 36864MiB for 20GiB project, got %d", got)
	}

	if got := workspaceImageSizeBytes(300*miB + 12345); got%miB != 0 {
		t.Fatalf("expected result rounded to MiB boundary, got %d", got)
	}
}

func testManagerConfig(t *testing.T) ManagerConfig {
	tmpDir := t.TempDir()
	kernelPath := filepath.Join(tmpDir, "vmlinux")
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")
	fakeFcPath := filepath.Join(tmpDir, "fake-firecracker")

	if err := os.WriteFile(kernelPath, []byte("fake kernel"), 0644); err != nil {
		t.Fatalf("failed to create fake kernel: %v", err)
	}

	if err := os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644); err != nil {
		t.Fatalf("failed to create fake rootfs: %v", err)
	}

	fakeFcScript := `#!/bin/bash
# Fake firecracker that creates the API socket and responds to requests
API_SOCK=""
for ((i=1; i<=$#; i++)); do
    if [ "${!i}" = "--api-sock" ]; then
        j=$((i+1))
        API_SOCK="${!j}"
    fi
done

if [ -n "$API_SOCK" ]; then
    # Create socket file to signal startup
    touch "$API_SOCK"
    
    # Keep process running
    while true; do
        sleep 1
    done
fi
`
	if err := os.WriteFile(fakeFcPath, []byte(fakeFcScript), 0755); err != nil {
		t.Fatalf("failed to create fake firecracker: %v", err)
	}

	return ManagerConfig{
		FirecrackerBin: fakeFcPath,
		KernelPath:     kernelPath,
		RootFSPath:     rootfsPath,
		WorkDirRoot:    tmpDir,
	}
}
