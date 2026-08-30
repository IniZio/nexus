package recovery_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	. "github.com/IniZio/nexus3/internal/core/recovery"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

// testEnv holds the components of a recovery test.
type testEnv struct {
	rec *Recoverer
	st  *store.FileStore
	drv *fake.FakeDriver
	dir string // store root; used for injecting corrupt records
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := fake.New()
	return testEnv{
		rec: New(st, drv),
		st:  st,
		drv: drv,
		dir: dir,
	}
}

// sandboxOpt is a functional option for createSandbox.
type sandboxOpt func(*domain.Sandbox)

func withRemoveOnExit() sandboxOpt {
	return func(s *domain.Sandbox) { s.RemoveOnExit = true }
}

// createSandbox persists a sandbox and returns it.
func createSandbox(t *testing.T, ctx context.Context, st store.Store, state domain.State, opts ...sandboxOpt) domain.Sandbox {
	t.Helper()
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "test",
		Project: "proj",
		State:   state,
	}
	for _, o := range opts {
		o(&sb)
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}
	return sb
}

// findOutcome returns the SandboxOutcome for id from report, or fails the test.
func findOutcome(t *testing.T, report Report, id domain.SandboxID) SandboxOutcome {
	t.Helper()
	for _, o := range report.Outcomes {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("no outcome for sandbox %s in report", id)
	panic("unreachable")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestRecover_OldBugRegression_StaleRecordIgnoredWhenVMIsAlive is the
// regression test for the predecessor bug: adoption was gated on the recorded
// state, so a sandbox whose record said "snapshotting" (a transient state that
// forced stale records in the old system — equivalent here to stored Paused
// while the VM is Running after a resume) matched neither the adoption gate
// nor the reaper gate, causing a healthy VM to be destroyed.
//
// Here: stored state is Paused (snapshot was in progress, VM was resumed but
// crash hit before the record was updated). The VM is actually Running.
// Recovery must adopt the VM and correct the record to Running — never destroy
// or error a live VM regardless of what the record says.
func TestRecover_OldBugRegression_StaleRecordIgnoredWhenVMIsAlive(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Stored record says Paused — stale from a crash after resume.
	sb := createSandbox(t, ctx, env.st, domain.Paused)
	// The VM is actually alive and running (the resume succeeded).
	env.drv.SetRunning(sb.ID)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdopted {
		t.Errorf("want %s, got %s (reason: %q)", OutcomeAdopted, out.Kind, out.Reason)
	}

	// Record must be corrected to Running — the VM is the authority.
	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if updated.State != domain.Running {
		t.Errorf("want record state=running, got %s", updated.State)
	}
}

// TestRecover_Unknown_IndeterminateNothingDestructive verifies that when the
// driver cannot determine the VM's state, recovery reports the sandbox as
// indeterminate and takes no destructive action. Unknown must never be treated
// as Absent.
func TestRecover_Unknown_IndeterminateNothingDestructive(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Running)
	env.drv.SetObserveError(errors.New("substrate unreachable"))

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeIndeterminate {
		t.Errorf("want %s, got %s (reason: %q)", OutcomeIndeterminate, out.Kind, out.Reason)
	}

	// Record must be completely untouched.
	unchanged, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if unchanged.State != domain.Running {
		t.Errorf("record was modified: want running, got %s", unchanged.State)
	}

	// No destructive driver calls — specifically no Stop.
	for _, c := range env.drv.Calls() {
		if c.Kind == fake.CallStop {
			t.Errorf("destructive call %s was made after Unknown observation", c.Kind)
		}
	}
}

// TestRecover_PausedAbsent_MemoryLostSurfacedInReport verifies that when a
// paused sandbox's VM is absent (host reboot / VMM kill / power loss destroyed
// its in-RAM memory state), recovery transitions the record to stopped via
// TriggerSubstrateLost and surfaces the memory loss in the report reason.
func TestRecover_PausedAbsent_MemoryLostSurfacedInReport(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Paused)
	env.drv.SimulateHostReboot() // all Paused VMs become Absent

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeResolvedStopped {
		t.Errorf("want %s, got %s (reason: %q)", OutcomeResolvedStopped, out.Kind, out.Reason)
	}
	if !strings.Contains(out.Reason, "memory") {
		t.Errorf("reason must mention memory loss, got %q", out.Reason)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.State != domain.Stopped {
		t.Errorf("want stopped, got %s", updated.State)
	}
}

// TestRecover_RemoveOnExit_MarkerAbsent_Removed verifies that when
// RemoveOnExit is set, the VM is absent, and no removal marker is present,
// the sandbox is removed. Removal is unconditional on exit status — a non-zero
// primary command exit still triggers removal, because RemoveOnExit records
// the user's intent at creation time, not the process exit code.
func TestRecover_RemoveOnExit_MarkerAbsent_Removed(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// VM absent (the --rm sandbox ran and exited; the process crashed before
	// removal). RemoveOnExit is set. No removal marker.
	sb := createSandbox(t, ctx, env.st, domain.Running, withRemoveOnExit())
	// drv has no entry for sb.ID → Observe returns Absent.

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeRemoved {
		t.Errorf("want %s, got %s (reason: %q)", OutcomeRemoved, out.Kind, out.Reason)
	}

	// Sandbox must no longer exist.
	_, err = env.st.Get(ctx, sb.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound after removal, got %v", err)
	}
}

// TestRecover_RemoveOnExit_NonZeroExit_StillRemoved confirms that removal
// with RemoveOnExit is unconditional on exit status. The recovery package
// never inspects exit codes; the RemoveOnExit flag alone drives the decision.
// This test makes the non-zero-exit path explicit and named.
func TestRecover_RemoveOnExit_NonZeroExit_StillRemoved(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Simulate: primary command exited with non-zero status, VM is absent,
	// no removal marker set (the process crashed or died before starting removal).
	sb := createSandbox(t, ctx, env.st, domain.Running, withRemoveOnExit())
	// No drv.SetRunning → Observe returns Absent, as after a non-zero exit.

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeRemoved {
		t.Errorf("want %s (non-zero exit still removed), got %s (reason: %q)",
			OutcomeRemoved, out.Kind, out.Reason)
	}
}

// TestRecover_RemoveOnExit_StopCalledBeforeDelete verifies that on the --rm
// delete path recovery calls driver.Stop before store.Delete. This handles the
// "VMM alive but VM-less" scenario: a crash between vm.delete and vmm.shutdown
// in the driver's Stop sequence leaves an empty VMM process alive. Observe
// correctly reports Absent for such a VMM (a VMM with no VM has no VM), so the
// process is indistinguishable from a plain absent VM at the driver interface.
// Without the Stop call, the orphaned process would never be cleaned up.
//
// Two sub-tests: the normal case (Stop succeeds) and the non-fatal error case
// (Stop fails but Delete still runs — failing to delete because cleanup failed
// would resurrect the wedge class fixed twice already).
func TestRecover_RemoveOnExit_StopCalledBeforeDelete(t *testing.T) {
	t.Run("stop_called_and_record_deleted", func(t *testing.T) {
		ctx := context.Background()
		env := newTestEnv(t)

		// --rm sandbox, Running state, no VM in the driver (Observe → Absent).
		// This fixture models both the plain-absent case and the VMM-alive-but-
		// VM-less case: at the driver interface they are identical.
		sb := createSandbox(t, ctx, env.st, domain.Running, withRemoveOnExit())

		report, err := env.rec.Recover(ctx)
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		out := findOutcome(t, report, sb.ID)
		if out.Kind != OutcomeRemoved {
			t.Errorf("want %s, got %s (reason: %q)", OutcomeRemoved, out.Kind, out.Reason)
		}

		// Stop must have been called.
		var stopCalled bool
		for _, c := range env.drv.Calls() {
			if c.Kind == fake.CallStop && c.ID == sb.ID {
				stopCalled = true
				break
			}
		}
		if !stopCalled {
			t.Errorf("driver.Stop was not called on the --rm delete path — " +
				"without Stop, an orphaned VMM process (alive but VM-less) would " +
				"be leaked permanently")
		}

		// Record must be gone.
		if _, err := env.st.Get(ctx, sb.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("want ErrNotFound after --rm removal, got %v", err)
		}
	})

	t.Run("stop_error_is_nonfatal_delete_still_runs", func(t *testing.T) {
		ctx := context.Background()
		env := newTestEnv(t)

		// Inject a Stop error.
		env.drv.SetStopError(errors.New("vmm unreachable"))
		sb := createSandbox(t, ctx, env.st, domain.Running, withRemoveOnExit())

		report, err := env.rec.Recover(ctx)
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		out := findOutcome(t, report, sb.ID)

		// A Stop error must not prevent deletion — the wedge risk is worse than
		// the cleanup failure.
		if out.Kind != OutcomeRemoved {
			t.Errorf("Stop error must be non-fatal: want %s, got %s (reason: %q) — "+
				"failing to delete because cleanup failed resurrects the wedge class",
				OutcomeRemoved, out.Kind, out.Reason)
		}
		if _, err := env.st.Get(ctx, sb.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Stop error: sandbox must still be deleted, got %v", err)
		}
	})
}

// TestRecover_RemovalMarkerPresent_Terminal verifies that a set removal marker
// means removal was in progress when the crash hit. Recovery reports the
// sandbox as terminal (manual cleanup required) and must NOT delete it or
// retry the removal.
func TestRecover_RemovalMarkerPresent_Terminal(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Running, withRemoveOnExit())
	// Simulate: removal marker was written, then the process crashed before Delete.
	if err := env.st.SetRemovalMarker(ctx, sb.ID); err != nil {
		t.Fatalf("SetRemovalMarker: %v", err)
	}
	// VM is absent (it had already stopped when removal began).

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeTerminal {
		t.Errorf("want %s, got %s (reason: %q)", OutcomeTerminal, out.Kind, out.Reason)
	}

	// Sandbox must still exist — removal is terminal, not retried.
	if _, err := env.st.Get(ctx, sb.ID); err != nil {
		t.Errorf("sandbox should still exist after terminal outcome: %v", err)
	}
}

// TestRecover_Idempotent verifies that a second consecutive Recover call
// makes no further changes. Two sub-cases: the paused→stopped path (most
// interesting for idempotence) and the adopt path.
func TestRecover_Idempotent(t *testing.T) {
	t.Run("paused_to_stopped", func(t *testing.T) {
		ctx := context.Background()
		env := newTestEnv(t)

		sb := createSandbox(t, ctx, env.st, domain.Paused)
		env.drv.SimulateHostReboot() // paused becomes absent

		// First run: resolves paused → stopped.
		r1, err := env.rec.Recover(ctx)
		if err != nil {
			t.Fatalf("Recover (first): %v", err)
		}
		out1 := findOutcome(t, r1, sb.ID)
		if out1.Kind != OutcomeResolvedStopped {
			t.Errorf("first run: want %s, got %s", OutcomeResolvedStopped, out1.Kind)
		}

		// Second run: stopped + absent → no action.
		r2, err := env.rec.Recover(ctx)
		if err != nil {
			t.Fatalf("Recover (second): %v", err)
		}
		out2 := findOutcome(t, r2, sb.ID)
		if out2.Kind != OutcomeUnchanged {
			t.Errorf("second run: want %s, got %s (reason: %q)", OutcomeUnchanged, out2.Kind, out2.Reason)
		}

		updated, err := env.st.Get(ctx, sb.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if updated.State != domain.Stopped {
			t.Errorf("state must remain stopped, got %s", updated.State)
		}
	})

	t.Run("adopt_running", func(t *testing.T) {
		ctx := context.Background()
		env := newTestEnv(t)

		// Stale stored state; VM is running.
		sb := createSandbox(t, ctx, env.st, domain.Paused)
		env.drv.SetRunning(sb.ID)

		// First run: corrects record to running.
		r1, err := env.rec.Recover(ctx)
		if err != nil {
			t.Fatalf("Recover (first): %v", err)
		}
		out1 := findOutcome(t, r1, sb.ID)
		if out1.Kind != OutcomeAdopted {
			t.Errorf("first run: want %s, got %s", OutcomeAdopted, out1.Kind)
		}

		// Second run: running + running → adopted (no write needed).
		r2, err := env.rec.Recover(ctx)
		if err != nil {
			t.Fatalf("Recover (second): %v", err)
		}
		out2 := findOutcome(t, r2, sb.ID)
		if out2.Kind != OutcomeAdopted {
			t.Errorf("second run: want %s, got %s (reason: %q)", OutcomeAdopted, out2.Kind, out2.Reason)
		}
	})
}

// TestRecover_CorruptRecord_SkippedNotPanic verifies that a sandbox directory
// with a corrupt record.json does not panic and does not block recovery of
// other healthy sandboxes. store.List silently skips unreadable records; this
// test confirms that the skip propagates correctly through recovery.
func TestRecover_CorruptRecord_SkippedNotPanic(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Create a healthy sandbox.
	healthy := createSandbox(t, ctx, env.st, domain.Running)
	env.drv.SetRunning(healthy.ID)

	// Inject a corrupt sandbox directory directly into the store layout.
	// store.List walks root/sandboxes/ and skips entries it cannot decode.
	corruptID := domain.NewSandboxID()
	corruptDir := filepath.Join(env.dir, "sandboxes", corruptID.String())
	if err := os.MkdirAll(corruptDir, 0700); err != nil {
		t.Fatalf("mkdir corrupt dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(corruptDir, "record.json"),
		[]byte(`{{{not valid json`),
		0600,
	); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	// Must not panic and must still recover the healthy sandbox.
	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// The corrupt record is silently skipped by store.List; it never reaches
	// recoverByID. The healthy sandbox is the only outcome.
	if len(report.Outcomes) != 1 {
		t.Errorf("want 1 outcome (healthy sandbox only), got %d", len(report.Outcomes))
	}
	out := findOutcome(t, report, healthy.ID)
	if out.Kind != OutcomeAdopted {
		t.Errorf("healthy sandbox: want %s, got %s (reason: %q)", OutcomeAdopted, out.Kind, out.Reason)
	}
}

// TestRecover_Unknown_NilError_Indeterminate verifies that when the driver
// returns State=Unknown with a nil error — the "genuinely indeterminate"
// observation where the driver successfully queried the substrate but cannot
// determine VM state — recovery reports OutcomeIndeterminate and takes no
// destructive action.
//
// Biting assertion: without the "|| obs.State == driver.Unknown" guard in
// recoverByID, Unknown+nil error falls through to the switch's default case,
// which produces reason "unexpected run state unknown from driver; no action
// taken". The guard produces "driver could not determine VM state; no action
// taken". Asserting the latter fails when the guard is deleted.
func TestRecover_Unknown_NilError_Indeterminate(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Running)
	env.drv.SetRunning(sb.ID)
	env.drv.SetIndeterminate(true)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeIndeterminate {
		t.Errorf("Unknown+nil error: want %s, got %s (reason: %q)",
			OutcomeIndeterminate, out.Kind, out.Reason)
	}
	// Biting: the guard's reason differs from the switch default's reason.
	// This assertion fails when the || obs.State == driver.Unknown guard is deleted.
	if !strings.Contains(out.Reason, "driver could not determine") {
		t.Errorf("Unknown+nil error: Reason=%q must contain \"driver could not determine\" — "+
			"this fires when the || obs.State == driver.Unknown guard is missing "+
			"(the switch default case gives \"unexpected run state\" instead)", out.Reason)
	}

	// No destructive calls.
	for _, c := range env.drv.Calls() {
		if c.Kind == fake.CallStop {
			t.Errorf("Unknown+nil error: destructive call %s made; "+
				"Unknown must never trigger destruction", c.Kind)
		}
	}

	// Record must be completely untouched.
	unchanged, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Unknown+nil error: Get: %v", err)
	}
	if unchanged.State != domain.Running {
		t.Errorf("Unknown+nil error: record changed to %s; must stay Running", unchanged.State)
	}
}

// TestRecover_Unknown_NilError_PausedNotResolvedToStopped verifies that a
// Paused sandbox observed as Unknown+nil error is NOT resolved to stopped.
// Resolving to stopped is Absent behaviour — treating Unknown as Absent
// would be the bug.
func TestRecover_Unknown_NilError_PausedNotResolvedToStopped(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Paused)
	env.drv.SetPaused(sb.ID)
	env.drv.SetIndeterminate(true)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)

	// Must not be treated as Absent (which produces OutcomeResolvedStopped).
	if out.Kind == OutcomeResolvedStopped {
		t.Errorf("Unknown+nil error on Paused sandbox: got %s — "+
			"Unknown must not be treated as Absent; that is the bug", out.Kind)
	}
	if out.Kind != OutcomeIndeterminate {
		t.Errorf("Unknown+nil error on Paused sandbox: want %s, got %s (reason: %q)",
			OutcomeIndeterminate, out.Kind, out.Reason)
	}
	// Biting: fails when the || obs.State == driver.Unknown guard is deleted.
	if !strings.Contains(out.Reason, "driver could not determine") {
		t.Errorf("Unknown+nil error on Paused sandbox: Reason=%q must contain "+
			"\"driver could not determine\" — fires when the guard is missing", out.Reason)
	}

	// Record must still be Paused — no transition occurred.
	unchanged, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Unknown+nil error on Paused sandbox: Get: %v", err)
	}
	if unchanged.State != domain.Paused {
		t.Errorf("Unknown+nil error on Paused sandbox: record changed to %s; must stay Paused",
			unchanged.State)
	}
}

// TestRecover_Unknown_NilError_RemoveOnExitNotRemoved verifies that a
// RemoveOnExit sandbox observed as Unknown+nil error is NOT removed.
// Removal is triggered only by Absent; treating Unknown as Absent would
// silently destroy a VM that may still be running.
func TestRecover_Unknown_NilError_RemoveOnExitNotRemoved(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Running, withRemoveOnExit())
	env.drv.SetRunning(sb.ID)
	env.drv.SetIndeterminate(true)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)

	// Must not be removed — that is Absent behaviour.
	if out.Kind == OutcomeRemoved {
		t.Errorf("Unknown+nil error on --rm sandbox: got %s — "+
			"Unknown must not trigger removal; the VM may still be running", out.Kind)
	}
	if out.Kind != OutcomeIndeterminate {
		t.Errorf("Unknown+nil error on --rm sandbox: want %s, got %s (reason: %q)",
			OutcomeIndeterminate, out.Kind, out.Reason)
	}
	// Biting: fails when the || obs.State == driver.Unknown guard is deleted.
	if !strings.Contains(out.Reason, "driver could not determine") {
		t.Errorf("Unknown+nil error on --rm sandbox: Reason=%q must contain "+
			"\"driver could not determine\" — fires when the guard is missing", out.Reason)
	}

	// Sandbox must still exist in the store.
	if _, err := env.st.Get(ctx, sb.ID); err != nil {
		t.Errorf("Unknown+nil error on --rm sandbox: sandbox should still exist: %v", err)
	}
}

// ── Defect regression: --rm from never-started states ────────────────────────

// TestRecover_RemoveOnExit_Created_Unchanged verifies that a sandbox in the
// Created state (never started) with --rm set is NOT removed during recovery.
// The lifecycle machine has no removal edge from Created; the sandbox has never
// run and there is nothing to remove.
//
// Biting assertion: without the machine-routing fix in resolveAbsent, the bare
// "if sb.RemoveOnExit" block deletes the sandbox unconditionally, producing
// OutcomeRemoved instead of OutcomeUnchanged.
func TestRecover_RemoveOnExit_Created_Unchanged(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Created state: sandbox was created but never started. VM absent by default.
	sb := createSandbox(t, ctx, env.st, domain.Created, withRemoveOnExit())

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)

	// Biting: without the fix this is OutcomeRemoved.
	if out.Kind != OutcomeUnchanged {
		t.Errorf("Created+--rm: want %s (sandbox never ran, nothing to remove), "+
			"got %s (reason: %q) — "+
			"fix: route --rm removal through r.mach.Next; Created has no removal edge",
			OutcomeUnchanged, out.Kind, out.Reason)
	}

	// Sandbox must still exist.
	if _, err := env.st.Get(ctx, sb.ID); err != nil {
		t.Errorf("Created+--rm: sandbox must still exist after recovery, got err=%v — "+
			"without the fix the sandbox is deleted", err)
	}
}

// TestRecover_RemoveOnExit_Stopped_Unchanged verifies that a sandbox in the
// Stopped state with --rm set is NOT removed during recovery. The lifecycle
// machine has no removal edge from Stopped.
//
// Biting assertion: without the machine-routing fix the sandbox is deleted.
func TestRecover_RemoveOnExit_Stopped_Unchanged(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Stopped, withRemoveOnExit())

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)

	// Biting: without the fix this is OutcomeRemoved.
	if out.Kind != OutcomeUnchanged {
		t.Errorf("Stopped+--rm: want %s (sandbox already stopped, nothing to remove), "+
			"got %s (reason: %q) — "+
			"fix: route --rm removal through r.mach.Next; Stopped has no removal edge",
			OutcomeUnchanged, out.Kind, out.Reason)
	}

	if _, err := env.st.Get(ctx, sb.ID); err != nil {
		t.Errorf("Stopped+--rm: sandbox must still exist, got err=%v", err)
	}
}

// TestRecover_RemoveOnExit_Error_Unchanged verifies that a sandbox in the
// Error state with --rm set is NOT removed during recovery. The lifecycle
// machine has no removal edge from Error.
//
// Biting assertion: without the machine-routing fix the sandbox is deleted.
func TestRecover_RemoveOnExit_Error_Unchanged(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Error, withRemoveOnExit())

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)

	// Biting: without the fix this is OutcomeRemoved.
	if out.Kind != OutcomeUnchanged {
		t.Errorf("Error+--rm: want %s (sandbox in error state, nothing to remove), "+
			"got %s (reason: %q) — "+
			"fix: route --rm removal through r.mach.Next; Error has no removal edge",
			OutcomeUnchanged, out.Kind, out.Reason)
	}

	if _, err := env.st.Get(ctx, sb.ID); err != nil {
		t.Errorf("Error+--rm: sandbox must still exist, got err=%v", err)
	}
}

// TestRecover_Adopt_PausedWithMarker_MarkerCleared verifies that the adopt
// path clears the removal marker when the VM is observed as Paused — not only
// when it is Running. The ClearRemovalMarker call must be unconditional on the
// observed live state (Running or Paused), not gated on Running alone.
//
// Structural coverage without this test: adopt() calls ClearRemovalMarker
// before the state-correction branch, so a Paused VM exercises the same code
// path. But "same code path" is only structurally safe if the call stays
// above the state branch — a refactor that moves the clear inside a
// Running-only branch would silently break Paused sandboxes. This test bites
// on that refactor.
//
// Biting assertion: if ClearRemovalMarker is moved inside a Running-only
// guard (e.g. "if sb.RemovalMarker && observed == domain.Running"), this test
// fails because afterAdopt.RemovalMarker is still true.
func TestRecover_Adopt_PausedWithMarker_MarkerCleared(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Sandbox recorded as Paused, removal marker set (interrupted remove attempt
	// while the VM was alive — exactly the state that triggered the wedge bug).
	sb := createSandbox(t, ctx, env.st, domain.Paused)
	env.drv.SetPaused(sb.ID)
	if err := env.st.SetRemovalMarker(ctx, sb.ID); err != nil {
		t.Fatalf("SetRemovalMarker: %v", err)
	}

	// Recovery observes Paused → adopt path runs.
	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdopted {
		t.Errorf("want %s (VM is alive and Paused), got %s (reason: %q)",
			OutcomeAdopted, out.Kind, out.Reason)
	}

	// Biting assertion: the removal marker must be cleared.
	// Without the fix (ClearRemovalMarker only in Running branch), this fails
	// because afterAdopt.RemovalMarker is still true — wedging the sandbox.
	afterAdopt, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if afterAdopt.RemovalMarker {
		t.Errorf("removal marker still set after adopt of Paused VM; " +
			"it must be cleared unconditionally for any live state (Running or Paused), " +
			"not only when observed Running — " +
			"bites when ClearRemovalMarker is moved inside a Running-only branch")
	}
}

// ── Defect regression: abandoned removal marker wedge sequence ────────────────

// TestRecover_AbandonedMarker_Cleared_WhenVMAlive verifies the full four-step
// wedge sequence that Defect 2 introduced:
//
//  1. A Running --rm sandbox has its removal marker set (failed removal attempt
//     left the marker), but the VM is still alive.
//  2. Recovery observes the live VM → adopt path. The marker must be CLEARED
//     because the live substrate proves removal did not complete.
//  3. The VM later stops cleanly (state→Stopped, VM absent, no marker).
//  4. Recovery runs again → OutcomeUnchanged, NOT OutcomeTerminal.
//
// Without the fix: step 2 notes the marker but does NOT clear it. At step 4
// the marker is still set, so resolveAbsent returns OutcomeTerminal, permanently
// wedging a healthy sandbox as "terminal / manual cleanup required".
//
// Biting assertion: at step 4, want OutcomeUnchanged; without the fix it is
// OutcomeTerminal.
func TestRecover_AbandonedMarker_Cleared_WhenVMAlive(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Step 1: Running sandbox, --rm, VM alive, removal marker set (abandoned rm).
	sb := createSandbox(t, ctx, env.st, domain.Running, withRemoveOnExit())
	env.drv.SetRunning(sb.ID)
	if err := env.st.SetRemovalMarker(ctx, sb.ID); err != nil {
		t.Fatalf("step 1 SetRemovalMarker: %v", err)
	}

	// Step 2: Recovery — VM is alive, so adopt path runs.
	report1, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("step 2 Recover: %v", err)
	}
	out1 := findOutcome(t, report1, sb.ID)
	if out1.Kind != OutcomeAdopted {
		t.Errorf("step 2: want %s (VM alive), got %s (reason: %q)",
			OutcomeAdopted, out1.Kind, out1.Reason)
	}

	// Step 2 assertion: marker must be cleared by the adopt path.
	afterAdopt, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("step 2 Get: %v", err)
	}
	if afterAdopt.RemovalMarker {
		t.Errorf("step 2: removal marker must be cleared after adopt with live VM; " +
			"without the fix (ClearRemovalMarker call missing in adopt) it stays set, " +
			"which wedges the sandbox at step 4")
	}

	// Step 3: Simulate clean stop — VM exits, store state updated to Stopped.
	env.drv.SimulateCrash(sb.ID) // removes the VM from the fake driver table
	if err := env.st.Update(ctx, sb.ID, func(s *domain.Sandbox) error {
		s.State = domain.Stopped
		return nil
	}); err != nil {
		t.Fatalf("step 3 Update to Stopped: %v", err)
	}

	// Step 4: Recovery — Stopped + absent + no marker → OutcomeUnchanged.
	// Without the fix the marker was left set → OutcomeTerminal.
	env.drv.ResetCalls()
	report2, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("step 4 Recover: %v", err)
	}
	out2 := findOutcome(t, report2, sb.ID)
	if out2.Kind != OutcomeUnchanged {
		t.Errorf("step 4: after clean stop, want %s, got %s (reason: %q) — "+
			"OutcomeTerminal here means the marker was not cleared at step 2; "+
			"fix: call r.st.ClearRemovalMarker in the adopt path when marker is set",
			OutcomeUnchanged, out2.Kind, out2.Reason)
	}
}

// ── Concurrency tests ─────────────────────────────────────────────────────────

// gatedDriver wraps a FakeDriver and enforces a deterministic interleaving for
// the concurrency test. On the first Observe call that returns Absent it:
//  1. Closes recoveryObserved to signal the concurrent Start goroutine.
//  2. Blocks until startCommitted is closed, or until a 500 ms timeout elapses.
//
// This gate simulates the race window between an outer Observe and a
// concurrent service.Start:
//   - OLD CODE (Observe outside the lock): the gate fires before the flock is
//     acquired, so Start's store.Update succeeds immediately; startCommitted
//     closes quickly; Observe returns a stale Absent. Recovery then overwrites
//     the live Running record with Stopped — the bug.
//   - NEW CODE (Observe inside the lock): the gate fires while the flock is
//     held. Start's store.Update blocks waiting for the flock; startCommitted
//     never closes before the 500 ms timeout. Observe returns Absent (under
//     lock). Recovery writes Stopped (inside the lock) then releases; Start
//     acquires the lock and writes Running. Final state: Running — consistent.
type gatedDriver struct {
	*fake.FakeDriver
	once             sync.Once
	recoveryObserved chan struct{} // closed by first Absent observation
	startCommitted   chan struct{} // closed by Start goroutine when done
}

func newGatedDriver(drv *fake.FakeDriver) *gatedDriver {
	return &gatedDriver{
		FakeDriver:       drv,
		recoveryObserved: make(chan struct{}),
		startCommitted:   make(chan struct{}),
	}
}

func (d *gatedDriver) Observe(ctx context.Context, id domain.SandboxID) (driver.Observation, error) {
	obs, err := d.FakeDriver.Observe(ctx, id)
	if obs.State == driver.Absent {
		d.once.Do(func() {
			close(d.recoveryObserved)
			select {
			case <-d.startCommitted:
			case <-time.After(500 * time.Millisecond):
				// Start could not commit because it is blocked on the flock that
				// recovery holds (the correct outcome). Release and proceed.
			case <-ctx.Done():
			}
		})
	}
	return obs, err
}

// TestRecover_Concurrent_ConsistentWithStart proves that recovery never writes
// Stopped over a live Running VM when service.Start commits a VM concurrently.
//
// The test uses gatedDriver to force a deterministic interleaving:
//
//	recover goroutine           |  start goroutine
//	─────────────────────────── | ──────────────────────────────
//	Observe → Absent            |
//	(gate: signal start, wait)  |  st.Update: rec.State=Running
//	                            |  drv.SetRunning(id)
//	                            |  close startCommitted
//	(gate unblocks)             |
//	decide: stored=Running,     |
//	  observed=Absent → Stopped |
//	st.Update: write Stopped    |  ← BUG in old code
//
// With the fix (Observe inside Update callback): Start blocks on the flock
// while recovery holds it. startCommitted never closes before the 500 ms
// timeout. Recovery decides Absent under the lock. The post-lock state is
// consistent (Start wins and writes Running after).
//
// Biting assertion: with old code, finalRecord.State == Stopped while the
// driver still reports the VM as Running — an orphaned live VM. The test
// catches this as the invariant "if driver=Running then record≠Stopped".
func TestRecover_Concurrent_ConsistentWithStart(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	fs, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	rawDrv := fake.New()
	gated := newGatedDriver(rawDrv)

	// Sandbox stored as Running; driver has NO entry (simulates a state where
	// the VM exited or was never started in the current session).
	sb, err := func() (domain.Sandbox, error) {
		s := domain.Sandbox{
			ID:      domain.NewSandboxID(),
			Name:    "concurrent-test",
			Project: "proj",
			State:   domain.Running,
		}
		return s, fs.Create(ctx, s)
	}()
	if err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}

	rec := New(fs, gated)

	// Start goroutine: simulates service.Start committing a live Running VM
	// concurrently with recovery's observation phase.
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		// Wait until recovery has observed Absent before racing against it.
		select {
		case <-gated.recoveryObserved:
		case <-time.After(5 * time.Second):
			return // test will fail on its own assertion
		}

		// Write Running + live VM to the store. Under the fix, this blocks on
		// the per-sandbox flock held by recovery's Update callback.
		_ = fs.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
			rec.State = domain.Running
			rec.InstanceID = "live-instance-id"
			return nil
		})
		rawDrv.SetRunning(sb.ID)
		close(gated.startCommitted)
	}()

	// Run recovery. Must complete within a reasonable time.
	recoverDone := make(chan error, 1)
	go func() {
		_, err := rec.RecoverOne(ctx, sb.ID)
		recoverDone <- err
	}()

	select {
	case err := <-recoverDone:
		if err != nil {
			t.Fatalf("RecoverOne: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RecoverOne deadlocked or timed out")
	}

	// Wait for the start goroutine too.
	select {
	case <-startDone:
	case <-time.After(5 * time.Second):
		t.Fatal("start goroutine timed out")
	}

	// ── Key invariant ─────────────────────────────────────────────────────────
	// If the driver still reports the VM as Running, the record must NOT be
	// Stopped. A record=Stopped with a live Running VM means recovery orphaned
	// a live VM — the race bug.
	driverObs, _ := rawDrv.Observe(ctx, sb.ID)
	finalRecord, err := fs.Get(ctx, sb.ID)
	if err != nil {
		// Sandbox was deleted — only acceptable if OutcomeRemoved, not the bug.
		t.Logf("sandbox was deleted during test; driver state=%s", driverObs.State)
		return
	}

	if driverObs.State == driver.Running && finalRecord.State == domain.Stopped {
		t.Errorf(
			"RACE BUG: recovery wrote Stopped over a live Running VM — "+
				"record.State=%s but driver.State=%s; "+
				"fix: move driver.Observe inside the store.Update callback so the "+
				"observe-decide-write sequence is atomic under the per-sandbox flock",
			finalRecord.State, driverObs.State,
		)
	}
}

// ── Edge 10 and StopReason qualifier (rulings 10 / 12) ──────────────────────

// TestEdge10_RunningCrash_StopReasonMemoryLost verifies edge 10 (ruling 12):
// a durable non-rm sandbox in running whose VM is observed Absent (host reboot
// / VMM kill / power loss) with no removal marker resolves to stopped with
// StopReason=memory_lost. It must NOT become error — the user explicitly
// declined route-to-error (gamma).
func TestEdge10_RunningCrash_StopReasonMemoryLost(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Non-rm sandbox in running state. No driver entry → VM is Absent.
	sb := createSandbox(t, ctx, env.st, domain.Running)

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)

	// Must resolve to stopped — never to error.
	if out.Kind != OutcomeResolvedStopped {
		t.Errorf("edge 10: want OutcomeResolvedStopped, got %s (reason: %q) — "+
			"a crashed running VM must resolve to stopped, not error",
			out.Kind, out.Reason)
	}
	if out.Kind == OutcomeIndeterminate {
		t.Errorf("edge 10: got Indeterminate — TriggerSubstrateLost edge for Running may be missing from the transition table")
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}

	if updated.State != domain.Stopped {
		t.Errorf("edge 10: State=%q, want stopped", updated.State)
	}
	if updated.StopReason != domain.StopReasonMemoryLost {
		t.Errorf("edge 10: StopReason=%q, want memory_lost — "+
			"ruling 12: StopReason must qualify the stopped state for substrate loss",
			updated.StopReason)
	}

	// Memory loss must be mentioned in the outcome reason.
	if !strings.Contains(strings.ToLower(out.Reason), "memory") {
		t.Errorf("edge 10: reason must mention memory loss, got %q", out.Reason)
	}

	// No destructive driver calls — just a record correction.
	for _, c := range env.drv.Calls() {
		if c.Kind == fake.CallStop {
			t.Errorf("edge 10: Stop called for a non-rm running crash; must only correct the record")
		}
	}
}

// TestEdge10_RunningCrash_NotError verifies the non-error constraint for edge 10:
// the outcome must never be an error state regardless of recovery outcome kind.
func TestEdge10_RunningCrash_NotError(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Running)
	// VM absent by default (no driver entry).

	_, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if updated.State == domain.Error {
		t.Errorf("edge 10: state became Error — " +
			"ruling 12 forbids route-to-error for a running crash; must go to stopped")
	}
}

// TestEdge10_RunningCrashWithRm_NotAffected verifies that edge 10 does NOT
// apply when RemoveOnExit is set; the --rm path (OutcomeRemoved) still applies.
func TestEdge10_RunningCrashWithRm_NotAffected(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Running, withRemoveOnExit())
	// VM absent by default → --rm path fires.

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)

	// --rm sandboxes with absent VMs are removed, not resolved to stopped.
	if out.Kind != OutcomeRemoved {
		t.Errorf("running + --rm + absent: want OutcomeRemoved, got %s (reason: %q)",
			out.Kind, out.Reason)
	}
}

// TestPausedCrash_StopReasonMemoryLost verifies that the existing paused-crash
// path also sets StopReason=memory_lost (completing ruling 12 for both cases).
func TestPausedCrash_StopReasonMemoryLost(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Paused)
	env.drv.SimulateHostReboot() // all paused VMs become Absent.

	_, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if updated.StopReason != domain.StopReasonMemoryLost {
		t.Errorf("paused crash: StopReason=%q, want memory_lost", updated.StopReason)
	}
}

// TestCleanStop_StopReasonClean verifies that a cleanly stopped sandbox carries
// StopReason=clean (ruling 12: clean stop yields StopReasonClean), and that
// recovery leaves the record unchanged.
func TestCleanStop_StopReasonClean(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Simulate a clean user-requested stop: set State=stopped with
	// StopReason=clean (as service.Stop would write).
	sb := createSandbox(t, ctx, env.st, domain.Running)
	if err := env.st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		rec.State = domain.Stopped
		rec.StopReason = domain.StopReasonClean
		rec.InstanceID = ""
		return nil
	}); err != nil {
		t.Fatalf("simulating clean stop: %v", err)
	}

	// StopReason=clean must survive a store round-trip.
	afterStop, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after clean stop: %v", err)
	}
	if afterStop.StopReason != domain.StopReasonClean {
		t.Errorf("clean stop: StopReason=%q, want clean — round-trip failed", afterStop.StopReason)
	}

	// Recovery on a stopped sandbox with no VM must leave it unchanged.
	// The StopReason must not be overwritten.
	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeUnchanged {
		t.Errorf("clean stop recovery: want OutcomeUnchanged, got %s (reason: %q)",
			out.Kind, out.Reason)
	}

	// StopReason must still be clean after recovery (unchanged path must not zero it).
	afterRecovery, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if afterRecovery.StopReason != domain.StopReasonClean {
		t.Errorf("clean stop after recovery: StopReason=%q, want clean — "+
			"recovery must not clear StopReason on an unchanged stopped sandbox",
			afterRecovery.StopReason)
	}
}

// TestStopReason_ClearedOnRestart verifies that StopReason is cleared when a
// stopped sandbox is adopted back into running (the field only qualifies stopped).
func TestStopReason_ClearedOnRestart(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Sandbox was cleanly stopped.
	sb := createSandbox(t, ctx, env.st, domain.Stopped)
	if err := env.st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		rec.StopReason = domain.StopReasonClean
		return nil
	}); err != nil {
		t.Fatalf("set StopReason: %v", err)
	}

	// VM is now running (e.g. service.Start was called and set driver entry).
	env.drv.SetRunning(sb.ID)

	// Recovery adopts the live VM.
	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeAdopted {
		t.Errorf("restart: want OutcomeAdopted, got %s (reason: %q)", out.Kind, out.Reason)
	}

	// StopReason must be cleared — it only qualifies stopped.
	afterAdopt, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after adopt: %v", err)
	}
	if afterAdopt.StopReason != "" {
		t.Errorf("restart: StopReason=%q, want empty — StopReason must be cleared when sandbox returns to running",
			afterAdopt.StopReason)
	}
}

// perSandboxBlockDriver blocks Observe for a specific sandbox until its gate
// channel is closed. Used to prove that per-sandbox locking does not prevent
// independent sandboxes from making progress simultaneously.
type perSandboxBlockDriver struct {
	driver.Driver
	blockID domain.SandboxID
	gate    chan struct{} // close to unblock
}

func (d *perSandboxBlockDriver) Observe(ctx context.Context, id domain.SandboxID) (driver.Observation, error) {
	if id == d.blockID {
		select {
		case <-d.gate:
		case <-ctx.Done():
			return driver.Observation{State: driver.Unknown}, ctx.Err()
		}
	}
	return d.Driver.Observe(ctx, id)
}

// TestRecover_RemoveOnExit_ReapsDiskCopy_NoOrphan is the regression test for
// the CoW orphan bug: a cache-image --rm sandbox whose VM crashed left a full
// ~5GiB <diskDir>/<id>.raw because the recovery --rm path called st.Delete
// directly, bypassing the shared service.ReapDiskCopy helper.
//
// Fixture: store record with RemoveOnExit=true, Running state (VM absent from
// driver), and a real dummy <id>.raw in a temp diskDir. After recovery the
// .raw file must be gone — no orphan.
func TestRecover_RemoveOnExit_ReapsDiskCopy_NoOrphan(t *testing.T) {
	ctx := context.Background()

	storeDir := t.TempDir()
	st, err := store.NewFileStore(storeDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := fake.New()

	// Place a dummy .raw file in a controlled diskDir.
	diskDir := t.TempDir()
	sb := createSandbox(t, ctx, st, domain.Running, withRemoveOnExit())
	rawPath := filepath.Join(diskDir, sb.ID.String()+".raw")
	if err := os.WriteFile(rawPath, []byte("dummy ext4 image"), 0600); err != nil {
		t.Fatalf("write dummy .raw: %v", err)
	}

	// VM absent: drv has no entry → Observe returns Absent.
	rec := New(st, drv).WithDiskDir(diskDir)

	report, err := rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeRemoved {
		t.Errorf("want %s, got %s (reason: %q)", OutcomeRemoved, out.Kind, out.Reason)
	}

	// Regression: the .raw file must be gone — no orphan.
	if _, statErr := os.Stat(rawPath); !os.IsNotExist(statErr) {
		t.Errorf("orphan .raw file still exists at %s after recovery --rm removal: %v; "+
			"fix: call service.ReapDiskCopy in the recovery needDelete path", rawPath, statErr)
	}

	// Record must be gone too.
	if _, err := st.Get(ctx, sb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound after removal, got %v", err)
	}
}

// TestRecover_MultiSandbox_NoGlobalLock proves that recovering two sandboxes
// does not serialise on a single global lock. Sandbox A's Observe is blocked
// until sandbox B's recovery completes. Under per-sandbox locking:
//   - B's RecoverOne runs concurrently with A's blocked Observe.
//   - B completes → the gate closes → A's Observe unblocks → A completes.
//
// Under a global lock both goroutines would contend on the same mutex: A
// holds it and blocks in Observe while B waits to acquire it, causing a
// deadlock. The 5-second timeout catches that.
//
// Biting assertion: if a global lock is introduced, B's RecoverOne blocks
// forever (waiting for A to release the global lock, but A is blocked on
// Observe waiting for B to finish → deadlock → 5s timeout → test fails).
func TestRecover_MultiSandbox_NoGlobalLock(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	fs, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	rawDrv := fake.New()

	sbA := domain.Sandbox{ID: domain.NewSandboxID(), Name: "a", Project: "p", State: domain.Stopped}
	sbB := domain.Sandbox{ID: domain.NewSandboxID(), Name: "b", Project: "p", State: domain.Stopped}
	for _, sb := range []domain.Sandbox{sbA, sbB} {
		if err := fs.Create(ctx, sb); err != nil {
			t.Fatalf("Create %s: %v", sb.ID, err)
		}
	}

	gate := make(chan struct{})
	blockDrv := &perSandboxBlockDriver{Driver: rawDrv, blockID: sbA.ID, gate: gate}
	rec := New(fs, blockDrv)

	// Launch A in background — it will block inside Observe.
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		rec.RecoverOne(ctx, sbA.ID) //nolint:errcheck
	}()

	// B must complete independently (per-sandbox lock does not block B).
	bDone := make(chan struct{})
	go func() {
		defer close(bDone)
		rec.RecoverOne(ctx, sbB.ID) //nolint:errcheck
	}()

	// B completes → close gate → A unblocks.
	select {
	case <-bDone:
		// Good: B completed while A was still blocked.
	case <-time.After(5 * time.Second):
		t.Fatal("sandbox B timed out — a global lock is blocking B while A is stuck in Observe")
	}
	close(gate) // unblock A

	select {
	case <-aDone:
		// Good: A completed after the gate opened.
	case <-time.After(5 * time.Second):
		t.Fatal("sandbox A timed out after gate opened")
	}
}

// ── MAJOR 1 regression: netns fields zeroed on substrate-loss recovery ────────

// setNetnsFields writes non-zero adoption identity onto a sandbox record so
// the test can verify that recover.go clears them.
func setNetnsFields(t *testing.T, ctx context.Context, st store.Store, id domain.SandboxID) {
	t.Helper()
	if err := st.Update(ctx, id, func(rec *domain.Sandbox) error {
		rec.NetnsChildPID = 12345
		rec.NetnsChildPGID = 12346
		rec.NetnsChildStartTime = 999999
		rec.GuestTapName = "tap0"
		rec.CHAPISocket = "/run/ch.sock"
		return nil
	}); err != nil {
		t.Fatalf("setNetnsFields: %v", err)
	}
}

// assertNetnsFieldsZeroed confirms that all five netns identity fields on the
// record are zero/empty after recovery.
func assertNetnsFieldsZeroed(t *testing.T, sb domain.Sandbox) {
	t.Helper()
	if sb.NetnsChildPID != 0 {
		t.Errorf("NetnsChildPID = %d, want 0", sb.NetnsChildPID)
	}
	if sb.NetnsChildPGID != 0 {
		t.Errorf("NetnsChildPGID = %d, want 0", sb.NetnsChildPGID)
	}
	if sb.NetnsChildStartTime != 0 {
		t.Errorf("NetnsChildStartTime = %d, want 0", sb.NetnsChildStartTime)
	}
	if sb.GuestTapName != "" {
		t.Errorf("GuestTapName = %q, want empty", sb.GuestTapName)
	}
	if sb.CHAPISocket != "" {
		t.Errorf("CHAPISocket = %q, want empty", sb.CHAPISocket)
	}
}

// TestRecover_Running_SubstrateLost_ClearsNetnsFields is the MAJOR 1 regression
// test for the Running→Stopped arm: recover.go must zero all five netns identity
// fields when resolving a crashed running sandbox to stopped.
//
// Mutation proof: if any one of the clearing lines is removed from recover.go,
// the corresponding assertNetnsFieldsZeroed check below turns RED.
func TestRecover_Running_SubstrateLost_ClearsNetnsFields(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Running)
	// Pre-populate netns fields to simulate a previously adopted sandbox.
	setNetnsFields(t, ctx, env.st, sb.ID)
	// VM is absent (no driver entry) → substrate_lost → resolved to Stopped.

	report, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	out := findOutcome(t, report, sb.ID)
	if out.Kind != OutcomeResolvedStopped {
		t.Fatalf("want OutcomeResolvedStopped, got %s", out.Kind)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	assertNetnsFieldsZeroed(t, updated)
}

// TestRecover_Paused_SubstrateLost_ClearsNetnsFields mirrors the Running case
// for the Paused→Stopped arm.
func TestRecover_Paused_SubstrateLost_ClearsNetnsFields(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	sb := createSandbox(t, ctx, env.st, domain.Paused)
	setNetnsFields(t, ctx, env.st, sb.ID)
	env.drv.SimulateHostReboot() // all paused VMs become Absent.

	_, err := env.rec.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	updated, err := env.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	assertNetnsFieldsZeroed(t, updated)
}
