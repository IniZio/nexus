package supervisor

import (
	"os"
	"testing"
)

// TestReadCAOutcome_UnknownOnEveryUnreadableShape is the fail-honest guard.
// Absent, empty, garbage, and a value from some future build must all read as
// UNKNOWN. If any of them read as CAOutcomeRecovered, `nexus3 recover` would
// tell an operator that in-guest TLS survived on the strength of a file that
// says nothing of the kind.
func TestReadCAOutcome_UnknownOnEveryUnreadableShape(t *testing.T) {
	for name, write := range map[string]func(dir string){
		"absent":     func(string) {},
		"empty":      func(dir string) { mustWriteOutcome(t, dir, "") },
		"whitespace": func(dir string) { mustWriteOutcome(t, dir, "\n\n") },
		"garbage":    func(dir string) { mustWriteOutcome(t, dir, "yes") },
		"future":     func(dir string) { mustWriteOutcome(t, dir, "partially-recovered") },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(dir)
			if got := ReadCAOutcome(dir); got != CAOutcomeUnknown {
				t.Errorf("ReadCAOutcome(%s) = %q, want %q — an undetermined outcome must never "+
					"become a definite one", name, got, CAOutcomeUnknown)
			}
		})
	}
}

// TestWriteReadCAOutcome_RoundTrip covers the two definite states.
func TestWriteReadCAOutcome_RoundTrip(t *testing.T) {
	for _, want := range []CAOutcome{CAOutcomeRecovered, CAOutcomeLost} {
		dir := t.TempDir()
		if err := WriteCAOutcome(dir, want); err != nil {
			t.Fatalf("WriteCAOutcome(%q): %v", want, err)
		}
		if got := ReadCAOutcome(dir); got != want {
			t.Errorf("ReadCAOutcome = %q, want %q", got, want)
		}
	}
}

// TestClearCAOutcome_RemovesPreviousRun is the staleness guard. A sandbox
// recovered twice would otherwise have the FIRST run's answer reported as the
// second's — the same stale-assertion shape as the hardcoded caLost=true this
// mechanism replaced.
func TestClearCAOutcome_RemovesPreviousRun(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCAOutcome(dir, CAOutcomeRecovered); err != nil {
		t.Fatalf("WriteCAOutcome: %v", err)
	}
	if err := ClearCAOutcome(dir); err != nil {
		t.Fatalf("ClearCAOutcome: %v", err)
	}
	if got := ReadCAOutcome(dir); got != CAOutcomeUnknown {
		t.Errorf("outcome survived ClearCAOutcome: got %q", got)
	}
	// Idempotent: clearing an absent file is not an error, so the spawn path
	// does not refuse on the ordinary first-recovery case.
	if err := ClearCAOutcome(dir); err != nil {
		t.Errorf("ClearCAOutcome on an absent file returned %v; want nil", err)
	}
}

// TestRecordReacquireCAOutcome_MatchesTheSeedDecision drives the exact
// function RunReacquire calls with the exact value it passes
// (ReacquireResult.CALost, which RunReacquire sets from reacquireSeedInput).
//
// This is the link that carries the answer out of the detached supervisor and
// into `nexus3 recover`. Inverting the mapping here — recording Lost when the
// CA was in fact recovered — reproduces the live-proven defect, so this test
// must fail if it is.
func TestRecordReacquireCAOutcome_MatchesTheSeedDecision(t *testing.T) {
	for _, tc := range []struct {
		caLost bool
		want   CAOutcome
	}{
		{caLost: false, want: CAOutcomeRecovered},
		{caLost: true, want: CAOutcomeLost},
	} {
		dir := t.TempDir()
		if got := recordReacquireCAOutcome(dir, tc.caLost); got != tc.want {
			t.Errorf("recordReacquireCAOutcome(caLost=%v) returned %q, want %q", tc.caLost, got, tc.want)
		}
		if got := ReadCAOutcome(dir); got != tc.want {
			t.Errorf("recordReacquireCAOutcome(caLost=%v) persisted %q, want %q — the spawning CLI "+
				"reads this file and would report the wrong answer", tc.caLost, got, tc.want)
		}
	}
}

func mustWriteOutcome(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(CAOutcomePath(dir), []byte(content), 0o600); err != nil {
		t.Fatalf("write outcome: %v", err)
	}
}
