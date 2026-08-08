package lifecycle

import (
	"fmt"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// Intent carries the options set when a sandbox was created that govern how
// lifecycle events are interpreted. It is a read-only snapshot of creation-time
// flags; it never changes after the sandbox exists.
type Intent struct {
	// RemoveOnExit, when true, instructs the machine to remove the sandbox when
	// its primary command exits, regardless of exit status. This is the --rm
	// flag, modelled exactly like docker run --rm: the output of a --rm run is
	// the exit code and whatever was streamed, nothing else.
	RemoveOnExit bool
}

// ExitOutcome describes what should happen after the primary command exits.
//
// Exactly one of Remove and NextState is meaningful:
//   - Remove==true  → the sandbox must be deleted; NextState is the zero value.
//   - Remove==false → the sandbox transitions to NextState (or remains in the
//     current state if NextState equals From).
//
// ExitOutcome is the only type that can express a removal signal. Removal does
// not go through domain.State so that a caller cannot accidentally write a
// removal outcome as a durable state to the store.
type ExitOutcome struct {
	// Remove signals that the sandbox must be deleted, not transitioned.
	// Removal is unconditional: the primary command's exit code affects what
	// is surfaced to the caller, not whether removal happens.
	Remove bool

	// NextState is the state to write when Remove is false. Zero when Remove
	// is true.
	NextState domain.State
}

// OnPrimaryCommandExit decides what happens when the primary command inside a
// sandbox exits.
//
//   - If intent.RemoveOnExit is true, the outcome is always Remove=true,
//     regardless of exitCode. A non-zero exit still triggers removal.
//   - If intent.RemoveOnExit is false, the sandbox stays in its current state;
//     the primary command exiting does not cause a lifecycle transition. The
//     returned outcome has Remove=false and NextState==from.
//
// The machine parameter is required to validate that from is a state from which
// TriggerPrimaryCommandExit is legal (currently only Running). Callers that
// hold a substrate lease guaranteeing the sandbox is running may safely
// treat the error as impossible, but they must still handle it.
func OnPrimaryCommandExit(m Machine, intent Intent, from domain.State, exitCode int) (ExitOutcome, error) {
	if !intent.RemoveOnExit {
		// Without --rm, primary command exit is not a lifecycle event.
		// The sandbox continues running; no state transition occurs.
		return ExitOutcome{Remove: false, NextState: from}, nil
	}

	// --rm path: use the machine to validate the edge before committing.
	tr, err := m.Next(from, TriggerPrimaryCommandExit)
	if err != nil {
		return ExitOutcome{}, fmt.Errorf("OnPrimaryCommandExit: %w", err)
	}
	// Defensive: the table says Running->Removal for this trigger; anything
	// else is a bug in the table, not in the caller.
	if !tr.Remove {
		return ExitOutcome{}, fmt.Errorf(
			"lifecycle: primary_command_exit produced a non-removal transition "+
				"(NextState=%q); expected Removal==true — this is a bug in the transition table",
			tr.NextState,
		)
	}

	// exitCode is intentionally unused: removal is unconditional.
	_ = exitCode
	return ExitOutcome{Remove: true}, nil
}
