// Package oomattr snapshots kernel OOM counters and classifies sandbox deaths.
// No sqlite.
package oomattr

import (
	"encoding/json"

	"github.com/IniZio/nexus/internal/hubclient"
)

// Snapshot is a point-in-time reading of OOM-relevant counters.
type Snapshot struct {
	BootID          string `json:"boot_id"`
	VmstatOOMKill   int64  `json:"vmstat_oom_kill"`
	ScopeOOMKill    int64  `json:"scope_oom_kill"`
	AncestorOOMKill int64  `json:"ancestor_oom_kill"`
	// ScopeOOM and AncestorOOM count cgroup limit hits (memory.events "oom");
	// AncestorOOM sums every ancestor slice.
	ScopeOOM    int64 `json:"scope_oom"`
	AncestorOOM int64 `json:"ancestor_oom"`
}

// SignalUnknown marks an exit whose signal could not be observed. Only
// adopted runtimes (not the parent of the VM process) use it.
const SignalUnknown = -1

// Signal describes how the runtime exited. Signal 0 is a normal exit with
// Code; SignalUnknown lets counter evidence classify the death.
type Signal struct {
	Signal int
	Code   int
}

// UnknownExit is the Signal for adopted runtimes.
var UnknownExit = Signal{Signal: SignalUnknown}

// Take reads current counters.
func Take() Snapshot { return takeFrom("") }

// Classify attributes a death from counter deltas and the exit signal.
// Only SIGKILL or SignalUnknown can be an OOM death; any normal exit is unknown.
func Classify(before, after Snapshot, sig Signal) hubclient.Cause {
	return classify(before, after, sig)
}

// BackfillCause infers a cause from a sandbox.started payload and a current
// snapshot when the supervisor died with the sandbox.
func BackfillCause(startedPayload json.RawMessage, now Snapshot) hubclient.Cause {
	return backfill(startedPayload, now)
}
