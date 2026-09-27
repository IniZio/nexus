//go:build linux

package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSupervisorScopeUnit(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"abc123", "nexus-sb-abc123"},
		{"proj/name", "nexus-sb-proj_name"},
		{"a/b/c-d_e", "nexus-sb-a_b_c-d_e"},
		{"AABBCC", "nexus-sb-AABBCC"},
		{"hello world", "nexus-sb-hello_world"},
	}
	for _, tc := range cases {
		got := supervisorScopeUnit(tc.ref)
		if got != tc.want {
			t.Errorf("supervisorScopeUnit(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

func TestBuildSystemdScopeArgs(t *testing.T) {
	args := buildSystemdScopeArgs("nexus-sb-abc", "/usr/bin/nexus", []string{"__supervisor", "--sandbox-ref", "abc"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--user", "--scope", "--collect", "--unit=nexus-sb-abc", "/usr/bin/nexus", "__supervisor"} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildSystemdScopeArgs: missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "OOMScore") {
		t.Errorf("OOMScoreAdjust must not appear in scope args (not a valid scope property): %q", joined)
	}
}

func TestSpawnDetached_SystemdPathUsed(t *testing.T) {
	var capturedArgs []string
	orig := execSystemdRun
	origProbe := systemdUserProbe
	t.Cleanup(func() {
		execSystemdRun = orig
		systemdUserProbe = origProbe
	})

	systemdUserProbe = func() bool { return true }
	execSystemdRun = func(sdArgs []string, _ *os.File) (*exec.Cmd, error) {
		capturedArgs = sdArgs
		cmd := exec.Command("sleep", "10")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}

	stateDir := t.TempDir()
	const fakePID = 99999
	pidfile := PidfilePath(stateDir)

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(pidfile, []byte(strconv.Itoa(fakePID)+"\n"), 0o644)
	}()

	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "myproj/mybox",
			StoreRoot:  stateDir,
			StateDir:   stateDir,
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  stateDir,
			KernelPath: "/nonexistent/vmlinux",
			DiskPath:   "/nonexistent/sb.raw",
		},
		Exe:          "/bin/true",
		ReadyTimeout: 5 * time.Second,
	}

	pid, watchdog, err := SpawnDetached(cfg)
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	if watchdog != nil {
		t.Error("watchdog should be nil for non-ephemeral scope spawn")
	}
	if pid != fakePID {
		t.Errorf("pid = %d, want %d", pid, fakePID)
	}
	if len(capturedArgs) == 0 {
		t.Fatal("execSystemdRun was not called")
	}
	if !strings.Contains(strings.Join(capturedArgs, " "), "nexus-sb-myproj_mybox") {
		t.Errorf("unit name not found in systemd-run args: %v", capturedArgs)
	}
}

func TestSpawnDetached_FallbackWhenNoSystemd(t *testing.T) {
	orig := systemdUserProbe
	t.Cleanup(func() { systemdUserProbe = orig })
	systemdUserProbe = func() bool { return false }

	stateDir := t.TempDir()

	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "test-nosystemd",
			StoreRoot:  stateDir,
			StateDir:   stateDir,
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  stateDir,
			KernelPath: "/nonexistent/vmlinux",
			DiskPath:   "/nonexistent/sb.raw",
		},
		Exe:          "/bin/true",
		ReadyTimeout: 500 * time.Millisecond,
	}

	_, _, err := SpawnDetached(cfg)
	if err == nil {
		t.Fatal("expected error (no pidfile written by /bin/true), got nil")
	}
	if strings.Contains(err.Error(), "systemd") {
		t.Errorf("error mentions systemd unexpectedly (probe was false): %v", err)
	}
}

func TestSpawnDetached_EphemeralSkipsScope(t *testing.T) {
	origProbe := systemdUserProbe
	origExec := execSystemdRun
	t.Cleanup(func() {
		systemdUserProbe = origProbe
		execSystemdRun = origExec
	})

	systemdUserProbe = func() bool { return true }
	execSystemdRun = func(_ []string, _ *os.File) (*exec.Cmd, error) {
		panic("execSystemdRun must not be called for Ephemeral spawn")
	}

	stateDir := t.TempDir()
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "test-ephemeral",
			StoreRoot:  stateDir,
			StateDir:   stateDir,
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  stateDir,
			KernelPath: "/nonexistent/vmlinux",
			DiskPath:   "/nonexistent/sb.raw",
			Ephemeral:  true,
		},
		Exe:          "/bin/true",
		ReadyTimeout: 500 * time.Millisecond,
	}

	_, _, err := SpawnDetached(cfg)
	if err == nil {
		t.Fatal("expected error (no pidfile written by /bin/true), got nil")
	}
}

func TestDefaultSystemdUserProbe_NoXDGRuntimeDir(t *testing.T) {
	orig := os.Getenv("XDG_RUNTIME_DIR")
	t.Cleanup(func() { _ = os.Setenv("XDG_RUNTIME_DIR", orig) })
	_ = os.Unsetenv("XDG_RUNTIME_DIR")

	if defaultSystemdUserProbe() {
		t.Error("probe should return false when XDG_RUNTIME_DIR is unset")
	}
}

func TestDefaultSystemdUserProbe_MissingPrivateDir(t *testing.T) {
	dir := t.TempDir()
	orig := os.Getenv("XDG_RUNTIME_DIR")
	t.Cleanup(func() { _ = os.Setenv("XDG_RUNTIME_DIR", orig) })
	_ = os.Setenv("XDG_RUNTIME_DIR", dir)

	if defaultSystemdUserProbe() {
		t.Error("probe should return false when systemd/private does not exist")
	}
}

func TestDefaultSystemdUserProbe_PresentPrivateDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "systemd", "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := os.Getenv("XDG_RUNTIME_DIR")
	t.Cleanup(func() { _ = os.Setenv("XDG_RUNTIME_DIR", orig) })
	_ = os.Setenv("XDG_RUNTIME_DIR", dir)

	if !defaultSystemdUserProbe() {
		t.Error("probe should return true when systemd/private exists")
	}
}

// TestSpawnViaSystemdScope_RealScope verifies that defaultExecSystemdRun:
// (a) returns in < 2 s (proving cmd.Start() not cmd.Run() semantics), and
// (b) places the launched process in the expected scope cgroup.
// Skipped when no systemd user manager is available.
func TestSpawnViaSystemdScope_RealScope(t *testing.T) {
	if !defaultSystemdUserProbe() {
		t.Skip("systemd user manager not available")
	}

	rand := fmt.Sprintf("%x", time.Now().UnixNano())
	unit := "nexus-test-" + rand[:8]

	logFile, err := os.CreateTemp(t.TempDir(), "scope-test-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	sdArgs := buildSystemdScopeArgs(unit, "sleep", []string{"30"})

	start := time.Now()
	cmd, err := defaultExecSystemdRun(sdArgs, logFile)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("defaultExecSystemdRun: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = exec.Command("systemctl", "--user", "stop", unit+".scope").Run()
	})

	if elapsed > 2*time.Second {
		t.Errorf("scope start took %v, want < 2s (cmd.Run would block for 30s)", elapsed)
	}

	time.Sleep(200 * time.Millisecond)

	cgroupData, cgErr := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", cmd.Process.Pid))
	if cgErr != nil {
		t.Fatalf("read cgroup for pid %d: %v", cmd.Process.Pid, cgErr)
	}
	if !strings.Contains(string(cgroupData), unit+".scope") {
		t.Errorf("process %d not in expected scope cgroup %q; /proc/cgroup:\n%s",
			cmd.Process.Pid, unit+".scope", cgroupData)
	}
}
