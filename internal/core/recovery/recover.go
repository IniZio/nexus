// Package recovery reconciles persisted sandbox records against the live
// substrate at CLI invocation time.
//
// # The governing rule
//
// The substrate is authoritative. The stored record is a cache. Where a live
// VM disagrees with the stored state, the VM wins. [Recoverer.recoverByID]
// calls [driver.Driver.Observe] before consulting any stored field; the
// ordering is structural, not incidental, and is enforced by the exclusive
// per-sandbox flock that spans the entire observe → decide → write sequence.
//
// # Concurrency safety — per-sandbox exclusive lock
//
// [Recoverer.recoverByID] holds the per-sandbox exclusive flock for the
// entire observe → decide → write sequence by running inside a single
// [store.Store.Update] callback. Driver.Observe is called first, inside the
// callback. Every branch on stored state is therefore downstream of both the
// substrate observation and the lock acquisition, which prevents a concurrent
// [service.Service.Start] from committing a live VM state between the
// observation and the record write.
//
// The lock is per-sandbox (one flock file per sandbox directory). Recovering
// N sandboxes acquires N independent flocks — never all at once — so recovery
// does not serialise into a single global bottleneck.
//
// # Reentrancy constraint
//
// [driver.Driver.Observe] is called while the per-sandbox exclusive flock is
// held. The [driver.Driver] interface prohibits re-entrant store calls from
// within a driver method; a re-entrant call would deadlock because the flock
// is non-recursive. See the Driver interface doc for the full prohibition.
//
// # Ordering invariant
//
// The substrate-first ordering is guaranteed by the flock: Observe runs inside
// the exclusive Update callback, so every decision branch on rec fields is
// downstream of both the observation and the lock acquisition. The staleness
// hazard is eliminated rather than merely reordered: the substrate is always
// queried before any stored field is consulted, and under the lock no
// concurrent writer can change the record between observation and write.
//
// # Corrupt records
//
// [store.Store.List] silently skips sandbox directories with corrupt or
// unreadable record.json files (interrupted creates, future-schema records,
// JSON parse errors). That silent skip IS the "report it and continue"
// guarantee for this package: a corrupt entry never reaches [Recoverer.Recover]
// and therefore never blocks recovery of healthy sandboxes. No additional
// handling is needed at this layer.
package recovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// errSkipWrite is returned from an Update callback to signal that the record
// should not be written. The outcome is captured in a closure variable.
// It is never propagated to callers of recoverByID.
var errSkipWrite = errors.New("recovery: no write needed")

// OutcomeKind classifies the result of recovering a single sandbox.
type OutcomeKind string

const (
	// OutcomeAdopted means the VM was found alive (running or paused) and the
	// record was corrected to match the observed state. The sandbox is healthy.
	OutcomeAdopted OutcomeKind = "adopted"

	// OutcomeResolvedStopped means an absent VM caused the record to be
	// transitioned to stopped. For stored paused + absent this implies memory
	// loss, which is surfaced in the Reason field.
	OutcomeResolvedStopped OutcomeKind = "resolved_stopped"

	// OutcomeRemoved means the sandbox was deleted because RemoveOnExit was set
	// and the VM was absent with no removal marker.
	OutcomeRemoved OutcomeKind = "removed"

	// OutcomeTerminal means a removal marker was already set when the crash hit.
	// Removal is terminal and must never be retried. Manual cleanup is required.
	OutcomeTerminal OutcomeKind = "terminal"

	// OutcomeIndeterminate means the driver returned Unknown, or recovery
	// encountered an error that prevents a safe decision. No destructive action
	// was taken and the record is unchanged.
	OutcomeIndeterminate OutcomeKind = "indeterminate"

	// OutcomeUnchanged means the sandbox required no action (e.g. already
	// stopped with no running VM, or already in the correct state).
	OutcomeUnchanged OutcomeKind = "unchanged"

	// OutcomeAdoptable means the VM was found alive (running or paused) but
	// its recorded supervisor is dead: [supervisor.CheckAndReconcile] found
	// the persisted (SupervisorPID, SupervisorSock) pair does not answer.
	// This is the live-VM/dead-supervisor class (AC-8): the sandbox needs a
	// replacement supervisor, and is reported as such rather than as plainly
	// running. The stale SupervisorPID/SupervisorSock are cleared from the
	// record (per CheckAndReconcile's documented caller contract) but the VM
	// itself is never touched — recovery may adopt, never stop, a live VM
	// (D-HSH-04).
	OutcomeAdoptable OutcomeKind = "adoptable"
)

// SandboxOutcome is the result of recovering a single sandbox.
type SandboxOutcome struct {
	// ID is the sandbox that was examined.
	ID domain.SandboxID `json:"id"`

	// Kind classifies the recovery outcome.
	Kind OutcomeKind `json:"kind"`

	// Reason is a display-ready explanation suitable for terminal or log output.
	Reason string `json:"reason"`
}

// Report is the result of a [Recoverer.Recover] run.
type Report struct {
	// Outcomes holds one entry per sandbox examined. Order is unspecified.
	Outcomes []SandboxOutcome `json:"outcomes"`
}

// Recoverer reconciles persisted sandbox records against the live substrate.
// Construct with [New].
type Recoverer struct {
	st      store.Store
	drv     driver.Driver
	mach    lifecycle.Machine
	diskDir string // durable dir for per-sandbox ext4 copies (S-COW); empty = defaultDiskDir()

	// checkSupervisor determines whether the supervisor recorded as
	// (pid, sockPath) is alive. nil (the New default) means "not wired" and
	// the supervisor-liveness cross-check (OutcomeAdoptable, AC-8) is
	// skipped entirely — recovery falls back to today's record-level-only
	// behaviour.
	//
	// This package deliberately does NOT import internal/supervisor and
	// default-wire [supervisor.CheckAndReconcile] itself:
	// internal/core/driver/cloudhypervisor's test package imports
	// internal/core/recovery (ch_netns_lifecycle_test.go), and
	// internal/supervisor imports internal/core/driver/cloudhypervisor
	// (adopt.go) — a direct import here closes that into an import cycle.
	// The real production callback is wired one layer up, in the CLI
	// package, which already imports both without cycling. Set with
	// [Recoverer.WithSupervisorCheck]; the same primitive the orphan-sweep
	// path uses (signal 0 plus a 500 ms socket dial) so recovery does not
	// re-derive liveness with a looser check of its own.
	checkSupervisor func(pid int, sockPath string) (alive bool, err error)

	// spawnAdopt, when wired, is called for every sandbox classified
	// [OutcomeAdoptable] whose record carries a netns control socket. It
	// spawns a long-lived replacement supervisor that rebuilds the perimeter
	// through the surviving netns child (production:
	// supervisor.SpawnReacquireDetached via the CLI).
	//
	// Injected as a callback for the same import-cycle reason as
	// checkSupervisor: this package must not import internal/supervisor.
	//
	// Nil (the New default) means the spawn half is unwired and recovery
	// only REPORTS the adoptable class, which is the behaviour that shipped
	// with AC-8. The report is emitted either way — a sandbox that cannot be
	// spawned against (no control socket, or no spawner) is still surfaced
	// to the operator rather than silently skipped.
	spawnAdopt func(sb domain.Sandbox) (ca CAOutcome, err error)
}

// CAOutcome is what a spawned replacement supervisor did with the MITM CA,
// as REPORTED BY THAT SUPERVISOR — not as guessed by the spawner.
//
// This package cannot import internal/supervisor (import cycle), so the
// supervisor's own outcome type is mapped onto this one by the CLI adapter
// that owns both imports. The three states are identical and deliberate: the
// zero value is [CAUnknown], so a spawner that forgets to report, or a
// supervisor whose outcome could not be read, produces an honest "could not
// determine" rather than either definite claim.
type CAOutcome int

const (
	// CAUnknown means the replacement's CA outcome could not be determined.
	// It is the zero value so silence is never mistaken for good news.
	CAUnknown CAOutcome = iota

	// CARecovered means the replacement re-seeded the persisted CA, so the
	// guest's existing TLS trust survived the recovery.
	CARecovered

	// CALost means the replacement had to mint a FRESH CA, so in-guest TLS
	// sessions fail until the guest re-imports it.
	CALost
)

// New constructs a Recoverer backed by the given store and driver. The
// supervisor-liveness cross-check (OutcomeAdoptable) is unwired until the
// caller supplies one via [Recoverer.WithSupervisorCheck] — see that field's
// doc comment for why New cannot default-wire it itself.
func New(st store.Store, drv driver.Driver) *Recoverer {
	return &Recoverer{
		st:   st,
		drv:  drv,
		mach: lifecycle.New(),
	}
}

// WithSupervisorCheck wires the supervisor-liveness primitive used to detect
// the live-VM/dead-supervisor class (AC-8, OutcomeAdoptable). Production
// callers (internal/cli) pass [supervisor.CheckAndReconcile]; tests pass a
// fake to simulate a dead supervisor without a real process and Unix-domain
// socket. A nil fn (the New default) disables the cross-check.
func (r *Recoverer) WithSupervisorCheck(fn func(pid int, sockPath string) (bool, error)) *Recoverer {
	r.checkSupervisor = fn
	return r
}

// WithAdoptSpawner wires the spawn half of crash recovery (D-HSH-15,
// operator-ratified TBR-4: recover adopts AUTOMATICALLY). Production callers
// (internal/cli) pass a closure over supervisor.SpawnReacquireDetached; tests
// pass a fake that records invocations without forking a process.
//
// Without this, recovery detects the live-VM/dead-supervisor class and stops
// — which is exactly the gap this leaves: a mechanism with no caller. A nil
// fn (the New default) preserves that report-only behaviour.
func (r *Recoverer) WithAdoptSpawner(fn func(sb domain.Sandbox) (ca CAOutcome, err error)) *Recoverer {
	r.spawnAdopt = fn
	return r
}

// WithDiskDir sets the directory where per-sandbox ext4 disk copies are reaped
// on the --rm removal path. When not set, service.ReapDiskCopy falls back to
// defaultDiskDir() which mirrors the store's durable root
// (store.DefaultRoot()/disks). Tests set this to t.TempDir() so copies stay
// inside the test filesystem tree and are cleaned up automatically.
func (r *Recoverer) WithDiskDir(dir string) *Recoverer {
	r.diskDir = dir
	return r
}

// Recover examines every sandbox in the store and reconciles its record
// against the live substrate. It is safe to call multiple times; a second
// consecutive call makes no further changes (idempotent).
//
// An error is returned only if the store cannot be listed. Errors for
// individual sandboxes are captured in the returned [Report.Outcomes].
func (r *Recoverer) Recover(ctx context.Context) (Report, error) {
	sandboxes, err := r.st.List(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("recovery: list sandboxes: %w", err)
	}

	// Extract IDs only. Per-sandbox logic calls Observe before re-reading the
	// record, making the substrate-first ordering structurally enforceable:
	// any code path that branches on the stored state must be downstream of
	// the Observe call.
	ids := make([]domain.SandboxID, len(sandboxes))
	for i, sb := range sandboxes {
		ids[i] = sb.ID
	}

	outcomes := make([]SandboxOutcome, 0, len(ids))
	for _, id := range ids {
		outcomes = append(outcomes, r.recoverByID(ctx, id))
	}
	return Report{Outcomes: outcomes}, nil
}

// RecoverOne reconciles a single sandbox record against the live substrate.
func (r *Recoverer) RecoverOne(ctx context.Context, id domain.SandboxID) (SandboxOutcome, error) {
	return r.recoverByID(ctx, id), nil
}

// recoverByID is the per-sandbox recovery logic.
//
// The entire observe → decide → write sequence runs inside a single
// store.Update callback so that the exclusive per-sandbox flock spans all
// three steps. Within the callback:
//
//  1. driver.Observe  — substrate is the authority; called first, under lock.
//  2. decide          — branch on observed state, not stored state.
//  3. mutate *rec     — applied directly; a single Update write covers all
//     changes. No nested Update / SetRemovalMarker /
//     ClearRemovalMarker calls are made; they would deadlock.
//
// When deletion is needed (--rm path), the removal marker is written by the
// Update callback (under the lock); then Stop is called to clean up any
// orphaned VMM process; then Delete is called. All three run after Update
// returns and outside the flock (the flock is non-recursive). The marker
// fences concurrent Start: service.Start re-validates RemovalMarker=true and
// rejects. A crash in the gap between Update and Delete leaves the marker set;
// the next recovery observes Absent+marker → Terminal — identical crash
// semantics to the prior SetRemovalMarker-then-Delete sequence. The WAL
// ordering is: marker (under lock) → Stop → Delete, consistent with Ruling 5.
//
// Note: holding the flock across an Observe call is consistent with the
// reentrancy prohibition on the Driver interface. A hung substrate blocks
// other operations on this sandbox only — never the entire system. A blocked
// operation is recoverable; an orphaned VM is not. (See service.Start's doc
// for the same trade-off.)
func (r *Recoverer) recoverByID(ctx context.Context, id domain.SandboxID) SandboxOutcome {
	var outcome SandboxOutcome
	var needDelete bool

	// ── ORDERING INVARIANT — do not move Observe out of this callback ────────
	// Substrate-first ordering (observe → decide → write) is guaranteed by the
	// exclusive per-sandbox flock that store.Update holds for the entire
	// callback body. Moving driver.Observe back outside the callback would
	// silently remove that protection: a concurrent service.Start could commit
	// a live Running state between an outside Observe and the lock acquisition,
	// causing recovery to overwrite a live record with Stopped.
	// The flock IS the guarantee. Keep Observe inside.
	// adoptable snapshots the record of a sandbox classified OutcomeAdoptable
	// so the replacement supervisor can be spawned AFTER the flock is
	// released. Taken inside the lock because *rec is only valid there.
	var adoptable domain.Sandbox
	var haveAdoptable bool

	updateErr := r.st.Update(ctx, id, func(rec *domain.Sandbox) error {
		// ── Step 1: observe the substrate — first, always ────────────────────
		// Called first, inside the exclusive flock. Every decision branch below
		// is downstream of both the substrate observation and the lock.
		obs, obsErr := r.drv.Observe(ctx, id)

		// Unknown means the driver could not determine the VM's state. Never
		// treat Unknown as Absent — conflating them is how a live VM gets
		// destroyed.
		if obsErr != nil || obs.State == driver.Unknown {
			reason := "driver could not determine VM state; no action taken"
			if obsErr != nil {
				reason = fmt.Sprintf("driver observe error: %v; no action taken", obsErr)
			}
			outcome = SandboxOutcome{ID: id, Kind: OutcomeIndeterminate, Reason: reason}
			return errSkipWrite
		}

		// ── Step 2: decide based on what the substrate reports ───────────────
		switch obs.State {
		case driver.Running:
			wrote := r.applyAdopt(rec, domain.Running, obs.InstanceID, &outcome)
			wroteSup := r.applySupervisorLiveness(rec, &outcome)
			if outcome.Kind == OutcomeAdoptable {
				adoptable = *rec
				haveAdoptable = true
			}
			if !wrote && !wroteSup {
				return errSkipWrite
			}
		case driver.Paused:
			wrote := r.applyAdopt(rec, domain.Paused, obs.InstanceID, &outcome)
			wroteSup := r.applySupervisorLiveness(rec, &outcome)
			if outcome.Kind == OutcomeAdoptable {
				adoptable = *rec
				haveAdoptable = true
			}
			if !wrote && !wroteSup {
				return errSkipWrite
			}
		case driver.Absent:
			wrote, del := r.applyAbsent(rec, &outcome)
			if del {
				needDelete = true
			}
			if !wrote {
				return errSkipWrite
			}
		default:
			outcome = SandboxOutcome{ID: id, Kind: OutcomeIndeterminate, Reason: fmt.Sprintf("unexpected run state %s from driver; no action taken", obs.State)}
			return errSkipWrite
		}
		return nil // write the mutated record
	})

	if updateErr != nil && !errors.Is(updateErr, errSkipWrite) {
		if errors.Is(updateErr, store.ErrNotFound) {
			return SandboxOutcome{ID: id, Kind: OutcomeUnchanged, Reason: "sandbox not found; may have been deleted concurrently"}
		}
		return SandboxOutcome{ID: id, Kind: OutcomeIndeterminate, Reason: fmt.Sprintf("lock or write error: %v", updateErr)}
	}

	// ── Spawn the replacement supervisor for an adoptable sandbox ─────────
	//
	// IMPORTANT: this is OUTSIDE the store.Update callback, and must stay
	// there. RunReacquire resolves the sandbox and writes its own supervisor
	// identity via store.Update; the per-sandbox flock is non-recursive, so
	// spawning from inside the callback would deadlock against the lock this
	// very function holds — and because the spawner waits for the replacement
	// to report ready, it would deadlock until timeout rather than fail fast.
	if haveAdoptable {
		r.spawnReplacement(adoptable, &outcome)
	}

	// Stop and Delete are called outside the lock because they also acquire the
	// per-sandbox flock (which is non-recursive). The removal marker written
	// inside the callback (under the lock) acts as a WAL entry: if this
	// process dies between Update and Delete, the next recovery sees the
	// marker and returns OutcomeTerminal — the same semantics as today.
	if needDelete {
		// Stop is called before Delete to clean up any orphaned VMM process.
		// Scenario: a crash between vm.delete and vmm.shutdown in the driver's
		// Stop sequence leaves an empty VMM process alive. Observe correctly
		// reports Absent for such a VMM (a VMM with no VM has no VM), so the
		// process is indistinguishable from a plain absent VM at this layer.
		// Without this Stop call the empty process would be orphaned permanently.
		//
		// Stop is idempotent by contract: calling it when nothing is running
		// is safe. Treat a Stop error as non-fatal: record it in the outcome
		// reason and still proceed to Delete. Failing to delete because cleanup
		// failed would resurrect the wedge class this project has fixed twice.
		//
		// WAL ordering: marker (under Update lock) → Stop → Delete. This is
		// consistent with Ruling 5 (SetRemovalMarker before destructive work)
		// and with service.Remove's own ordering.
		//
		// IMPORTANT: this call is OUTSIDE the store.Update callback (and thus
		// outside the exclusive per-sandbox flock). Do NOT move it inside —
		// the flock is non-recursive and a re-entrant substrate call inside the
		// callback would deadlock.
		if stopErr := r.drv.Stop(ctx, id); stopErr != nil {
			outcome.Reason += fmt.Sprintf(" (note: Stop before delete failed: %v; proceeding with delete)", stopErr)
		}
		if err := r.st.Delete(ctx, id); err != nil {
			return SandboxOutcome{
				ID:     id,
				Kind:   OutcomeIndeterminate,
				Reason: fmt.Sprintf("--rm: failed to delete sandbox: %v", err),
			}
		}
		// Reap the per-sandbox ext4 disk copy (S-COW). Non-fatal: if the reap
		// fails the record is already deleted and the caller sees a successful
		// remove. Idempotent — a missing file is not an error.
		_ = service.ReapDiskCopy(r.diskDir, id)
	}
	return outcome
}

// applyAdopt corrects *rec to match the observed live state. The VM is
// healthy; no destructive action is ever taken. All mutations are applied
// directly to *rec; the caller (inside store.Update) writes the result.
//
// Returns true when *rec was mutated and must be written. Returns false when
// no changes are needed and the caller should return errSkipWrite.
func (r *Recoverer) applyAdopt(rec *domain.Sandbox, observed domain.State, instanceID string, out *SandboxOutcome) (wrote bool) {
	stateCorrect := rec.State == observed
	idCorrect := rec.InstanceID == instanceID
	markerSet := rec.RemovalMarker

	// Fast path: nothing has changed.
	if stateCorrect && idCorrect && !markerSet {
		*out = SandboxOutcome{
			ID:     rec.ID,
			Kind:   OutcomeAdopted,
			Reason: fmt.Sprintf("VM %s; record matches observed state", observed),
		}
		return false
	}

	var reason string
	if stateCorrect && idCorrect {
		reason = fmt.Sprintf("VM %s; record matches observed state", observed)
	} else {
		reason = fmt.Sprintf("VM %s; record corrected from %s", observed, rec.State)
	}

	// Note an interrupted removal in the reason. The live VM proves removal
	// did not complete, so the marker is stale. Clearing it directly here
	// (no nested ClearRemovalMarker call — that would deadlock).
	if markerSet {
		reason += " (note: removal marker was set; removal is abandoned because VM is alive)"
	}

	if !stateCorrect {
		// State transition through the machine: the VM is authoritative, but
		// we still validate that the edge exists to catch impossible states.
		trigger, trigErr := adoptionTrigger(rec.State, observed)
		if trigErr != nil {
			*out = SandboxOutcome{
				ID:   rec.ID,
				Kind: OutcomeIndeterminate,
				Reason: fmt.Sprintf(
					"VM observed %s but no lifecycle edge exists from stored state %s: %v; VM left alone",
					observed, rec.State, trigErr,
				),
			}
			return false
		}
		tr, err := r.mach.Next(rec.State, trigger)
		if err != nil {
			*out = SandboxOutcome{
				ID:     rec.ID,
				Kind:   OutcomeIndeterminate,
				Reason: fmt.Sprintf("machine transition error: %v; VM left alone", err),
			}
			return false
		}
		rec.State = tr.NextState
	}
	rec.InstanceID = instanceID
	rec.RemovalMarker = false
	rec.StopReason = "" // cleared: VM is alive; StopReason only qualifies stopped

	*out = SandboxOutcome{ID: rec.ID, Kind: OutcomeAdopted, Reason: reason}
	return true
}

// applySupervisorLiveness cross-checks the sandbox's persisted supervisor
// identity against the live substrate and reclassifies a healthy-VM outcome
// as [OutcomeAdoptable] when the VM is alive but its supervisor is not (AC-8:
// the live-VM/dead-supervisor class).
//
// Called after [Recoverer.applyAdopt], and only takes effect when applyAdopt
// classified the sandbox as [OutcomeAdopted] — a supervisor cross-check on
// top of an indeterminate or unresolved state correction would either mask
// that problem or misreport a sandbox recovery already declined to touch.
//
// # Never a false positive (the dangerous direction)
//
// A slow-to-answer supervisor is not a dead one — spawning a second
// supervisor over a live one creates two owners for the same VM, which is
// worse than the bug this ticket exists to fix. This delegates the liveness
// verdict entirely to r.checkSupervisor (production:
// [supervisor.CheckAndReconcile], the same signal-0-plus-500ms-socket-dial
// primitive the orphan sweep uses, which already treats "PID alive but
// socket not connectable" as stale rather than live and "PID alive with no
// recorded socket" as live) rather than re-deriving liveness with a looser
// check here.
//
// Returns true when *rec was mutated (SupervisorPID/SupervisorSock cleared)
// and must be written.
func (r *Recoverer) applySupervisorLiveness(rec *domain.Sandbox, out *SandboxOutcome) (wrote bool) {
	if out.Kind != OutcomeAdopted {
		return false
	}
	if r.checkSupervisor == nil {
		// Not wired (see the checkSupervisor field doc for why New cannot
		// default it): the cross-check is a no-op, not a false positive.
		return false
	}
	if rec.SupervisorPID <= 0 {
		// No supervisor was ever recorded for this sandbox (record predates the
		// SupervisorPID field). Nothing to cross-check; leave the Adopted outcome.
		return false
	}
	alive, err := r.checkSupervisor(rec.SupervisorPID, rec.SupervisorSock)
	if err != nil {
		// Auxiliary check only: a failure here must never downgrade or
		// mutate an already-correct record-level adoption.
		out.Reason += fmt.Sprintf(" (supervisor liveness check error: %v; supervisor status not determined)", err)
		return false
	}
	if alive {
		return false
	}

	// VM alive (we are inside the OutcomeAdopted branch, so the substrate
	// reported Running or Paused), supervisor dead: adoptable rather than
	// plainly running. Clear the stale supervisor identity per
	// CheckAndReconcile's documented caller contract, so a future Start does
	// not attempt to reuse a dead pid/socket. The VM itself is left
	// completely alone (D-HSH-04: recovery may adopt, never stop).
	prevPID := rec.SupervisorPID
	rec.SupervisorPID = 0
	rec.SupervisorSock = ""
	out.Kind = OutcomeAdoptable
	out.Reason = fmt.Sprintf("VM %s but supervisor pid %d is dead; sandbox needs a replacement supervisor", rec.State, prevPID)
	return true
}

// adoptionTrigger returns the lifecycle trigger that moves from the stored
// state to the observed state. Returns an error when no direct edge exists.
func adoptionTrigger(from domain.State, to domain.State) (lifecycle.Trigger, error) {
	switch to {
	case domain.Running:
		switch from {
		case domain.Created, domain.Stopped:
			return lifecycle.TriggerStart, nil
		case domain.Paused:
			return lifecycle.TriggerResume, nil
		}
	case domain.Paused:
		switch from {
		case domain.Running:
			return lifecycle.TriggerPause, nil
		}
	}
	return "", fmt.Errorf("no adoption edge from %s to %s", from, to)
}

// applyAbsent handles the case where the substrate reports no running VM.
// Only here — after confirming the VM is absent — does the stored record drive
// the decision. All mutations are applied directly to *rec; the caller writes
// the result via store.Update.
//
// Returns (wrote, needDelete):
//   - wrote=true means *rec was mutated and must be written.
//   - needDelete=true means the sandbox must be deleted after the Update returns
//     (Delete also acquires the flock and cannot be called from inside the callback).
func (r *Recoverer) applyAbsent(rec *domain.Sandbox, out *SandboxOutcome) (wrote bool, needDelete bool) {
	// Removal marker check takes absolute precedence. If the marker is set,
	// removal was in progress when the crash hit. The sandbox state is
	// unknown; retrying removal risks double-destruction. Terminal.
	if rec.RemovalMarker {
		*out = SandboxOutcome{
			ID:   rec.ID,
			Kind: OutcomeTerminal,
			Reason: "removal marker was set when the process crashed; " +
				"removal is terminal and must not be retried; manual cleanup required",
		}
		return false, false
	}

	// --rm rule: marker absent + VM absent + RemoveOnExit → honour the removal
	// request, but ONLY if the lifecycle machine says removal is valid from
	// the sandbox's stored state. The only removal edge is Running →
	// TriggerPrimaryCommandExit (row 13). For Created, Stopped, and Error the
	// machine returns *IllegalTransitionError — those sandboxes never reached
	// the running state so there is nothing to remove; leave them unchanged.
	if rec.RemoveOnExit {
		tr, machErr := r.mach.Next(rec.State, lifecycle.TriggerPrimaryCommandExit)
		if machErr != nil {
			// No removal edge from this state: the sandbox never ran and the
			// --rm intent cannot be honoured through a state that precedes
			// (or follows) execution. Leave the record untouched.
			*out = SandboxOutcome{
				ID:   rec.ID,
				Kind: OutcomeUnchanged,
				Reason: fmt.Sprintf(
					"--rm flag set but sandbox was never running (stored state %s); "+
						"removal only applies after a running VM exits; nothing to remove",
					rec.State,
				),
			}
			return false, false
		}
		if !tr.Remove {
			// Machine returned a non-removal transition for this trigger.
			// This is unreachable today (the only primary_command_exit edge
			// bears Removal: true), but guard explicitly to avoid silent
			// fallthrough if the table ever grows a new edge for this trigger.
			*out = SandboxOutcome{
				ID:   rec.ID,
				Kind: OutcomeIndeterminate,
				Reason: fmt.Sprintf(
					"--rm: machine returned a non-removal transition from state %s; no action taken",
					rec.State,
				),
			}
			return false, false
		}
		// tr.Remove == true: set the WAL marker on *rec. The Update callback
		// writes this marker to disk; then Delete is called after the lock is
		// released. A crash between the two leaves the marker set, which the
		// next recovery surfaces as OutcomeTerminal — identical crash semantics
		// to the prior SetRemovalMarker-then-Delete sequence.
		rec.RemovalMarker = true
		*out = SandboxOutcome{
			ID:     rec.ID,
			Kind:   OutcomeRemoved,
			Reason: "removed per --rm flag; removal was requested and had not started",
		}
		return true, true
	}

	// The stored state drives the remaining logic — but only because we have
	// already confirmed that the VM is absent and no removal is pending.
	switch rec.State {
	case domain.Paused:
		// A paused sandbox's memory lives in host RAM. Host reboot, VMM kill,
		// or power loss destroys that memory. Surface the loss explicitly.
		// Sets StopReason=memory_lost on the record (ruling 12 qualifier).
		tr, err := r.mach.Next(domain.Paused, lifecycle.TriggerSubstrateLost)
		if err != nil {
			*out = SandboxOutcome{
				ID:     rec.ID,
				Kind:   OutcomeIndeterminate,
				Reason: fmt.Sprintf("substrate_lost transition unavailable: %v", err),
			}
			return false, false
		}
		rec.State = tr.NextState
		rec.StopReason = domain.StopReasonMemoryLost
		// Clear netns identity fields: the VM is dead; a stale pid must not
		// reach AdoptNetnsRuntime on a future Start.
		rec.NetnsChildPID = 0
		rec.NetnsChildPGID = 0
		rec.NetnsChildStartTime = 0
		rec.GuestTapName = ""
		rec.CHAPISocket = ""
		*out = SandboxOutcome{
			ID:   rec.ID,
			Kind: OutcomeResolvedStopped,
			Reason: "paused sandbox lost its substrate; memory was destroyed " +
				"(host reboot, VMM kill, or power loss); resolved to stopped",
		}
		return true, false

	case domain.Running:
		// Edge 10 (ruling 12): a running VM that is observed Absent with no
		// removal marker and no --rm flag is resolved to stopped with
		// StopReason=memory_lost. This covers host reboot, VMM kill, and power
		// loss for running sandboxes, completing ruling 9's "surface the loss"
		// requirement for the running case.
		//
		// TriggerSubstrateLost (row 6b) is used rather than TriggerStop
		// (row 4) because this is system-initiated reconciliation, not a
		// user-requested shutdown. It must NOT go to error — the user declined
		// that route (gamma); recovery must always land at stopped.
		tr, err := r.mach.Next(domain.Running, lifecycle.TriggerSubstrateLost)
		if err != nil {
			*out = SandboxOutcome{
				ID:     rec.ID,
				Kind:   OutcomeIndeterminate,
				Reason: fmt.Sprintf("running substrate_lost transition unavailable: %v", err),
			}
			return false, false
		}
		rec.State = tr.NextState
		rec.StopReason = domain.StopReasonMemoryLost
		// Clear netns identity fields: the VM is dead; a stale pid must not
		// reach AdoptNetnsRuntime on a future Start.
		rec.NetnsChildPID = 0
		rec.NetnsChildPGID = 0
		rec.NetnsChildStartTime = 0
		rec.GuestTapName = ""
		rec.CHAPISocket = ""
		*out = SandboxOutcome{
			ID:   rec.ID,
			Kind: OutcomeResolvedStopped,
			Reason: "running VM was absent; substrate lost (host reboot, VMM kill, or power loss); " +
				"resolved to stopped; memory lost",
		}
		return true, false

	case domain.Created, domain.Stopped, domain.Error:
		*out = SandboxOutcome{
			ID:     rec.ID,
			Kind:   OutcomeUnchanged,
			Reason: fmt.Sprintf("stored state %s; VM absent; no action needed", rec.State),
		}
		return false, false

	default:
		*out = SandboxOutcome{
			ID:     rec.ID,
			Kind:   OutcomeUnchanged,
			Reason: fmt.Sprintf("stored state %s; VM absent; no action needed", rec.State),
		}
		return false, false
	}
}

// spawnReplacement starts a long-lived replacement supervisor for a sandbox
// classified [OutcomeAdoptable], and records what happened in out.Reason.
//
// This is the ACT half of D-HSH-15 (operator-ratified TBR-4: recover adopts
// automatically). Before it existed, recovery detected the
// live-VM/dead-supervisor class and stopped, leaving the re-acquisition
// mechanism with no caller and AC-1b unreachable by any operator.
//
// # What it never does
//
// It never touches the VM. D-HSH-04 is intact: recovery may adopt a live VM,
// never stop one. Every branch here either spawns a replacement or appends an
// explanation to the outcome — none of them signals, kills, or reconfigures
// the running VM, and the outcome stays [OutcomeAdoptable] throughout so the
// sandbox is REPORTED whether or not the spawn was possible.
//
// # Fail-closed, non-retroactive
//
// A sandbox whose record carries no NetnsControlSocket was booted before the
// control-socket mechanism existed (D-HSH-17). Its netns child has no control
// socket to answer on, so no replacement can rebuild its perimeter. That is
// refused here rather than attempted — and still reported, because an
// operator needs to know the sandbox needs a manual restart. This
// non-retroactivity is correct, not a gap: the alternative is a spawn that
// fails partway and leaves a partial perimeter, which reads as working while
// bypassing egress policy.
func (r *Recoverer) spawnReplacement(sb domain.Sandbox, out *SandboxOutcome) {
	if r.spawnAdopt == nil {
		// Spawner not wired (report-only mode, the AC-8 behaviour).
		out.Reason += " (no adopt spawner wired; sandbox reported but not adopted)"
		return
	}
	if sb.NetnsControlSocket == "" {
		out.Reason += " (sandbox predates the netns control socket, so its perimeter cannot be rebuilt; " +
			"the VM is left running and untouched — restart it manually to restore guest networking)"
		return
	}

	ca, err := r.spawnAdopt(sb)
	if err != nil {
		// The spawn refused or failed. The VM is untouched by contract (the
		// re-acquisition path never calls rt.Stop() on a refusal), so the
		// sandbox stays adoptable and the operator is told why.
		out.Reason += fmt.Sprintf(" (adopt spawn failed: %v; VM left running and untouched)", err)
		return
	}

	out.Kind = OutcomeAdopted
	out.Reason += " — a replacement supervisor was started and has rebuilt the perimeter"

	// The CA outcome comes FROM the replacement supervisor, which is the only
	// process that made the decision. All three states are reported distinctly:
	// saying "lost" when the CA was recovered is the defect this reporting
	// replaced, and saying "recovered" when it was actually lost would be worse
	// — an operator told TLS survived diagnoses the resulting failures as a
	// network fault instead of as a CA they need to re-import.
	switch ca {
	case CARecovered:
		out.Reason += "; the MITM CA was re-seeded from its persisted copy, so in-guest TLS sessions continue uninterrupted"
	case CALost:
		out.Reason += "; NOTE: the MITM CA could not be recovered from the crashed supervisor, " +
			"so in-guest TLS sessions will FAIL until the guest re-imports the new CA (plain networking is restored)"
	default:
		// Wording note: each of the three messages must be identifiable by a
		// substring that appears in NO other one, or a test asserting "the
		// recovered wording is absent" passes on a message that merely
		// mentions recovery. Here that distinct substring is "could not
		// determine"; the others are "re-seeded" and "could not be recovered".
		out.Reason += "; NOTE: could not determine whether the MITM CA survived this recovery — " +
			"check the replacement supervisor's log for supervisor.reacquire.ca_recovered or .ca_lost " +
			"before assuming in-guest TLS still works"
	}
}
