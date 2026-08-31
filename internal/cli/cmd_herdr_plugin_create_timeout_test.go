package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestHerdrArmCreateGracefulCancel_SendsSIGTERMNotKill proves the TBD-3 fix at
// the mechanism level: when the create subprocess's context expires,
// herdrArmCreateGracefulCancel must deliver SIGTERM (which the child's own
// root context, wired via signal.NotifyContext in root.go, can catch and
// unwind from cooperatively) rather than the exec package's default
// Process.Kill() (SIGKILL, uncatchable, no grace).
//
// The child script traps SIGTERM and writes a sentinel file before exiting
// cleanly. Without herdrArmCreateGracefulCancel, the default cancel behaviour
// is an immediate SIGKILL that the trap can never run, so the sentinel file
// is never written. This is the exact distinction that determines whether
// BuildInVM's lifecycle.SyncAndStop ever gets a chance to run on a real
// timeout — see the herdrWorktreeCreateGraceTimeout doc comment.
func TestHerdrArmCreateGracefulCancel_SendsSIGTERMNotKill(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("signal semantics assumed are linux-specific")
	}

	dir := t.TempDir()
	sentinel := filepath.Join(dir, "got-term")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// "sleep 5 & wait $!" (rather than a bare foreground "sleep 5") so the
	// shell is interruptible by the trap immediately instead of deferring
	// signal delivery until the foreground child exits on its own.
	script := `trap 'echo got-term > "$1"; exit 0' TERM; sleep 5 & wait $!`
	cmd := exec.CommandContext(ctx, "sh", "-c", script, "sh", sentinel)
	herdrArmCreateGracefulCancel(cmd)
	// Keep the test fast: the grace period only needs to be long enough for
	// the trap to run, not the production 60s value.
	cmd.WaitDelay = 2 * time.Second

	_ = cmd.Run() // expected to return a cancellation-flavoured error; not asserted here

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected sentinel file from a caught SIGTERM, got stat error: %v", err)
	}
}

// TestHerdrArmCreateGracefulCancel_SetsFields is a narrow assertion that the
// helper wires both cmd.Cancel and cmd.WaitDelay, guarding against a partial
// edit (e.g. WaitDelay left at its zero value, which would make Cancel's
// SIGTERM meaningless because Wait would then read pipes to EOF indefinitely
// instead of ever escalating to a kill).
func TestHerdrArmCreateGracefulCancel_SetsFields(t *testing.T) {
	// exec.CommandContext already sets a non-nil default Cancel (Process.Kill,
	// i.e. SIGKILL) itself, so a nil-check precondition would not distinguish
	// "default Kill" from "our SIGTERM override". Assert WaitDelay instead,
	// which CommandContext leaves at its zero value: an unset WaitDelay after
	// arming Cancel is the "grace period" bug this helper exists to prevent
	// (Cancel alone, with WaitDelay left at zero, means Wait keeps reading
	// pipes to EOF instead of ever escalating to a kill on a wedged child).
	cmd := exec.CommandContext(context.Background(), "true")
	if cmd.WaitDelay != 0 {
		t.Fatalf("precondition: exec.CommandContext must leave WaitDelay at zero, got %v", cmd.WaitDelay)
	}
	herdrArmCreateGracefulCancel(cmd)
	if cmd.Cancel == nil {
		t.Fatal("herdrArmCreateGracefulCancel did not set cmd.Cancel")
	}
	if cmd.WaitDelay != herdrWorktreeCreateGraceTimeout {
		t.Fatalf("cmd.WaitDelay = %v, want %v", cmd.WaitDelay, herdrWorktreeCreateGraceTimeout)
	}
}

// TestHerdrWorktreeCreateLockTimeout_ExceedsWorstCaseCreate guards the
// invariant documented on herdrWorktreeCreateLockTimeout: a second concurrent
// caller waiting for the create-intent lock must never give up before the
// first caller's worst-case total time, which is now
// herdrWorktreeCreateTimeout (soft deadline) +
// herdrWorktreeCreateGraceTimeout (SIGTERM grace before a hard SIGKILL).
func TestHerdrWorktreeCreateLockTimeout_ExceedsWorstCaseCreate(t *testing.T) {
	worstCase := herdrWorktreeCreateTimeout + herdrWorktreeCreateGraceTimeout
	if herdrWorktreeCreateLockTimeout <= worstCase {
		t.Fatalf("herdrWorktreeCreateLockTimeout (%v) must exceed herdrWorktreeCreateTimeout+herdrWorktreeCreateGraceTimeout (%v)",
			herdrWorktreeCreateLockTimeout, worstCase)
	}
}
