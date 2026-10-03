package hubclient

import "encoding/json"

// Event type names.
const (
	TypeSandboxCreated  = "sandbox.created"
	TypeSandboxStarted  = "sandbox.started"
	TypeSandboxStopped  = "sandbox.stopped"
	TypeSandboxDied     = "sandbox.died"
	TypeSandboxRemoved  = "sandbox.removed"
	TypeBinaryInstalled = "binary.installed"
)

// Cause classifies why a sandbox stopped or died.
type Cause string

const (
	CauseUser            Cause = "user"
	CauseOwner           Cause = "owner"
	CauseTeardown        Cause = "teardown"
	CauseHostOOM         Cause = "host_oom"
	CauseSliceOOM        Cause = "slice_oom"
	CauseGuestOOM        Cause = "guest_oom"
	CauseSupervisorCrash Cause = "supervisor_crash"
	CauseReap            Cause = "reap"
	CauseUnknown         Cause = "unknown"
)

// Event is the row shape: {seq, ts, topic, type, actor, subject, payload}.
// Seq and TS are assigned by the store on append (TS is unix milliseconds).
type Event struct {
	Seq     int64           `json:"seq"`
	TS      int64           `json:"ts"`
	Topic   string          `json:"topic"`
	Type    string          `json:"type"`
	Actor   string          `json:"actor"`
	Subject string          `json:"subject"`
	Payload json.RawMessage `json:"payload"`
}

// WatchLine is one JSONL line from `watch`. For kind "event" the embedded row
// is set; for kind "gap" From/To bound the missed seq range and Count is the
// number of events dropped or coalesced.
type WatchLine struct {
	Kind string `json:"kind"`
	Event
	Count int64 `json:"count,omitempty"`
	From  int64 `json:"from,omitempty"`
	To    int64 `json:"to,omitempty"`
}

// AppendResult is the stdout of `append`.
type AppendResult struct {
	Seq int64 `json:"seq"`
}

// OOMDelta records the OOM counter deltas observed around a death.
type OOMDelta struct {
	VmstatDelta       int64 `json:"vmstat_delta"`
	ScopeOOMKillDelta int64 `json:"scope_oom_kill_delta"`
	AncestorOOMDelta  int64 `json:"ancestor_oom_delta"`
}

// SandboxPayload is the payload of sandbox.{created,started,stopped,died,removed}.
type SandboxPayload struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Cause  Cause  `json:"cause"`
	By     string `json:"by"`
}

// SandboxStartedPayload is the payload of sandbox.started.
type SandboxStartedPayload struct {
	SandboxPayload
	BootID  string `json:"boot_id"`
	OOMKill int64  `json:"oom_kill"`
}

// SandboxDiedPayload is the payload of sandbox.died.
type SandboxDiedPayload struct {
	SandboxPayload
	Signal     int      `json:"signal"`
	ExitCode   int      `json:"exit_code"`
	Backfilled bool     `json:"backfilled"`
	OOM        OOMDelta `json:"oom"`
}

// BinaryInstalledPayload is the payload of binary.installed (topic "host").
type BinaryInstalledPayload struct {
	Path      string `json:"path"`
	Version   string `json:"version"`
	AgentHash string `json:"agent_hash"`
}
