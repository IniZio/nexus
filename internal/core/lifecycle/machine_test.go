package lifecycle_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
)

// ── Guard: state count ───────────────────────────────────────────────────────

// TestStateSetIsExactlyFive asserts that domain.AllStates() returns exactly
// five elements. If a sixth state is added to the domain package, this test
// breaks immediately, forcing the author to revisit the transition table.
func TestStateSetIsExactlyFive(t *testing.T) {
	t.Parallel()
	const want = 5
	got := len(domain.AllStates())
	if got != want {
		t.Errorf("domain.AllStates() returned %d states; want exactly %d", got, want)
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// allTriggers is the exhaustive list of every trigger the machine knows about.
// When a new trigger is added to transitions.go, add it here too — the
// cross-product test will then exercise it against every state automatically.
var allTriggers = []lifecycle.Trigger{
	lifecycle.TriggerStart,
	lifecycle.TriggerPause,
	lifecycle.TriggerResume,
	lifecycle.TriggerStop,
	lifecycle.TriggerSubstrateLost,
	lifecycle.TriggerFail,
	lifecycle.TriggerReset,
	lifecycle.TriggerPrimaryCommandExit,
	lifecycle.TriggerSnapshot, // added P2W0: snapshot self-edge
	lifecycle.TriggerFork,     // added P2W0: fork (child-creation, always illegal from parent)
}

// ── Exhaustive cross-product ─────────────────────────────────────────────────

// TestExhaustiveCrossProduct iterates all five states × all triggers and
// asserts that each pair is either accepted (in the table) or rejected with a
// well-formed *IllegalTransitionError. No pair may silently misbehave.
func TestExhaustiveCrossProduct(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	// Build a lookup of what the table declares so we can compare against it.
	type key struct {
		from    domain.State
		trigger lifecycle.Trigger
	}
	declared := make(map[key]lifecycle.Edge)
	for _, e := range m.All() {
		declared[key{e.From, e.Trigger}] = e
	}

	for _, state := range domain.AllStates() {
		for _, trigger := range allTriggers {
			state, trigger := state, trigger // capture for parallel sub-tests
			t.Run(state.String()+"/"+string(trigger), func(t *testing.T) {
				t.Parallel()
				k := key{state, trigger}

				tr, err := m.Next(state, trigger)

				if wantEdge, inTable := declared[k]; inTable {
					// The pair IS in the table: Next must succeed.
					if err != nil {
						t.Fatalf("Next(%q, %q) = _, %v; want nil error (edge is in table)", state, trigger, err)
					}
					// Verify the transition matches the declared edge.
					if tr.Remove != wantEdge.Removal {
						t.Errorf("Next(%q, %q).Remove = %v; edge declares Removal=%v", state, trigger, tr.Remove, wantEdge.Removal)
					}
					if !tr.Remove && tr.NextState != wantEdge.To {
						t.Errorf("Next(%q, %q).NextState = %q; want %q", state, trigger, tr.NextState, wantEdge.To)
					}
					// Can must agree.
					if !m.Can(state, trigger) {
						t.Errorf("Can(%q, %q) = false; want true (edge is in table)", state, trigger)
					}
				} else {
					// The pair is NOT in the table: Next must return *IllegalTransitionError.
					if err == nil {
						t.Fatalf("Next(%q, %q) = %v, nil; want error (edge not in table)", state, trigger, tr)
					}
					var ite *lifecycle.IllegalTransitionError
					if !errors.As(err, &ite) {
						t.Fatalf("Next(%q, %q) returned %T; want *IllegalTransitionError", state, trigger, err)
					}
					if ite.From != state {
						t.Errorf("IllegalTransitionError.From = %q; want %q", ite.From, state)
					}
					if ite.Trigger != trigger {
						t.Errorf("IllegalTransitionError.Trigger = %q; want %q", ite.Trigger, trigger)
					}
					// LegalTriggers must not be nil and must be sorted.
					for i := 1; i < len(ite.LegalTriggers); i++ {
						if ite.LegalTriggers[i] < ite.LegalTriggers[i-1] {
							t.Errorf(
								"LegalTriggers not sorted at index %d: %q > %q",
								i, ite.LegalTriggers[i-1], ite.LegalTriggers[i],
							)
						}
					}
					// Can must agree.
					if m.Can(state, trigger) {
						t.Errorf("Can(%q, %q) = true; want false (edge not in table)", state, trigger)
					}
					// Error message must mention the state and trigger.
					msg := err.Error()
					if !strings.Contains(msg, string(trigger)) {
						t.Errorf("error message %q does not mention trigger %q", msg, trigger)
					}
					if !strings.Contains(msg, state.String()) {
						t.Errorf("error message %q does not mention state %q", msg, state)
					}
				}
			})
		}
	}
}

// ── Declared edges ───────────────────────────────────────────────────────────

// TestDeclaredEdges verifies every row in the transition table is reachable and
// produces the expected outcome (removal signal or next state).
func TestDeclaredEdges(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	for _, e := range m.All() {
		e := e
		t.Run(e.From.String()+"/"+string(e.Trigger), func(t *testing.T) {
			t.Parallel()
			tr, err := m.Next(e.From, e.Trigger)
			if err != nil {
				t.Fatalf("Next(%q, %q) = _, %v; want nil", e.From, e.Trigger, err)
			}
			if tr.Remove != e.Removal {
				t.Errorf("Next(%q, %q).Remove = %v; edge declares Removal=%v", e.From, e.Trigger, tr.Remove, e.Removal)
			}
			if !tr.Remove && tr.NextState != e.To {
				t.Errorf("Next(%q, %q).NextState = %q; want %q", e.From, e.Trigger, tr.NextState, e.To)
			}
		})
	}
}

// ── Specific correctness assertions ─────────────────────────────────────────

// TestCreatedToRunning verifies the basic start edge.
func TestCreatedToRunning(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()
	tr, err := m.Next(domain.Created, lifecycle.TriggerStart)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.Remove {
		t.Error("Next(created, start).Remove = true; want false")
	}
	if tr.NextState != domain.Running {
		t.Errorf("Next(created, start).NextState = %q; want running", tr.NextState)
	}
}

// TestPauseAndResumeCycle verifies the round-trip running->paused->running.
func TestPauseAndResumeCycle(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	pause, err := m.Next(domain.Running, lifecycle.TriggerPause)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if pause.NextState != domain.Paused {
		t.Errorf("pause: got %q; want paused", pause.NextState)
	}

	resume, err := m.Next(domain.Paused, lifecycle.TriggerResume)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resume.NextState != domain.Running {
		t.Errorf("resume: got %q; want running", resume.NextState)
	}
}

// TestSubstrateLost verifies that a paused sandbox transitions to stopped when
// the substrate is lost, and that the transition is system-initiated.
func TestSubstrateLost(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	tr, err := m.Next(domain.Paused, lifecycle.TriggerSubstrateLost)
	if err != nil {
		t.Fatalf("substrate_lost: %v", err)
	}
	if tr.NextState != domain.Stopped {
		t.Errorf("Next(paused, substrate_lost).NextState = %q; want stopped", tr.NextState)
	}

	initiator, err := m.Initiator(domain.Paused, lifecycle.TriggerSubstrateLost)
	if err != nil {
		t.Fatalf("Initiator(paused, substrate_lost): %v", err)
	}
	if initiator != lifecycle.InitiatorSystem {
		t.Errorf("Initiator(paused, substrate_lost) = %q; want system", initiator)
	}
}

// TestInitiators verifies the user/system distinction for key edges.
func TestInitiators(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	cases := []struct {
		from     domain.State
		trigger  lifecycle.Trigger
		wantInit lifecycle.Initiator
	}{
		{domain.Running, lifecycle.TriggerPause, lifecycle.InitiatorUser},
		{domain.Paused, lifecycle.TriggerResume, lifecycle.InitiatorUser},
		{domain.Paused, lifecycle.TriggerSubstrateLost, lifecycle.InitiatorSystem},
		{domain.Running, lifecycle.TriggerFail, lifecycle.InitiatorSystem},
		{domain.Error, lifecycle.TriggerReset, lifecycle.InitiatorUser},
		{domain.Running, lifecycle.TriggerPrimaryCommandExit, lifecycle.InitiatorSystem},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.from.String()+"/"+string(tc.trigger), func(t *testing.T) {
			t.Parallel()
			got, err := m.Initiator(tc.from, tc.trigger)
			if err != nil {
				t.Fatalf("Initiator(%q, %q): %v", tc.from, tc.trigger, err)
			}
			if got != tc.wantInit {
				t.Errorf("Initiator(%q, %q) = %q; want %q", tc.from, tc.trigger, got, tc.wantInit)
			}
		})
	}
}

// TestIllegalTransitionError verifies that an illegal transition returns a
// well-formed error with useful content.
func TestIllegalTransitionError(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	// pause from created is not in the table.
	_, err := m.Next(domain.Created, lifecycle.TriggerPause)
	if err == nil {
		t.Fatal("Next(created, pause): expected error, got nil")
	}
	var ite *lifecycle.IllegalTransitionError
	if !errors.As(err, &ite) {
		t.Fatalf("expected *IllegalTransitionError, got %T: %v", err, err)
	}
	if ite.From != domain.Created {
		t.Errorf("ite.From = %q; want created", ite.From)
	}
	if ite.Trigger != lifecycle.TriggerPause {
		t.Errorf("ite.Trigger = %q; want pause", ite.Trigger)
	}
	// LegalTriggers must include "start" and "fail" (the legal edges from created).
	legal := make(map[lifecycle.Trigger]bool)
	for _, tr := range ite.LegalTriggers {
		legal[tr] = true
	}
	for _, want := range []lifecycle.Trigger{lifecycle.TriggerStart, lifecycle.TriggerFail} {
		if !legal[want] {
			t.Errorf("LegalTriggers missing %q; got %v", want, ite.LegalTriggers)
		}
	}
	// Error message must be human-readable.
	msg := err.Error()
	if !strings.Contains(msg, "pause") {
		t.Errorf("error %q does not mention trigger 'pause'", msg)
	}
	if !strings.Contains(msg, "created") {
		t.Errorf("error %q does not mention state 'created'", msg)
	}
}

// TestLegalTriggersSorted verifies that LegalTriggers always returns a sorted
// slice for every state.
func TestLegalTriggersSorted(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()
	for _, state := range domain.AllStates() {
		triggers := m.LegalTriggers(state)
		for i := 1; i < len(triggers); i++ {
			if triggers[i] < triggers[i-1] {
				t.Errorf(
					"LegalTriggers(%q) not sorted at index %d: %q > %q",
					state, i, triggers[i-1], triggers[i],
				)
			}
		}
	}
}

// ── --rm intent ──────────────────────────────────────────────────────────────

// TestRmRemovesOnExitZero asserts that --rm fires on exit code 0.
func TestRmRemovesOnExitZero(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()
	intent := lifecycle.Intent{RemoveOnExit: true}

	outcome, err := lifecycle.OnPrimaryCommandExit(m, intent, domain.Running, 0)
	if err != nil {
		t.Fatalf("exit 0: %v", err)
	}
	if !outcome.Remove {
		t.Error("exit 0 with --rm: Remove=false; want true")
	}
}

// TestRmRemovesOnExitNonZero asserts that --rm fires on non-zero exit codes —
// "unconditional" is the whole point of --rm.
func TestRmRemovesOnExitNonZero(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()
	intent := lifecycle.Intent{RemoveOnExit: true}

	for _, code := range []int{1, 2, 127, 255} {
		code := code
		t.Run("exit"+string(rune('0'+code%10)), func(t *testing.T) {
			t.Parallel()
			outcome, err := lifecycle.OnPrimaryCommandExit(m, intent, domain.Running, code)
			if err != nil {
				t.Fatalf("exit %d: %v", code, err)
			}
			if !outcome.Remove {
				t.Errorf("exit %d with --rm: Remove=false; want true", code)
			}
		})
	}
}

// TestNoRmKeepsSandbox asserts that without --rm, primary command exit does
// not remove the sandbox and returns the current state unchanged.
func TestNoRmKeepsSandbox(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()
	intent := lifecycle.Intent{RemoveOnExit: false}

	outcome, err := lifecycle.OnPrimaryCommandExit(m, intent, domain.Running, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Remove {
		t.Error("without --rm: Remove=true; want false")
	}
	if outcome.NextState != domain.Running {
		t.Errorf("without --rm: NextState=%q; want running", outcome.NextState)
	}
}

// TestRemovalTransitionCannotBePersisted asserts the core data-integrity
// property: the --rm edge signals removal without producing a storable
// domain.State. The Transition.Remove flag is true, and the Transition.NextState
// (zero value) fails marshalling, so the original bug — "uninitialised Sandbox
// silently persists State(0)" — is now caught at both the type level and the
// JSON layer.
func TestRemovalTransitionCannotBePersisted(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	tr, err := m.Next(domain.Running, lifecycle.TriggerPrimaryCommandExit)
	if err != nil {
		t.Fatalf("Next(running, primary_command_exit): %v", err)
	}

	// The --rm edge must signal removal, not a state transition.
	if !tr.Remove {
		t.Error("--rm edge: Transition.Remove = false; want true")
	}

	// Transition.NextState when Remove==true is the zero value of domain.State.
	// It must not be a valid durable state.
	if tr.NextState.Valid() {
		t.Errorf("--rm edge: Transition.NextState.Valid() = true for state %q; "+
			"zero value must not be valid when Remove==true", tr.NextState)
	}

	// It must also fail to marshal, providing a defence-in-depth guard at the
	// store boundary.
	_, marshalErr := json.Marshal(tr.NextState)
	if marshalErr == nil {
		t.Error("--rm edge: json.Marshal(Transition.NextState) succeeded; want error so removal cannot be stored")
	}

	// Verify the transition table still contains the --rm edge (edge 13).
	rmEdgeFound := false
	for _, e := range m.All() {
		if e.From == domain.Running && e.Trigger == lifecycle.TriggerPrimaryCommandExit {
			if !e.Removal {
				t.Errorf("--rm edge in table has Removal=false; want true")
			}
			rmEdgeFound = true
		}
	}
	if !rmEdgeFound {
		t.Error("--rm edge (running/primary_command_exit) not found in transition table")
	}
}

// TestRmEdgeYieldsRemoval verifies the --rm edge directly via Machine.Next.
func TestRmEdgeYieldsRemoval(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	tr, err := m.Next(domain.Running, lifecycle.TriggerPrimaryCommandExit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tr.Remove {
		t.Error("Next(running, primary_command_exit): Remove=false; want true")
	}
}

// ── No time-based triggers ───────────────────────────────────────────────────

// TestNoTimeBasedTriggers asserts that no trigger in the table is purely
// time-based. Automatic lifecycle transitions (TTL, idle-stop, auto-destroy,
// eviction) are explicitly prohibited in v1. The test inspects trigger names
// for time-related keywords.
func TestNoTimeBasedTriggers(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	// Keywords that would indicate a time-based trigger.
	timeTriggers := []string{
		"ttl", "timeout", "expire", "idle", "evict", "auto_stop",
		"auto_destroy", "tick", "timer", "schedule",
	}

	seen := make(map[lifecycle.Trigger]bool)
	for _, e := range m.All() {
		seen[e.Trigger] = true
	}
	for trigger := range seen {
		name := strings.ToLower(string(trigger))
		for _, kw := range timeTriggers {
			if strings.Contains(name, kw) {
				t.Errorf(
					"trigger %q looks time-based (contains %q); "+
						"automatic lifecycle transitions are prohibited in v1",
					trigger, kw,
				)
			}
		}
	}
}

// ── Machine integrity ────────────────────────────────────────────────────────

// TestAllTableEdgesReachable verifies that every edge in the table is reachable
// via Next and returns the declared target. This catches inconsistency between
// the table and any future index structure.
func TestAllTableEdgesReachable(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()
	for _, e := range m.All() {
		tr, err := m.Next(e.From, e.Trigger)
		if err != nil {
			t.Errorf("table edge (%q, %q): Next returned error %v", e.From, e.Trigger, err)
			continue
		}
		if tr.Remove != e.Removal {
			t.Errorf("table edge (%q, %q): Next.Remove=%v; table says Removal=%v", e.From, e.Trigger, tr.Remove, e.Removal)
		}
		if !tr.Remove && tr.NextState != e.To {
			t.Errorf("table edge (%q, %q): Next.NextState=%q; table says To=%q", e.From, e.Trigger, tr.NextState, e.To)
		}
	}
}

// TestInitiatorMatchesTable verifies that Initiator returns the value from the
// table for every declared edge.
func TestInitiatorMatchesTable(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()
	for _, e := range m.All() {
		got, err := m.Initiator(e.From, e.Trigger)
		if err != nil {
			t.Errorf("Initiator(%q, %q): %v", e.From, e.Trigger, err)
			continue
		}
		if got != e.Initiator {
			t.Errorf("Initiator(%q, %q) = %q; table says %q", e.From, e.Trigger, got, e.Initiator)
		}
	}
}

// TestTableHasExactly16Edges verifies the transition table has exactly 16
// declared edges. If a new edge is added, this test breaks deliberately,
// requiring the author to update both the table and this count.
//
// Edge 6b (Running+SubstrateLost→Stopped) was added in S3 to remove the
// semantic wart of using TriggerStop (InitiatorUser) for system-initiated
// substrate loss recovery of running sandboxes. It mirrors edge 6
// (Paused+SubstrateLost→Stopped) and completes edge 10 of ticket-19.
//
// Edges S1 and S2 (TriggerSnapshot: Running→Running, Stopped→Stopped) were
// added in P2W0 as state-preserving self-edges for snapshot operations. Fork
// (TriggerFork) has no table entry — the parent has no transition (spec 06
// edge 5: ∅→running for children).
func TestTableHasExactly16Edges(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()
	const want = 16
	got := len(m.All())
	if got != want {
		t.Errorf("transition table has %d edges; want exactly %d", got, want)
	}
}

// ── P2W0 snapshot + fork edges ───────────────────────────────────────────────

// TestSnapshotSelfEdges verifies that TriggerSnapshot is a self-edge: the
// sandbox remains in its current state after the operation.
func TestSnapshotSelfEdges(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	cases := []struct {
		from domain.State
		want domain.State
	}{
		{domain.Running, domain.Running},
		{domain.Stopped, domain.Stopped},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.from.String()+"/snapshot", func(t *testing.T) {
			t.Parallel()
			tr, err := m.Next(tc.from, lifecycle.TriggerSnapshot)
			if err != nil {
				t.Fatalf("Next(%q, snapshot): %v", tc.from, err)
			}
			if tr.Remove {
				t.Errorf("Next(%q, snapshot).Remove = true; want false", tc.from)
			}
			if tr.NextState != tc.want {
				t.Errorf("Next(%q, snapshot).NextState = %q; want %q", tc.from, tr.NextState, tc.want)
			}
		})
	}
}

// TestSnapshotIllegalFromCreatedPausedError verifies that TriggerSnapshot is
// rejected from states where a snapshot is not defined.
func TestSnapshotIllegalFromCreatedPausedError(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	for _, state := range []domain.State{domain.Created, domain.Paused, domain.Error} {
		state := state
		t.Run(state.String()+"/snapshot", func(t *testing.T) {
			t.Parallel()
			_, err := m.Next(state, lifecycle.TriggerSnapshot)
			if err == nil {
				t.Errorf("Next(%q, snapshot): expected error (illegal transition), got nil", state)
			}
		})
	}
}

// TestSnapshotInitiatorIsUser verifies that snapshot transitions are
// user-initiated (not system-initiated).
func TestSnapshotInitiatorIsUser(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	for _, state := range []domain.State{domain.Running, domain.Stopped} {
		state := state
		t.Run(state.String()+"/snapshot", func(t *testing.T) {
			t.Parallel()
			got, err := m.Initiator(state, lifecycle.TriggerSnapshot)
			if err != nil {
				t.Fatalf("Initiator(%q, snapshot): %v", state, err)
			}
			if got != lifecycle.InitiatorUser {
				t.Errorf("Initiator(%q, snapshot) = %q; want user", state, got)
			}
		})
	}
}

// TestForkAlwaysIllegalFromParent verifies that TriggerFork returns
// IllegalTransitionError from every valid parent state. Fork is pure
// child-creation (spec 06, edge 5: ∅→running); the parent has no transition.
func TestForkAlwaysIllegalFromParent(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	for _, state := range domain.AllStates() {
		state := state
		t.Run(state.String()+"/fork", func(t *testing.T) {
			t.Parallel()
			_, err := m.Next(state, lifecycle.TriggerFork)
			if err == nil {
				t.Errorf("Next(%q, fork): expected error (fork has no parent transition), got nil", state)
			}
		})
	}
}

// TestNoDuplicateTableEdges verifies that no (From, Trigger) pair appears more
// than once in the transition table. A duplicate would mean Machine.Next
// silently returns only the first match while the second row is unreachable.
func TestNoDuplicateTableEdges(t *testing.T) {
	t.Parallel()
	m := lifecycle.New()

	type key struct {
		from    domain.State
		trigger lifecycle.Trigger
	}
	seen := make(map[key]bool)
	for _, e := range m.All() {
		k := key{e.From, e.Trigger}
		if seen[k] {
			t.Errorf("duplicate table edge: (from=%q, trigger=%q) — second row is unreachable", e.From, e.Trigger)
		}
		seen[k] = true
	}
}
