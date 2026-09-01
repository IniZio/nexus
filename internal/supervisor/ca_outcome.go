package supervisor

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus3/internal/core/statedir"
)

// CAOutcome is what a crash-path re-acquisition did with the MITM CA, as
// reported by the replacement supervisor itself.
//
// It is THREE-state on purpose. The process that knows the answer is the
// detached replacement, not the CLI that spawned it, so the answer has to
// travel through the filesystem — and a file can be missing, unreadable, or
// written by a build that predates this mechanism. Collapsing that third case
// into either boolean would make the CLI state a fact it does not have:
// coercing to "lost" is the reporting bug this type exists to fix, and
// coercing to "recovered" is worse, because it tells an operator that TLS
// survived when it may not have.
type CAOutcome string

const (
	// CAOutcomeUnknown means no outcome was recorded, or what was recorded
	// could not be read or parsed. Report it as undetermined; never as either
	// definite answer.
	CAOutcomeUnknown CAOutcome = "unknown"

	// CAOutcomeRecovered means the persisted MITM CA was loaded and the
	// replacement perimeter signs with the CA the guest already trusts.
	CAOutcomeRecovered CAOutcome = "recovered"

	// CAOutcomeLost means no usable CA could be recovered and the replacement
	// perimeter minted a fresh one, so in-guest TLS breaks until the guest
	// re-imports it.
	CAOutcomeLost CAOutcome = "lost"
)

// caOutcomeFile is the filename [RunReacquire] writes its CA outcome to,
// inside the per-sandbox supervisor state dir. It follows the shape of
// supervisorErrFile: a tiny file the detached process writes and the spawning
// CLI reads back, rather than a new IPC channel.
const caOutcomeFile = "reacquire-ca.outcome"

// CAOutcomePath returns <stateDir>/reacquire-ca.outcome.
func CAOutcomePath(stateDir string) string {
	return filepath.Join(stateDir, caOutcomeFile)
}

// WriteCAOutcome records o in stateDir.
//
// Called by [RunReacquire] STRICTLY BEFORE serveAdoptedSupervisor writes the
// pidfile, which is the readiness signal [SpawnReacquireDetached] waits on.
// That ordering is what makes the file readable by the spawner the moment the
// spawn returns success: there is no window in which the CLI observes READY
// but not the outcome.
//
// A write failure is deliberately NOT fatal to the re-acquisition — refusing
// to supervise a healthy VM because a diagnostic file could not be written
// would trade a live sandbox for a report. The cost of the failure is that the
// spawner reads [CAOutcomeUnknown] and says so.
func WriteCAOutcome(stateDir string, o CAOutcome) error {
	return os.WriteFile(CAOutcomePath(stateDir), []byte(string(o)+"\n"), statedir.FileMode)
}

// ReadCAOutcome reads back what [WriteCAOutcome] recorded.
//
// Absent, unreadable, empty, or unrecognised content all return
// [CAOutcomeUnknown]. There is no error return precisely so that no caller can
// accidentally treat "I could not find out" as "the CA was fine": the only
// values this can produce are the three the reporting layer must distinguish.
func ReadCAOutcome(stateDir string) CAOutcome {
	data, err := os.ReadFile(CAOutcomePath(stateDir))
	if err != nil {
		return CAOutcomeUnknown
	}
	switch CAOutcome(strings.TrimSpace(string(data))) {
	case CAOutcomeRecovered:
		return CAOutcomeRecovered
	case CAOutcomeLost:
		return CAOutcomeLost
	default:
		return CAOutcomeUnknown
	}
}

// recordReacquireCAOutcome maps [ReacquireResult.CALost] onto a [CAOutcome],
// records it in stateDir, and returns what it recorded.
//
// It exists as a named function because [RunReacquire] calls it — the mapping
// and the write must not drift apart, and this is the one place a test can
// reach the exact code the crash path runs, without a live netns child.
//
// A write failure is logged and swallowed: see [WriteCAOutcome] for why a
// diagnostic file is not worth refusing a healthy VM over.
func recordReacquireCAOutcome(stateDir string, caLost bool) CAOutcome {
	outcome := CAOutcomeRecovered
	if caLost {
		outcome = CAOutcomeLost
	}
	if err := WriteCAOutcome(stateDir, outcome); err != nil {
		slog.Warn("supervisor.reacquire.ca_outcome_write_failed",
			"path", CAOutcomePath(stateDir), "outcome", string(outcome), "err", err)
	}
	return outcome
}

// ClearCAOutcome removes any recorded outcome from stateDir.
//
// [SpawnReacquireDetached] calls this before spawning, for the same reason it
// clears the stale pidfile and SpawnDetached clears supervisor.err: a sandbox
// that has been recovered before still carries the PREVIOUS run's outcome, and
// reading that would attribute an old answer to the new supervisor — the exact
// stale-assertion class this whole change is fixing.
func ClearCAOutcome(stateDir string) error {
	if err := os.Remove(CAOutcomePath(stateDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
