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
	// Labels is an arbitrary key=value map for grouping and selecting sandboxes.
	// The CLI exposes this via repeatable --label KEY=VALUE flags; multiple
	// flags are AND-matched on fleet verbs (D-PD-21).
	// Nil and empty map are equivalent: the sandbox carries no labels.
	Labels map[string]string

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

	// SupervisorPID is the PID of the detached per-sandbox supervisor process.
	// Zero means no supervisor is running (in-process perimeter). It is set by
	// the orca create path after the supervisor reports ready and IS persisted
	// to the store (D-PP-01 §S2) so destroy can find and stop the supervisor.
	SupervisorPID int `json:"supervisor_pid,omitempty"`

	// SupervisorSock is the absolute path of the supervisor's Unix-domain
	// IPC socket. Empty when SupervisorPID is zero.
	SupervisorSock string `json:"supervisor_sock,omitempty"`

	// CreatorPID is the OS PID of the process that created this sandbox record.
	// It is non-zero only for transient __builder records created by BuildInVM.
	// The service uses kill(CreatorPID, 0) to detect stale orphans: ESRCH means
	// the creator process has exited and the record is safe to reap. Records
	// written before this field existed have CreatorPID == 0 and cannot be
	// automatically reaped (they remain hidden from List by the __builder filter).
	CreatorPID int `json:"creator_pid,omitempty"`

	// BaseRef is the full 40-hex SHA of the host repository's HEAD commit at the
	// time the sandbox was created (D-PD-19). It marks the shallow-clone boundary:
	// the guest's git history begins at this commit, and git bundle operations
	// (G2) use it as the base ref anchor.
	//
	// Empty for sandboxes created without a git workspace (no WorkspaceSpec) or
	// before G1 was introduced. G2 (nexus3 bundle) fails fast when BaseRef is
	// absent on a motive sandbox — callers must check.
	BaseRef string `json:"base_ref,omitempty"`

	// MountedVolumes lists the named volumes attached to this sandbox at creation
	// time (D-PD-82). Nil and empty slice are equivalent: no volumes are attached.
	// Elements are frozen at creation and must not be mutated after the record is
	// written. The concurrency guard for named volumes is dual-source: this field
	// and the volume store's meta.json are unioned; this field wins on conflict.
	MountedVolumes []VolumeAttachment `json:"mounted_volumes,omitempty"`

	// LiveMounts lists host-directory mounts wired into this sandbox as live
	// virtiofs shares (D-PD-53). Nil and empty slice are equivalent: no live
	// mounts are present. Elements are frozen at creation and must not be mutated
	// after the record is written.
	//
	// The virtiofs tag (the kernel mount label passed to -device vhost-user-fs)
	// is NOT stored here; it is derived downstream by the hypervisor layer from
	// the sandbox ID and the element index. Storing it here would couple the
	// domain type to a transport detail and create a redundant source of truth.
	LiveMounts []LiveMount `json:"live_mounts,omitempty"`
}

// LiveMount describes a single live host-directory virtiofs share attached to
// a sandbox (D-PD-53). All fields are set at creation time and must not be
// mutated after the sandbox record is written.
type LiveMount struct {
	// HostPath is the absolute path on the host that is shared into the guest.
	HostPath string `json:"host_path"`

	// GuestPath is the absolute path inside the guest at which the share is
	// mounted (e.g. "/workspace").
	GuestPath string `json:"guest_path"`

	// ReadOnly is true when the share is exposed read-only inside the guest;
	// false means read-write.
	ReadOnly bool `json:"read_only"`
}

// VolumeAttachment describes a single named volume attached to a sandbox.
// All fields are set at attachment time and must not be mutated after the
// sandbox record is written.
type VolumeAttachment struct {
	// Name is the user-assigned name of the volume (unique within the project).
	Name string `json:"name"`

	// GuestPath is the absolute path inside the guest at which the volume is
	// mounted (e.g. "/mnt/data").
	GuestPath string `json:"guest_path"`

	// Kind is the attachment kind: "dir" for a host-directory virtiofs mount,
	// "disk" for a virtio-blk block device.
	Kind string `json:"kind"`

	// ReadOnly is true when the volume is attached read-only; false means
	// read-write.
	ReadOnly bool `json:"read_only"`
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

	// SSHPublicKey is the OpenSSH-format public key to inject into
	// /root/.ssh/authorized_keys at boot and restart. Empty means no SSH key
	// is provisioned and the guest authorized_keys file is not written.
	// Set by CreateAndBoot when CreateAndBootOptions.SSHPublicKey is non-empty.
	SSHPublicKey string

	// SecretHosts are hostnames that receive MITM credential swap without
	// entering AllowedHosts. Used by the human create path (AllowAll egress)
	// so GitHub can be swapped while every other host still tunnels with a
	// real server certificate (D-PD-25). Empty means no secret-only MITM.
	SecretHosts []string

	// SecretSpecs are the ENV@host[,host…] binds frozen at create (no tokens).
	// The detached supervisor re-resolves real tokens (builtin gh / --secret)
	// into its in-process broker on every Start.
	SecretSpecs []string

	// OpenEgress, when true, disarms the egress ACL so the sandbox has
	// unrestricted outbound connectivity (docker pulls, apt-get, pip, etc.).
	// This field MUST be set explicitly by the caller; an empty AllowedHosts
	// is NOT a sentinel for open access (D-PD-33). Only the human create path
	// (sandbox create --workspace / --file / --image) sets this to true.
	// Agent sandboxes (WireClaudeEgress / orca / herdr) must never set it.
	OpenEgress bool `json:"open_egress,omitempty"`

	// AllowedRepo, when non-empty, scopes the MITM request allowlist to a
	// single GitHub repository in "owner/repo" format. The MITM proxy enforces
	// a per-request path allowlist for github.com, api.github.com, and
	// uploads.github.com, refusing any path not explicitly needed for the PR
	// and release flow (D-PD-36). This is the ONLY control bounding the
	// operator's full-scope GitHub token for agent sandboxes; it must never
	// be left empty when GitHub hosts appear in AllowedHosts or SecretHosts.
	// An empty string disables the repo-scoped path check (human sandboxes
	// with AllowAll egress do not need path restriction).
	AllowedRepo string `json:"allowed_repo,omitempty"`
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
