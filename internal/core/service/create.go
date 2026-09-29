package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/builder/builderimage"
	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/fsutil"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/volumestore"
)

// ErrAgentUnreachable is returned by CreateAndBoot when the VM starts but the
// guest agent does not become reachable within the configured timeout.
var ErrAgentUnreachable = errors.New("service: guest agent did not answer after VM boot")

var ociPullAndCacheFn = builderimage.PullAndCacheOCI

var ociPullAndCacheToolsFn = builderimage.PullAndCacheOCIWithTools

// ErrAgentBytesRequired is returned by CreateAndBoot when an OCI pull is
// required but no agent binary was supplied in CreateAndBootOptions.
var ErrAgentBytesRequired = errors.New("no agent binary available for OCI pull (set AgentBytes in CreateAndBootOptions)")

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

// ScratchDisk* constants define the naming and guest-side contract for the
// per-sandbox scratch disk that replaces /tmp tmpfs for workspace sandboxes
// (D-DC-32, D-SD-01, D-SD-02). These are the authoritative contract consumed
// by this package (host creation), the CLI (cmdline token), and the in-guest
// agent (wipe+mount).
const (
	// ScratchDiskGuestMount is the in-guest mount point of the scratch disk.
	ScratchDiskGuestMount = "/tmp"
	// ScratchDiskDefaultBytes is the initial sparse size of the scratch disk.
	// The file is hole-punched (no bytes allocated on host until guest writes).
	ScratchDiskDefaultBytes int64 = 8 << 30 // 8 GiB
)

// ScratchDiskHostPath returns the host filesystem path for a sandbox's scratch
// disk image. Follows the same <diskDir>/<id>-<role>.ext4 convention as the
// workspace disk.
func ScratchDiskHostPath(diskDir, id string) string {
	return filepath.Join(diskDir, id+"-scratch.ext4")
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

	// Ref is a human-readable image tag (e.g. "nexus-base:20260807"). The
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

	// AgentBytes is the raw content of the nexus-agent binary. Required only
	// when Image.Ref names an OCI image that is not yet in the cache; in that
	// case resolveExt4 pulls the image from the registry and injects the agent
	// binary as /sbin/nexus-agent (PID 1). For cached images this field may
	// be nil (the agent was already injected when the image was first cached).
	AgentBytes []byte

	// SandboxTools lists host-verified tool binaries (e.g. gh) to inject into
	// the OCI-derived ext4 image at pull time. The tools are staged via
	// toolcache.StageTree inside the image rootfs so they are available in every
	// sandbox booted from that image. nil and empty slice are equivalent (no
	// extra tools). When non-empty a different image-cache slot is used (keyed
	// by builderimage.CacheTag(agentBytes, tools)) so that existing cached images
	// without tools are not inadvertently reused.
	SandboxTools []toolcache.Fetched

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

	// MCPPolicies carries per-server MCP egress policies to freeze into the
	// Envelope. See domain.EgressMCPPolicies for the key structure.
	MCPPolicies domain.EgressMCPPolicies

	// AllowedBranches is the list of git ref patterns the sandbox may push to
	// through the host-side git MITM. When left nil AND the create call also
	// binds a workspace (Workspace, or a LiveMounts entry at /workspace),
	// CreateAndBoot derives it automatically from that worktree's current
	// branch — see resolveAllowedBranches. Set this explicitly only to
	// override that derivation. With no workspace bound at all, nil is
	// stored as-is and Envelope.ResolvedAllowedBranches returns the hardcoded
	// default ["refs/heads/nexus/**"] at runtime.
	AllowedBranches []string

	// ExtraSecretHosts lists additional hostnames to include in
	// Envelope.SecretHosts without creating a corresponding SecretSpecs entry.
	// Use this for agent open-egress sandboxes (D-PD-33 dev-egress posture):
	// the agent's CredentialedHost must appear in SecretHosts so the AllowAll
	// tunnel routes it through the MITM proxy for placeholder→real swap, but
	// the host is managed by the supervisor's seedGuestAgent path — not by
	// ResolveEnvelopeSecrets — so no SecretSpecs entry should be created.
	ExtraSecretHosts []string

	// ExtraSecretHostSuffixes lists dot-anchored DNS suffixes to include in
	// Envelope.SecretHostSuffixes. Suffix-matched hosts are MITM'd and
	// credential-swapped like exact SecretHosts but cover sharded/regional
	// endpoints whose names vary (e.g. ".cursor.sh"). Each suffix must begin
	// with ".". No SecretSpecs entry is created for suffix hosts.
	ExtraSecretHostSuffixes []string

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
	// NEXUS_CRED_* vars. Set via WireClaudeEgress.
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
	// seed payload. Set by WireClaudeEgress to cred.MustProfileByName(cred.ClaudeCodeProfileName).
	// Zero value is treated as cred.MustProfileByName(cred.ClaudeCodeProfileName) in CreateAndBoot.
	AgentProfile cred.AgentProfile

	// ExtraAgentProfiles holds the resolved profiles for extra agents listed in
	// sandbox.agents beyond the primary (D-TP-09). Each profile's credentials
	// are seeded alongside the primary in a single guest write so the MITM proxy
	// holds a real token to swap for every intercepted agent host. Nil means
	// no extra agents — the primary-only path is unchanged.
	ExtraAgentProfiles []cred.AgentProfile

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

	// NoScratchDisk disables the per-sandbox scratch disk even when Workspace is
	// non-nil. The zero value (false) attaches a scratch disk to every workspace
	// sandbox (D-SD-02: off-switch, not an on-flag).
	NoScratchDisk bool

	// GitSeeder is an optional GuestSeeder that delivers the per-sandbox git
	// identity configuration (user.name, user.email, safe.directory,
	// init.defaultBranch) to GuestGitconfigPath (/etc/gitconfig) in the
	// guest. When non-nil, step 11 of CreateAndBoot calls SeedGitIdentity.
	// When nil, git identity seeding is skipped (backward compatible).
	//
	// Use NewGuestFileSeeder(client, GuestGitconfigPath) to produce this seeder
	// from a live agent client (G1, D-PD-02).
	GitSeeder GuestSeeder

	// BaseRef is the full 40-hex SHA of the host repository's HEAD commit
	// at sandbox-creation time (D-PD-19). Recorded on the Sandbox domain record
	// as the shallow-clone boundary for G2 (nexus bundle). Empty means no git
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

	// Vault is the credential vault for principal-scoped token resolution.
	// When set, the principal is derived from NEXUS_PRINCIPAL env or LocalPrincipal.
	Vault vault.Vault

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

// WireAgentEgress configures opts for an agent sandbox running the given
// profile. It is the profile-generic form of [WireClaudeEgress] and sets all
// per-profile fields from profile rather than hardcoding [cred.MustProfileByName(cred.ClaudeCodeProfileName)].
//
// Adding a third agent requires no new function: pass its profile and a matching
// CredentialSource (from [cred.NewCredentialSourceForProfile] or a [cred.Refresher]).
//
// The caller owns broker, seeder, and src; WireAgentEgress does not retain
// them beyond writing them into opts.
func WireAgentEgress(opts *CreateAndBootOptions, profile cred.AgentProfile, broker *cred.Broker, seeder GuestSeeder, src cred.CredentialSource) {
	opts.AllowedHosts = AgentEgressHosts(profile)
	opts.Broker = broker
	opts.Seeder = seeder
	opts.UseAgentSeed = true
	opts.AgentCredSource = src
	opts.AgentProfile = profile
}

// WireClaudeEgress configures opts for an agent sandbox that runs claude
// (Haiku/Sonnet/etc.) in-guest and needs egress to the Anthropic API.
//
// It sets:
//   - AllowedHosts to [AgentEgressHosts] (api.anthropic.com + platform.claude.com)
//   - Broker, Seeder, and AgentCredSource to the provided values
//   - UseAgentSeed = true so step 9 of CreateAndBoot calls seedGuestAgent
//   - AgentProfile = cred.MustProfileByName(cred.ClaudeCodeProfileName) (CLAUDE_CODE_OAUTH_TOKEN placeholder)
//
// src is the credential source for the real bearer token. Pass
//
//	cred.NewStaticCredentialSource(&cred.DedicatedCredStore{AccessToken: tok})
//
// for a fixed token read from the NEXUS_CLAUDE_OAUTH_TOKEN env var, or a
// *cred.Refresher constructed from [DefaultDedicatedCredStorePath] for
// automatic token rotation. When src is nil no real token is wired and the
// MITM proxy forwards the placeholder (egress still works, bearer is invalid).
//
// The caller owns broker, seeder, and src; WireClaudeEgress delegates to
// [WireAgentEgress] and is kept for compatibility.
func WireClaudeEgress(opts *CreateAndBootOptions, broker *cred.Broker, seeder GuestSeeder, src cred.CredentialSource) {
	WireAgentEgress(opts, cred.MustProfileByName(cred.ClaudeCodeProfileName), broker, seeder, src)
}

// DedicatedCredStorePathForProfile returns the host-side OAuth credential store
// path for the given agent profile.
//
// claude-code is a special case: it always resolves to ~/.config/nexus/creds.json —
// the legacy single-tenant path. Operators have live credentials at that path and
// changing it would silently log them out of every existing sandbox. The
// NEXUS_DEDICATED_CRED_STORE environment variable applies only to this alias.
//
// All other profiles resolve to ~/.config/nexus/agent-creds/<name>.json where
// <name> is the sanitized profile name, following the same sanitization convention
// as DefaultMCPOAuthStoreRoot (see mcpoauth_refresh.go:sanitizeForFS).
func DedicatedCredStorePathForProfile(profile cred.AgentProfile) string {
	// claude-code: preserve the legacy path unchanged. Live operator credentials live
	// here; any change silently invalidates every existing sandbox. The env-var
	// override applies only to this alias so it stays forward-compatible.
	if profile.Name == "" || profile.Name == cred.ClaudeCodeProfileName {
		if p := os.Getenv("NEXUS_DEDICATED_CRED_STORE"); p != "" {
			return p
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "nexus", "creds.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "nexus", "agent-creds", sanitizeForFS(profile.Name)+".json")
}

// DedicatedLockFilePathForProfile returns the advisory lock file path for the
// credential store of the given agent profile. Mirrors the convention used by the
// cred package: storePath + ".lock" (see cred/store.go:lockFilePath).
func DedicatedLockFilePathForProfile(profile cred.AgentProfile) string {
	return DedicatedCredStorePathForProfile(profile) + ".lock"
}

// DefaultDedicatedCredStorePath returns the path for nexus's dedicated OAuth
// credential store. Deprecated: all production call sites now use
// [DedicatedCredStorePathForProfile] with the agent's profile. This wrapper
// delegates to DedicatedCredStorePathForProfile for the claude-code alias and
// has no production callers — kept only as a named compatibility shim.
func DefaultDedicatedCredStorePath() string {
	return DedicatedCredStorePathForProfile(cred.MustProfileByName(cred.ClaudeCodeProfileName))
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
	if nmErr := CheckNetModeEnv(); nmErr != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w", project, name, nmErr)
	}

	r := &createRun{svc: svc, project: project, name: name, opts: opts}
	if err := r.resolveImage(ctx, cache); err != nil {
		return domain.Sandbox{}, err
	}
	if err := r.planDisks(); err != nil {
		return domain.Sandbox{}, err
	}
	if err := r.preflightDiskSpace(); err != nil {
		return domain.Sandbox{}, err
	}
	if err := r.writeIntent(); err != nil {
		return domain.Sandbox{}, err
	}
	defer r.releaseDisks()
	defer r.releaseVolumes(ctx)

	for _, phase := range []func() error{
		func() error { return r.precheckVolumes(ctx) },
		r.copyRootDisk,
		func() error { return r.captureWorkspace(ctx) },
		func() error { return r.attachVolumes(ctx) },
		r.addScratchDisk,
		func() error { return r.buildRecord(newDriver) },
		r.guardSecrets,
		func() error { return r.persist(ctx) },
		func() error { return r.boot(ctx) },
		func() error { return r.probeReachable(ctx, probe) },
		func() error { return r.seedCredentials(ctx) },
		func() error { return r.seedSSHKeys(ctx) },
		func() error { return r.seedGit(ctx) },
	} {
		if err := phase(); err != nil {
			return domain.Sandbox{}, err
		}
	}

	r.success = true
	return r.booted, nil
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
	if err := fsutil.CopyFileReflink(src, dst); err != nil {
		return "", fmt.Errorf("cow ext4: copy %s → %s: %w", src, dst, err)
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
	return resolveExt4WithTools(ctx, spec, cache, cacheRoot, agentBytes, nil)
}

// resolveExt4WithTools is the tools-aware variant of resolveExt4. tools is the
// set of host-verified binaries to inject into the image on an OCI pull. When
// tools is nil or empty the behaviour is identical to resolveExt4 so that all
// existing callers and tests continue to work unchanged.
//
// On a cache miss or stale-agent hit:
//   - len(tools)==0 → ociPullAndCacheFn (the original two-arg fn) so existing
//     stubs in test files using the old signature keep compiling and passing.
//   - len(tools)>0  → ociPullAndCacheToolsFn which also stages the tool binaries.
//
// The agent-tag comparison uses builderimage.CacheTag(agentBytes, tools) instead
// of image.BuilderAgentTag(agentBytes) so that an image cached without tools is
// treated as stale once tools are requested, and an image cached with the same
// tool set hits the cache without re-pulling.
func resolveExt4WithTools(
	ctx context.Context,
	spec ImageSpec,
	cache *image.Cache,
	cacheRoot string,
	agentBytes []byte,
	tools []toolcache.Fetched,
) (ext4Path, imageDigest string, err error) {
	// pull invokes the appropriate pull function depending on whether tools are
	// requested, then recurses by digest to return the resolved path.
	pull := func(ref string) (string, string, error) {
		var digest string
		var pullErr error
		if len(tools) == 0 {
			digest, pullErr = ociPullAndCacheFn(ctx, ref, cache, agentBytes)
		} else {
			digest, pullErr = ociPullAndCacheToolsFn(ctx, ref, cache, agentBytes, tools)
		}
		if pullErr != nil {
			return "", "", fmt.Errorf("resolve image: pull OCI %q: %w", ref, pullErr)
		}
		return resolveExt4WithTools(ctx, ImageSpec{Digest: digest}, cache, cacheRoot, nil, nil)
	}

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
			// inject the nexus-agent binary (and tools when requested), and store
			// in the image cache. Then recurse by digest.
			if len(agentBytes) == 0 {
				return "", "", fmt.Errorf("resolve image: no cached image with ref %q: %w", spec.Ref, ErrAgentBytesRequired)
			}
			return pull(spec.Ref)
		case 1:
			m := matches[0]
			wantTag := builderimage.CacheTag(agentBytes, tools)
			if len(agentBytes) > 0 && m.Kind == domain.KindBase && m.AgentTag != wantTag {
				return pull(spec.Ref)
			}
			d := m.Digest
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

// extraAgentNamesFromProfiles extracts the Name field from each AgentProfile in
// profiles, returning nil when the slice is empty. Used to persist extra agent
// names on domain.Sandbox.ExtraAgentNames at create time (D-TP-09).
func extraAgentNamesFromProfiles(profiles []cred.AgentProfile) []string {
	if len(profiles) == 0 {
		return nil
	}
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names
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

// hostWorkspacePath returns the host path bound to the sandbox's /workspace
// mount, if any. A given CreateAndBoot call populates at most one of the two
// mechanisms that carry a worktree host path:
//   - opts.Workspace (the human `--workspace` ext4-capture path), or
//   - an opts.LiveMounts entry at "/workspace" or "/workspace/<name>" (the
//     worktree-sandbox live-virtiofs path built by herdrWorktreeSandbox).
//
// resolveAllowedBranches uses the result to derive a push allowlist from the
// worktree's own branch (TBD-1). Returns ok=false when no workspace is
// bound, in which case there is nothing to derive a branch from.
func hostWorkspacePath(opts CreateAndBootOptions) (path string, ok bool) {
	if opts.Workspace != nil && opts.Workspace.SourcePath != "" {
		return opts.Workspace.SourcePath, true
	}
	// Delegate to the exported single-implementation predicate so the
	// /workspace match string is defined exactly once (workspace.go).
	return WorkspaceMountHostPath(opts.LiveMounts)
}

// hostWorktreeBranch returns the branch currently checked out at repoPath.
// It fails (rather than guessing) on a detached HEAD, a repoPath that is not
// a git worktree, or any other condition that prevents git from resolving a
// symbolic ref — the caller (resolveAllowedBranches) must deny-closed on
// error, not substitute a default.
func hostWorktreeBranch(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("service: create-and-boot: resolve branch in %s: %w", repoPath, err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", fmt.Errorf("service: create-and-boot: resolve branch in %s: git returned an empty branch name", repoPath)
	}
	return branch, nil
}

// resolveAllowedBranches derives the Envelope.AllowedBranches value for a
// CreateAndBoot call (TBD-1: what should bound a sandbox's pushable
// branches once the nexus-only default no longer fits every repo).
//
// A caller-supplied opts.AllowedBranches always wins — it is an explicit
// override and is returned unchanged.
//
// Otherwise, when the sandbox has a workspace bound (hostWorkspacePath finds
// one), the sandbox is scoped to exactly that worktree's current branch: the
// sandbox exists to do work on that branch, so a single exact
// "refs/heads/<branch>" ref is both sufficient (AC-1: that branch can be
// pushed) and no wider than necessary (AC-2: the repository's default branch
// and any ref belonging to unrelated work are still denied — a single exact
// ref matches nothing else). A namespace-style "<branch>/**" pattern was
// considered and rejected: it would let a sandbox push siblings under its
// own branch's prefix that it never touched, which is exactly the kind of
// unrelated-ref widening AC-2 rules out.
//
// When a workspace IS bound but its branch cannot be derived (detached HEAD,
// git unavailable, unreadable worktree), this fails closed: it returns
// domain.UnresolvedBranchSentinel, a ref pattern that can never match a real
// push, rather than falling back to the nexus-only default (wrong for a
// non-nexus repo, and would incorrectly widen access) or to an empty slice
// (which Envelope.ResolvedAllowedBranches treats as "unset" and would apply
// that same wrong default).
//
// When NO workspace is bound at all, nil is returned unchanged: there is no
// worktree to derive a branch from, and this path is unrelated to the
// worktree-sandbox defect this function fixes. Envelope.ResolvedAllowedBranches
// applies its default at runtime in that case, same as before.
func resolveAllowedBranches(opts CreateAndBootOptions) []string {
	if len(opts.AllowedBranches) > 0 {
		return opts.AllowedBranches
	}
	hostPath, ok := hostWorkspacePath(opts)
	if !ok {
		return opts.AllowedBranches
	}
	branch, err := hostWorktreeBranch(hostPath)
	if err != nil {
		slog.Warn("service: create-and-boot: could not derive pushable branch from bound workspace; denying all pushes (D-PD-38 fail-closed)",
			"workspace", hostPath, "err", err)
		return []string{domain.UnresolvedBranchSentinel}
	}
	return []string{"refs/heads/" + branch}
}

// createSparseDisk creates a sparse raw disk image of the given size.
// os.Truncate creates a sparse (hole-punched) file on Linux — no host bytes
// are allocated until the guest actually writes. No filesystem is written;
// the in-guest agent reformats the device at every boot (D-SD-01).
func createSparseDisk(path string, sizeBytes int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err = f.Truncate(sizeBytes); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
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
