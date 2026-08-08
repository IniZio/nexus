// Package domain defines the core entities and value objects for nexus3.
//
// # State machine design rationale
//
// There are exactly five durable states. Transient operation states (cloning,
// forking, snapshotting, stopping, restoring, restored) are deliberately
// absent. An in-flight operation is represented as a lease held alongside the
// record, never as a state.
//
// A predecessor project had twelve states. A parent sandbox stuck in a
// transient "snapshotting" state with a perfectly healthy live VM matched
// neither its adoption gate (keyed on "running") nor its reaper gate (keyed
// on pid==0), so a working VM was discarded. This design exists to make that
// impossible.
//
// # Paused is a resting state
//
// "paused" is a real resting place a user can put a sandbox in via a pause
// command. It is NOT merely an internal step inside a save operation.
package domain

import (
	"encoding/json"
	"fmt"
)

// State is the durable state of a Sandbox. Exactly five values exist.
type State int

const (
	Created State = iota + 1
	Running
	Paused
	Stopped
	Error
)

// AllStates returns all valid states. Tests assert len(AllStates()) == 5 to
// ensure that adding a sixth state breaks the test suite deliberately.
func AllStates() []State {
	return []State{Created, Running, Paused, Stopped, Error}
}

// String returns the lowercase wire representation of the state.
func (s State) String() string {
	switch s {
	case Created:
		return "created"
	case Running:
		return "running"
	case Paused:
		return "paused"
	case Stopped:
		return "stopped"
	case Error:
		return "error"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// ParseState parses a state from its lowercase wire string.
func ParseState(s string) (State, error) {
	switch s {
	case "created":
		return Created, nil
	case "running":
		return Running, nil
	case "paused":
		return Paused, nil
	case "stopped":
		return Stopped, nil
	case "error":
		return Error, nil
	default:
		return 0, fmt.Errorf("unknown state %q", s)
	}
}

// Valid reports whether s is one of the five durable states. The zero value of
// State is not valid and cannot be persisted.
func (s State) Valid() bool {
	switch s {
	case Created, Running, Paused, Stopped, Error:
		return true
	}
	return false
}

// MarshalJSON encodes the state using its lowercase wire string.
// It returns an error for any State value that is not one of the five valid
// durable states (created, running, paused, stopped, error), including the zero
// value. This prevents an uninitialised Sandbox from silently persisting a
// garbage state.
func (s State) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("marshal State: invalid state value %d", int(s))
	}
	return json.Marshal(s.String())
}

// UnmarshalJSON decodes a state from its lowercase wire string.
func (s *State) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	parsed, err := ParseState(str)
	if err != nil {
		return fmt.Errorf("unmarshal State: %w", err)
	}
	*s = parsed
	return nil
}
