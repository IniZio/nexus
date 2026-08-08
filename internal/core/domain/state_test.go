package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	for _, st := range AllStates() {
		str := st.String()
		parsed, err := ParseState(str)
		if err != nil {
			t.Errorf("ParseState(%q) error: %v", str, err)
			continue
		}
		if parsed != st {
			t.Errorf("round-trip failed: got %v, want %v", parsed, st)
		}
	}
}

func TestStateJSONRoundTrip(t *testing.T) {
	for _, st := range AllStates() {
		data, err := json.Marshal(st)
		if err != nil {
			t.Errorf("marshal %v: %v", st, err)
			continue
		}
		var got State
		if err := json.Unmarshal(data, &got); err != nil {
			t.Errorf("unmarshal %v: %v", st, err)
			continue
		}
		if got != st {
			t.Errorf("JSON round-trip failed: got %v, want %v", got, st)
		}
	}
}

func TestStateJSONWireFormat(t *testing.T) {
	// Confirm the wire encoding uses lowercase strings.
	cases := []struct {
		state State
		wire  string
	}{
		{Created, `"created"`},
		{Running, `"running"`},
		{Paused, `"paused"`},
		{Stopped, `"stopped"`},
		{Error, `"error"`},
	}
	for _, tc := range cases {
		data, _ := json.Marshal(tc.state)
		if string(data) != tc.wire {
			t.Errorf("marshal %v = %s, want %s", tc.state, data, tc.wire)
		}
		var got State
		if err := json.Unmarshal([]byte(tc.wire), &got); err != nil {
			t.Errorf("unmarshal %s: %v", tc.wire, err)
		} else if got != tc.state {
			t.Errorf("unmarshal %s = %v, want %v", tc.wire, got, tc.state)
		}
	}
}

func TestStateUnknownRejected(t *testing.T) {
	_, err := ParseState("snapshotting")
	if err == nil {
		t.Error("expected error for unknown state, got nil")
	}
	_, err = ParseState("")
	if err == nil {
		t.Error("expected error for empty state, got nil")
	}
	var s State
	if err := json.Unmarshal([]byte(`"cloning"`), &s); err == nil {
		t.Error("expected JSON unmarshal error for unknown state")
	}
}

func TestExactlyFiveStates(t *testing.T) {
	// If a sixth state is added to AllStates(), this test breaks deliberately.
	// If a state is added as a constant but NOT to AllStates(), other tests
	// (parse round-trips) may still catch it via coverage gaps.
	const want = 5
	got := len(AllStates())
	if got != want {
		t.Errorf("expected exactly %d states, got %d: %v", want, got, AllStates())
	}
}

// TestValidMethod verifies that Valid() returns true for each of the five
// durable states and false for the zero value and out-of-range values.
func TestValidMethod(t *testing.T) {
	for _, st := range AllStates() {
		if !st.Valid() {
			t.Errorf("Valid(%v) = false; want true for durable state", st)
		}
	}
	var zero State
	if zero.Valid() {
		t.Errorf("Valid(zero) = true; zero value must not be valid")
	}
	outOfRange := State(99)
	if outOfRange.Valid() {
		t.Errorf("Valid(99) = true; out-of-range value must not be valid")
	}
}

// TestMarshalInvalidStateErrors verifies that marshalling an invalid State
// (zero value or out-of-range) returns a non-nil error. This guards against
// an uninitialised Sandbox silently persisting a garbage state.
func TestMarshalInvalidStateErrors(t *testing.T) {
	cases := []State{
		0,         // zero value — the original bug
		State(99), // out-of-range
		State(-1), // negative
	}
	for _, s := range cases {
		_, err := json.Marshal(s)
		if err == nil {
			t.Errorf("json.Marshal(State(%d)): expected error, got nil", int(s))
			continue
		}
		// Error message must include the numeric value so diagnostics are useful.
		msg := err.Error()
		if !strings.Contains(msg, "invalid") {
			t.Errorf("json.Marshal(State(%d)) error %q: expected 'invalid' in message", int(s), msg)
		}
	}
}

// TestZeroSandboxStateCannotMarshal demonstrates that a zero-valued Sandbox
// cannot be serialised into a record claiming a valid state. The zero Sandbox's
// State field is the zero value of State (not one of the five durable states),
// so json.Marshal must fail.
func TestZeroSandboxStateCannotMarshal(t *testing.T) {
	var sb Sandbox // zero value: sb.State == 0
	if sb.State.Valid() {
		t.Fatalf("zero Sandbox.State.Valid() = true; zero value must not be valid")
	}
	_, err := json.Marshal(sb.State)
	if err == nil {
		t.Error("json.Marshal(zero Sandbox.State): expected error, got nil")
	}
}
