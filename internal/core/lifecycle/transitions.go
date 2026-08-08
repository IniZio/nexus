// Package lifecycle implements the state machine for a Sandbox.
//
// # Five states, zero transient states
//
// A predecessor project (nexus2) had twelve states, including transient
// operation states such as "snapshotting" and "restoring". A sandbox stuck in
// "snapshotting" with a perfectly healthy live VM matched neither the adoption
// gate (keyed on "running") nor the reaper gate (keyed on pid==0), so a working
// VM was discarded. This package exists to make that impossible.
//
// Rule: domain.State's five values (created, running, paused, stopped, error)
// are the only states a sandbox ever occupies. An operation in flight is a
// lease held alongside the record — never a transition into a transient state.
//
// # Paused is a real resting state
//
// "paused" is a stable resting place a user deliberately puts a sandbox in via
// the pause command. It is NOT an internal step inside a checkpoint or snapshot
// operation, and a user can leave a sandbox paused indefinitely. Model it as
// a first-class state, not a transient marker.
package lifecycle

import "github.com/IniZio/nexus3/internal/core/domain"

// Trigger names the event that causes a state transition.
type Trigger string

const (
	// TriggerStart moves a sandbox from created or stopped into running.
	TriggerStart Trigger = "start"

	// TriggerPause moves a running sandbox into paused.
	TriggerPause Trigger = "pause"

	// TriggerResume moves a paused sandbox back into running.
	TriggerResume Trigger = "resume"

	// TriggerStop moves a running sandbox into stopped (user-requested graceful
	// shutdown).
	TriggerStop Trigger = "stop"

	// TriggerSubstrateLost moves a running or paused sandbox into stopped when
	// the substrate is observed absent.
	//
	// A paused sandbox's memory lives in host RAM. A running sandbox's state
	// lives in the VMM process. A host reboot, VMM kill, or power loss destroys
	// both. When such an event is detected during recovery, the honest
	// resolution is to mark the sandbox stopped and surface the loss to the
	// operator via domain.StopReasonMemoryLost.
	//
	// This is system-initiated but is emphatically NOT an automatic lifecycle
	// decision: nothing decided to stop the sandbox. An external event destroyed
	// its state and the record is being reconciled with reality. It is not
	// comparable to TTL-based eviction or idle-stop, which are prohibited in v1.
	TriggerSubstrateLost Trigger = "substrate_lost"

	// TriggerFail transitions any sandbox into the error state. It is emitted by
	// the substrate watchdog, the VMM signal handler, or any component that
	// detects an unrecoverable condition.
	TriggerFail Trigger = "fail"

	// TriggerReset moves an errored sandbox into stopped. It is the explicit user
	// action to acknowledge an error and prepare the sandbox for restart or
	// deletion. Without this acknowledgement the sandbox remains in error.
	TriggerReset Trigger = "reset"

	// TriggerPrimaryCommandExit is fired when the primary command inside a --rm
	// sandbox exits. It is only meaningful when the sandbox was created with
	// RemoveOnExit=true; see intent.go and the Removal field on the --rm edge.
	//
	// For sandboxes without --rm the primary command exiting does not cause a
	// state transition; the sandbox keeps running.
	TriggerPrimaryCommandExit Trigger = "primary_command_exit"

	// TriggerSnapshot fires when a snapshot is taken of a sandbox. It is a
	// self-edge: the sandbox state is unchanged (running→running or
	// stopped→stopped). The operation is held under a lease, not a state
	// transition — see the package-level note on transient states.
	TriggerSnapshot Trigger = "snapshot"

	// TriggerFork represents the fork operation from the perspective of the
	// CHILDREN, not the parent. The parent has no transition (spec 06, edge 5:
	// ∅→running; "fork: parent unchanged; edge 5 is the child"). TriggerFork
	// has no entry in the transition table; Machine.Next will return
	// IllegalTransitionError for any (parentState, TriggerFork) pair.
	//
	// Child sandboxes are created directly in Running state by the service layer
	// (Wave-1 P2b), bypassing the table. TriggerFork exists as a constant for
	// documentation, call-log labelling, and test cross-product coverage.
	TriggerFork Trigger = "fork"
)

// Initiator identifies whether a transition was caused by a user action or an
// autonomous system event. The distinction is load-bearing: the driver uses it
// to decide whether to surface a transition as a user-visible notification or
// an audit log entry, and the CLI uses it to determine whether to prompt for
// confirmation.
type Initiator string

const (
	// InitiatorUser means the transition was explicitly requested by a user.
	InitiatorUser Initiator = "user"

	// InitiatorSystem means the transition was caused by an autonomous system
	// event: a VMM signal, a kernel event, a watchdog, a substrate loss, etc.
	// The user did not ask for this transition.
	InitiatorSystem Initiator = "system"
)

// Edge is one row in the transition table.
//
// For every edge, exactly one of the following holds:
//   - Removal==false: To is the next durable domain.State the sandbox enters.
//   - Removal==true:  the sandbox must be deleted rather than transitioned to a
//     new state. To is the zero value of domain.State and must never be written
//     to the store. This form exists only for the --rm edge (edge 13).
//
// By keeping Removal inside the table rather than returning a domain.State
// sentinel, Machine.Next can express removal without producing a domain.State
// value a caller could accidentally persist.
type Edge struct {
	From      domain.State
	To        domain.State // zero value when Removal==true; do not write to store
	Trigger   Trigger
	Initiator Initiator
	Removal   bool // true only for the --rm edge; To is invalid when true
}

// table is THE transition table. Every legal state transition is listed here in
// one place. An edge absent from this table is illegal by construction —
// Machine.Next returns *IllegalTransitionError for any (From, Trigger) pair
// not present.
//
// Design decisions (justify your reasoning, not just the outcome):
//
//   - created->stopped is absent. A sandbox that was never started can simply
//     be deleted; there is no realistic path from created to stopped without
//     passing through running first. The fail trigger covers created->error,
//     and error->stopped covers recovery. Adding created->stopped would
//     introduce a shortcut with no clear semantic and confuse the "stopped means
//     it ran and halted" invariant relied on by restart logic.
//
//   - paused->error is present (row 9) because fail covers every state. A VMM
//     crash, host OOM kill, or storage fault while a sandbox is paused is a
//     real failure mode that must be representable.
//
//   - error->error (row 11) is present so that a second fail signal while the
//     sandbox is already in error does not produce an IllegalTransitionError.
//     Systems under stress emit multiple failure signals; the machine accepts
//     them idempotently, recording only the first error cause matters.
//
//   - Rows 7–11 expand the "any state -> error (system: fail)" shorthand into
//     one concrete row per source state, keeping the table flat and enumerable.
//
//   - Row 13 has Removal==true. The --rm edge does not transition the sandbox
//     to a new durable state; instead it signals that the sandbox must be
//     deleted. Removal is expressed in the Edge rather than as a domain.State
//     sentinel (as a previous design used) because that sentinel (zero value)
//     was indistinguishable from an uninitialised struct and could be
//     accidentally marshalled and written to the store. With Removal==true and
//     domain.State.Valid()==false for zero, both the type system and the JSON
//     marshaller prevent that class of error.
var table = []Edge{
	// ── Normal operation ───────────────────────────────────────────────────────
	{From: domain.Created, To: domain.Running, Trigger: TriggerStart, Initiator: InitiatorUser}, // 1
	{From: domain.Running, To: domain.Paused, Trigger: TriggerPause, Initiator: InitiatorUser},  // 2
	{From: domain.Paused, To: domain.Running, Trigger: TriggerResume, Initiator: InitiatorUser}, // 3
	{From: domain.Running, To: domain.Stopped, Trigger: TriggerStop, Initiator: InitiatorUser},  // 4
	{From: domain.Stopped, To: domain.Running, Trigger: TriggerStart, Initiator: InitiatorUser}, // 5

	// ── Snapshot self-edges ────────────────────────────────────────────────────
	// Snapshot is a state-preserving self-edge: the sandbox remains in its
	// current state while the operation runs under a lease (no transient state).
	// Legal from running and stopped; not legal from created, paused, or error
	// (a paused VM's memory state cannot be safely snapshotted on all platforms).
	// edge 4 (restore-in-place, stopped→running) intentionally deferred pending
	// operator ratification — see spec 06 / ticket 19.
	{From: domain.Running, To: domain.Running, Trigger: TriggerSnapshot, Initiator: InitiatorUser}, // S1
	{From: domain.Stopped, To: domain.Stopped, Trigger: TriggerSnapshot, Initiator: InitiatorUser}, // S2

	// ── Substrate loss ─────────────────────────────────────────────────────────
	// Running or paused sandbox loses its substrate (host reboot / VMM kill /
	// power loss). System-initiated reconciliation, not an automatic lifecycle
	// decision. See domain.StopReasonMemoryLost for the qualifier written to
	// the durable record; the lifecycle machine records only the state change.
	{From: domain.Paused, To: domain.Stopped, Trigger: TriggerSubstrateLost, Initiator: InitiatorSystem},  // 6
	{From: domain.Running, To: domain.Stopped, Trigger: TriggerSubstrateLost, Initiator: InitiatorSystem}, // 6b (edge 10)

	// ── Failure: any state -> error (one row per source state) ─────────────────
	{From: domain.Created, To: domain.Error, Trigger: TriggerFail, Initiator: InitiatorSystem}, // 7
	{From: domain.Running, To: domain.Error, Trigger: TriggerFail, Initiator: InitiatorSystem}, // 8
	{From: domain.Paused, To: domain.Error, Trigger: TriggerFail, Initiator: InitiatorSystem},  // 9
	{From: domain.Stopped, To: domain.Error, Trigger: TriggerFail, Initiator: InitiatorSystem}, // 10
	{From: domain.Error, To: domain.Error, Trigger: TriggerFail, Initiator: InitiatorSystem},   // 11 (idempotent)

	// ── Recovery ───────────────────────────────────────────────────────────────
	// User acknowledges the error; sandbox is reset to stopped and may be
	// restarted or deleted from there.
	{From: domain.Error, To: domain.Stopped, Trigger: TriggerReset, Initiator: InitiatorUser}, // 12

	// ── --rm edge ──────────────────────────────────────────────────────────────
	// Primary command exits on a --rm sandbox -> remove the sandbox.
	// Removal==true signals deletion, not a transition to a durable state.
	// To is the zero value of domain.State (not a valid storable state).
	// This edge fires regardless of exit status (zero or non-zero). See intent.go.
	{From: domain.Running, Trigger: TriggerPrimaryCommandExit, Initiator: InitiatorSystem, Removal: true}, // 13
}
