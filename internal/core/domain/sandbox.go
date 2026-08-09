package domain

import (
	"fmt"
	"strings"
)

// StopReason is a narrow qualifier on the stopped state. It records the
// proximate cause that moved a sandbox into stopped, distinguishing a clean
// user-requested shutdown from an involuntary substrate loss.
//
// Only two values are valid. This is intentionally NOT a general-purpose
// reason string; callers that need human-readable detail should use the
// recovery report's Reason field.
type StopReason string

const (
	// StopReasonClean indicates the sandbox was stopped by an explicit user
	// command (e.g. `nexus3 stop`). All state was flushed cleanly; the
	// sandbox is safe to restart.
	StopReasonClean StopReason = "clean"

	// StopReasonMemoryLost indicates the sandbox's in-memory state was
	// destroyed by a substrate event — host reboot, VMM kill, or power loss.
	// Any work in progress at the time of the event was lost. The operator
	// should be informed of the loss (see recovery.SandboxOutcome.Reason).
	StopReasonMemoryLost StopReason = "memory_lost"
)

// Sandbox is the ONE durable entity in nexus3. There is no separate VM or
// instance entity; a running Sandbox IS the VM.
type Sandbox struct {
	// Identity
	ID      SandboxID
	Name    string
	Project string
	// MotiveID associates this sandbox with a named external work thread
	// (motive). Empty string means the sandbox is unassociated.
	MotiveID string

	// State is a cache. The substrate (the VMM) is authoritative; where a
	// live VM disagrees with this field, the VM wins.
	State State

	// Envelope is the frozen configuration resolved at sandbox creation. It
	// must not be mutated after creation. Later slices fill in the full policy
	// and agent attachment.
	Envelope Envelope

	// InstanceID is the identifier of the current running instantiation.
	//
	// This field MUST NEVER be used to key runtime-scoped resources (e.g. as a
	// map key in a process table or a path component in a socket address).
	// Keeping it as an opaque internal field preserves the option to split
	// Sandbox and VM into separate types in a future slice; leaking it into
	// external keys would foreclose that split.
	InstanceID string

	// RemoveOnExit records the --rm flag as set at creation time. Durable.
	RemoveOnExit bool

	// RemovalMarker is a write-ahead marker. It is set BEFORE any destructive
	// removal work begins and cleared only after the removal has completed
	// successfully. If the process crashes while RemovalMarker is true, the
	// removal is terminal and must never be retried — the sandbox is gone.
	RemovalMarker bool

	// StopReason qualifies the stopped state. It is set when State transitions
	// to stopped and cleared when the sandbox transitions back to running.
	// The zero value (empty string) is valid and means no reason is recorded
	// (e.g. for sandboxes stopped before this field was introduced).
	//
	// Only meaningful when State == stopped; callers must not read this field
	// when State is any other value.
	StopReason StopReason

	// Provenance records fork lineage for sandboxes created via fork. Nil for
	// sandboxes created with Create (not fork). Frozen at creation time and
	// must not be mutated after the sandbox record is written.
	Provenance *Provenance `json:"provenance,omitempty"`
}

// Provenance records the lineage of a sandbox created as a fork child.
// Both fields are set at creation time and never modified.
type Provenance struct {
	// ParentID is the ID of the sandbox that was forked to produce this child.
	ParentID SandboxID `json:"parent_id"`

	// SourceSnapshot is the artifact snapshot ID (as a string, to avoid
	// importing the artifact package from domain) used to initialise this
	// sandbox. Corresponds to artifact.SnapshotID.
	SourceSnapshot string `json:"source_snapshot"`
}

// Envelope is the frozen configuration of a Sandbox. It is resolved at
// creation and must not change thereafter.
type Envelope struct {
	// ImageDigest is the content-addressable digest of the rootfs image.
	ImageDigest string

	// AllowedHosts is the list of hostnames the sandbox is permitted to reach
	// through the egress perimeter. At boot the host mints one placeholder
	// credential per entry and seeds it into the guest (P1-S6).
	// TODO(S6): agent attachment.
	AllowedHosts []string
}

// Handle returns the human handle for the sandbox in "<project>/<name>" form.
func (s *Sandbox) Handle() string {
	return s.Project + "/" + s.Name
}

// ParseHandle parses a human handle in "<project>/<name>" form into its
// constituent project and name components.
func ParseHandle(handle string) (project, name string, err error) {
	parts := strings.SplitN(handle, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid handle %q: expected <project>/<name>", handle)
	}
	return parts[0], parts[1], nil
}
