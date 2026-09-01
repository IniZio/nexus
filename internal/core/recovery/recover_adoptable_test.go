package recovery_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	. "github.com/IniZio/nexus3/internal/core/recovery"
)

// withSupervisor sets a recorded (pid, sock) pair on the sandbox at creation.
func withSupervisor(pid int, sock string) sandboxOpt {
	return func(s *domain.Sandbox) {
		s.SupervisorPID = pid
		s.SupervisorSock = sock
	}
}

// deadSupervisorCheck simulates supervisor.CheckAndReconcile always finding
// the recorded supervisor dead.
func deadSupervisorCheck(pid int, sock string) (bool, error) { return false, nil }

// liveSupervisorCheck simulates supervisor.CheckAndReconcile always finding
// the recorded supervisor alive.
func liveSupervisorCheck(pid int, sock string) (bool, error) { return true, nil }

// erroringSupervisorCheck simulates the liveness primitive itself failing
// (e.g. a transient I/O error), as distinct from it succeeding and reporting
// "dead". This must never be conflated with deadSupervisorCheck: an
// indeterminate check result must never downgrade or mutate an
// already-correct record-level adoption.
var errSupervisorCheckFailed = errors.New("supervisor check: transient failure")

func erroringSupervisorCheck(pid int, sock string) (bool, error) {
	return false, errSupervisorCheckFailed
}

// TestRecover_LiveVMDeadSupervisor_Adoptable is the direct proof of AC-8: a
// sandbox whose VM is alive (Running) but whose recorded supervisor does not
// answer is reported as OutcomeAdoptable rather than plainly running, and the
// stale supervisor identity is cleared from the record. The VM itself must be
// left completely untouched (D-HSH-04).
func TestRecover_LiveVMDeadSupervisor_Adoptable(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	env.rec.WithSupervisorCheck(deadSupervisorCheck)

	sb := createSandbox(t, ctx, env.st, domain.Running, withSupervisor(12345, "/tmp/sock"))
	env.drv.SetRunning(sb.ID)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdoptable {
		t.Fatalf("want %s, got %s (reason: %q)", OutcomeAdoptable, out.Kind, out.Reason)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if updated.State != domain.Running {
		t.Errorf("VM must stay running (never stopped by recovery): got %s", updated.State)
	}
	if updated.SupervisorPID != 0 || updated.SupervisorSock != "" {
		t.Errorf("stale supervisor identity must be cleared, got pid=%d sock=%q", updated.SupervisorPID, updated.SupervisorSock)
	}

	// No destructive driver call was made.
	for _, c := range env.drv.Calls() {
		if c.Kind == "Stop" {
			t.Errorf("destructive Stop call made against an adoptable (live VM) sandbox")
		}
	}
}

// TestRecover_LiveVMLiveSupervisor_NotAdoptable is the negative-direction
// proof this motive has been burned by omitting before: a healthy sandbox
// (live VM, live supervisor) must NOT be classified adoptable and its
// supervisor identity must NOT be touched. A slow-but-alive supervisor must
// never look like a dead one.
func TestRecover_LiveVMLiveSupervisor_NotAdoptable(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	env.rec.WithSupervisorCheck(liveSupervisorCheck)

	sb := createSandbox(t, ctx, env.st, domain.Running, withSupervisor(12345, "/tmp/sock"))
	env.drv.SetRunning(sb.ID)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdopted {
		t.Fatalf("want %s, got %s (reason: %q)", OutcomeAdopted, out.Kind, out.Reason)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if updated.SupervisorPID != 12345 || updated.SupervisorSock != "/tmp/sock" {
		t.Errorf("live supervisor identity must not be touched, got pid=%d sock=%q", updated.SupervisorPID, updated.SupervisorSock)
	}
}

// TestRecover_NoSupervisorRecorded_NotAdoptable verifies that a sandbox which
// never recorded a supervisor identity (SupervisorPID == 0 — e.g. predates
// the field, or a lifecycle that never sets one) is left as plain
// OutcomeAdopted rather than misclassified adoptable. There is nothing to
// adopt when nothing was ever recorded.
func TestRecover_NoSupervisorRecorded_NotAdoptable(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	env.rec.WithSupervisorCheck(deadSupervisorCheck) // even though "dead", pid<=0 short-circuits

	sb := createSandbox(t, ctx, env.st, domain.Running) // no withSupervisor()
	env.drv.SetRunning(sb.ID)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdopted {
		t.Fatalf("want %s, got %s (reason: %q)", OutcomeAdopted, out.Kind, out.Reason)
	}
}

// TestRecover_SupervisorCheckUnwired_NoRegression verifies that a Recoverer
// built without WithSupervisorCheck (nil checkSupervisor — every other test
// in this package, and New's default) behaves exactly as before this slice:
// the cross-check is a no-op, never a false positive.
func TestRecover_SupervisorCheckUnwired_NoRegression(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t) // WithSupervisorCheck NOT called

	sb := createSandbox(t, ctx, env.st, domain.Running, withSupervisor(12345, "/tmp/sock"))
	env.drv.SetRunning(sb.ID)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdopted {
		t.Fatalf("want %s, got %s (reason: %q)", OutcomeAdopted, out.Kind, out.Reason)
	}
}

// TestRecover_LiveVMDeadSupervisor_Paused_Adoptable exercises the Paused
// branch of the switch (the second call site wired to
// applySupervisorLiveness), not just Running, so both switch arms are
// mutation-provable independently.
func TestRecover_LiveVMDeadSupervisor_Paused_Adoptable(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	env.rec.WithSupervisorCheck(deadSupervisorCheck)

	sb := createSandbox(t, ctx, env.st, domain.Paused, withSupervisor(999, "/tmp/sock2"))
	env.drv.SetPaused(sb.ID)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdoptable {
		t.Fatalf("want %s, got %s (reason: %q)", OutcomeAdoptable, out.Kind, out.Reason)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if updated.State != domain.Paused {
		t.Errorf("VM must stay paused (never stopped by recovery): got %s", updated.State)
	}
	if updated.SupervisorPID != 0 || updated.SupervisorSock != "" {
		t.Errorf("stale supervisor identity must be cleared, got pid=%d sock=%q", updated.SupervisorPID, updated.SupervisorSock)
	}
}

// TestRecover_LiveVMDeadSupervisor_AlreadyCorrectRecord_StillWrites proves
// that OutcomeAdoptable fires even when applyAdopt's own fast path found
// nothing to correct (record State/InstanceID/RemovalMarker already match
// the substrate) — the write must still happen to clear the stale supervisor
// identity. This is the case that would silently regress if the `wrote ||
// wroteSup` OR were narrowed back to `wrote` alone.
func TestRecover_LiveVMDeadSupervisor_AlreadyCorrectRecord_StillWrites(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	env.rec.WithSupervisorCheck(deadSupervisorCheck)

	sb := createSandbox(t, ctx, env.st, domain.Running, withSupervisor(555, "/tmp/sock3"))
	env.drv.SetRunning(sb.ID) // observed InstanceID will match what applyAdopt writes on first pass

	// First recover pass: corrects InstanceID and clears supervisor identity.
	if _, err := env.rec.Recover(ctx); err != nil {
		t.Fatalf("first Recover: %v", err)
	}
	// Re-arm the supervisor identity as if a fresh (still-dead) supervisor
	// pid were persisted again, but leave the record otherwise fully correct
	// so applyAdopt's fast path returns wrote=false on the second pass.
	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	updated.SupervisorPID = 555
	updated.SupervisorSock = "/tmp/sock3"
	if err := env.st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		*rec = updated
		return nil
	}); err != nil {
		t.Fatalf("re-arm supervisor identity: %v", err)
	}

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdoptable {
		t.Fatalf("want %s, got %s (reason: %q)", OutcomeAdoptable, out.Kind, out.Reason)
	}
	final, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get final: %v", err)
	}
	if final.SupervisorPID != 0 || final.SupervisorSock != "" {
		t.Errorf("second pass must still clear supervisor identity even though applyAdopt's own fast path had nothing to correct; got pid=%d sock=%q", final.SupervisorPID, final.SupervisorSock)
	}
}

// TestRecover_SupervisorCheckErrors_NeverDowngradesOrMutates proves the
// safety claim asserted only in a comment at applySupervisorLiveness's error
// branch (recover.go): when the liveness primitive itself fails (as opposed
// to succeeding and reporting "dead"), the outcome must stay OutcomeAdopted
// — never OutcomeAdoptable — and the persisted SupervisorPID/SupervisorSock
// must be left completely untouched. An indeterminate check result is not
// evidence of death; treating it as one would be the exact "slow supervisor
// misread as a dead one" hazard this motive is guarding against, just routed
// through the error path instead of a false "alive" reading.
func TestRecover_SupervisorCheckErrors_NeverDowngradesOrMutates(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	env.rec.WithSupervisorCheck(erroringSupervisorCheck)

	sb := createSandbox(t, ctx, env.st, domain.Running, withSupervisor(777, "/tmp/sock4"))
	env.drv.SetRunning(sb.ID)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdopted {
		t.Fatalf("a supervisor-check error must never downgrade the outcome: want %s, got %s (reason: %q)",
			OutcomeAdopted, out.Kind, out.Reason)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if updated.SupervisorPID != 777 || updated.SupervisorSock != "/tmp/sock4" {
		t.Errorf("a supervisor-check error must never mutate the persisted supervisor identity; got pid=%d sock=%q",
			updated.SupervisorPID, updated.SupervisorSock)
	}
}
