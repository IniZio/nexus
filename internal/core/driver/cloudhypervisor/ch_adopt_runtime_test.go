// ch_adopt_runtime_test.go — tests for CHDriver.AdoptRuntime, the seam that
// installs an already-adopted NetnsRuntime into a driver's in-memory state
// so Observe/Stop/GuestNetworkFD operate on it as though this driver's own
// Start had produced it (motive nexus3-host-supervisor-hotswap, slice 07).
package cloudhypervisor

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// newTestAdoptedRuntime builds a real *NetnsRuntime via AdoptNetnsRuntime,
// backed by a live "sleep" process standing in for the netns child (same
// pattern as TestAdoptNetnsRuntime_Rebuilds).
func newTestAdoptedRuntime(t *testing.T) *NetnsRuntime {
	t.Helper()
	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() { pumpFile.Close() })

	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	startTime, err := readProcStartTime(pid)
	if err != nil {
		t.Fatalf("readProcStartTime(%d): %v", pid, err)
	}

	rt, err := AdoptNetnsRuntime(pid, pid, startTime, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
	if err != nil {
		t.Fatalf("AdoptNetnsRuntime: %v", err)
	}
	return rt
}

func newAdoptTestDriver(t *testing.T) *CHDriver {
	t.Helper()
	drv, err := New(Config{SocketDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return drv
}

// TestAdoptRuntime_InstallsIntoDriver proves the positive path: after
// AdoptRuntime, GuestNetworkFD returns the runtime's perimeter conn exactly
// once (the ownership-transfer contract GuestNetworkFD already enforces).
func TestAdoptRuntime_InstallsIntoDriver(t *testing.T) {
	drv := newAdoptTestDriver(t)
	rt := newTestAdoptedRuntime(t)
	id := domain.SandboxID{}

	if err := drv.AdoptRuntime(id, rt); err != nil {
		t.Fatalf("AdoptRuntime: %v", err)
	}

	fd, err := drv.GuestNetworkFD(t.Context(), id)
	if err != nil {
		t.Fatalf("GuestNetworkFD after AdoptRuntime: %v", err)
	}
	if fd == nil {
		t.Fatal("GuestNetworkFD returned nil fd")
	}

	// Second call must fail — ownership was already transferred.
	if _, err := drv.GuestNetworkFD(t.Context(), id); err == nil {
		t.Error("second GuestNetworkFD call succeeded; ownership-transfer guard was not installed correctly")
	}
}

// TestAdoptRuntime_RejectsNilRuntime is a precondition guard: a nil rt must
// be refused, not installed as a netState with a nil rt that would panic the
// first time Observe/Stop/teardownSandboxNet dereferences it.
func TestAdoptRuntime_RejectsNilRuntime(t *testing.T) {
	drv := newAdoptTestDriver(t)
	if err := drv.AdoptRuntime(domain.SandboxID{}, nil); err == nil {
		t.Fatal("expected error for nil runtime, got nil")
	}
}

// TestAdoptRuntime_RefusesDoubleAdopt is the mutation-bearing proof that
// AdoptRuntime does not silently overwrite an existing registration — which
// would leak the first runtime's fd/process-group ownership with nothing
// left able to Stop() it.
func TestAdoptRuntime_RefusesDoubleAdopt(t *testing.T) {
	drv := newAdoptTestDriver(t)
	id := domain.SandboxID{}
	first := newTestAdoptedRuntime(t)
	if err := drv.AdoptRuntime(id, first); err != nil {
		t.Fatalf("first AdoptRuntime: %v", err)
	}

	second := newTestAdoptedRuntime(t)
	err := drv.AdoptRuntime(id, second)
	if err == nil {
		t.Fatal("expected the second AdoptRuntime for the same sandbox ID to be refused")
	}
}
