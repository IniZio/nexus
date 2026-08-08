// Package acceptance — owner ruling tests.
//
// Each test encodes one of the nine explicit design rulings made by the project
// owner. They are the authoritative executable specification for nexus3's
// lifecycle and recovery behaviour. If a test fails, either the implementation
// violates the ruling, or the ruling changed and that change MUST be recorded
// upstream before this test is updated. Do not weaken these tests to make them
// pass.
package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/recovery"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── Ruling 1 ─────────────────────────────────────────────────────────────────

// TestRuling1_RmIsOneMachineOneExtraEdge asserts that --rm is implemented as a
// single extra edge in the standard five-state machine, not as a separate
// machine, additional states, or an --rm-specific state.
//
// Ruling: "A --rm sandbox uses the same five states and the same transition
// table as any other; the only difference is one extra edge,
// running → removed, triggered by primary-command exit."
func TestRuling1_RmIsOneMachineOneExtraEdge(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	// 1. Exactly five durable states. A sixth would mean a new machine shape.
	allStates := domain.AllStates()
	if n := len(allStates); n != 5 {
		t.Errorf("Ruling 1 broken: AllStates() returned %d states, want 5 — "+
			"adding a state creates a second shape for --rm", n)
	}

	// 2. No state name suggests --rm-specific state ("removed", "rm", etc.).
	for _, s := range allStates {
		name := strings.ToLower(s.String())
		if strings.Contains(name, "remov") || name == "rm" || strings.Contains(name, "_rm") {
			t.Errorf("Ruling 1 broken: state %q looks like an rm-specific state; "+
				"--rm must use the five states + one edge, not a new shape", s)
		}
	}

	// 3. Exactly one edge in the table has Removal==true.
	var rmEdges []lifecycle.Edge
	for _, e := range m.All() {
		if e.Removal {
			rmEdges = append(rmEdges, e)
		}
	}
	if len(rmEdges) != 1 {
		t.Fatalf("Ruling 1 broken: want exactly 1 Removal edge, got %d — "+
			"each extra Removal edge is an extra machine shape", len(rmEdges))
	}

	// 4. That one edge is Running/primary_command_exit.
	e := rmEdges[0]
	if e.From != domain.Running {
		t.Errorf("Ruling 1 broken: rm edge From=%q, want Running", e.From)
	}
	if e.Trigger != lifecycle.TriggerPrimaryCommandExit {
		t.Errorf("Ruling 1 broken: rm edge Trigger=%q, want %q",
			e.Trigger, lifecycle.TriggerPrimaryCommandExit)
	}

	// 5. Machine.Next yields Remove==true for that edge.
	tr, err := m.Next(domain.Running, lifecycle.TriggerPrimaryCommandExit)
	if err != nil {
		t.Fatalf("Ruling 1: Next(Running, primary_command_exit): %v", err)
	}
	if !tr.Remove {
		t.Errorf("Ruling 1 broken: Next(Running, primary_command_exit).Remove=false; must be true")
	}

	// 6. The removal Transition.NextState is the zero value (invalid for store).
	// This is what prevents accidentally persisting a removal as a durable state.
	if tr.Remove && tr.NextState.Valid() {
		t.Errorf("Ruling 1 broken: Transition.NextState.Valid()=true for a removal transition; " +
			"zero state must be invalid so it cannot be written to the store")
	}
}

// ── Ruling 2 ─────────────────────────────────────────────────────────────────

// TestRuling2_RemovalIsUnconditionalOnExitStatus asserts that a --rm sandbox
// is removed regardless of whether the primary command exited with code 0 or
// non-zero. The system has no mechanism to inspect exit codes; RemoveOnExit
// alone drives removal.
//
// Ruling: "The primary command exiting non-zero removes the sandbox just as
// exit 0 does, exactly like `docker run --rm`."
func TestRuling2_RemovalIsUnconditionalOnExitStatus(t *testing.T) {
	t.Parallel()

	// Both cases: VM absent after primary command exits, RemoveOnExit=true.
	// The driver has no entry for the sandbox ID so Observe returns Absent.
	// The store has no exit-code field; there is nothing for recovery to inspect.
	// Both sub-tests look identical at the store level — which IS the ruling.
	for _, name := range []string{"exit_zero", "exit_nonzero"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			ctx := context.Background()

			// Sandbox with --rm flag. VM is absent (primary command exited).
			// No exit code is stored; the ruling is that none is needed.
			sb := h.createSandbox(t, domain.Running, withRemoveOnExit())
			// h.drv has no entry for sb.ID → Observe returns Absent.

			report := h.runRecovery(t)
			out := findOutcome(t, report, sb.ID)

			if out.Kind != recovery.OutcomeRemoved {
				t.Errorf("Ruling 2 broken (%s): want %s, got %s (reason: %q) — "+
					"removal must be unconditional on exit status",
					name, recovery.OutcomeRemoved, out.Kind, out.Reason)
			}

			_, err := h.st.Get(ctx, sb.ID)
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Ruling 2 broken (%s): sandbox must be deleted after --rm removal, got err=%v",
					name, err)
			}
		})
	}

	// Assert the rm edge is system-initiated, not a user/exit-code choice.
	t.Run("rm_edge_is_system_initiated", func(t *testing.T) {
		t.Parallel()
		m := lifecycle.New()
		init, err := m.Initiator(domain.Running, lifecycle.TriggerPrimaryCommandExit)
		if err != nil {
			t.Fatalf("Ruling 2: Initiator(Running, primary_command_exit): %v", err)
		}
		if init != lifecycle.InitiatorSystem {
			t.Errorf("Ruling 2 broken: rm edge Initiator=%q, want %q — "+
				"the system fires the rm edge; exit code must not gate it",
				init, lifecycle.InitiatorSystem)
		}
	})
}

// ── Ruling 3 ─────────────────────────────────────────────────────────────────

// TestRuling3_PerSandboxSupervisor_NoDaemonRequired asserts that every
// operation — including removal and recovery — works from a cold invocation
// with no background process. A fresh harness IS a cold invocation.
//
// Ruling: "There is no central daemon. Every operation, including removal,
// works from a cold invocation with no background process."
func TestRuling3_PerSandboxSupervisor_NoDaemonRequired(t *testing.T) {
	t.Parallel()

	t.Run("remove_works_cold", func(t *testing.T) {
		t.Parallel()
		// Cold harness: no state exists. Create, then remove.
		h := newHarness(t)
		ctx := context.Background()

		sb, err := h.svc.Create(ctx, "proj", "ruling3-cold", service.CreateOptions{})
		if err != nil {
			t.Fatalf("Ruling 3: Create (cold): %v", err)
		}

		// Remove succeeds with no background process.
		if err := h.svc.Remove(ctx, sb.ID.String()); err != nil {
			t.Fatalf("Ruling 3 broken: Remove (cold, no daemon): %v — "+
				"removal must work from a cold invocation without a background process", err)
		}

		_, getErr := h.st.Get(ctx, sb.ID)
		if !errors.Is(getErr, store.ErrNotFound) {
			t.Errorf("Ruling 3: sandbox must be gone after Remove, got err=%v", getErr)
		}
	})

	t.Run("recovery_works_cold", func(t *testing.T) {
		t.Parallel()
		// Cold harness: create a --rm sandbox whose VM exited, run recovery.
		h := newHarness(t)
		ctx := context.Background()

		sb := h.createSandbox(t, domain.Running, withRemoveOnExit())
		// VM absent in driver; recovery should honour --rm without a daemon.

		report := h.runRecovery(t)
		out := findOutcome(t, report, sb.ID)

		if out.Kind != recovery.OutcomeRemoved {
			t.Errorf("Ruling 3 broken: recovery (cold, no daemon): want %s, got %s — "+
				"no daemon must be required for recovery to honour --rm",
				recovery.OutcomeRemoved, out.Kind)
		}

		_, getErr := h.st.Get(ctx, sb.ID)
		if !errors.Is(getErr, store.ErrNotFound) {
			t.Errorf("Ruling 3: sandbox must be deleted after cold recovery, got err=%v", getErr)
		}
	})
}

// ── Ruling 4 ─────────────────────────────────────────────────────────────────

// TestRuling4_ReconcileHonoursRmAfterCrash asserts that a sandbox marked
// running with RemoveOnExit, whose VM is absent and removal marker is absent,
// is removed on recovery — even though the exit code died with the supervisor.
//
// Ruling: "A sandbox marked running with RemoveOnExit, whose VM is gone and
// whose removal marker is ABSENT, is removed on recovery — even though the
// exit code died with the supervisor."
func TestRuling4_ReconcileHonoursRmAfterCrash(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	// Sandbox was running with --rm; supervisor crashed before removal could
	// start (no marker set). The VM is now absent.
	sb := h.createSandbox(t, domain.Running, withRemoveOnExit())
	h.simulateCrash(sb.ID) // VM absent; marker absent (crash was pre-removal)

	report := h.runRecovery(t)
	out := findOutcome(t, report, sb.ID)

	if out.Kind != recovery.OutcomeRemoved {
		t.Errorf("Ruling 4 broken: want %s after crash + --rm, got %s (reason: %q) — "+
			"reconcile must honour --rm even when exit code died with the supervisor",
			recovery.OutcomeRemoved, out.Kind, out.Reason)
	}

	_, err := h.st.Get(ctx, sb.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Ruling 4 broken: sandbox must be deleted after --rm reconcile, got err=%v", err)
	}
}

// ── Ruling 5 ─────────────────────────────────────────────────────────────────

// TestRuling5_WriteAheadMarker_MidRemovalIsTerminal asserts that:
//   - Marker absent + dead VM + --rm → OutcomeRemoved (normal path)
//   - Marker present + dead VM → OutcomeTerminal (mid-removal crash; never retried)
//   - SetRemovalMarker is called BEFORE driver.Stop and store.Delete (WAL ordering)
//
// Ruling: "Marker absent + dead VM + --rm → removed. Marker PRESENT → terminal,
// never retried. rm sets the marker BEFORE any destructive work."
func TestRuling5_WriteAheadMarker_MidRemovalIsTerminal(t *testing.T) {
	t.Parallel()

	t.Run("marker_absent_vm_absent_rm_removes", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		sb := h.createSandbox(t, domain.Running, withRemoveOnExit())
		// VM absent, marker absent.

		out := findOutcome(t, h.runRecovery(t), sb.ID)
		if out.Kind != recovery.OutcomeRemoved {
			t.Errorf("Ruling 5 broken: marker absent + VM absent + --rm: want %s, got %s (reason: %q)",
				recovery.OutcomeRemoved, out.Kind, out.Reason)
		}
	})

	t.Run("marker_present_vm_absent_is_terminal", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		sb := h.createSandbox(t, domain.Running, withRemoveOnExit())
		h.setRemovalMarker(t, sb) // marker set; crash happened mid-removal
		// VM absent (driver has no entry).

		out := findOutcome(t, h.runRecovery(t), sb.ID)
		if out.Kind != recovery.OutcomeTerminal {
			t.Errorf("Ruling 5 broken: marker present + VM absent: want %s, got %s (reason: %q) — "+
				"mid-removal crash must be terminal, never retried",
				recovery.OutcomeTerminal, out.Kind, out.Reason)
		}
	})

	t.Run("wal_ordering_marker_precedes_destructive_work", func(t *testing.T) {
		// Verify service.Remove calls SetRemovalMarker before driver.Stop and
		// store.Delete. Use recording wrappers to capture call order.
		root := t.TempDir()
		st, err := store.NewFileStore(root)
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		drv := fake.New()

		var mu sync.Mutex
		var callOrder []string
		record := func(name string) {
			mu.Lock()
			callOrder = append(callOrder, name)
			mu.Unlock()
		}

		rSt := &walStore{Store: st, record: record}
		rDrv := &walDriver{Driver: drv, record: record}
		svc := service.New(rSt, rDrv, lifecycle.New())
		ctx := context.Background()

		sb, err := svc.Create(ctx, "proj", "wal", service.CreateOptions{})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		mu.Lock()
		callOrder = callOrder[:0] // capture only Remove's calls
		mu.Unlock()

		if err := svc.Remove(ctx, sb.ID.String()); err != nil {
			t.Fatalf("Remove: %v", err)
		}

		mu.Lock()
		order := make([]string, len(callOrder))
		copy(order, callOrder)
		mu.Unlock()

		markerPos, stopPos, deletePos := -1, -1, -1
		for i, call := range order {
			switch call {
			case "SetRemovalMarker":
				if markerPos == -1 {
					markerPos = i
				}
			case "Stop":
				if stopPos == -1 {
					stopPos = i
				}
			case "Delete":
				if deletePos == -1 {
					deletePos = i
				}
			}
		}

		if markerPos == -1 {
			t.Fatalf("Ruling 5 broken: SetRemovalMarker never called in Remove — WAL protocol violated")
		}
		if stopPos != -1 && markerPos > stopPos {
			t.Errorf("Ruling 5 broken: SetRemovalMarker (pos %d) after Stop (pos %d) — "+
				"marker must precede all destructive work", markerPos, stopPos)
		}
		if deletePos != -1 && markerPos > deletePos {
			t.Errorf("Ruling 5 broken: SetRemovalMarker (pos %d) after Delete (pos %d) — "+
				"marker must precede all destructive work", markerPos, deletePos)
		}
	})
}

// ── Ruling 6 ─────────────────────────────────────────────────────────────────

// TestRuling6_RecoveryObservesSubstrateFirst asserts the named old-nexus
// regression: a record claiming a stale state while the VM is alive must
// result in adoption, never error-labelling or destruction.
//
// Ruling: "Recovery observes the substrate first; the record is a cache.
// A predecessor system destroyed working VMs by keying on the record."
//
// The substrate-first ordering is now guaranteed structurally by the exclusive
// per-sandbox flock that store.Update holds across the entire observe → decide
// → write sequence. The ordering is enforced by the lock, not by test
// instrumentation. The behavioural property — that a live VM is adopted
// regardless of the stored state — is what this test asserts. The sub-test
// below bites on any violation that changes the *decision* based on stored
// state before Observe (e.g. an early-return branch on rec.State). Note: it
// cannot detect a pre-Observe *read* of stored state that doesn't change the
// outcome — that is the honest boundary of this guard.
func TestRuling6_RecoveryObservesSubstrateFirst(t *testing.T) {
	t.Parallel()

	t.Run("old_nexus_regression_healthy_vm_with_stale_record_is_adopted", func(t *testing.T) {
		// THE KEY REGRESSION: VM is Running and healthy, record says Stopped
		// (stale). Recovery must adopt the VM (correct the record), never
		// relabel it error and never call Stop. The predecessor system destroyed
		// working VMs exactly here because it gated adoption on the record state.
		t.Parallel()
		h := newHarness(t)
		ctx := context.Background()

		// Stale record: Stopped. Live VM: Running.
		sb := h.createSandbox(t, domain.Stopped)
		h.drv.SetRunning(sb.ID)

		report := h.runRecovery(t)
		out := findOutcome(t, report, sb.ID)

		if out.Kind != recovery.OutcomeAdopted {
			t.Errorf("Ruling 6 / old-nexus regression broken: stored=Stopped VM=Running: "+
				"want %s, got %s (reason: %q) — "+
				"a healthy VM must be adopted, never relabelled error or destroyed",
				recovery.OutcomeAdopted, out.Kind, out.Reason)
		}

		for _, c := range h.drv.Calls() {
			if c.Kind == fake.CallStop {
				t.Error("Ruling 6 / old-nexus regression broken: driver.Stop called on a healthy VM — " +
					"this is exactly what the predecessor system did wrong")
			}
		}

		updated, err := h.st.Get(ctx, sb.ID)
		if err != nil {
			t.Fatalf("Get after adoption: %v", err)
		}
		if updated.State == domain.Error {
			t.Errorf("Ruling 6 / old-nexus regression broken: state became Error — " +
				"healthy VM must never be relabelled error")
		}
		if updated.State != domain.Running {
			t.Errorf("Ruling 6 / old-nexus regression: state after adoption=%q, want Running", updated.State)
		}
	})
}

// ── Ruling 7 ─────────────────────────────────────────────────────────────────

// TestRuling7_PausedIsStored_SurvivesRoundTrip asserts that the paused state
// is a real durable state that survives a store round-trip, not merely an
// in-memory observation.
//
// Ruling: "paused is stored, not merely observed."
func TestRuling7_PausedIsStored_SurvivesRoundTrip(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	sb := h.createSandbox(t, domain.Paused)

	got, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Ruling 7: Get: %v", err)
	}
	if got.State != domain.Paused {
		t.Errorf("Ruling 7 broken: state after Get round-trip: got %q, want %q — "+
			"paused must be a durable stored state, not merely observed",
			got.State, domain.Paused)
	}

	all, err := h.st.List(ctx)
	if err != nil {
		t.Fatalf("Ruling 7: List: %v", err)
	}
	found := false
	for _, s := range all {
		if s.ID == sb.ID {
			found = true
			if s.State != domain.Paused {
				t.Errorf("Ruling 7 broken: state from List: got %q, want %q", s.State, domain.Paused)
			}
		}
	}
	if !found {
		t.Error("Ruling 7: sandbox not found in List")
	}
}

// ── Ruling 8 ─────────────────────────────────────────────────────────────────

// TestRuling8_DurableFieldSetIsExact asserts that the persisted JSON record
// contains exactly the declared durable fields and nothing more, and that
// pause/resume are user-initiated transitions.
//
// Ruling: "The durable set is exactly: identity, envelope, five-value state,
// internal instance id, --rm intent, removal marker. pause and resume are user
// commands."
func TestRuling8_DurableFieldSetIsExact(t *testing.T) {
	t.Parallel()

	t.Run("json_keys_are_exactly_the_durable_set", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		st, err := store.NewFileStore(root)
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		ctx := context.Background()

		sb := domain.Sandbox{
			ID:            domain.NewSandboxID(),
			Name:          "ruling8",
			Project:       "proj",
			State:         domain.Running,
			Envelope:      domain.Envelope{ImageDigest: "sha256:ruling8"},
			InstanceID:    "inst-0",
			RemoveOnExit:  true,
			RemovalMarker: false,
		}
		if err := st.Create(ctx, sb); err != nil {
			t.Fatalf("Create: %v", err)
		}

		path := filepath.Join(root, "sandboxes", sb.ID.String(), "record.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}

		wantKeys := map[string]bool{
			"schema_version": true,
			"id":             true,
			"name":           true,
			"project":        true,
			"state":          true,
			"envelope":       true,
			"instance_id":    true,
			"remove_on_exit": true,
			"removal_marker": true,
		}

		for key := range raw {
			if !wantKeys[key] {
				t.Errorf("Ruling 8 broken: unexpected JSON key %q in record — "+
					"adding a field must be a deliberate schema decision that updates this test",
					key)
			}
		}
		for key := range wantKeys {
			if _, ok := raw[key]; !ok {
				t.Errorf("Ruling 8 broken: expected JSON key %q missing from record", key)
			}
		}

		// Explicitly assert no transient operation state leaks onto disk.
		for _, bad := range []string{
			"snapshotting", "forking", "restoring", "stopping",
			"cloning", "restored", "cloned", "snapshotted",
		} {
			if _, ok := raw[bad]; ok {
				t.Errorf("Ruling 8 broken: transient state %q found in record — "+
					"only durable fields may be persisted", bad)
			}
		}
	})

	t.Run("pause_and_resume_are_user_initiated", func(t *testing.T) {
		t.Parallel()
		m := lifecycle.New()

		pauseInit, err := m.Initiator(domain.Running, lifecycle.TriggerPause)
		if err != nil {
			t.Fatalf("Ruling 8: Initiator(Running, pause): %v", err)
		}
		if pauseInit != lifecycle.InitiatorUser {
			t.Errorf("Ruling 8 broken: TriggerPause Initiator=%q, want %q — "+
				"pause must be a user command, not an autonomous system event",
				pauseInit, lifecycle.InitiatorUser)
		}

		resumeInit, err := m.Initiator(domain.Paused, lifecycle.TriggerResume)
		if err != nil {
			t.Fatalf("Ruling 8: Initiator(Paused, resume): %v", err)
		}
		if resumeInit != lifecycle.InitiatorUser {
			t.Errorf("Ruling 8 broken: TriggerResume Initiator=%q, want %q — "+
				"resume must be a user command, not an autonomous system event",
				resumeInit, lifecycle.InitiatorUser)
		}
	})
}

// ── Ruling 9 ─────────────────────────────────────────────────────────────────

// TestRuling9_PausedSandboxMemoryGoneResolvesToStopped asserts that a sandbox
// stored as paused whose VM is absent (host reboot destroyed its in-RAM state)
// resolves to stopped, and that the memory loss is surfaced in the recovery
// report.
//
// Ruling: "A paused sandbox whose memory is gone resolves to stopped, and the
// loss is surfaced in the recovery report, not silent."
func TestRuling9_PausedSandboxMemoryGoneResolvesToStopped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	sb := h.createSandbox(t, domain.Paused)
	h.drv.SetPaused(sb.ID) // VM is live in memory on the fake substrate.

	// Host reboots: all Paused VMs lose their in-RAM state.
	h.simulateHostReboot()
	// Now: stored=Paused, VM=Absent (memory destroyed).

	report := h.runRecovery(t)
	out := findOutcome(t, report, sb.ID)

	if out.Kind != recovery.OutcomeResolvedStopped {
		t.Errorf("Ruling 9 broken: stored Paused + VM absent after reboot: "+
			"want %s, got %s (reason: %q) — "+
			"paused sandbox with memory gone must resolve to stopped",
			recovery.OutcomeResolvedStopped, out.Kind, out.Reason)
	}

	// Memory loss must be surfaced in the reason — the report is the user's
	// only signal that something was lost. Silent is wrong.
	if !strings.Contains(strings.ToLower(out.Reason), "memory") {
		t.Errorf("Ruling 9 broken: reason must mention memory loss, got %q", out.Reason)
	}

	updated, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Ruling 9: Get after recovery: %v", err)
	}
	if updated.State != domain.Stopped {
		t.Errorf("Ruling 9 broken: stored state after recovery=%q, want Stopped", updated.State)
	}
}

// ── Old-nexus regression (named explicitly) ──────────────────────────────────

// TestOldNexusRegression_HealthyVMWithStaleRecordNeverRelabelledErrorNeverDestroyed
// is the named regression for the predecessor system's bug: a sandbox whose
// record claimed a stale/transient state matched neither the adoption gate nor
// the reaper gate, so a working VM was relabelled error and destroyed.
// This test asserts that behaviour is impossible in nexus3.
func TestOldNexusRegression_HealthyVMWithStaleRecordNeverRelabelledErrorNeverDestroyed(t *testing.T) {
	t.Parallel()

	// Stored=Stopped (stale; the record was not updated before the previous
	// process exited). VM is Running and healthy. This is the exact predecessor
	// scenario: record says one thing, substrate says another.
	h := newHarness(t)
	ctx := context.Background()

	sb := h.createSandbox(t, domain.Stopped)
	h.drv.SetRunning(sb.ID)

	report := h.runRecovery(t)
	out := findOutcome(t, report, sb.ID)

	if out.Kind != recovery.OutcomeAdopted {
		t.Errorf("old-nexus regression: stored=Stopped VM=Running: want %s, got %s (reason: %q) — "+
			"a healthy VM must be adopted, never relabelled error or destroyed",
			recovery.OutcomeAdopted, out.Kind, out.Reason)
	}

	for _, c := range h.drv.Calls() {
		if c.Kind == fake.CallStop {
			t.Error("old-nexus regression: driver.Stop called on a healthy VM — " +
				"this is the exact predecessor defect: working VMs destroyed by stale records")
		}
	}

	updated, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("old-nexus regression: Get after adoption: %v", err)
	}
	if updated.State == domain.Error {
		t.Errorf("old-nexus regression: state became Error — " +
			"healthy VM must never be relabelled error")
	}
	if updated.State != domain.Running {
		t.Errorf("old-nexus regression: state after adoption=%q, want Running", updated.State)
	}
}

// ── Crash simulation coverage ─────────────────────────────────────────────────

// TestCrashSimulationCoverage exercises three crash scenarios that the recovery
// package must handle: mid-run VMM kill, mid-removal kill with marker set, and
// host-reboot memory loss for a paused sandbox.
func TestCrashSimulationCoverage(t *testing.T) {
	t.Parallel()

	t.Run("mid_run_kill_no_rm", func(t *testing.T) {
		// VMM crashes while sandbox is running; no --rm flag.
		// Recovery must transition the record to Stopped.
		t.Parallel()
		h := newHarness(t)
		ctx := context.Background()

		sb := h.createSandbox(t, domain.Running)
		h.drv.SetRunning(sb.ID)
		h.simulateCrash(sb.ID) // VMM gone; VM now absent.

		report := h.runRecovery(t)
		out := findOutcome(t, report, sb.ID)

		if out.Kind != recovery.OutcomeResolvedStopped {
			t.Errorf("crash sim / mid-run kill: want %s, got %s (reason: %q)",
				recovery.OutcomeResolvedStopped, out.Kind, out.Reason)
		}
		updated, err := h.st.Get(ctx, sb.ID)
		if err != nil {
			t.Fatalf("crash sim / mid-run kill: Get: %v", err)
		}
		if updated.State != domain.Stopped {
			t.Errorf("crash sim / mid-run kill: stored state=%q, want Stopped", updated.State)
		}
	})

	t.Run("mid_removal_kill_marker_present", func(t *testing.T) {
		// Process crashed during removal: marker is set, VM is absent.
		// Recovery must report OutcomeTerminal and leave the record alone.
		t.Parallel()
		h := newHarness(t)

		sb := h.createSandbox(t, domain.Running, withRemoveOnExit())
		h.setRemovalMarker(t, sb)
		// VM absent (driver has no entry — removal killed it before crash, or
		// it was already stopped).

		report := h.runRecovery(t)
		out := findOutcome(t, report, sb.ID)

		if out.Kind != recovery.OutcomeTerminal {
			t.Errorf("crash sim / mid-removal kill (marker set): want %s, got %s (reason: %q)",
				recovery.OutcomeTerminal, out.Kind, out.Reason)
		}
	})

	t.Run("host_reboot_memory_loss", func(t *testing.T) {
		// Host reboots: paused VM loses its in-RAM state. Running VM unaffected.
		t.Parallel()
		h := newHarness(t)
		ctx := context.Background()

		paused := h.createSandbox(t, domain.Paused)
		running := h.createSandbox(t, domain.Running)
		h.drv.SetPaused(paused.ID)
		h.drv.SetRunning(running.ID)

		h.simulateHostReboot()
		// Paused VM is now absent; Running VM is unaffected.

		report := h.runRecovery(t)

		outPaused := findOutcome(t, report, paused.ID)
		if outPaused.Kind != recovery.OutcomeResolvedStopped {
			t.Errorf("crash sim / host reboot: paused sandbox: want %s, got %s (reason: %q)",
				recovery.OutcomeResolvedStopped, outPaused.Kind, outPaused.Reason)
		}
		pausedAfter, err := h.st.Get(ctx, paused.ID)
		if err != nil {
			t.Fatalf("crash sim / host reboot: Get paused: %v", err)
		}
		if pausedAfter.State != domain.Stopped {
			t.Errorf("crash sim / host reboot: paused stored state=%q, want Stopped", pausedAfter.State)
		}

		outRunning := findOutcome(t, report, running.ID)
		if outRunning.Kind != recovery.OutcomeAdopted && outRunning.Kind != recovery.OutcomeUnchanged {
			t.Errorf("crash sim / host reboot: running sandbox: want Adopted or Unchanged, got %s (reason: %q)",
				outRunning.Kind, outRunning.Reason)
		}
	})
}

// ── Unknown is never treated as Absent ───────────────────────────────────────

// TestUnknownIsNeverTreatedAsAbsent asserts that when the driver cannot
// determine VM state (returns Unknown), no destructive action is taken and the
// stored record is unchanged.
func TestUnknownIsNeverTreatedAsAbsent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	sb := h.createSandbox(t, domain.Running)
	h.drv.SetRunning(sb.ID)
	h.drv.SetObserveError(errors.New("substrate unreachable: Unknown must not be Absent"))

	report := h.runRecovery(t)
	out := findOutcome(t, report, sb.ID)

	if out.Kind != recovery.OutcomeIndeterminate {
		t.Errorf("Unknown observation: want %s, got %s (reason: %q) — "+
			"Unknown must never be treated as Absent; conflating them destroys live VMs",
			recovery.OutcomeIndeterminate, out.Kind, out.Reason)
	}

	// No destructive action.
	for _, c := range h.drv.Calls() {
		if c.Kind == fake.CallStop {
			t.Error("Unknown observation: driver.Stop called — " +
				"Unknown must never trigger destruction")
		}
	}

	// Record unchanged.
	unchanged, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Unknown observation: Get: %v", err)
	}
	if unchanged.State != domain.Running {
		t.Errorf("Unknown observation: state changed to %q; record must be unchanged when observation fails",
			unchanged.State)
	}
}

// ── Unknown + nil error (genuinely indeterminate) ─────────────────────────────

// TestUnknownNilError_Indeterminate asserts that when the driver successfully
// queries the substrate but returns Unknown with a nil error — a genuine
// "I don't know" rather than a communication failure — recovery treats the
// sandbox as indeterminate and takes no destructive action.
//
// Biting assertion: without the "|| obs.State == driver.Unknown" guard in
// recoverByID, Unknown+nil error falls through to the switch default case,
// producing reason "unexpected run state unknown from driver; no action
// taken". The guard produces "driver could not determine VM state; no action
// taken". Asserting the latter fails when the guard is deleted.
func TestUnknownNilError_Indeterminate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	sb := h.createSandbox(t, domain.Running)
	h.drv.SetRunning(sb.ID)
	h.drv.SetIndeterminate(true)

	report := h.runRecovery(t)
	out := findOutcome(t, report, sb.ID)

	if out.Kind != recovery.OutcomeIndeterminate {
		t.Errorf("Unknown+nil error: want %s, got %s (reason: %q)",
			recovery.OutcomeIndeterminate, out.Kind, out.Reason)
	}
	// Biting: fails when the || obs.State == driver.Unknown guard is deleted.
	if !strings.Contains(out.Reason, "driver could not determine") {
		t.Errorf("Unknown+nil error: Reason=%q must contain \"driver could not determine\" — "+
			"this fires when the || obs.State == driver.Unknown guard is missing "+
			"(the switch default case gives \"unexpected run state\" instead)", out.Reason)
	}

	// No destructive action.
	for _, c := range h.drv.Calls() {
		if c.Kind == fake.CallStop {
			t.Error("Unknown+nil error: driver.Stop called — Unknown must never trigger destruction")
		}
	}

	// Record unchanged.
	unchanged, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Unknown+nil error: Get: %v", err)
	}
	if unchanged.State != domain.Running {
		t.Errorf("Unknown+nil error: record changed to %q; must be unchanged", unchanged.State)
	}
}

// TestUnknownNilError_PausedNotResolvedToStopped asserts that a Paused
// sandbox whose driver returns Unknown+nil error is NOT resolved to stopped.
// Resolving to stopped is the Absent path — treating Unknown as Absent is
// the bug.
func TestUnknownNilError_PausedNotResolvedToStopped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	sb := h.createSandbox(t, domain.Paused)
	h.drv.SetPaused(sb.ID)
	h.drv.SetIndeterminate(true)

	report := h.runRecovery(t)
	out := findOutcome(t, report, sb.ID)

	// Absent path produces OutcomeResolvedStopped; Unknown must never do that.
	if out.Kind == recovery.OutcomeResolvedStopped {
		t.Errorf("Unknown+nil error on Paused sandbox: got %s — "+
			"Unknown must not be treated as Absent; that is the bug", out.Kind)
	}
	if out.Kind != recovery.OutcomeIndeterminate {
		t.Errorf("Unknown+nil error on Paused sandbox: want %s, got %s (reason: %q)",
			recovery.OutcomeIndeterminate, out.Kind, out.Reason)
	}
	// Biting: fails when the || obs.State == driver.Unknown guard is deleted.
	if !strings.Contains(out.Reason, "driver could not determine") {
		t.Errorf("Unknown+nil error on Paused sandbox: Reason=%q must contain "+
			"\"driver could not determine\" — fires when the guard is missing", out.Reason)
	}

	// Record must still be Paused.
	unchanged, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Unknown+nil error on Paused sandbox: Get: %v", err)
	}
	if unchanged.State != domain.Paused {
		t.Errorf("Unknown+nil error on Paused sandbox: record changed to %q; must stay Paused",
			unchanged.State)
	}
}

// TestUnknownNilError_RemoveOnExitNotRemoved asserts that a RemoveOnExit
// sandbox whose driver returns Unknown+nil error is NOT removed. Removal is
// triggered only by Absent; treating Unknown as Absent would destroy a VM
// that may still be running.
func TestUnknownNilError_RemoveOnExitNotRemoved(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	sb := h.createSandbox(t, domain.Running, withRemoveOnExit())
	h.drv.SetRunning(sb.ID)
	h.drv.SetIndeterminate(true)

	report := h.runRecovery(t)
	out := findOutcome(t, report, sb.ID)

	// Absent path produces OutcomeRemoved; Unknown must never do that.
	if out.Kind == recovery.OutcomeRemoved {
		t.Errorf("Unknown+nil error on --rm sandbox: got %s — "+
			"Unknown must not trigger removal; the VM may still be running", out.Kind)
	}
	if out.Kind != recovery.OutcomeIndeterminate {
		t.Errorf("Unknown+nil error on --rm sandbox: want %s, got %s (reason: %q)",
			recovery.OutcomeIndeterminate, out.Kind, out.Reason)
	}
	// Biting: fails when the || obs.State == driver.Unknown guard is deleted.
	if !strings.Contains(out.Reason, "driver could not determine") {
		t.Errorf("Unknown+nil error on --rm sandbox: Reason=%q must contain "+
			"\"driver could not determine\" — fires when the guard is missing", out.Reason)
	}

	// Sandbox must still exist in the store.
	if _, err := h.st.Get(ctx, sb.ID); err != nil {
		t.Errorf("Unknown+nil error on --rm sandbox: sandbox should still exist: %v", err)
	}
}

// ── Idempotence ───────────────────────────────────────────────────────────────

// TestIdempotence_RunningRecoveryTwiceChangesNothingOnSecondPass asserts that
// Recover is safe to call multiple times; a second consecutive call makes no
// further changes.
func TestIdempotence_RunningRecoveryTwiceChangesNothingOnSecondPass(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	sb := h.createSandbox(t, domain.Running)
	h.drv.SetRunning(sb.ID)

	report1 := h.runRecovery(t)
	out1 := findOutcome(t, report1, sb.ID)

	// Reset the call log so the second pass starts fresh.
	h.drv.ResetCalls()

	report2 := h.runRecovery(t)
	out2 := findOutcome(t, report2, sb.ID)

	if out1.Kind != out2.Kind {
		t.Errorf("idempotence: first pass=%s, second pass=%s — "+
			"second recovery must make no further changes",
			out1.Kind, out2.Kind)
	}

	// Record state must be stable across passes.
	after1, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("idempotence: Get after pass 1: %v", err)
	}

	h.runRecovery(t) // third pass

	after2, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("idempotence: Get after pass 2: %v", err)
	}
	if after1.State != after2.State {
		t.Errorf("idempotence: state changed between recovery passes: %q → %q",
			after1.State, after2.State)
	}
}

// ── Regression: --rm from never-started states ───────────────────────────────

// TestRegression_RemoveOnExit_NeverStarted_NotRemoved asserts that a sandbox
// that was never started (Created, Stopped, or Error state) is NOT deleted by
// recovery even when RemoveOnExit is set. The lifecycle machine's only removal
// edge starts from Running; the --rm decision must go through the machine.
//
// Defect: resolveAbsent previously checked "if sb.RemoveOnExit" without
// consulting the machine, unconditionally deleting any absent --rm sandbox
// regardless of its stored state. A sandbox in Created, Stopped, or Error
// never ran — there is nothing to remove.
func TestRegression_RemoveOnExit_NeverStarted_NotRemoved(t *testing.T) {
	t.Parallel()

	states := []domain.State{domain.Created, domain.Stopped, domain.Error}
	for _, state := range states {
		state := state
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			ctx := context.Background()

			sb := h.createSandbox(t, state, withRemoveOnExit())
			// VM is absent by default — sandbox never ran.

			report := h.runRecovery(t)
			out := findOutcome(t, report, sb.ID)

			// Biting: without the machine-routing fix this is OutcomeRemoved.
			if out.Kind != recovery.OutcomeUnchanged {
				t.Errorf("state=%s +--rm: want %s (sandbox never ran, nothing to remove), "+
					"got %s (reason: %q) — "+
					"fix: route --rm through mach.Next; %s has no removal edge",
					state, recovery.OutcomeUnchanged, out.Kind, out.Reason, state)
			}

			// Sandbox must still exist.
			if _, err := h.st.Get(ctx, sb.ID); err != nil {
				t.Errorf("state=%s +--rm: sandbox must still exist after recovery, got err=%v",
					state, err)
			}
		})
	}
}

// ── Regression: abandoned removal marker wedge sequence ──────────────────────

// TestRegression_AbandonedMarker_ClearedOnAdopt_PreventsFalseTerminal asserts
// the full four-step wedge sequence:
//
//  1. Running --rm sandbox: removal marker set, VM alive (abandoned rm).
//  2. Recovery adopts the live VM and CLEARS the marker.
//  3. VM stops cleanly (state→Stopped, VM absent, marker gone).
//  4. Recovery yields OutcomeUnchanged — NOT OutcomeTerminal.
//
// Defect: adopt noted the marker but did not call ClearRemovalMarker.
// After a clean stop, resolveAbsent found the stale marker and returned
// OutcomeTerminal, permanently wedging a healthy sandbox.
func TestRegression_AbandonedMarker_ClearedOnAdopt_PreventsFalseTerminal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	// Step 1: Running + --rm, VM alive, removal marker set (abandoned rm).
	sb := h.createSandbox(t, domain.Running, withRemoveOnExit())
	h.drv.SetRunning(sb.ID)
	h.setRemovalMarker(t, sb)

	// Step 2: Recovery — VM alive → adopt. Marker must be cleared.
	report1 := h.runRecovery(t)
	out1 := findOutcome(t, report1, sb.ID)
	if out1.Kind != recovery.OutcomeAdopted {
		t.Errorf("step 2: want %s (VM alive), got %s (reason: %q)",
			recovery.OutcomeAdopted, out1.Kind, out1.Reason)
	}

	afterAdopt, err := h.st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("step 2 Get: %v", err)
	}
	if afterAdopt.RemovalMarker {
		t.Errorf("step 2: removal marker must be cleared after adopt with live VM; " +
			"without the fix (ClearRemovalMarker call missing) it stays set, wedging step 4")
	}

	// Step 3: Simulate clean stop — VM exits, store updated to Stopped.
	h.drv.SimulateCrash(sb.ID)
	if err := h.st.Update(ctx, sb.ID, func(s *domain.Sandbox) error {
		s.State = domain.Stopped
		return nil
	}); err != nil {
		t.Fatalf("step 3 Update to Stopped: %v", err)
	}

	// Step 4: Recovery — Stopped + absent + no marker → OutcomeUnchanged.
	// Without the fix the stale marker causes OutcomeTerminal.
	h.drv.ResetCalls()
	report2 := h.runRecovery(t)
	out2 := findOutcome(t, report2, sb.ID)
	if out2.Kind != recovery.OutcomeUnchanged {
		t.Errorf("step 4: after clean stop, want %s, got %s (reason: %q) — "+
			"OutcomeTerminal means the marker was not cleared at step 2; "+
			"fix: call st.ClearRemovalMarker in adopt when RemovalMarker is set",
			recovery.OutcomeUnchanged, out2.Kind, out2.Reason)
	}
}

// ── Local test infrastructure ─────────────────────────────────────────────────

// walStore wraps store.Store and records SetRemovalMarker / Delete calls.
// Used by Ruling 5's WAL ordering sub-test.
type walStore struct {
	store.Store
	record func(string)
}

func (w *walStore) SetRemovalMarker(ctx context.Context, id domain.SandboxID) error {
	w.record("SetRemovalMarker")
	return w.Store.SetRemovalMarker(ctx, id)
}

func (w *walStore) Delete(ctx context.Context, id domain.SandboxID) error {
	w.record("Delete")
	return w.Store.Delete(ctx, id)
}

// walDriver wraps driver.Driver and records Stop calls.
// Used by Ruling 5's WAL ordering sub-test.
type walDriver struct {
	driver.Driver
	record func(string)
}

func (w *walDriver) Stop(ctx context.Context, id domain.SandboxID) error {
	w.record("Stop")
	return w.Driver.Stop(ctx, id)
}
