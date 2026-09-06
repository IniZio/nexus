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

	// NetnsChildPID is the OS PID of the netns-runtime child process (the
	// re-exec'd binary running inside the per-sandbox user+network namespace
	// that hosts the CH VMM and frame pump — see
	// cloudhypervisor.NetnsRuntime). Zero means no netns child is running
	// (in-process perimeter, or the sandbox predates this field).
	//
	// A supervisor that did not fork this child cannot signal it by PID
	// alone: killing a single process in a process group started with
	// Setpgid:true does not reach the rest of the group, and the child's own
	// children (CH) are only reachable via the group. See NetnsChildPGID.
	NetnsChildPID int `json:"netns_child_pid,omitempty"`

	// NetnsChildPGID is the process group ID of the netns child. Because the
	// netns child is spawned with Setpgid:true and CH inherits that pgid
	// (Setpgid:false), a non-parent process that wants to cleanly stop the
	// VM must target the group (kill(-PGID, ...)), not just NetnsChildPID —
	// signalling the PID alone leaves CH (and any grandchildren) running.
	// Zero when NetnsChildPID is zero.
	NetnsChildPGID int `json:"netns_child_pgid,omitempty"`

	// GuestTapName is the guest-facing TAP interface name passed to CH's
	// vm.create for this sandbox's network device. A non-parent adopter
	// needs this to re-derive the network device configuration without
	// re-deriving it from scratch or re-reading netns-internal state it did
	// not create. Empty when no netns child is running.
	GuestTapName string `json:"guest_tap_name,omitempty"`

	// CHAPISocket is the absolute path of the cloud-hypervisor REST API
	// Unix socket for this sandbox's VM (NetnsRuntime.APISocket). A
	// non-parent adopter dials this socket directly; it does not need to be
	// the process that started CH to control it. Empty when no VM is
	// running under a netns child.
	CHAPISocket string `json:"ch_api_socket,omitempty"`

	// NetnsChildStartTime is the kernel starttime of the netns child process
	// (field 22 of /proc/<NetnsChildPID>/stat, in clock ticks since boot),
	// persisted by the supervisor immediately after StartNetnsRuntime returns.
	// A replacement supervisor passes this value to AdoptNetnsRuntime, which
	// reads the live starttime from /proc and refuses adoption if the two
	// values differ — guarding against pid recycling. Zero means no netns
	// child is running; AdoptNetnsRuntime refuses adoption when this field is
	// zero rather than proceeding unguarded.
	NetnsChildStartTime uint64 `json:"netns_child_start_time,omitempty"`

	// NetnsControlSocket and NetnsControlToken are the paths of the netns
	// child's control socket and its shared-secret token file. They are what
	// makes CRASH recovery possible at the network level: after a supervisor
	// is SIGKILLed there is no live sender to pass the perimeter fd over
	// SCM_RIGHTS, but the netns child survives, and a replacement supervisor
	// uses these two paths to ask that child for a fresh perimeter end
	// instead (cloudhypervisor.ReacquirePerimeter).
	//
	// Both empty when the netns child was started without a control socket,
	// in which case the VM is recoverable at the record level but NOT at the
	// network level — the distinction the charter originally conflated.
	NetnsControlSocket string `json:"netns_control_socket,omitempty"`
	NetnsControlToken  string `json:"netns_control_token,omitempty"`

	// CacheDiskSlot is the ImagePath of the leased builder cache-disk slot
	// backing this sandbox's VM, if any (builder.CacheDiskSpec.ImagePath;
	// see internal/core/builder/cachedisk.go). A non-parent adopter must
	// know which slot it now owns so it can release the lease on stop
	// instead of leaking it, and so two adopters can never believe they
	// hold the same slot. Empty means no cache disk is leased (e.g. a
	// non-builder sandbox).
	//
	// It is written by the supervisor that owns the VM (D-HSH-07) and read
	// back by an adopting or re-acquiring supervisor, which takes the SAME
	// slot by path (builder.AcquireCacheDiskSlot) rather than selecting a
	// new one. When a VM leases more than one slot the image paths are
	// comma-separated, in ExtraDisks order; decode with
	// builder.DecodeCacheDiskSlots.
	CacheDiskSlot string `json:"cache_disk_slot,omitempty"`

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

	// AgentName is the registered agent profile this sandbox was created for
	// (cred.AgentProfile.Name, e.g. "claude-code"). It is frozen at creation:
	// the agent a sandbox runs determines its egress allowlist and the shape of
	// the credential seed, both of which are baked into the Envelope and into
	// the guest at boot. Changing it in place would leave a sandbox whose
	// running perimeter disagrees with its record.
	//
	// Empty means the sandbox was created without an agent (a plain sandbox,
	// or a record written before this field existed). Callers must not read an
	// empty value as "the default agent" — a sandbox with no agent gets no
	// credential seed at all.
	//
	// The name is stored rather than the resolved profile so that fixing a
	// profile's egress list or capabilities takes effect on the next boot of an
	// existing sandbox, instead of freezing a stale copy into the store.
	AgentName string `json:"agent_name,omitempty"`
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

	// SecretHostSuffixes are dot-anchored DNS suffixes (e.g. ".cursor.sh")
	// that extend SecretHosts by suffix match. Any host ending with one of
	// these suffixes is MITM'd and credential-swapped by the proxy. Each
	// suffix MUST begin with "." (dot-boundary safety — see mitm.Config).
	SecretHostSuffixes []string `json:"secret_host_suffixes,omitempty"`

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

	// AllowedBranches is the list of git ref patterns the sandbox may push to
	// through the host-side git MITM. Patterns support a trailing "/**" for
	// namespace-prefix matching at any depth (e.g. "refs/heads/nexus3/**"),
	// or standard path.Match single-segment "*" for explicit patterns
	// (e.g. "refs/heads/nexus3/e2e/*"). When empty, ResolvedAllowedBranches
	// returns the hardcoded default ["refs/heads/nexus3/**"].
	AllowedBranches []string `json:"allowed_branches,omitempty"`

	// PathPolicies carries per-(placeholder, host) path restrictions frozen at
	// create time. The "" placeholder key acts as a wildcard covering any
	// authenticated request to the keyed host. Converted to mitm.PathPolicies at
	// sandbox start by the service layer. Empty means no per-secret path
	// restriction beyond what AllowedRepo provides via its wildcard shim.
	// Populated by the worktree-sandbox path for generic egress.secrets entries.
	PathPolicies EgressPathPolicies `json:"path_policies,omitempty"`
}

// EgressGitHubPolicy pins MITM enforcement to one GitHub repository.
// The MITM proxy applies the built-in method-aware GitHub path allowlist
// (D-PDE-16) rather than a generic glob.
type EgressGitHubPolicy struct {
	Owner string // case-sensitive GitHub owner name
	Name  string // case-sensitive GitHub repository name
}

// EgressHostPolicy is the path restriction for one (placeholder, host) pair
// frozen in the Envelope at create time. Exactly one of GitHub or Paths
// should be set. Paths holds raw glob patterns compiled by the MITM proxy at
// sandbox start time via mitm.CompileGlobPattern.
type EgressHostPolicy struct {
	GitHub *EgressGitHubPolicy
	Paths  []string // raw glob patterns; compiled by mitm at start time
}

// EgressPathPolicies is the frozen path-policy map stored in the Envelope.
// Outer key: placeholder token identifier; "" is the wildcard key, applying
// to all authenticated requests that lack a specific per-placeholder entry.
// Inner key: hostname.
// Converted to mitm.PathPolicies at sandbox start by the service layer.
type EgressPathPolicies map[string]map[string]EgressHostPolicy

// UnresolvedBranchSentinel is stored in Envelope.AllowedBranches by the
// create path when a sandbox has a workspace bound (there is a worktree to
// push from) but its branch could not be derived — detached HEAD, git
// unavailable, or an unreadable worktree. It contains a NUL byte, which git
// forbids in ref names, so it can never match a real push ref: every push is
// denied (D-PD-38) until whatever broke branch derivation is fixed. This is
// the fail-closed alternative to two unsafe options: falling back to the
// nexus3-only default (wrong repo, and would incorrectly permit a push the
// operator never scoped this sandbox for) or to an empty AllowedBranches
// slice (which ResolvedAllowedBranches would treat as "unset" and again
// apply the wrong default — see below).
const UnresolvedBranchSentinel = "refs/heads/\x00unresolved"

// ResolvedAllowedBranches returns AllowedBranches with the project default
// applied when the field is empty. The default is ["refs/heads/nexus3/**"],
// which permits any ref under the nexus3/ namespace at any depth — matching
// the D-PD-03 convention nexus3/<motive-slug>/<sandbox-short-id>. It applies
// only to sandboxes with no workspace bound (nothing to derive a branch
// from); the worktree-sandbox create path populates AllowedBranches
// explicitly from the bound worktree's own branch (see
// service.CreateAndBoot), so this default no longer governs those sandboxes.
// This is the single resolution site; callers must use this method rather
// than reading AllowedBranches directly.
func (e Envelope) ResolvedAllowedBranches() []string {
	if len(e.AllowedBranches) == 0 {
		return []string{"refs/heads/nexus3/**"}
	}
	out := make([]string, len(e.AllowedBranches))
	copy(out, e.AllowedBranches)
	return out
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
