package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAgentExecTimeoutDefault(t *testing.T) {
	t.Setenv("AGENT_EXEC_TIMEOUT_SEC", "")
	if got := agentExecTimeout(); got != 10*time.Minute {
		t.Fatalf("expected default exec timeout 10m, got %s", got)
	}
}

func TestAgentExecTimeoutOverride(t *testing.T) {
	t.Setenv("AGENT_EXEC_TIMEOUT_SEC", "7")
	if got := agentExecTimeout(); got != 7*time.Second {
		t.Fatalf("expected exec timeout 7s, got %s", got)
	}
}

func TestAgentExecTimeoutInvalidFallsBackToDefault(t *testing.T) {
	t.Setenv("AGENT_EXEC_TIMEOUT_SEC", "invalid")
	if got := agentExecTimeout(); got != 10*time.Minute {
		t.Fatalf("expected default exec timeout 10m on invalid input, got %s", got)
	}
}

func TestHandleExecRunsCommandAndReturnsExitCode(t *testing.T) {
	resp := handleExec(execRequest{Command: "bash", Args: []string{"-lc", "echo hi"}})
	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", resp.ExitCode)
	}
	if strings.TrimSpace(resp.Stdout) != "hi" {
		t.Fatalf("unexpected stdout: %q", resp.Stdout)
	}
}

func TestHandleExecReturnsNonZeroExitCodeOnFailure(t *testing.T) {
	resp := handleExec(execRequest{Command: "bash", Args: []string{"-lc", "exit 42"}})
	if resp.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", resp.ExitCode)
	}
}

func TestHandleExecCapturesStderr(t *testing.T) {
	resp := handleExec(execRequest{Command: "bash", Args: []string{"-lc", "echo error >&2"}})
	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Stderr, "error") {
		t.Fatalf("expected stderr to contain 'error', got: %q", resp.Stderr)
	}
}

func TestHandleExecIncludesStartErrorInStderr(t *testing.T) {
	resp := handleExec(execRequest{Command: "command-that-does-not-exist-for-test"})
	if resp.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code")
	}
	if !strings.Contains(resp.Stderr, "executable file not found") {
		t.Fatalf("expected stderr to include start error, got: %q", resp.Stderr)
	}
}

func TestHandleExecSetsDefaultPathWhenPathMissing(t *testing.T) {
	t.Setenv("PATH", "")
	resp := handleExec(execRequest{Command: "sh", Args: []string{"-lc", "echo ok"}})
	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with stderr %q", resp.ExitCode, resp.Stderr)
	}
	if strings.TrimSpace(resp.Stdout) != "ok" {
		t.Fatalf("unexpected stdout: %q", resp.Stdout)
	}
}

func TestServeConnSendsErrorOnDecodeFailure(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(server)
	}()

	// Send invalid JSON
	client.Write([]byte("not valid json\n"))

	// Read response
	decoder := json.NewDecoder(client)
	var resp execResponse
	if err := decoder.Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if resp.ExitCode != 1 {
		t.Fatalf("expected exit code 1 on decode error, got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Stderr, "decode error") {
		t.Fatalf("expected stderr to contain 'decode error', got: %q", resp.Stderr)
	}

	// Connection should close after error
	client.Close()
	<-done
}

func TestServeConnRejectsMissingRequestID(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(server)
	}()

	// Send request without ID
	encoder := json.NewEncoder(client)
	encoder.Encode(execRequest{ID: "", Command: "echo", Args: []string{"test"}})

	// Read error response
	decoder := json.NewDecoder(client)
	var resp execResponse
	if err := decoder.Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if resp.ExitCode != 1 {
		t.Fatalf("expected exit code 1 for missing ID, got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Stderr, "request ID is required") {
		t.Fatalf("expected stderr to contain 'request ID is required', got: %q", resp.Stderr)
	}

	// Server should still be listening for more requests
	encoder.Encode(execRequest{ID: "req-2", Command: "echo", Args: []string{"success"}})

	var successResp execResponse
	if err := decoder.Decode(&successResp); err != nil {
		t.Fatalf("failed to decode success response: %v", err)
	}

	if successResp.ID != "req-2" {
		t.Fatalf("expected ID 'req-2', got %q", successResp.ID)
	}
	if strings.TrimSpace(successResp.Stdout) != "success" {
		t.Fatalf("unexpected stdout: %q", successResp.Stdout)
	}

	client.Close()
	<-done
}

func TestServeConnHonorsWorkDirField(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(server)
	}()

	workDir := filepath.Join(t.TempDir(), "subdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	encoder := json.NewEncoder(client)
	request := map[string]any{
		"id":      "req-workdir",
		"command": "bash",
		"args":    []string{"-lc", "pwd"},
		"workdir": workDir,
	}
	if err := encoder.Encode(request); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	decoder := json.NewDecoder(client)
	var resp execResponse
	if err := decoder.Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with stderr %q", resp.ExitCode, resp.Stderr)
	}
	if strings.TrimSpace(resp.Stdout) != workDir {
		t.Fatalf("expected stdout %q, got %q", workDir, strings.TrimSpace(resp.Stdout))
	}

	client.Close()
	<-done
}

func TestHandleExecEnsuresWorkspaceMountForWorkspaceWorkDir(t *testing.T) {
	origSetupWorkspaceMount := setupWorkspaceMountRequiredFunc
	origWorkspaceMountPoint := workspaceMountPoint
	t.Cleanup(func() { setupWorkspaceMountRequiredFunc = origSetupWorkspaceMount })
	t.Cleanup(func() { workspaceMountPoint = origWorkspaceMountPoint })

	workspaceMountPoint = filepath.Join(t.TempDir(), "workspace")

	called := 0
	setupWorkspaceMountRequiredFunc = func() error {
		called++
		return nil
	}

	workspaceDir := workspaceMountPoint
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	resp := handleExec(execRequest{Command: "bash", Args: []string{"-lc", "true"}, WorkDir: workspaceDir})
	if resp.ExitCode != 0 {
		t.Fatalf("expected success, got exit code %d stderr=%q", resp.ExitCode, resp.Stderr)
	}
	if called == 0 {
		t.Fatal("expected handleExec to ensure workspace mount for /workspace workdir")
	}
}

func TestHandleExecFailsWhenWorkspaceRequiredMountUnavailable(t *testing.T) {
	origSetupWorkspaceMount := setupWorkspaceMountRequiredFunc
	origWorkspaceMountPoint := workspaceMountPoint
	t.Cleanup(func() { setupWorkspaceMountRequiredFunc = origSetupWorkspaceMount })
	t.Cleanup(func() { workspaceMountPoint = origWorkspaceMountPoint })

	workspaceMountPoint = filepath.Join(t.TempDir(), "workspace")
	setupWorkspaceMountRequiredFunc = func() error {
		return errors.New("workspace device /dev/vdb not available")
	}

	resp := handleExec(execRequest{Command: "bash", Args: []string{"-lc", "true"}, WorkDir: workspaceMountPoint})
	if resp.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code when required workspace mount is unavailable")
	}
	if !strings.Contains(resp.Stderr, "workspace mount ensure failed") {
		t.Fatalf("expected workspace mount failure in stderr, got: %q", resp.Stderr)
	}
}

func TestSetupDNSWritesResolvConf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")

	// Patch the function to use a temp path
	orig := setupDNSPath
	setupDNSPath = path
	t.Cleanup(func() { setupDNSPath = orig })

	setupDNS()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("resolv.conf not written: %v", err)
	}
	if !strings.Contains(string(data), "nameserver 8.8.8.8") {
		t.Errorf("expected nameserver 8.8.8.8 in resolv.conf, got: %q", string(data))
	}
}

func TestSetupDNSDoesNotOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")
	existing := "nameserver 192.168.1.1\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := setupDNSPath
	setupDNSPath = path
	t.Cleanup(func() { setupDNSPath = orig })

	setupDNS()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read resolv.conf: %v", err)
	}
	if string(data) != existing {
		t.Errorf("setupDNS should not overwrite existing resolv.conf; got %q", string(data))
	}
}

func TestStaticGuestIPForMAC(t *testing.T) {
	ip, err := staticGuestIPForMAC("aa:fc:00:00:03:e8")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ip != "172.26.3.232" {
		t.Fatalf("expected 172.26.3.232, got %q", ip)
	}
}

func TestStaticGuestIPForMACInvalid(t *testing.T) {
	if _, err := staticGuestIPForMAC("invalid-mac"); err == nil {
		t.Fatal("expected parse error for invalid mac")
	}
}

func TestBootstrapGuestEnvironmentPID1MountsAndConfiguresNetwork(t *testing.T) {
	origMount := mountKernelFilesystemsFunc
	origSetupNet := setupNetworkFunc
	origSetupDNS := setupDNSFunc
	t.Cleanup(func() {
		mountKernelFilesystemsFunc = origMount
		setupNetworkFunc = origSetupNet
		setupDNSFunc = origSetupDNS
	})

	calledMount := false
	calledNet := false
	calledDNS := false

	mountKernelFilesystemsFunc = func() {
		calledMount = true
	}
	setupNetworkFunc = func() error {
		calledNet = true
		return nil
	}
	setupDNSFunc = func() {
		calledDNS = true
	}

	bootstrapGuestEnvironment(1)

	if !calledMount {
		t.Fatal("expected mountKernelFilesystems to be called for pid 1")
	}
	if !calledNet {
		t.Fatal("expected setupNetwork to be called")
	}
	if !calledDNS {
		t.Fatal("expected setupDNS to be called")
	}
}

func TestBootstrapGuestEnvironmentNonPID1ConfiguresNetworkWithoutMount(t *testing.T) {
	origMount := mountKernelFilesystemsFunc
	origSetupNet := setupNetworkFunc
	origSetupDNS := setupDNSFunc
	origWorkspace := setupWorkspaceMountFunc
	t.Cleanup(func() {
		mountKernelFilesystemsFunc = origMount
		setupNetworkFunc = origSetupNet
		setupDNSFunc = origSetupDNS
		setupWorkspaceMountFunc = origWorkspace
	})

	calledMount := false
	calledNet := false
	calledDNS := false
	calledWorkspace := false

	mountKernelFilesystemsFunc = func() {
		calledMount = true
	}
	setupNetworkFunc = func() error {
		calledNet = true
		return nil
	}
	setupDNSFunc = func() {
		calledDNS = true
	}
	setupWorkspaceMountFunc = func() error {
		calledWorkspace = true
		return nil
	}

	bootstrapGuestEnvironment(42)

	if calledMount {
		t.Fatal("did not expect mountKernelFilesystems to be called for non-pid1")
	}
	if !calledNet {
		t.Fatal("expected setupNetwork to be called for non-pid1")
	}
	if !calledDNS {
		t.Fatal("expected setupDNS to be called for non-pid1")
	}
	if !calledWorkspace {
		t.Fatal("expected setupWorkspaceMount to be called for non-pid1")
	}
}

func TestEnsureAgentProcessPathSetsDefaultWhenEmpty(t *testing.T) {
	t.Setenv("PATH", "")

	ensureAgentProcessPath()

	if got := os.Getenv("PATH"); got != defaultAgentPath {
		t.Fatalf("expected PATH %q, got %q", defaultAgentPath, got)
	}
}

func TestSetupWorkspaceMountNoDevice(t *testing.T) {
	origDevice := workspaceDevicePath
	origMount := workspaceMountPoint
	origAttempts := workspaceDeviceAttempts
	origInterval := workspaceDeviceInterval
	origMkdir := workspaceMkdirAll
	origStat := workspaceStat
	origMountFunc := workspaceMountFunc
	t.Cleanup(func() {
		workspaceDevicePath = origDevice
		workspaceMountPoint = origMount
		workspaceDeviceAttempts = origAttempts
		workspaceDeviceInterval = origInterval
		workspaceMkdirAll = origMkdir
		workspaceStat = origStat
		workspaceMountFunc = origMountFunc
	})

	workspaceDevicePath = "/test/missing-dev"
	workspaceMountPoint = "/test/workspace"
	workspaceDeviceAttempts = 1
	workspaceDeviceInterval = 0
	workspaceMkdirAll = func(string, os.FileMode) error {
		return nil
	}
	calledStat := false
	workspaceStat = func(path string) (os.FileInfo, error) {
		calledStat = true
		if path != workspaceDevicePath {
			t.Fatalf("unexpected stat path %q", path)
		}
		return nil, os.ErrNotExist
	}
	mountCalled := false
	workspaceMountFunc = func(source, target, fstype string, flags uintptr, data string) error {
		mountCalled = true
		return nil
	}

	if err := setupWorkspaceMount(); err != nil {
		t.Fatalf("expected missing device to be non-fatal, got %v", err)
	}
	if !calledStat {
		t.Fatal("expected workspace device stat to be called")
	}
	if mountCalled {
		t.Fatal("did not expect mount when workspace device is missing")
	}
}

func TestSetupWorkspaceMountSuccess(t *testing.T) {
	origDevice := workspaceDevicePath
	origMount := workspaceMountPoint
	origAttempts := workspaceDeviceAttempts
	origInterval := workspaceDeviceInterval
	origMkdir := workspaceMkdirAll
	origStat := workspaceStat
	origMountFunc := workspaceMountFunc
	t.Cleanup(func() {
		workspaceDevicePath = origDevice
		workspaceMountPoint = origMount
		workspaceDeviceAttempts = origAttempts
		workspaceDeviceInterval = origInterval
		workspaceMkdirAll = origMkdir
		workspaceStat = origStat
		workspaceMountFunc = origMountFunc
	})

	workspaceDevicePath = "/test/vdb"
	workspaceMountPoint = "/test/workspace"
	workspaceDeviceAttempts = 1
	workspaceDeviceInterval = 0

	mkdirCalled := false
	mountCalled := false
	workspaceMkdirAll = func(path string, mode os.FileMode) error {
		mkdirCalled = true
		if path != workspaceMountPoint {
			t.Fatalf("unexpected mkdir path %q", path)
		}
		if mode != 0o755 {
			t.Fatalf("unexpected mkdir mode %v", mode)
		}
		return nil
	}
	workspaceStat = func(path string) (os.FileInfo, error) {
		if path != workspaceDevicePath {
			t.Fatalf("unexpected stat path %q", path)
		}
		return fakeFileInfo{name: "vdb"}, nil
	}
	workspaceMountFunc = func(source, target, fstype string, flags uintptr, data string) error {
		mountCalled = true
		if source != workspaceDevicePath || target != workspaceMountPoint || fstype != "ext4" {
			t.Fatalf("unexpected mount args source=%q target=%q fstype=%q", source, target, fstype)
		}
		return nil
	}

	if err := setupWorkspaceMount(); err != nil {
		t.Fatalf("expected setupWorkspaceMount success, got %v", err)
	}
	if !mkdirCalled {
		t.Fatal("expected workspace mkdir to be called")
	}
	if !mountCalled {
		t.Fatal("expected workspace mount to be called")
	}
}

func TestSetupWorkspaceMountBusyIsIgnored(t *testing.T) {
	origDevice := workspaceDevicePath
	origMount := workspaceMountPoint
	origAttempts := workspaceDeviceAttempts
	origInterval := workspaceDeviceInterval
	origMkdir := workspaceMkdirAll
	origStat := workspaceStat
	origMountFunc := workspaceMountFunc
	origUnmountFunc := workspaceUnmountFunc
	origReadProcMounts := workspaceReadProcMounts
	t.Cleanup(func() {
		workspaceDevicePath = origDevice
		workspaceMountPoint = origMount
		workspaceDeviceAttempts = origAttempts
		workspaceDeviceInterval = origInterval
		workspaceMkdirAll = origMkdir
		workspaceStat = origStat
		workspaceMountFunc = origMountFunc
		workspaceUnmountFunc = origUnmountFunc
		workspaceReadProcMounts = origReadProcMounts
	})

	workspaceDevicePath = "/test/vdb"
	workspaceMountPoint = "/test/workspace"
	workspaceDeviceAttempts = 1
	workspaceDeviceInterval = 0
	workspaceMkdirAll = func(string, os.FileMode) error { return nil }
	workspaceStat = func(string) (os.FileInfo, error) { return fakeFileInfo{name: "vdb"}, nil }
	workspaceMountFunc = func(string, string, string, uintptr, string) error { return unix.EBUSY }
	workspaceReadProcMounts = func(string) ([]byte, error) {
		return []byte("/test/vdb /test/workspace ext4 rw,relatime 0 0\n"), nil
	}

	if err := setupWorkspaceMount(); err != nil {
		t.Fatalf("expected EBUSY to be ignored, got %v", err)
	}
}

func TestSetupWorkspaceMountBusyWithoutActiveMountFails(t *testing.T) {
	origDevice := workspaceDevicePath
	origMount := workspaceMountPoint
	origAttempts := workspaceDeviceAttempts
	origInterval := workspaceDeviceInterval
	origMkdir := workspaceMkdirAll
	origStat := workspaceStat
	origMountFunc := workspaceMountFunc
	origReadProcMounts := workspaceReadProcMounts
	t.Cleanup(func() {
		workspaceDevicePath = origDevice
		workspaceMountPoint = origMount
		workspaceDeviceAttempts = origAttempts
		workspaceDeviceInterval = origInterval
		workspaceMkdirAll = origMkdir
		workspaceStat = origStat
		workspaceMountFunc = origMountFunc
		workspaceReadProcMounts = origReadProcMounts
	})

	workspaceDevicePath = "/test/vdb"
	workspaceMountPoint = "/test/workspace"
	workspaceDeviceAttempts = 1
	workspaceDeviceInterval = 0
	workspaceMkdirAll = func(string, os.FileMode) error { return nil }
	workspaceStat = func(string) (os.FileInfo, error) { return fakeFileInfo{name: "vdb"}, nil }
	workspaceMountFunc = func(string, string, string, uintptr, string) error { return unix.EBUSY }
	workspaceReadProcMounts = func(string) ([]byte, error) {
		return []byte("/dev/vda / ext4 rw,relatime 0 0\n"), nil
	}
	workspaceUnmountFunc = func(target string, flags int) error {
		if target == "/test/workspace/.nexus-docker" {
			return nil
		}
		return unix.ENOENT
	}

	err := setupWorkspaceMountRequired()
	if err == nil {
		t.Fatal("expected error when EBUSY but /workspace is not mounted")
	}
	if !strings.Contains(err.Error(), "workspace mount is not active") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetupWorkspaceMountBusyUnmountsNestedAndRetries(t *testing.T) {
	origDevice := workspaceDevicePath
	origMount := workspaceMountPoint
	origAttempts := workspaceDeviceAttempts
	origInterval := workspaceDeviceInterval
	origMkdir := workspaceMkdirAll
	origStat := workspaceStat
	origMountFunc := workspaceMountFunc
	origUnmountFunc := workspaceUnmountFunc
	origReadProcMounts := workspaceReadProcMounts
	t.Cleanup(func() {
		workspaceDevicePath = origDevice
		workspaceMountPoint = origMount
		workspaceDeviceAttempts = origAttempts
		workspaceDeviceInterval = origInterval
		workspaceMkdirAll = origMkdir
		workspaceStat = origStat
		workspaceMountFunc = origMountFunc
		workspaceUnmountFunc = origUnmountFunc
		workspaceReadProcMounts = origReadProcMounts
	})

	workspaceDevicePath = "/test/vdb"
	workspaceMountPoint = "/test/workspace"
	workspaceDeviceAttempts = 1
	workspaceDeviceInterval = 0
	workspaceMkdirAll = func(string, os.FileMode) error { return nil }
	workspaceStat = func(string) (os.FileInfo, error) { return fakeFileInfo{name: "vdb"}, nil }

	mountedActive := false
	workspaceReadProcMounts = func(string) ([]byte, error) {
		if mountedActive {
			return []byte("/test/vdb /test/workspace ext4 rw,relatime 0 0\n"), nil
		}
		return []byte("/dev/vda /test/workspace/.nexus-docker ext4 rw,relatime 0 0\n"), nil
	}

	unmounted := false
	workspaceUnmountFunc = func(target string, flags int) error {
		if target == "/test/workspace/.nexus-docker" {
			unmounted = true
			return nil
		}
		return unix.ENOENT
	}

	mountCalls := 0
	workspaceMountFunc = func(source, target, fstype string, flags uintptr, data string) error {
		mountCalls++
		if mountCalls == 1 {
			return unix.EBUSY
		}
		mountedActive = true
		return nil
	}

	if err := setupWorkspaceMountRequired(); err != nil {
		t.Fatalf("expected mount recovery success, got: %v", err)
	}
	if !unmounted {
		t.Fatal("expected nested workspace mount to be unmounted during recovery")
	}
	if mountCalls < 2 {
		t.Fatalf("expected mount to be retried after unmounting conflicts, got %d calls", mountCalls)
	}
}

func TestWaitForWorkspaceDeviceAppearsAfterRetries(t *testing.T) {
	origDevice := workspaceDevicePath
	origAttempts := workspaceDeviceAttempts
	origInterval := workspaceDeviceInterval
	origStat := workspaceStat
	t.Cleanup(func() {
		workspaceDevicePath = origDevice
		workspaceDeviceAttempts = origAttempts
		workspaceDeviceInterval = origInterval
		workspaceStat = origStat
	})

	workspaceDevicePath = "/test/vdb"
	workspaceDeviceAttempts = 3
	workspaceDeviceInterval = 0

	count := 0
	workspaceStat = func(path string) (os.FileInfo, error) {
		count++
		if path != workspaceDevicePath {
			t.Fatalf("unexpected stat path %q", path)
		}
		if count < 3 {
			return nil, os.ErrNotExist
		}
		return fakeFileInfo{name: "vdb"}, nil
	}

	available, err := waitForWorkspaceDevice()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !available {
		t.Fatal("expected workspace device to become available")
	}
}

func TestMountKernelFilesystemsMountsCgroupV1HierarchyWhenCgroup2Unavailable(t *testing.T) {
	origMkdir := kernelMkdirAll
	origMount := kernelMountFunc
	t.Cleanup(func() {
		kernelMkdirAll = origMkdir
		kernelMountFunc = origMount
	})

	called := make([]string, 0)
	kernelMkdirAll = func(string, os.FileMode) error { return nil }
	kernelMountFunc = func(source, target, fstype string, flags uintptr, data string) error {
		called = append(called, source+"|"+target+"|"+fstype+"|"+data)
		if target == "/sys/fs/cgroup" && fstype == "cgroup2" {
			return unix.EINVAL
		}
		return nil
	}

	mountKernelFilesystems()

	joined := strings.Join(called, "\n")
	if !strings.Contains(joined, "tmpfs|/sys/fs/cgroup|tmpfs|mode=755") {
		t.Fatalf("expected tmpfs mount fallback for cgroup v1, got calls:\n%s", joined)
	}
	if !strings.Contains(joined, "cgroup|/sys/fs/cgroup/devices|cgroup|devices") {
		t.Fatalf("expected devices cgroup mount, got calls:\n%s", joined)
	}
}

func TestMountKernelFilesystemsUsesCgroup2WhenAvailable(t *testing.T) {
	origMkdir := kernelMkdirAll
	origMount := kernelMountFunc
	t.Cleanup(func() {
		kernelMkdirAll = origMkdir
		kernelMountFunc = origMount
	})

	called := make([]string, 0)
	kernelMkdirAll = func(string, os.FileMode) error { return nil }
	kernelMountFunc = func(source, target, fstype string, flags uintptr, data string) error {
		called = append(called, source+"|"+target+"|"+fstype+"|"+data)
		return nil
	}

	mountKernelFilesystems()

	joined := strings.Join(called, "\n")
	if !strings.Contains(joined, "none|/sys/fs/cgroup|cgroup2|") {
		t.Fatalf("expected cgroup2 mount attempt, got calls:\n%s", joined)
	}
	if strings.Contains(joined, "/sys/fs/cgroup/devices|cgroup|devices") {
		t.Fatalf("did not expect cgroup v1 controller mounts when cgroup2 succeeds, got calls:\n%s", joined)
	}
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
