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
}

// New constructs a Recoverer backed by the given store and driver.
func New(st store.Store, drv driver.Driver) *Recoverer {
	return &Recoverer{
		st:   st,
		drv:  drv,
		mach: lifecycle.New(),
	}
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
			if !wrote {
				return errSkipWrite
			}
		case driver.Paused:
			wrote := r.applyAdopt(rec, domain.Paused, obs.InstanceID, &outcome)
			if !wrote {
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
