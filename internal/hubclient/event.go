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

	TypeMessage            = "message"
	TypeSeatTaken          = "seat.taken"
	TypeDelegateDone       = "delegate.done"
	TypeDelegatePermission = "delegate.permission"
	TypeDelegateFriction   = "delegate.friction"
	TypeBudgetRefused      = "budget.refused"
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

// Event is the row shape. ID is the CloudEvents id and Cursor the journal
// cursor of the read entry.
type Event struct {
	ID      string          `json:"id,omitempty"`
	Cursor  string          `json:"cursor,omitempty"`
	TS      int64           `json:"ts"`
	Topic   string          `json:"topic"`
	Type    string          `json:"type"`
	Actor   string          `json:"actor"`
	Subject string          `json:"subject"`
	Payload json.RawMessage `json:"payload"`
	// DataContentType is the CloudEvents datacontenttype; empty when unset.
	DataContentType string `json:"datacontenttype,omitempty"`
}

// WatchLine is one JSONL line from `watch`. For kind "event" the embedded row
// is set; for kind "gap" Count is the number of events dropped or coalesced.
type WatchLine struct {
	Kind string `json:"kind"`
	Event
	Count int64 `json:"count,omitempty"`
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
	// OwnerSeat is the seat that owns the sandbox; IsUrgent matches it.
	OwnerSeat string `json:"owner_seat,omitempty"`
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

// MessagePayload is the payload of message.
type MessagePayload struct {
	To   string `json:"to"`
	From string `json:"from"`
	Text string `json:"text"`
}

// SeatTakenPayload is the payload of seat.taken.
type SeatTakenPayload struct {
	Seat string `json:"seat"`
	From string `json:"from"`
	To   string `json:"to"`
}

// DelegatePayload is the payload of delegate.{done,permission,friction}.
type DelegatePayload struct {
	Sandbox string `json:"sandbox"`
	Blocked bool   `json:"blocked"`
	Line    string `json:"line"`
}

// IsUrgent reports whether evt should interrupt the seat ownerSeat.
func IsUrgent(evt Event, ownerSeat string) bool {
	var p struct {
		OwnerSeat string `json:"owner_seat"`
		To        string `json:"to"`
		Seat      string `json:"seat"`
		Blocked   bool   `json:"blocked"`
	}
	_ = json.Unmarshal(evt.Payload, &p)
	switch evt.Type {
	case TypeSandboxDied, TypeSandboxStopped:
		return ownerSeat != "" && p.OwnerSeat == ownerSeat
	case TypeDelegateDone:
		return true
	case TypeDelegateFriction:
		return p.Blocked
	case TypeMessage:
		return ownerSeat != "" && p.To == ownerSeat
	case TypeBudgetRefused:
		return ownerSeat != "" && p.Seat == ownerSeat
	}
	return false
}
