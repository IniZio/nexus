//go:build linux

package supervisor

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startTrapper launches a shell that loops forever, with SIGTERM either handled
// (exits 0) or ignored, and returns its pid.
func startTrapper(t *testing.T, script string) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	// Mirror SpawnDetached: a goroutine reaps the child and closes the channel,
	// so the caller can distinguish "exited" from "zombie".
	exited := make(chan struct{})
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait(); close(exited) }()
	t.Cleanup(func() { <-waitErr })
	t.Cleanup(func() { _ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL) })
	// Give the shell a moment to install its trap; without this the SIGTERM can
	// arrive before the handler exists and the "graceful" case would escalate.
	time.Sleep(200 * time.Millisecond)
	return cmd, exited
}

// TestTerminateSupervisor_GracefulExitIsNotKilled asserts that a supervisor
// which honours SIGTERM is allowed to shut down on its own.
//
// This is the case that matters for correctness: the supervisor's SIGTERM path
// calls svc.Remove/svc.Stop, which stops the VM through the driver. SIGKILL
// skips all of that and strands the VM (see terminateSupervisor's doc comment
// for the reproduced orphan).
func TestTerminateSupervisor_GracefulExitIsNotKilled(t *testing.T) {
	cmd, exited := startTrapper(t, `trap 'exit 0' TERM; while :; do sleep 0.05; done`)

	start := time.Now()
	terminateSupervisor(cmd.Process.Pid, exited, 5*time.Second)
	elapsed := time.Since(start)

	if elapsed >= 5*time.Second {
		t.Errorf("waited the full grace period (%v) for a process that handles SIGTERM; "+
			"terminateSupervisor must return as soon as it exits", elapsed)
	}
	select {
	case <-exited:
	default:
		t.Error("helper had not exited when terminateSupervisor returned")
	}
}

// TestTerminateSupervisor_EscalatesToKill asserts the last resort still fires:
// a supervisor wedged before it installed its signal handler must not be left
// running, even though killing it is what strands the VM.
func TestTerminateSupervisor_EscalatesToKill(t *testing.T) {
	cmd, exited := startTrapper(t, `trap '' TERM; while :; do sleep 0.05; done`)

	const grace = 500 * time.Millisecond
	start := time.Now()
	terminateSupervisor(cmd.Process.Pid, exited, grace)
	elapsed := time.Since(start)

	if elapsed < grace {
		t.Errorf("escalated to SIGKILL after %v, before the %v grace elapsed — "+
			"a supervisor mid-teardown would be cut off", elapsed, grace)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Error("process still alive after terminateSupervisor; SIGKILL escalation did not fire")
	}
}

// TestTerminateSupervisor_AlreadyGone asserts the no-op path: an already-exited
// supervisor must not block for the grace period.
func TestTerminateSupervisor_AlreadyGone(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run /bin/true: %v", err)
	}

	closed := make(chan struct{})
	close(closed)
	start := time.Now()
	terminateSupervisor(cmd.Process.Pid, closed, 5*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("blocked %v on an already-exited process; must return immediately", elapsed)
	}
}
