//go:build linux

package supervisor

import (
	"os"
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
	for _, want := range []string{"--user", "--scope", "--collect", "--unit=nexus-sb-abc", "OOMScoreAdjust=0", "/usr/bin/nexus", "__supervisor"} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildSystemdScopeArgs: missing %q in %q", want, joined)
		}
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
	execSystemdRun = func(sdArgs []string, _ *os.File) error {
		capturedArgs = sdArgs
		return nil
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
	execSystemdRun = func(_ []string, _ *os.File) error {
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
