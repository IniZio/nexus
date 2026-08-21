package cloudhypervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// fakeVirtiofsdBindThenDie writes a script that creates the socket file and
// THEN exits successfully. This is the shape that defeated the original
// socket-only readiness check: virtiofsd binds, readiness sees the socket and
// reports success, virtiofsd dies, and CH is left holding a vhost-user-fs
// device with no backend — which hangs the guest with no host-side error.
//
// The short sleep makes the ordering deterministic: the first readiness poll
// happens before the socket exists, so the check that must catch this is the
// liveness check on a LATER poll, not a lucky first-poll observation.
func fakeVirtiofsdBindThenDie(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "virtiofsd-bind-then-die")
	src := `#!/bin/sh
sock=""
while [ $# -gt 0 ]; do
  case "$1" in
    --socket-path) sock="$2"; shift 2 ;;
    *) shift ;;
  esac
done
sleep 0.1
[ -n "$sock" ] && touch "$sock"
exit 0
`
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatalf("write fake virtiofsd: %v", err)
	}
	return script
}

// TestSpawnVirtiofsd_BindThenDieIsNotReady pins the liveness check. Without it
// spawnVirtiofsd returns a nil error for a dead backend.
func TestSpawnVirtiofsd_BindThenDieIsNotReady(t *testing.T) {
	bin := fakeVirtiofsdBindThenDie(t)
	d := testDriver(t, bin, []domain.LiveMount{
		{HostPath: t.TempDir(), GuestPath: "/work"},
	})

	var id domain.SandboxID
	_, err := d.spawnVirtiofsdForMounts(t.Context(), id)
	if err == nil {
		t.Fatal("spawnVirtiofsdForMounts returned nil error for a virtiofsd that " +
			"bound its socket and then exited; a dead backend must not be reported ready")
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("error should name the exit-during-startup cause, got: %v", err)
	}
}

// TestProcessExited_LiveAndDead pins processExited itself in both directions,
// so the helper cannot silently start answering "false" for everything (which
// would restore the original defect while leaving the call site intact).
func TestProcessExited_LiveAndDead(t *testing.T) {
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatalf("start live process: %v", err)
	}
	t.Cleanup(func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	})
	if exited, state := processExited(live.Process.Pid); exited {
		t.Errorf("processExited reported a running process as exited (state %q)", state)
	}

	// A process that has exited but not been reaped is a zombie, and a zombie
	// is exited for our purposes — the backend is gone even though the PID
	// still resolves. Deliberately NOT calling Wait here is what creates it.
	dead := exec.Command("true")
	if err := dead.Start(); err != nil {
		t.Fatalf("start dead process: %v", err)
	}
	deadPID := dead.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	var exited bool
	var state string
	for time.Now().Before(deadline) {
		if exited, state = processExited(deadPID); exited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !exited {
		t.Errorf("processExited did not report an exited process as exited (state %q)", state)
	}
	_ = dead.Wait()
}
