package lifecycle

import (
	"fmt"
	"sort"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// Machine evaluates the sandbox lifecycle transition table. All methods are
// pure: they read the package-level table and return a result without modifying
// any state. The zero value is ready to use.
type Machine struct{}

// New returns a ready-to-use Machine. Identical to the zero value; provided
// for callers that prefer an explicit constructor.
func New() Machine { return Machine{} }

// Transition is the result of a successful Machine.Next call. Exactly one of
// Remove and NextState is meaningful:
//   - Remove==true:  the sandbox must be deleted; NextState is the zero value
//     of domain.State, which is invalid and must not be written to the store.
//   - Remove==false: NextState holds the next durable domain.State.
//
// Callers must check Remove before consuming NextState. The design ensures that
// the only path to a remove signal is through this struct, making it impossible
// to accidentally write a removal outcome into the store as if it were a state.
type Transition struct {
	// Remove signals that the sandbox must be deleted, not transitioned.
	Remove bool

	// NextState is the next durable state when Remove is false.
	// When Remove is true, NextState is the zero value of domain.State,
	// which is not a valid state (domain.State.Valid()==false) and will
	// error if marshalled.
	NextState domain.State
}

// IllegalTransitionError is returned by Next and Initiator when the (From,
// Trigger) pair names no edge in the transition table. The error message
// includes the legal triggers from the source state so that a human debugging
// at 3 am can immediately see what to try instead.
type IllegalTransitionError struct {
	// From is the state the sandbox was in when the illegal trigger arrived.
	From domain.State
	// Trigger is the trigger that was attempted.
	Trigger Trigger
	// LegalTriggers lists the triggers that ARE valid from From, sorted
	// lexicographically. Empty when no triggers are valid (unreachable in
	// practice but handled for completeness).
	LegalTriggers []Trigger
}

// Error implements the error interface. The message names the state, the
// attempted trigger, and every legal alternative.
func (e *IllegalTransitionError) Error() string {
	if len(e.LegalTriggers) == 0 {
		return fmt.Sprintf(
			"illegal transition: trigger %q is not valid from state %q "+
				"(no triggers are valid from this state)",
			e.Trigger, e.From,
		)
	}
	legal := make([]string, len(e.LegalTriggers))
	for i, t := range e.LegalTriggers {
		legal[i] = string(t)
	}
	return fmt.Sprintf(
		"illegal transition: trigger %q is not valid from state %q; "+
			"valid triggers from this state are: %v",
		e.Trigger, e.From, legal,
	)
}

// Can reports whether trigger is a legal event from state from.
func (m Machine) Can(from domain.State, trigger Trigger) bool {
	for _, e := range table {
		if e.From == from && e.Trigger == trigger {
			return true
		}
	}
	return false
}

// Next returns the Transition for the (from, trigger) pair. If the pair does
// not appear in the transition table, Next returns *IllegalTransitionError with
// the legal alternatives populated.
//
// When the returned Transition has Remove==true, the caller must delete the
// sandbox record rather than writing a new durable state. The Transition.NextState
// is the zero value of domain.State in that case, which domain.State.Valid()
// reports as false and MarshalJSON refuses to encode. This makes it impossible
// to accidentally persist a removal outcome as a durable state.
func (m Machine) Next(from domain.State, trigger Trigger) (Transition, error) {
	for _, e := range table {
		if e.From == from && e.Trigger == trigger {
			return Transition{Remove: e.Removal, NextState: e.To}, nil
		}
	}
	return Transition{}, &IllegalTransitionError{
		From:          from,
		Trigger:       trigger,
		LegalTriggers: m.LegalTriggers(from),
	}
}

// Initiator returns the initiator for the (from, trigger) pair. If the pair
// does not appear in the transition table, Initiator returns
// *IllegalTransitionError.
func (m Machine) Initiator(from domain.State, trigger Trigger) (Initiator, error) {
	for _, e := range table {
		if e.From == from && e.Trigger == trigger {
			return e.Initiator, nil
		}
	}
	return "", &IllegalTransitionError{
		From:          from,
		Trigger:       trigger,
		LegalTriggers: m.LegalTriggers(from),
	}
}

// LegalTriggers returns all triggers that are valid from state from, sorted
// lexicographically. The sorted order is stable and suitable for help text and
// error messages.
func (m Machine) LegalTriggers(from domain.State) []Trigger {
	seen := make(map[Trigger]struct{})
	for _, e := range table {
		if e.From == from {
			seen[e.Trigger] = struct{}{}
		}
	}
	triggers := make([]Trigger, 0, len(seen))
	for t := range seen {
		triggers = append(triggers, t)
	}
	sort.Slice(triggers, func(i, j int) bool {
		return triggers[i] < triggers[j]
	})
	return triggers
}

// All returns a copy of the entire transition table. Intended for tests and
// documentation generators. Callers must not modify the returned slice.
func (m Machine) All() []Edge {
	out := make([]Edge, len(table))
	copy(out, table)
	return out
}
