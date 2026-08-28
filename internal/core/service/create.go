package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/builder/builderimage"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// ErrAgentUnreachable is returned by CreateAndBoot when the VM starts but the
// guest agent does not become reachable within the configured timeout.
var ErrAgentUnreachable = errors.New("service: guest agent did not answer after VM boot")

// ErrAgentBytesRequired is returned by CreateAndBoot when an OCI pull is
// required but no agent binary was supplied in CreateAndBootOptions.
var ErrAgentBytesRequired = errors.New("no agent binary available for OCI pull (set AgentBytes in CreateAndBootOptions)")

// testHookBeforeStoreCreate is called inside CreateAndBoot immediately before
// svc.store.Create commits the sandbox record. It is nil in production and is
// set only by tests that need to observe the volume-lease state in the D2
// window (between vs.AttachLocked and store.Create). If the hook returns a
// non-nil error, CreateAndBoot propagates it and aborts without writing the
// record.
var testHookBeforeStoreCreate atomic.Pointer[func() error]

// ExtraDisk describes an additional raw ext4 disk image to attach to the
// sandbox VM at boot time. The underlying driver maps them to virtio-blk
// devices after the rootfs vda: ExtraDisks[0] → /dev/vdb, [1] → /dev/vdc, …
//
// ExtraDisk mirrors cloudhypervisor.ExtraDisk so that callers in the CLI
// layer can pass extra disks through CreateAndBootOptions without the service
// package depending on a concrete driver implementation.
type ExtraDisk struct {
	// Path is the host filesystem path to the raw ext4 disk image.
	Path string
}

// WorkspaceSpec describes a host git worktree to capture and attach to the
// sandbox VM as a read-write workspace disk. When set in CreateAndBootOptions,
// CreateAndBoot calls builder.WorktreeToDisk to snapshot the tree to a raw
// ext4 image, then appends an ExtraDisk entry for it so the DriverFactory
// receives it as a virtio-blk device alongside any caller-supplied ExtraDisks.
type WorkspaceSpec struct {
	// SourcePath is the absolute path to the host git worktree root.
	SourcePath string

	// GuestPath is the absolute path inside the VM where the in-guest agent
	// will mount the workspace disk (e.g. "/workspace/myrepo"). CreateAndBoot
	// does not perform the mount; it records GuestPath so the guest agent can
	// derive the correct agent.GuestMount mapping from the device index.
	GuestPath string

	// CaptureMaxBytes caps the workspace capture passed to builder.WorktreeToDisk.
	//   - Positive value: explicit byte cap on raw included file size.
	//   - Zero or negative: auto mode — the cap is derived from free space on the
	//     filesystem that will hold the ext4 image (80 % of available). Auto mode
	//     is the recommended default; it rejects captures whose projected image
	//     would endanger host disk space without requiring the caller to guess a
	//     threshold. The guard is surfaced as an actionable error listing the
	//     largest contributor directories.
	CaptureMaxBytes int64
}

// DriverFactory constructs a Driver pre-configured for a specific ext4 disk
// image and an optional set of additional disks. CreateAndBoot calls it with
// the resolved ext4 path and opts.ExtraDisks so that each sandbox boot uses a
// fresh driver instance with the correct DiskImagePath and extra volumes.
// The returned Driver is owned by CreateAndBoot and is not retained by svc.
type DriverFactory func(ext4Path string, extraDisks []ExtraDisk) (driver.Driver, error)

// ProbeFunc verifies that the guest agent inside the newly-booted VM is
// reachable and ready. It is called with a context that already carries the
// ReachabilityTimeout deadline. An error return causes CreateAndBoot to stop
// the VM, delete the record, and return ErrAgentUnreachable.
//
// The production implementation makes a lightweight connection attempt via
// driver.GuestDialer. Tests inject a stub (returning nil = reachable, error
// = unreachable) so no real VM or vsock is needed.
type ProbeFunc func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error

// ImageSpec describes how to locate a bootable ext4 artifact. Exactly one
// field should be set; CreateAndBoot returns an error if all are empty.
type ImageSpec struct {
	// Digest is a canonical "sha256:<hex>" image digest. The artifact is read
	// from the cache at <cacheRoot>/<algo>/<hex>/artifact.
	Digest string

	// Ref is a human-readable image tag (e.g. "nexus3-base:20260807"). The
	// image cache is scanned to find the matching entry. Ref may also be a
	// "sha256:<hex>" digest string — ParseDigest is tried first.
	Ref string

	// RootfsPath is a direct path to a raw ext4 file. No cache lookup is
	// performed. Intended as a dev convenience (--rootfs).
	RootfsPath string
}

// CreateAndBootOptions carries the options for CreateAndBoot.
type CreateAndBootOptions struct {
	// PreMintedID, when non-zero, is used as the sandbox ID instead of minting a
	// fresh one at step 3. The CLI sets this so it can stage per-sandbox
	// resources at a STABLE, ID-keyed path BEFORE CreateAndBoot — specifically
	// the A-MOUNT agent-config overlay dir named "<id>-agentcfg-lower" under the
	// disk dir. Staging at a path known up front means the RO live mount's
	// HostPath never changes across boot → supervisor handoff (no post-boot
	// rename), and because the dir is ID-keyed under the disk dir it is reclaimed
	// by ReapDiskCopy on Remove. Zero value = mint the ID here (default).
	PreMintedID domain.SandboxID

	// Labels is the arbitrary key=value map stamped onto the sandbox record at
	// creation time. Callers set labels via repeatable --label KEY=VALUE flags;
	// fleet verbs select by label with AND-semantics (D-PD-21).
	// Nil and empty map are equivalent (sandbox carries no labels).
	Labels map[string]string

	// RemoveOnExit records the --rm intent durably at creation time.
	RemoveOnExit bool

	// ForceDiskSpace skips the disk-space preflight (step 3.55). Set by the
	// --force flag. The preflight projects allocated bytes and can refuse a
	// create that would in fact succeed — most notably on btrfs/xfs, where
	// cowExt4's `cp --reflink=auto` clones extents at near-zero cost while the
	// projection still charges the full parent size. --force is the escape
	// hatch for that and for any other case where the operator knows better.
	ForceDiskSpace bool

	// DiskPreflight is the injectable disk-space check used at step 3.55.
	// Nil means CheckDiskSpaceBytes (production). Tests override it to drive
	// the refusal path without filling a real filesystem.
	DiskPreflight func(diskDir string, projected int64, detail string) (*DiskPreflightResult, error)

	// Image describes how to resolve the bootable ext4 artifact.
	Image ImageSpec

	// CacheRoot is the filesystem root of the image cache. Required when
	// Image.Digest or Image.Ref is set; ignored when Image.RootfsPath is set.
	CacheRoot string

	// AgentBytes is the raw content of the nexus3-agent binary. Required only
	// when Image.Ref names an OCI image that is not yet in the cache; in that
	// case resolveExt4 pulls the image from the registry and injects the agent
	// binary as /sbin/nexus3-agent (PID 1). For cached images this field may
	// be nil (the agent was already injected when the image was first cached).
	AgentBytes []byte

	// ReachabilityTimeout is the maximum time to wait for the guest agent to
	// become reachable after the VM starts. Defaults to 30 seconds.
	ReachabilityTimeout time.Duration

	// AllowedHosts is the list of hostnames the sandbox may reach through the
	// egress perimeter. Stored frozen on the Envelope and used at boot to mint
	// placeholder credentials. If empty no credentials are seeded.
	// An empty AllowedHosts does NOT imply open egress — set OpenEgress for
	// that (D-PD-33).
	AllowedHosts []string

	// OpenEgress, when true, stores OpenEgress=true in the Envelope so that
	// startSupervisor disarms the ACL for unrestricted outbound connectivity.
	// Use this for human sandboxes (--workspace, --file, --image) that need
	// unrestricted egress (docker pulls, apt-get, pip, etc.). Agent sandboxes
	// (WireClaudeEgress / orca / herdr) must never set this. D-PD-33.
	OpenEgress bool

	// AllowedRepo, when non-empty, scopes the MITM path allowlist to a single
	// GitHub repository ("owner/repo"). Required when GitHub hosts appear in
	// SecretHosts and OpenEgress is false (--egress closed). Stored frozen on
	// the Envelope; validated at create time (D-PD-36).
	AllowedRepo string

	// PathPolicies carries per-(placeholder, host) path restrictions to freeze
	// into the Envelope. Built from config.EgressSecret entries by the worktree
	// path; CLI callers use AllowedRepo (the coarser shim) for GitHub.
	// See domain.EgressPathPolicies for the key structure.
	PathPolicies domain.EgressPathPolicies

	// AllowedBranches is the list of git ref patterns the sandbox may push to
	// through the host-side git MITM. When nil the Envelope stores nil and
	// Envelope.ResolvedAllowedBranches returns the hardcoded default
	// ["refs/heads/nexus3/**"] at runtime.
	AllowedBranches []string

	// ExtraSecretHosts lists additional hostnames to include in
	// Envelope.SecretHosts without creating a corresponding SecretSpecs entry.
	// Use this for agent open-egress sandboxes (D-PD-33 dev-egress posture):
	// the agent's CredentialedHost must appear in SecretHosts so the AllowAll
	// tunnel routes it through the MITM proxy for placeholder→real swap, but
	// the host is managed by the supervisor's seedGuestAgent path — not by
	// ResolveEnvelopeSecrets — so no SecretSpecs entry should be created.
	ExtraSecretHosts []string

	// Broker is the host-side credential broker used to mint placeholder
	// credentials for AllowedHosts. If nil, credential seeding is skipped even
	// when AllowedHosts is non-empty.
	Broker *cred.Broker

	// Seeder delivers the minted placeholder env file into the guest. If nil,
	// credential seeding is skipped. See NewAgentCopySeeder for the production
	// implementation; tests inject a capture stub.
	Seeder GuestSeeder

	// SSHPublicKey is an OpenSSH-format public key to inject into the guest at
	// /root/.ssh/authorized_keys after boot (step 10). When non-empty, the key
	// is stored in Envelope.SSHPublicKey so Start (restart) can re-inject it.
	// Leave empty to skip SSH provisioning; existing behaviour is unchanged.
	SSHPublicKey string

	// MemoryMiB is the guest RAM in mebibytes to pass to the driver factory.
	// When zero the driver factory uses its built-in default (512 MiB).
	MemoryMiB uint32

	// VCPUs is the number of virtual CPUs to pass to the driver factory.
	// When zero the driver factory uses its built-in default (1 vCPU).
	VCPUs uint32

	// NestedVirt opts the sandbox into KVM-accelerated nested virtualisation.
	// When true the driver factory must set NestedVirt on cloudhypervisor.Config
	// (exposes /dev/kvm inside the guest). Default false keeps the hardened
	// default posture (D-ORCH-06 / AC-9).
	NestedVirt bool

	// DiskDir is the directory where the per-sandbox ext4 disk copy is
	// written (S-COW). When empty, defaultDiskDir() is used, which mirrors
	// the P2 snapshot-dir precedent: store.DefaultRoot()/disks.
	// Tests should set this to t.TempDir() so copies stay inside the test tree.
	DiskDir string

	// UseAgentSeed selects the agent-specific seeding path in step 9.
	// When true, SeedGuestAgent is called instead of SeedGuest; the resulting
	// payload includes CLAUDE_CODE_OAUTH_TOKEN, NODE_EXTRA_CA_CERTS, and
	// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC in addition to the generic
	// NEXUS3_CRED_* vars. Set via WireClaudeEgress.
	UseAgentSeed bool

	// AgentEgressToken is the real bearer token for the direct API-key path
	// (ANTHROPIC_AUTH_TOKEN). It is wired into the broker after placeholder
	// registration. WireClaudeEgress (OAuth path) does not use this field —
	// it sets AgentCredSource instead so the token can be sourced from a Refresher.
	AgentEgressToken string

	// AgentCredSource is the credential source for the OAuth egress path.
	// Set by WireClaudeEgress. When non-nil, step 9 of CreateAndBoot calls
	// Token(ctx) to obtain the real bearer token instead of reading
	// AgentEgressToken. Use cred.NewStaticCredentialSource for a fixed token
	// or cred.NewRefresher (backed by DefaultDedicatedCredStorePath) for
	// automatic rotation. If AgentCredSource also implements
	//   Register(domain.SandboxID)
	// (e.g. *cred.Refresher) it is invoked after SetRealToken so the sandbox
	// is enrolled for rotation.
	AgentCredSource cred.CredentialSource

	// AgentProfile is the per-sandbox agent profile used to resolve the
	// placeholder env-var name (e.g. CLAUDE_CODE_OAUTH_TOKEN) in the guest
	// seed payload. Set by WireClaudeEgress to cred.ClaudeCodeProfile.
	// Zero value is treated as cred.ClaudeCodeProfile in CreateAndBoot.
	AgentProfile cred.AgentProfile

	// AgentCredKind selects whether the guest seed payload carries the OAuth
	// placeholder (CLAUDE_CODE_OAUTH_TOKEN) or the direct API-key placeholder
	// (ANTHROPIC_AUTH_TOKEN). The zero value (kindUnset) defers to
	// resolveAgentCredKind: kindAuthToken when ANTHROPIC_AUTH_TOKEN is present
	// in the host environment, kindOAuth otherwise.
	//
	// Set AgentCredKind explicitly to override env-based resolution so that two
	// CreateAndBoot calls in the same process can use different credential kinds
	// (the N-way multiplexer prerequisite, D-P4-02).
	AgentCredKind agentCredKind

	// ExtraDisks are additional raw ext4 disk images to attach at VM boot.
	// Passed verbatim to the DriverFactory as extraDisks; the factory is
	// responsible for mapping them to cloudhypervisor.ExtraDisk and wiring
	// them into the driver Config. ExtraDisks[0] becomes /dev/vdb, [1] /dev/vdc,
	// and so on. Leave nil to attach only the rootfs disk (vda).
	ExtraDisks []ExtraDisk

	// Workspace, when non-nil, captures the host git worktree described by
	// WorkspaceSpec to a raw ext4 disk image and appends it to ExtraDisks
	// before the DriverFactory is called. The workspace disk therefore occupies
	// /dev/vd{b + len(ExtraDisks)} in attachment order. Nil means no workspace
	// capture; existing callers that omit this field are unaffected.
	Workspace *WorkspaceSpec

	// WorkspaceCapturer is the function used to build the workspace ext4 image
	// when Workspace is non-nil. Nil selects builder.WorktreeToDisk (the
	// production implementation). Inject a stub in tests to avoid requiring
	// mke2fs on the test host.
	WorkspaceCapturer func(ctx context.Context, srcDir, outExt4 string, maxBytes int64) error

	// GitSeeder is an optional GuestSeeder that delivers the per-sandbox git
	// identity configuration (user.name, user.email, safe.directory,
	// init.defaultBranch) to GuestGitconfigPath (/root/.gitconfig) in the
	// guest. When non-nil, step 11 of CreateAndBoot calls SeedGitIdentity.
	// When nil, git identity seeding is skipped (backward compatible).
	//
	// Use NewGuestFileSeeder(client, GuestGitconfigPath) to produce this seeder
	// from a live agent client (G1, D-PD-02).
	GitSeeder GuestSeeder

	// BaseRef is the full 40-hex SHA of the host repository's HEAD commit
	// at sandbox-creation time (D-PD-19). Recorded on the Sandbox domain record
	// as the shallow-clone boundary for G2 (nexus3 bundle). Empty means no git
	// workspace is attached; G2 will fail fast for such sandboxes.
	//
	// Compute this value via HostHeadSHA(Workspace.SourcePath) before calling
	// CreateAndBoot when Workspace is non-nil.
	BaseRef string

	// Secrets are host-side credential binds (D-PD-23 / D-PD-25). Each bind
	// mints a guest placeholder env var; the real token stays in the broker.
	// GitHub hosts listed here do NOT enter AllowedHosts — human create is
	// AllowAll and a curated allowlist would 403 every other host.
	// D-SHL-05: agent create (UseAgentSeed) MAY include a GitHub bind when
	// AllowedRepo is set; the pre-boot guard 6b enforces that combination.
	// Without AllowedRepo the same guard still rejects any GitHub bind.
	Secrets []SecretBind

	// Volumes is the volume store for named-volume operations. Required when
	// NamedVolumeMounts is non-empty. If nil, NamedVolumeMounts is ignored.
	Volumes *volumestore.VolumeStore

	// NamedVolumeMounts are --mount-named attachments to create and wire at
	// VM boot time. Each mount's kind=disk backing file is prepended to
	// ExtraDisks (before workspace) in declaration order, so the first mount
	// gets the first available device letter. Kind=dir mounts are virtiofs
	// (TBD-SD2-LIVE-4; attachment is recorded but no cmdline is emitted yet).
	// Guest paths containing .git are rejected by the CLI before this point.
	NamedVolumeMounts []NamedVolumeMount

	// LiveMounts are live host-directory virtiofs shares to attach at boot
	// (D-PD-53). Each entry is stored on the sandbox record; the newDriver
	// closure in the CLI wires them into the driver Config.LiveMounts and
	// emits the matching --workspace-mount guest arguments. Nil or empty means
	// no virtiofs shares; existing callers are unaffected.
	LiveMounts []domain.LiveMount
}

// NamedVolumeMount describes a single --mount-named attachment resolved by the
// CLI and passed to CreateAndBoot. The Name identifies a VolumeStore entry;
// GuestPath is the absolute path inside the guest.
type NamedVolumeMount struct {
	Name      string                 // volume name in the VolumeStore
	GuestPath string                 // absolute path inside the guest
	Kind      volumestore.VolumeKind // KindDisk or KindDir
	SizeBytes int64                  // kind=disk only; 0 = DefaultDiskSizeBytes
	ReadOnly  bool
}

// WireClaudeEgress configures opts for an agent sandbox that runs claude
// (Haiku/Sonnet/etc.) in-guest and needs egress to the Anthropic API.
//
// It sets:
//   - AllowedHosts to [AgentEgressHosts] (api.anthropic.com + platform.claude.com)
//   - Broker, Seeder, and AgentCredSource to the provided values
//   - UseAgentSeed = true so step 9 of CreateAndBoot calls seedGuestAgent
//   - AgentProfile = cred.ClaudeCodeProfile (CLAUDE_CODE_OAUTH_TOKEN placeholder)
//
// src is the credential source for the real bearer token. Pass
//
//	cred.NewStaticCredentialSource(&cred.DedicatedCredStore{AccessToken: tok})
//
// for a fixed token read from the NEXUS3_CLAUDE_OAUTH_TOKEN env var, or a
// *cred.Refresher constructed from [DefaultDedicatedCredStorePath] for
// automatic token rotation. When src is nil no real token is wired and the
// MITM proxy forwards the placeholder (egress still works, bearer is invalid).
//
// The caller owns broker, seeder, and src; WireClaudeEgress does not retain
// them beyond writing them into opts.
func WireClaudeEgress(opts *CreateAndBootOptions, broker *cred.Broker, seeder GuestSeeder, src cred.CredentialSource) {
	opts.AllowedHosts = AgentEgressHosts(cred.ClaudeCodeProfile)
	opts.Broker = broker
	opts.Seeder = seeder
	opts.UseAgentSeed = true
	opts.AgentCredSource = src
	opts.AgentProfile = cred.ClaudeCodeProfile
}

// DefaultDedicatedCredStorePath returns the path for nexus3's dedicated OAuth
// credential store used by the host-side Refresher ([cred.NewRefresher]).
//
// Default: ~/.config/nexus3/creds.json
// Override: NEXUS3_DEDICATED_CRED_STORE environment variable.
//
// S4 dogfood places the credential file at this path; see charter TBD-P5-2.
// Construct a *cred.Refresher from this path and pass it to WireClaudeEgress
// to enable automatic token rotation across sandboxes.
func DefaultDedicatedCredStorePath() string {
	if p := os.Getenv("NEXUS3_DEDICATED_CRED_STORE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "nexus3", "creds.json")
}

// CreateAndBoot creates a sandbox record, boots a VM for it, verifies the
// guest agent is reachable, and returns the running sandbox.
//
// On any failure after the record has been created the record is deleted and
// no orphan state is left behind. This includes driver.Start failure and probe
// timeout — either way the caller sees a clear error with no zombie record.
//
// The Driver returned by newDriver is owned exclusively by this call; it is
// not retained by svc. A separate call to svc.Start/Stop uses svc's driver.
// This design allows S2 to pass a CHDriver configured with a per-sandbox
// DiskImagePath without changing the CHDriver constructor signature.
func CreateAndBoot(
	ctx context.Context,
	svc *Service,
	cache *image.Cache,
	newDriver DriverFactory,
	probe ProbeFunc,
	project, name string,
	opts CreateAndBootOptions,
) (domain.Sandbox, error) {
	// 1. Resolve ext4 path from the image spec
	ext4Path, resolvedDigest, err := resolveExt4(ctx, opts.Image, cache, opts.CacheRoot, opts.AgentBytes)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w", project, name, err)
	}

	// 2. Guard against duplicate handles (mirrors service.Create)
	handle := project + "/" + name
	if _, err := svc.store.ResolveByHandle(ctx, handle); err == nil {
		return domain.Sandbox{}, fmt.Errorf("sandbox %q already exists: %w", handle, store.ErrAlreadyExists)
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot: check handle %q: %w", handle, err)
	}

	// 3. Mint sandbox ID
	//
	// ID is minted here (before driver construction) so the per-sandbox disk
	// copy can be named deterministically by ID. The old placement (after driver
	// construction) is preserved by moving it up; existing behaviour is unchanged.
	//
	// A caller may pre-mint the ID (opts.PreMintedID) so it can stage ID-keyed
	// per-sandbox resources before this call (A-MOUNT); an unset value falls back
	// to minting here so every other caller is unchanged.
	id := opts.PreMintedID
	if id == (domain.SandboxID{}) {
		id = domain.NewSandboxID()
	}

	// 3.5 Resolve disk dir and pre-compute disk resource paths
	//
	// diskDir is resolved once here (before any materialisation) so that:
	//   (a) the create-intent file can be written before cowExt4 runs (R2-AC1);
	//   (b) the disk dir is not re-derived independently for cowExt4 and the
	//       workspace capture (both use the same directory).
	needsDisk := opts.Image.RootfsPath == ""
	needsWorkspace := opts.Workspace != nil
	needsNamedVols := opts.Volumes != nil && len(opts.NamedVolumeMounts) > 0
	var diskDir string
	if needsDisk || needsWorkspace || needsNamedVols {
		diskDir = opts.DiskDir
		if diskDir == "" {
			diskDir, err = defaultDiskDir()
			if err != nil {
				return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w", project, name, err)
			}
		}
	}

	// Pre-compute planned disk paths so the intent can record them before the
	// files actually exist on disk.
	var diskCopyPath, workspaceDiskPath string
	if needsDisk {
		diskCopyPath = filepath.Join(diskDir, id.String()+".raw")
	}
	if needsWorkspace {
		workspaceDiskPath = filepath.Join(diskDir, id.String()+"-workspace.ext4")
	}

	// 3.55 Disk-space preflight
	//
	// Runs BEFORE the create intent and before any byte is written, so a host
	// with no room refuses immediately with an actionable message instead of
	// failing partway through a multi-gigabyte cp with whatever errno the
	// filesystem produces (TBD-PD-26, M3-AC2).
	//
	// The projection is a conservative upper bound: the root figure is the
	// source artifact's allocated size, which cowExt4's `cp --sparse=always`
	// can only shrink (measured on a real host: 6.00 GiB artifact -> 2.64 GiB
	// copy). The workspace capture cannot be measured before it runs and falls
	// back to a sample of existing workspace disks. When neither applies
	// (--rootfs with no workspace) the projection is zero and the check is
	// skipped entirely rather than charged against a default estimate.
	if !opts.ForceDiskSpace {
		src := ""
		if needsDisk {
			src = ext4Path
		}
		if projected, detail := ProjectCreateBytes(diskDir, src, needsWorkspace); projected > 0 {
			check := opts.DiskPreflight
			if check == nil {
				check = CheckDiskSpaceBytes
			}
			if _, dErr := check(diskDir, projected, detail); dErr != nil {
				return domain.Sandbox{}, fmt.Errorf(
					"service: create-and-boot %s/%s: %w", project, name, dErr)
			}
		}
	}

	// 3.6 Write create intent
	//
	// The intent is written to <diskDir>/<id>.create-intent.json before any
	// host resource (disk copy, workspace disk) is materialized. It records the
	// planned disk paths so the R1 reaper can reclaim them if this process is
	// killed between step 3.6 and the durable store.Create at step 6.
	//
	// On clean exit (error or success) the deferred cleanup always removes the
	// intent file. On an unclean exit (SIGKILL, panic, power loss) the defer
	// does not run; the intent survives and the reaper can discover it.
	//
	// The intent file also carries an flock(2) lease held for the whole create
	// window (see createIntentLease). It is what tells a concurrent reaper that
	// the disks below belong to a create that is still running: between here
	// and step 6 no process carries the ULID in its cmdline, so the reaper's
	// /proc liveness gate cannot see this create. The kernel drops the lease if
	// this process dies, so a crashed create still leaves reclaimable disks.
	var intentLease *createIntentLease
	// The intent lease is required whenever the §4.1 guard depends on it.
	// That is: whenever a disk copy or workspace disk will be written (the
	// original cases), OR whenever named volumes are being attached rw (M-a,
	// D-PD-93). In --rootfs mode with no workspace but with named volumes,
	// diskCopyPath and workspaceDiskPath are both empty — without this third
	// arm the intent file is never written and Row 3 cannot see this create
	// as in-flight, so two concurrent rootfs creates both fall through to
	// Row 5 (stale prune) and both attach the same rw volume.
	if diskCopyPath != "" || workspaceDiskPath != "" || needsNamedVols {
		intentLease, err = writeCreateIntent(diskDir, id, diskCopyPath, workspaceDiskPath)
		if err != nil {
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: write create intent: %w", project, name, err)
		}
	}

	// success is set to true only when CreateAndBoot completes without error.
	// The deferred cleanup:
	//   - always removes the intent file (crash: defer doesn't run → intent survives
	//     for the reaper);
	//   - removes disk files on any failure so no orphan accumulates on clean errors.
	success := false
	defer func() {
		// release removes the intent file and drops the in-flight lease that
		// keeps a concurrent reaper off these disks. It runs only after
		// store.Create has committed the record (step 6), so the resources go
		// straight from leased to owned with no unprotected instant between.
		intentLease.release()
		if !success {
			if diskCopyPath != "" {
				_ = os.Remove(diskCopyPath)
			}
			if workspaceDiskPath != "" {
				_ = os.Remove(workspaceDiskPath)
			}
		}
	}()

	// 3.7 Named-volume setup — variable declarations and deferred cleanup
	//
	// The per-volume flock (volumeLeases) is held from vs.AttachLocked /
	// checkRWAttach until svc.store.Create commits the sandbox record (D2).
	// The attach loop itself runs AFTER workspace capture (step 4.7) so the
	// hold does not span the I/O-heavy capture. The protected window becomes:
	//   attach → driver construction → §6 guards → store.Create → unlock.
	//
	// The intent lease written at step 3.6 continues to cover the entire window
	// including capture; it is what tells a concurrent reaper that this create
	// is in flight (§4.1 Row 3 — load-bearing, unaffected by this reorder).
	var namedDiskAttached []string // for rollback on failure
	// volumeLeases holds the per-volume flocks returned by checkRWAttach /
	// vs.AttachLocked. Each lock is held from step 4.7 until svc.store.Create
	// commits the sandbox record, preventing Prune from deleting a volume that
	// has been attached in meta.json but whose sandbox record does not yet
	// exist (D2).
	var volumeLeases []*store.Lock
	defer func() {
		// Release D2 leases FIRST so vs.Detach (called below) can re-lock.
		for _, lk := range volumeLeases {
			_ = lk.Unlock()
			_ = lk.Close()
		}
		if !success && opts.Volumes != nil {
			// ctx may already be cancelled on the error path; use WithoutCancel
			// so the rollback ctx is not pre-cancelled, then bound it (RISK-SD2-1).
			rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer rollbackCancel()
			for _, vname := range namedDiskAttached {
				_ = detachVolumeLocked(rollbackCtx, opts.Volumes, vname, id.String())
			}
		}
	}()

	// 3.75 Unlocked rw-volume pre-check (fail-fast UX only — NO correctness weight)
	//
	// Reads the volume record and the sandbox store WITHOUT holding the
	// per-volume lock, applies rows 1–4 of the §4.1 verdict table. Because the
	// lock is not held, the result may be stale (a concurrent create could win
	// the lock between this check and step 4.7). Its sole purpose is to surface
	// "volume already in use" BEFORE workspace capture so a user on the error
	// path does not wait 28–40 s for capture to complete. The authoritative
	// locked check runs at step 4.7 regardless of whether this pre-check passes.
	if vs := opts.Volumes; vs != nil {
		for _, mount := range opts.NamedVolumeMounts {
			if mount.Kind == volumestore.KindDisk && !mount.ReadOnly {
				if preErr := preCheckRWAttachUnlocked(ctx, vs, svc.store, diskDir, mount.Name, id.String()); preErr != nil {
					return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w", project, name, preErr)
				}
			}
		}
	}

	// 4. CoW copy: per-sandbox ext4 (cache artifact only, not --rootfs)
	//
	// Each sandbox boots from its own writable copy so the shared cache
	// artifact (digest-addressed, content-immutable) is never mutated by the
	// guest kernel's root=/dev/vda rw mount. cp --reflink=auto is free (a CoW
	// clone) on btrfs/xfs and falls back silently on other filesystems;
	// --sparse=always ensures the fallback path also preserves holes so the
	// per-sandbox copy does not inflate to the full apparent image size.
	//
	// diskCopyPath is non-empty iff a copy will be created here. The deferred
	// cleanup removes it on any subsequent failure so no 5 GiB orphan is left
	// on disk if driver construction, record creation, boot, probe, or seeding
	// fails. On success the copy is retained for the lifetime of the sandbox
	// and reaped by Service.Remove (via ReapDiskCopy).
	if needsDisk {
		cp, cpErr := cowExt4(ext4Path, diskDir, id)
		if cpErr != nil {
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w", project, name, cpErr)
		}
		// cp == diskCopyPath: cowExt4 uses the same <diskDir>/<id>.raw convention.
		ext4Path = cp
	}

	// 4.5 Capture workspace to ext4 (if requested)
	//
	// WorktreeToDisk (or the injected WorkspaceCapturer stub) walks the host
	// source tree, applies the .dockerignore + nexus3 exclusion policy, enforces
	// the size guard, and writes a raw ext4 image. The image is appended to
	// opts.ExtraDisks so the DriverFactory sees it as the last extra disk:
	// caller-supplied ExtraDisks[0..n-1] keep their positions and the workspace
	// lands at /dev/vd{b+n}. On any failure the deferred cleanup removes the
	// partial image so no orphan disk leaks to DiskDir.
	if ws := opts.Workspace; ws != nil {
		captureFn := opts.WorkspaceCapturer
		if captureFn == nil {
			captureFn = builder.WorktreeToDisk
		}
		// maxBytes <= 0 means auto (free-space-derived guard); positive values
		// are explicit caps. WorktreeToDisk handles both cases.
		maxBytes := ws.CaptureMaxBytes
		if err = os.MkdirAll(diskDir, 0o700); err != nil {
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: workspace disk dir mkdir: %w", project, name, err)
		}
		// workspaceDiskPath already resolved above; pass it directly.
		if err = captureFn(ctx, ws.SourcePath, workspaceDiskPath, maxBytes); err != nil {
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: capture workspace: %w", project, name, err)
		}
		opts.ExtraDisks = append(opts.ExtraDisks, ExtraDisk{Path: workspaceDiskPath})
	}

	// 4.7 Named-volume create + concurrency guard (moved from step 3.7)
	//
	// Runs AFTER workspace capture so the per-volume flock does not span the
	// I/O-heavy capture (up to 40 s at monorepo scale). The protected window
	// is now: attach → driver construction → §6 guards → store.Create → unlock.
	//
	// Lock acquisition order: a COPY of NamedVolumeMounts is sorted by Name
	// before the attach loop. Two concurrent creates that name the same volumes
	// in opposite declaration order (A,B and B,A) would otherwise deadlock on
	// the per-volume flocks. Consistent sorted order eliminates the ABBA cycle.
	//
	// Device-letter order (§1.5): namedDiskExtras is built from the ORIGINAL
	// declaration order, not the sorted order, so named volumes always receive
	// the device letters the caller expects regardless of sort outcome.
	//
	// The intent lease from step 3.6 remains held across this block; Row 3
	// detection is unaffected.
	if vs := opts.Volumes; vs != nil && len(opts.NamedVolumeMounts) > 0 {
		// Create all volumes idempotently first (no locking required here).
		for _, mount := range opts.NamedVolumeMounts {
			if _, err = vs.Create(ctx, mount.Name, mount.Kind, mount.SizeBytes, ""); err != nil {
				return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: create volume %s: %w", project, name, mount.Name, err)
			}
		}

		// Acquire per-volume locks in sorted-name order (ABBA deadlock prevention).
		// Declaration order is preserved in the namedDiskExtras loop below.
		sortedMounts := make([]NamedVolumeMount, len(opts.NamedVolumeMounts))
		copy(sortedMounts, opts.NamedVolumeMounts)
		sort.Slice(sortedMounts, func(i, j int) bool {
			return sortedMounts[i].Name < sortedMounts[j].Name
		})
		for _, mount := range sortedMounts {
			if mount.Kind == volumestore.KindDisk && !mount.ReadOnly {
				// Guard: one rw kind=disk attach at a time. Use a deadline so a
				// wedged lock-holder surfaces as an error, not a hung CLI (RISK-SD2-1).
				// The returned lock is held until svc.store.Create commits (D2).
				guardCtx, guardCancel := context.WithTimeout(ctx, 10*time.Second)
				var lk *store.Lock
				lk, err = checkRWAttach(guardCtx, vs, svc.store, diskDir, mount.Name, id.String())
				guardCancel()
				if err != nil {
					return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: volume %s: %w", project, name, mount.Name, err)
				}
				volumeLeases = append(volumeLeases, lk)
				namedDiskAttached = append(namedDiskAttached, mount.Name)
			} else {
				// ro kind=disk or kind=dir: attach and hold the per-volume flock
				// across svc.store.Create (D2). Use the same guardCtx budget as
				// the rw path — 10 s to acquire; held until step 6.
				guardCtx, guardCancel := context.WithTimeout(ctx, 10*time.Second)
				var lk *store.Lock
				lk, err = vs.AttachLocked(guardCtx, mount.Name, id.String())
				guardCancel()
				if err != nil {
					return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: attach volume %s: %w", project, name, mount.Name, err)
				}
				volumeLeases = append(volumeLeases, lk)
				namedDiskAttached = append(namedDiskAttached, mount.Name)
			}
		}

		// Build namedDiskExtras in DECLARATION order (§1.5: named disks must
		// keep their declared device-letter assignment regardless of which order
		// the locks were acquired above). Prepend before caller-supplied
		// ExtraDisks and the workspace disk appended by step 4.5.
		var namedDiskExtras []ExtraDisk
		for _, mount := range opts.NamedVolumeMounts {
			if mount.Kind == volumestore.KindDisk {
				namedDiskExtras = append(namedDiskExtras, ExtraDisk{Path: vs.DiskPath(mount.Name)})
			}
		}
		if len(namedDiskExtras) > 0 {
			opts.ExtraDisks = append(namedDiskExtras, opts.ExtraDisks...)
		}
	}

	// 5. Construct per-sandbox driver instance
	bootDrv, err := newDriver(ext4Path, opts.ExtraDisks)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: init driver: %w", project, name, err)
	}

	// Resolve this sandbox's agent profile ONCE, ahead of the record, so that
	// the persisted AgentName and the credential seed delivered to the guest
	// can never disagree about which agent is running. A caller that asks for
	// the agent seed without naming a profile gets the default, matching the
	// pre-TBD-PD-32 behaviour.
	agentProfile := opts.AgentProfile
	if opts.UseAgentSeed && agentProfile.PlaceholderEnvVar == "" {
		agentProfile = cred.ClaudeCodeProfile
	}

	sb := domain.Sandbox{
		ID:      id,
		Name:    name,
		Project: project,
		Labels:  opts.Labels,
		State:   domain.Created,
		Envelope: domain.Envelope{
			ImageDigest:  resolvedDigest,
			AllowedHosts: opts.AllowedHosts, // frozen at creation (P1-S6)
			SSHPublicKey: opts.SSHPublicKey, // frozen at creation (ORCA-S1)
			SecretHosts:  append(secretHostsFromBinds(opts.Secrets), opts.ExtraSecretHosts...),
			SecretSpecs:  secretSpecsFromBinds(opts.Secrets),
			OpenEgress:      opts.OpenEgress,      // D-PD-33: explicit opt-in; never inferred from empty AllowedHosts
			AllowedRepo:     opts.AllowedRepo,     // D-PD-36: per-repo path allowlist; enforced below
			AllowedBranches: opts.AllowedBranches, // S0: nil = use default at runtime via ResolvedAllowedBranches
			PathPolicies:    opts.PathPolicies,    // T4: per-secret path policies; converted to mitm.PathPolicies at start
		},
		RemoveOnExit:   opts.RemoveOnExit,
		BaseRef:        opts.BaseRef, // G1: shallow-clone boundary SHA (D-PD-19); empty if no git workspace
		MountedVolumes: namedVolumeAttachments(opts.NamedVolumeMounts),
		LiveMounts:     opts.LiveMounts,
		AgentName:      agentProfile.Name, // TBD-PD-32: empty when no agent is attached
	}
	// 6a. Mixed-host guard: a bind must not span GitHub and non-GitHub hosts
	//
	// Non-GitHub hosts carry no path filter, so mixing them with GitHub hosts
	// in a single bind would forward the real token to any path on those hosts.
	// The operator already holds the token (no escalation), but the config is
	// refused to prevent accidental misconfiguration. Checked before the
	// AllowedRepo guard below because it is unconditional.
	for _, b := range opts.Secrets {
		if SecretMixesGitHubHosts(b) {
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w", project, name, ErrMixedGitHubSecret)
		}
	}
	// 6b. D-PD-36 / D-PDE-16 invariant: every secret bind touching a GitHub
	// host MUST carry a path policy (AllowedRepo shim OR a PathPolicies entry
	// for that host). Non-GitHub host binds without a path policy are permitted
	// — host-only bound is legal for non-GitHub hosts (D-PDE-16 asymmetry).
	// This guard runs before store.Create and before boot, so a violating
	// sandbox cannot be persisted or started. Primary enforcement point for
	// ALL callers (CLI, MCP, orca, herdr).
	//
	// Rationale (load-bearing): operator github credential is an unrotated
	// FULL-SCOPE PAT auto-sourced from `gh auth token`; host-bounding does not
	// bound it across repos — only the positive path policy does (denylist
	// enumeration failed twice; only the positive allowlist held). Do NOT add
	// an opt-in flag — that makes a safety guard on a full-scope PAT opt-out.
	//
	// D-SHL-05: agent sandboxes MAY bind a GitHub secret when a path policy is
	// set — the guest holds only a 64-hex placeholder; the host-side MITM proxy
	// enforces the policy before forwarding.
	//
	// The post-boot ErrAgentGitHubSecret guard that previously rejected agent
	// sandboxes with any GitHub secret has been removed as an intentional
	// reversal of D-PD-23 under D-SHL-05 — NOT as dead-code cleanup.
	//
	// Independent backstops for records already on disk:
	//   - service.go Start (~line 413): same guard at boot time
	//   - service.go startSupervisor (~line 787): same guard before proxy starts
	for _, b := range opts.Secrets {
		for _, h := range b.Hosts {
			if isGitHubHost(h) && !githubHostBoundByPolicy(h, opts.AllowedRepo, opts.PathPolicies) {
				return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w", project, name, ErrUnboundGitHubSecret)
			}
		}
	}
	// D2 test seam: fires while volumeLeases are still held (locks not yet
	// released). Nil in production. Tests use this to call Prune inside the
	// window and confirm the held lease blocks deletion.
	if hookPtr := testHookBeforeStoreCreate.Load(); hookPtr != nil {
		if hookErr := (*hookPtr)(); hookErr != nil {
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: testHookBeforeStoreCreate: %w", project, name, hookErr)
		}
	}
	if err := svc.store.Create(ctx, sb); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: create record: %w", project, name, err)
	}
	// D2: sandbox record is now durable — release volume leases so Prune can
	// observe the committed record and correctly classify the volume as live.
	for _, lk := range volumeLeases {
		_ = lk.Unlock()
		_ = lk.Close()
	}
	volumeLeases = nil

	// 7. Boot VM inside store.Update (same locking pattern as service.Start)
	//
	// driver.Start is called inside Update so the substrate call and the record
	// write are guarded by the same per-sandbox exclusive flock. This prevents
	// the double-boot race described in service.Start.
	var booted domain.Sandbox
	bootErr := svc.store.Update(ctx, id, func(rec *domain.Sandbox) error {
		// Re-validate inside the lock (authoritative check).
		tr, err := svc.machine.Next(rec.State, lifecycle.TriggerStart)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		instanceID, err := bootDrv.Start(ctx, driver.StartRequest{
			SandboxID:   rec.ID,
			ImageDigest: rec.Envelope.ImageDigest,
		})
		if err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		rec.State = tr.NextState
		rec.InstanceID = instanceID
		rec.StopReason = "" // cleared: sandbox is running
		booted = *rec
		return nil
	})
	if bootErr != nil {
		// Boot failed — delete the record so no orphan exists in Created state.
		_ = svc.store.Delete(ctx, id)
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: boot: %w", project, name, bootErr)
	}

	// 8. Reachability probe
	if probe != nil {
		timeout := opts.ReachabilityTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		if err := probe(rCtx, bootDrv, booted.ID); err != nil {
			// Agent unreachable — stop the VM and remove the record.
			_ = bootDrv.Stop(ctx, booted.ID)
			_ = svc.store.Delete(ctx, booted.ID)
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w: %v",
				project, name, ErrAgentUnreachable, err)
		}
	}

	// 9. Seed guest with placeholder credentials (P1-S6)
	//
	// SeedGuest / seedGuestAgent are no-ops when Broker or Seeder is nil, so
	// existing callers that omit these fields are unaffected.
	//
	// When UseAgentSeed is true the agent-specific path is taken:
	//   a) seedGuestAgent mints placeholders for AgentEgressHosts and delivers
	//      the augmented payload (profile-driven credential var + CA cert path +
	//      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC) to the guest.
	//   b) The real token is obtained from AgentCredSource.Token(ctx) (OAuth
	//      path, set by WireClaudeEgress) or from AgentEgressToken directly
	//      (API-key path, set by the caller via AgentCredKind=kindAuthToken).
	//   c) broker.SetRealToken wires the real token host-side so the MITM proxy
	//      can swap the placeholder on each forwarded request.
	//   d) If AgentCredSource also implements Register(domain.SandboxID)
	//      (i.e. is a *cred.Refresher), the sandbox is enrolled for automatic
	//      token rotation.
	//      If no real token is available a warning is logged; egress still works
	//      but the proxy will forward the placeholder rather than a valid bearer.
	//
	// NOTE: service.Start (restart of a stopped sandbox) will also need seeding
	// once it gains a reachability probe — /run is tmpfs and does not survive
	// a guest restart. That wiring is deferred until the restart probe exists.
	// D-SHL-05: the pre-boot guard 6b above (ErrUnboundGitHubSecret) now covers
	// agent sandboxes and non-agent sandboxes alike (AllowedRepo == "" is the
	// only forbidden shape regardless of UseAgentSeed).
	// The post-boot ErrAgentGitHubSecret guard has been removed as a deliberate
	// reversal of D-PD-23 under D-SHL-05 — it was reachable for the
	// agent+AllowedRepo case D-SHL-05 permits, not dead code.

	if opts.UseAgentSeed {
		// agentProfile was resolved above and is the same value recorded as
		// sb.AgentName, so the guest seed and the sandbox record cannot diverge.
		recs, err := seedGuestAgent(ctx, opts.Broker, booted.ID, opts.Seeder, agentProfile, opts.AgentCredKind)
		if err != nil {
			_ = bootDrv.Stop(ctx, booted.ID)
			_ = svc.store.Delete(ctx, booted.ID)
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: seed agent: %w", project, name, err)
		}

		// Resolve the real token: AgentCredSource (OAuth/Refresher path) takes
		// priority; AgentEgressToken is the fallback for the direct API-key path.
		var realToken string
		if opts.AgentCredSource != nil {
			t, _, credErr := opts.AgentCredSource.Token(ctx)
			if credErr != nil {
				slog.Warn("create-and-boot: get real token from credential source",
					"sandbox", booted.ID, "host", agentProfile.CredentialedHost, "err", credErr)
			} else {
				realToken = t
			}
		} else if opts.AgentEgressToken != "" {
			// API-key path: caller sets AgentEgressToken and AgentCredKind=kindAuthToken.
			realToken = opts.AgentEgressToken
		}

		if realToken != "" && opts.Broker != nil && len(recs) > 0 {
			if err := opts.Broker.SetRealToken(booted.ID, agentProfile.CredentialedHost, realToken); err != nil {
				// Non-fatal: the proxy will forward the placeholder, which is
				// useless but not a build-path failure. Log and continue.
				slog.Warn("create-and-boot: set real token for agent egress",
					"sandbox", booted.ID, "host", agentProfile.CredentialedHost, "err", err)
			}
			// Enrol in the Refresher if the source supports it (type assert to
			// avoid importing cred.Refresher directly and to stay interface-clean).
			type sandboxRegistrar interface{ Register(domain.SandboxID) }
			if reg, ok := opts.AgentCredSource.(sandboxRegistrar); ok {
				reg.Register(booted.ID)
			}
			// Mirror-enrol for deregistration on Remove. sandboxDeregistrar is
			// defined in service.go (same package) and *cred.Refresher satisfies it.
			if dr, ok := opts.AgentCredSource.(sandboxDeregistrar); ok {
				svc.storeDeregistrar(booted.ID, dr)
			}
		} else if realToken == "" {
			slog.Warn("create-and-boot: no real token for agent egress; egress will send placeholder",
				"sandbox", booted.ID, "host", agentProfile.CredentialedHost,
				"hint", "set ANTHROPIC_AUTH_TOKEN (auth-token path) or configure NEXUS3_DEDICATED_CRED_STORE (OAuth path)")
		}
	} else {
		var combined []byte
		capture := func(_ context.Context, _ domain.SandboxID, payload []byte) error {
			combined = append(combined, payload...)
			return nil
		}
		seedFn := opts.Seeder
		if seedFn != nil {
			seedFn = capture
		}
		if _, err := SeedGuest(ctx, opts.Broker, booted.ID, booted.Envelope.AllowedHosts, seedFn); err != nil {
			_ = bootDrv.Stop(ctx, booted.ID)
			_ = svc.store.Delete(ctx, booted.ID)
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: seed: %w", project, name, err)
		}
		extra, _, err := applySecrets(opts.Broker, booted.ID, opts.Secrets)
		if err != nil {
			_ = bootDrv.Stop(ctx, booted.ID)
			_ = svc.store.Delete(ctx, booted.ID)
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: secrets: %w", project, name, err)
		}
		combined = append(combined, extra...)
		if opts.Seeder != nil && len(combined) > 0 {
			if err := opts.Seeder(ctx, booted.ID, combined); err != nil {
				_ = bootDrv.Stop(ctx, booted.ID)
				_ = svc.store.Delete(ctx, booted.ID)
				return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: seed: %w", project, name, err)
			}
		}
	}

	// 10. Inject SSH authorized_keys (ORCA-S1)
	//
	// SeedSSHAuthorizedKeys is a no-op when SSHPublicKey is empty or when the
	// service has no sshSeeder attached, preserving existing behaviour.
	// On failure the VM is stopped and the record deleted — SSH provisioning is
	// a hard requirement when requested (unlike best-effort CA seeding).
	if booted.Envelope.SSHPublicKey != "" {
		if err := SeedSSHAuthorizedKeys(ctx, booted.Envelope.SSHPublicKey, booted.ID, svc.sshSeeder); err != nil {
			_ = bootDrv.Stop(ctx, booted.ID)
			_ = svc.store.Delete(ctx, booted.ID)
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: ssh seed: %w", project, name, err)
		}
	}

	// 11. Seed git identity (G1, D-PD-03)
	//
	// SeedGitIdentity is a no-op when GitSeeder is nil (existing callers that
	// omit GitSeeder are unaffected). When set, it resolves the operator's git
	// identity from the host's global git config (user.name, user.email) and
	// pushes a gitconfig to GuestGitconfigPath (/root/.gitconfig) configuring
	// that real identity, the workspace safe.directory, and the per-sandbox
	// branch name (nexus3/<motive-slug>/<short-id>).
	//
	// If the host git identity is not configured, CreateAndBoot returns an
	// actionable error — a silent bot-identity fallback is deliberately absent
	// (operator decision 2026-08-15, reversing D-PD-02).
	//
	// BaseRef was already recorded on the Sandbox domain record at step 6.
	// The in-guest `git clone --depth 1 file://<host-path>` that establishes the
	// shallow boundary is an in-guest startup action (live-VM, out of scope here).
	if opts.GitSeeder != nil {
		var workspacePath string
		if opts.Workspace != nil {
			workspacePath = opts.Workspace.GuestPath
		}
		if _, err := SeedGitIdentity(ctx, booted.ID, opts.Labels, SourceGuestPaths(workspacePath, opts.LiveMounts), opts.GitSeeder); err != nil {
			_ = bootDrv.Stop(ctx, booted.ID)
			_ = svc.store.Delete(ctx, booted.ID)
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: git identity: %w", project, name, err)
		}
	}

	success = true
	return booted, nil
}

// defaultDiskDir returns the durable directory for per-sandbox ext4 disk
// copies, mirroring the P2 snapshot precedent: store.DefaultRoot()/disks.
func defaultDiskDir() (string, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return "", fmt.Errorf("determine disk dir: %w", err)
	}
	return filepath.Join(root, "disks"), nil
}

// cowExt4 copies src to <diskDir>/<id>.raw preserving sparseness.
//
// It uses two flags:
//   - --reflink=auto: free CoW clone on btrfs/xfs; silent full-copy fallback
//     on ext4 and other filesystems that do not support reflinks.
//   - --sparse=always: on the ext4 fallback path, detect zero runs and punch
//     holes so that a sparse source image (e.g. one built by runMke2fs with
//     lazy_itable_init=1) yields a sparse destination. Without this flag the
//     fallback copy reads the zero blocks and writes them out, filling the
//     holes and inflating on-disk usage to the full apparent image size.
func cowExt4(src, diskDir string, id domain.SandboxID) (string, error) {
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		return "", fmt.Errorf("cow ext4: mkdir %s: %w", diskDir, err)
	}
	dst := filepath.Join(diskDir, id.String()+".raw")
	var stderr bytes.Buffer
	cmd := exec.Command("cp", "--reflink=auto", "--sparse=always", src, dst)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cow ext4: cp --reflink=auto --sparse=always %s → %s: %w: %s", src, dst, err, stderr.String())
	}
	return dst, nil
}

// resolveExt4 determines the path to a bootable ext4 artifact from spec.
// Returns the path and the digest string to store in Envelope.ImageDigest.
// When spec.RootfsPath is set the digest is empty (no cache entry).
//
// agentBytes is required only on an OCI cache miss: if spec.Ref names a
// public OCI image that is not yet in the cache, resolveExt4 pulls and
// converts it (injecting agentBytes as PID 1) before returning. For cached
// images agentBytes may be nil.
func resolveExt4(
	ctx context.Context,
	spec ImageSpec,
	cache *image.Cache,
	cacheRoot string,
	agentBytes []byte,
) (ext4Path, imageDigest string, err error) {
	switch {
	case spec.RootfsPath != "":
		// Direct ext4 path — no cache lookup.
		return spec.RootfsPath, "", nil

	case spec.Digest != "":
		d, err := domain.ParseDigest(spec.Digest)
		if err != nil {
			return "", "", fmt.Errorf("resolve image: invalid digest %q: %w", spec.Digest, err)
		}
		if _, err := cache.Get(ctx, d); err != nil {
			return "", "", fmt.Errorf("resolve image: digest %q: %w", spec.Digest, err)
		}
		return filepath.Join(cacheRoot, d.Algo(), d.Hex(), "artifact"), string(d), nil

	case spec.Ref != "":
		// Accept digest strings in the Ref field as a convenience.
		if d, err := domain.ParseDigest(spec.Ref); err == nil {
			if _, err := cache.Get(ctx, d); err != nil {
				return "", "", fmt.Errorf("resolve image: digest %q: %w", spec.Ref, err)
			}
			return filepath.Join(cacheRoot, d.Algo(), d.Hex(), "artifact"), string(d), nil
		}
		// Scan the cache list for a matching human-readable ref.
		imgs, err := cache.List(ctx)
		if err != nil {
			return "", "", fmt.Errorf("resolve image: list cache: %w", err)
		}
		var matches []domain.Image
		for _, img := range imgs {
			if img.Ref == spec.Ref {
				matches = append(matches, img)
			}
		}
		switch len(matches) {
		case 0:
			// Cache miss: pull from the OCI registry, convert to an ext4 rootfs,
			// inject the nexus3-agent binary, and store in the image cache. Then
			// recurse by digest so the path is returned from the single-match branch.
			if len(agentBytes) == 0 {
				return "", "", fmt.Errorf("resolve image: no cached image with ref %q: %w", spec.Ref, ErrAgentBytesRequired)
			}
			digest, pullErr := builderimage.PullAndCacheOCI(ctx, spec.Ref, cache, agentBytes)
			if pullErr != nil {
				return "", "", fmt.Errorf("resolve image: pull OCI %q: %w", spec.Ref, pullErr)
			}
			return resolveExt4(ctx, ImageSpec{Digest: digest}, cache, cacheRoot, nil)
		case 1:
			d := matches[0].Digest
			return filepath.Join(cacheRoot, d.Algo(), d.Hex(), "artifact"), string(d), nil
		default:
			// Refuse rather than pick. Cache.Put transfers a ref to the newest
			// holder, so more than one holder means the cache predates that
			// rule — and guessing here is how a stale image boots while the
			// operator believes they are testing the new one. Newest first so
			// the digest they most likely want is the first line.
			sort.Slice(matches, func(i, j int) bool {
				return matches[i].CreatedAt.After(matches[j].CreatedAt)
			})
			var b strings.Builder
			fmt.Fprintf(&b, "resolve image: ref %q is ambiguous — %d cached images carry it:", spec.Ref, len(matches))
			for _, m := range matches {
				fmt.Fprintf(&b, "\n  %s  (created %s)", m.Digest, m.CreatedAt.UTC().Format(time.RFC3339))
			}
			fmt.Fprintf(&b, "\nPass one of these digests instead, or rebuild the image to reassign the ref to the newest.")
			return "", "", errors.New(b.String())
		}

	default:
		return "", "", fmt.Errorf("resolve image: one of Digest, Ref, or RootfsPath must be set")
	}
}

func secretHostsFromBinds(binds []SecretBind) []string {
	if len(binds) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, b := range binds {
		for _, h := range b.Hosts {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "" {
				continue
			}
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			out = append(out, h)
		}
	}
	return out
}

func secretSpecsFromBinds(binds []SecretBind) []string {
	if len(binds) == 0 {
		return nil
	}
	out := make([]string, 0, len(binds))
	for _, b := range binds {
		if b.Env == "" || len(b.Hosts) == 0 {
			continue
		}
		out = append(out, b.Env+"@"+strings.Join(b.Hosts, ","))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// namedVolumeAttachments converts NamedVolumeMount slice to domain.VolumeAttachment
// slice for storage on the sandbox record (MountedVolumes field).
func namedVolumeAttachments(mounts []NamedVolumeMount) []domain.VolumeAttachment {
	if len(mounts) == 0 {
		return nil
	}
	vas := make([]domain.VolumeAttachment, len(mounts))
	for i, m := range mounts {
		vas[i] = domain.VolumeAttachment{
			Name:      m.Name,
			GuestPath: m.GuestPath,
			Kind:      string(m.Kind),
			ReadOnly:  m.ReadOnly,
		}
	}
	return vas
}
