// Package supervisor implements the detached per-sandbox supervisor for
// nexus3's persistent-perimeter architecture.
//
// # Architecture
//
// Each orca sandbox has a corresponding supervisor process that:
//   - boots and owns the VM (via svc.Start → driver.Start),
//   - starts the network perimeter (gvproxy + MITM + netfilter) in-process,
//   - owns a long-lived credential Broker for host-side token injection,
//   - signals readiness by writing supervisor.pid (socket is bound earlier),
//   - blocks until SIGTERM or a /supervisor/stop IPC request.
//
// The supervisor is launched as a detached process (Setsid) by the spawning
// CLI so the perimeter survives after the spawning process exits. This is the
// core invariant: in-guest egress continues after `nexus3 orca create` returns.
//
// # Subcommand dispatch
//
// The hidden `nexus3 __supervisor` subcommand is dispatched in
// cmd/nexus3/main.go BEFORE the standard CLI routing, following the same
// pattern as old-nexus's `__detached-supervisor`. The subcommand parses flags
// and calls RunDetached.
//
// # IPC contract
//
// The supervisor exposes an HTTP server on supervisor.sock (Unix domain socket)
// with the following endpoints:
//
//   - POST /supervisor/stop  — request graceful shutdown; returns JSON {"ok":true}
//
// # Readiness signal
//
// The supervisor writes supervisor.pid AFTER the VM is running and the
// perimeter supervisor is active. The spawning CLI polls for this file to
// know the supervisor is ready (D-PP-01).
package supervisor

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/govern"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/statedir"
	"github.com/IniZio/nexus3/internal/core/store"
)

// HiddenSubcommand is the argv[1] token that runs a detached supervisor inside
// the nexus3 binary. It is handled before CLI routing in cmd/nexus3/main.go.
const HiddenSubcommand = "__supervisor"

// maxSeedAttempts is the upper bound on CA+agent seed attempts before the
// supervisor gives up and writes READY anyway. At 2 s/attempt this is ~60 s.
// The VM and perimeter are already live at that point so egress still works;
// only the in-guest placeholder and CA cert may be absent.
const maxSeedAttempts = 30

// Config carries all parameters needed to run a detached sandbox supervisor.
// All paths must be absolute.
type Config struct {
	// SandboxRef is the sandbox ID hex string or "<project>/<name>" handle.
	// The sandbox record must already exist in the store (State=Created or
	// State=Stopped). The supervisor calls svc.Start to boot it.
	SandboxRef string

	// StoreRoot is the FileStore root directory (the nexus3 state dir).
	StoreRoot string

	// StateDir is where supervisor.pid and supervisor.sock are written.
	// The directory is created if it does not exist.
	StateDir string

	// CHBin is the absolute path to the cloud-hypervisor binary.
	CHBin string

	// SocketDir is the directory for per-sandbox CH API and vsock sockets.
	// Must fit within the 107-byte Linux sun_path limit.
	SocketDir string

	// KernelPath is the absolute path to the guest kernel image.
	KernelPath string

	// DiskPath is the absolute path to the per-sandbox ext4 disk image.
	// Typically <diskDir>/<sandboxID>.raw created by CreateAndBoot.
	DiskPath string

	// ExtraDisks are the host filesystem paths of extra raw ext4 disk images to
	// re-attach at VM boot alongside the rootfs. The workspace disk (if any) is
	// at ExtraDisks[WorkspaceDiskIndex]. Paths are forwarded verbatim from the
	// caller to the supervisor subprocess via repeated --extra-disk flags.
	ExtraDisks []string

	// WorkspaceGuestPath is the in-guest mount point of the workspace disk
	// (e.g. /workspace/<name>). Empty when the sandbox has no workspace disk.
	// When non-empty AND the host workspace carried a .git, the supervisor
	// seeds the operator's git identity to GuestGitconfigPath (GIT-SEED,
	// D-PD-29) so in-guest commits carry the human's identity (ID-1).
	WorkspaceGuestPath string

	// CredsFile is the optional path to creds.json for real-token seeding.
	// When empty, the broker is wired but holds no real tokens.
	CredsFile string

	// MemoryMiB is the guest RAM in mebibytes. Defaults to 512 when zero.
	MemoryMiB uint32

	// BootVCPUs is the vCPU count the guest VM was started with.
	// It seeds the SandboxResizer's atomic vCPU counter so CurrentVCPUs returns
	// the correct value before the first resize call. When zero, the supervisor
	// applies the cloudhypervisor driver default (1 vCPU).
	BootVCPUs uint32

	// NestedVirt enables KVM nested virtualisation in the guest VM so the
	// guest can itself run hardware-accelerated VMs (e.g. `nexus3 create
	// --nested` inside a sandbox). The zero value (false) means nested-OFF.
	//
	// Security contract D-N3N-02: nested MUST be explicitly opt-in and
	// default-off at every hop. The CH driver sends CpusConfig.Nested=false
	// EXPLICITLY when this is false — it is never omitted — because CH v53
	// treats a missing Nested field as nested-ON by default. Absent or zero
	// at any point in the chain must mean nested-OFF, never nested-ON.
	NestedVirt bool

	// HasWorkspaceDisk indicates the supervisor's CHDriver was constructed with a
	// workspace disk in ExtraDisks. When false, the disk auto-resize axis is not
	// registered regardless of GovBounds.DiskMaxBytes — preventing GrowDisk from
	// targeting ExtraDisks[0] on a VM that has no workspace disk attached.
	HasWorkspaceDisk bool

	// WorkspaceDiskIndex is the 0-based ExtraDisks index of the workspace disk.
	// Meaningful only when HasWorkspaceDisk is true. Derive from the number of
	// shadow disks that precede the workspace disk (len(shadowSpecs)), NOT from a
	// hardcoded constant — a wrong index means resize2fs targets the wrong
	// filesystem, which is data loss, not a build failure.
	WorkspaceDiskIndex int

	// HasScratchDisk indicates a scratch disk is attached as the last ExtraDisk.
	// When true, the supervisor passes --scratch-disk-index to the in-guest init
	// so it can wipe and mount the device as /tmp (D-DC-32, D-SD-01).
	HasScratchDisk bool

	// ScratchDiskIndex is the 0-based ExtraDisks index of the scratch disk.
	// Meaningful only when HasScratchDisk is true. Always len(ExtraDisks)-1
	// at supervisor spawn — the scratch disk is always the last disk.
	// A wrong index means mkfs.ext4 targets the wrong device: data loss.
	ScratchDiskIndex int

	// ResizableDiskIndices lists the 0-based ExtraDisks indices whose ext4
	// filesystems the governor may auto-grow.  This is the generic replacement
	// for HasWorkspaceDisk/WorkspaceDiskIndex: when non-empty it takes
	// precedence and allows the governor to manage multiple disks per VM
	// (e.g. the workspace disk for orca sandboxes AND the buildkit cache disk
	// for builder VMs).  HasWorkspaceDisk/WorkspaceDiskIndex are kept for
	// backward compatibility; later slices will bridge the two representations
	// inside wireGovernorAxes.
	ResizableDiskIndices []int

	// CacheDiskSlots are the builder cache-disk slot image paths whose leases
	// this supervisor owns for the lifetime of its VM (D-HSH-07). Empty for
	// every non-builder sandbox.
	//
	// The lease used to be released by a `defer` in the CLI, so it was
	// CLI-scoped while cloud-hypervisor's write lock on the image is
	// VM-scoped. A VM that outlived its CLI left the slot reading free while
	// the image was still locked, and the next build failed to boot with an
	// opaque CH error. Ownership therefore belongs here, with the process
	// whose lifetime matches the VM's.
	CacheDiskSlots []string

	// CacheDiskLeaseFDs are inherited descriptors — one per CacheDiskSlots
	// entry, in the same order — that ALREADY hold the slot's flock, passed
	// down by the spawning CLI through exec.Cmd.ExtraFiles. Inheriting the
	// open file description means the lease is never momentarily free
	// between the selecting process and this one, so a concurrent build
	// cannot steal the slot mid-handoff.
	//
	// Empty on the adopt and re-acquire paths: there is no live sender to
	// pass a descriptor, so those paths take the slot by path instead
	// (builder.AcquireCacheDiskSlot), reading it from the sandbox record.
	CacheDiskLeaseFDs []int

	// GovBounds configures the auto-resize governor. When MemMinBytes or
	// MemMaxBytes is zero, the governor runs in passive mode (polls but
	// never resizes). The governor is single-tenant (D-DC-12) and lives
	// in the supervisor for the VM's full lifetime.
	GovBounds resize.Bounds

	// Cmdline is the full kernel command line to pass to cloud-hypervisor.
	// When empty, the driver uses its disk-boot default ("root=/dev/vda rw
	// init=/sbin/nexus3-agent console=ttyS0").
	//
	// The supervisor reboots the VM independently of the CLI process that ran
	// CreateAndBoot; it must carry the same cmdline that the CLI built, otherwise
	// the guest agent starts without --workspace-mount= args and disk telemetry
	// is permanently blind (selectWorkspaceMount returns ok=false, DiskSupported=false).
	//
	// Callers (buildOrcaSpawnConfig) are responsible for computing this cmdline
	// via workspaceMountCmdline + autoResizePID1Args before spawning the supervisor,
	// since the supervisor package cannot import internal/cli without a cycle.
	Cmdline string

	// LiveMounts are host-directory virtiofs shares that must be re-attached
	// each time the supervisor boots the VM (D-PD-53). Mirrors
	// cloudhypervisor.Config.LiveMounts. Must be set whenever the sandbox was
	// created with --mount; empty slice is safe when no mounts were requested.
	LiveMounts []domain.LiveMount

	// VirtiofsdPath is the absolute path to the virtiofsd 1.x binary.
	// Must be non-empty when LiveMounts is non-empty; ignored otherwise.
	VirtiofsdPath string

	// MCPOAuthRefreshConfigs carries the OAuth token material for each shared
	// HTTP MCP server that needs unattended token refresh. Set by the CLI at
	// create time via BuildMCPOAuthBinds; persisted in spawn.json so the
	// detached supervisor re-initialises the refreshers on every reboot.
	// Empty when no OAuth MCP servers are configured.
	MCPOAuthRefreshConfigs []service.MCPOAuthRefreshConfig

	// Ephemeral selects one-shot / builder mode. When true the supervisor is
	// expected to host a short-lived VM (e.g. an in-VM builder) and exit as
	// soon as the caller signals completion via POST /supervisor/stop. In
	// non-ephemeral (persistent perimeter) mode the stop verb is still
	// honoured, but the expected lifecycle is SIGTERM from the operator.
	//
	// Behavioural invariant: both modes respond to SIGTERM/SIGINT AND to the
	// /supervisor/stop IPC verb. The distinction is semantic (intended caller
	// and lifecycle) and expressed in the shutdown log message, not in the
	// control flow.
	//
	// Governor: the governor is started in both modes. With zero GovBounds the
	// govern.Run loop immediately returns ("bounds_not_configured"); no poll
	// loop spins. When GovBounds is non-zero auto-resize is active regardless
	// of Ephemeral — the builder VM gets the same always-on resize as a
	// persistent sandbox.
	Ephemeral bool

	// ParentPipeFD is a file descriptor number (≥ 3) for the read end of a
	// pipe whose write end is held by the spawning CLI process. Used only when
	// Ephemeral is true.
	//
	// When the CLI exits for any reason — including SIGKILL, which bypasses all
	// defers — the OS closes the write end, the supervisor reads EOF on this fd,
	// and cancels its context. This triggers the same graceful shutdown path as
	// SIGTERM: svc.Remove is called, the VM is stopped, and the detached process
	// exits. Without this mechanism a SIGKILL'ed CLI would orphan both the
	// supervisor process and the cloud-hypervisor child indefinitely.
	//
	// Zero means no watchdog pipe (non-ephemeral mode; persistent perimeter
	// supervisors are intentionally long-lived after CLI exit).
	ParentPipeFD int
}

// PidfilePath returns the canonical path of the supervisor.pid file.
func PidfilePath(stateDir string) string {
	return filepath.Join(stateDir, "supervisor.pid")
}

// SockPath returns the canonical path of the supervisor.sock file.
func SockPath(stateDir string) string {
	return filepath.Join(stateDir, "supervisor.sock")
}

// RunDetached runs the long-lived detached supervisor for a single sandbox.
// It is the entry point for `nexus3 __supervisor`. Blocks until SIGTERM,
// SIGINT, or a /supervisor/stop request is received, then stops the VM and
// returns.
//
// The function signals readiness (D-PP-01 §S1) by writing supervisor.pid to
// cfg.StateDir AFTER the VM is running and the perimeter is active.
// seedRoute is the routing decision for guest-credential seeding.
type seedRoute int

const (
	routeNone         seedRoute = iota // no MITM proxy: skip seeding entirely
	routeCombined                      // agent + human secrets: seedAgentAndHumanSecrets
	routeHumanSecrets                  // human/git secrets only: seedHumanSecrets
	routeAgent                         // agent-only (or no secrets): SeedLoop
)

// chooseSeedRoute is a pure function that decides which seeding path RunDetached
// takes for a given sandbox. Extracted so the decision is testable independently
// of RunDetached's I/O.
//
// Branch ordering is load-bearing: routeCombined MUST be evaluated before
// routeHumanSecrets so that an agent+secrets sandbox is never routed to the
// human-only path and silently loses its Claude credential.
func chooseSeedRoute(sb domain.Sandbox) seedRoute {
	if !service.SandboxHasMITMProxy(sb) {
		return routeNone
	}
	agentSandbox := sb.AgentName != ""
	humanSecrets := len(sb.Envelope.SecretHosts) > 0
	switch {
	case agentSandbox && humanSecrets:
		return routeCombined
	case humanSecrets:
		return routeHumanSecrets
	default:
		return routeAgent
	}
}

// seedRouteInputs bundles the already-constructed seeders and clients that
// runSeedRoute needs to dispatch to the right seeder without re-deriving them.
type seedRouteInputs struct {
	SB              domain.Sandbox
	Cert            *x509.Certificate
	CASeeder        service.GuestSeeder
	AgentSeeder     service.GuestSeeder
	// CredFileSeeder delivers the file-based credential payload (e.g. Cursor's
	// ~/.config/cursor/auth.json) to a profile-specific path inside the guest.
	// It must be bound to service.GuestCredFilePath(profile) via
	// service.NewGuestFileSeeder. Nil is safe: SeedGuestCredFile is a no-op
	// when seeder is nil or the profile has no CredentialFile.
	CredFileSeeder  service.GuestSeeder
	Broker          *cred.Broker
	Refreshers      []*cred.Refresher
	Svc             PerimeterCAGetter
	// StaticCredSrc is the credential source for file-based JWT agents (e.g.
	// cursor-agent). After seeding registers the placeholder, runSeedRoute calls
	// StaticCredSrc.Token() and broker.SetRealToken to wire the real token.
	// Nil for OAuth agents (Claude), which push via Refresher.ForcePush instead.
	StaticCredSrc   cred.CredentialSource
}

// Package-level function vars so tests can spy which seeder runSeedRoute
// invokes for a given route, following the same pattern as seedShellProfileFn.
//
// A test swaps one of these to a spy, calls runSeedRoute, and asserts the spy
// fired (or did not). Restoring after the test is the caller's responsibility.
var seedAgentAndHumanSecretsFn = seedAgentAndHumanSecrets
var seedHumanSecretsFn = seedHumanSecrets
var seedLoopFn = SeedLoop

// runSeedRoute dispatches to the seeder selected by route and returns
// (ok, guestEverResponded). For routeNone it logs and returns (false, false);
// RunDetached guards the post-seed checks with route != routeNone so the
// (false, false) return is never treated as a seed failure.
func runSeedRoute(ctx context.Context, route seedRoute, in seedRouteInputs) (ok, guestEverResponded bool) {
	switch route {
	case routeNone:
		slog.Info("supervisor.seed_not_applicable",
			"sandbox", in.SB.ID,
			"reason", "no MITM proxy for this sandbox: open egress, no secrets, no agent")
		return false, false
	case routeCombined:
		ok, guestEverResponded = seedAgentAndHumanSecretsFn(ctx, in.SB, in.Cert, in.CASeeder, in.AgentSeeder, in.Broker, in.Refreshers, in.Svc, resolveSeedProfile(in.SB), in.CredFileSeeder)
	case routeHumanSecrets:
		ok, guestEverResponded = seedHumanSecretsFn(ctx, in.SB, in.Cert, in.CASeeder, in.AgentSeeder, in.Broker, in.Svc)
	default: // routeAgent
		agentSandbox := in.SB.AgentName != ""
		ok, guestEverResponded = seedLoopFn(ctx, in.SB.ID, &in.Cert, in.CASeeder, in.AgentSeeder, in.Broker, in.Refreshers,
			maxSeedAttempts, 2*time.Second, in.Svc, agentSandbox, resolveSeedProfile(in.SB), in.CredFileSeeder)
	}
	// For file-based credential agents (e.g. cursor-agent), push the real token
	// after seeding registers the placeholder. Refreshers do this automatically
	// via ForcePush for OAuth agents; static JWT agents need an explicit push.
	if ok && in.StaticCredSrc != nil {
		profile := resolveSeedProfile(in.SB)
		if tok, _, tokErr := in.StaticCredSrc.Token(ctx); tokErr != nil {
			slog.Warn("supervisor.static_cred_token_failed",
				"host", profile.CredentialedHost, "err", tokErr)
		} else if setErr := in.Broker.SetRealToken(in.SB.ID, profile.CredentialedHost, tok); setErr != nil {
			slog.Warn("supervisor.static_cred_set_token_failed",
				"host", profile.CredentialedHost, "err", setErr)
		} else {
			slog.Info("supervisor.real_token_pushed",
				"host", profile.CredentialedHost, "sandbox", in.SB.ID)
		}
	}
	return
}

// resolveSeedProfile resolves the [cred.AgentProfile] the re-seed loop must
// use for sb, from the agent name persisted on the sandbox at creation
// (domain.Sandbox.AgentName). Falling back to [cred.ClaudeCodeProfile] for an
// empty or unregistered name matches the pre-existing behaviour for agent
// sandboxes created before per-agent profiles existed; the profile is only
// actually read by the caller when the route seeds agent credentials at all
// (seedAgentCreds / routeCombined), so this fallback is inert for sandboxes
// with no attached agent.
func resolveSeedProfile(sb domain.Sandbox) cred.AgentProfile {
	if profile, ok := cred.ProfileByName(sb.AgentName); ok {
		return profile
	}
	return cred.ClaudeCodeProfile
}

// buildSeedEgressOpts resolves the agent profile for sb, constructs the static
// credential source for file-backed agents (e.g. cursor-agent) via
// [cred.NewCredentialSourceForProfile], and wires both into the returned
// [service.CreateAndBootOptions] via [service.WireAgentEgress].
//
// For OAuth-backed profiles ([cred.ClaudeCodeProfile]) the returned
// AgentCredSource is nil; those agents push credentials via
// [cred.Refresher].ForcePush instead.
//
// The broker is threaded through WireAgentEgress. The seeder parameter is nil
// because the supervisor re-seeds an existing sandbox and does not use
// CreateAndBootOptions' Seeder field; only AgentCredSource and AgentProfile
// are consumed by the supervisor's seedRouteInputs.
func buildSeedEgressOpts(sb domain.Sandbox, broker *cred.Broker) (service.CreateAndBootOptions, error) {
	sbProfile := resolveSeedProfile(sb)
	src, err := cred.NewCredentialSourceForProfile(sbProfile)
	if err != nil {
		return service.CreateAndBootOptions{}, err
	}
	var opts service.CreateAndBootOptions
	service.WireAgentEgress(&opts, sbProfile, broker, nil, src)
	return opts, nil
}

// buildSeedRouteInputs assembles the [seedRouteInputs] from the already-resolved
// components. It is a pure constructor: no side effects, no RPCs. Extracted from
// [RunDetached] so that the StaticCredSrc assignment site — specifically the
// field assignment "StaticCredSrc: egressWire.AgentCredSource" — can be covered
// by a unit test (TestBuildSeedRouteInputs_WiresStaticCredSrc) without booting a VM.
func buildSeedRouteInputs(
	sb domain.Sandbox,
	cert *x509.Certificate,
	caSeeder service.GuestSeeder,
	agentSeeder service.GuestSeeder,
	agentClient *agent.Client,
	broker *cred.Broker,
	refreshers []*cred.Refresher,
	egressWire service.CreateAndBootOptions,
	svc PerimeterCAGetter,
) seedRouteInputs {
	return seedRouteInputs{
		SB:          sb,
		Cert:        cert,
		CASeeder:    caSeeder,
		AgentSeeder: agentSeeder,
		// CredFileSeeder is bound to the profile-specific path so
		// SeedGuestCredFile writes cursor/auth.json (or equivalent)
		// under GuestCredDirPath, where the redirected CredDirEnvVar
		// points. Claude Code (CredentialFile == "") ignores this seeder.
		CredFileSeeder: service.NewGuestFileSeeder(agentClient, service.GuestCredFilePath(resolveSeedProfile(sb))),
		Broker:         broker,
		Refreshers:     refreshers,
		StaticCredSrc:  egressWire.AgentCredSource,
		Svc:            svc,
	}
}

func RunDetached(cfg Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := statedir.Ensure(cfg.StateDir); err != nil {
		return fmt.Errorf("supervisor: mkdir state dir %s: %w", cfg.StateDir, err)
	}

	// ── 1. Open sandbox store ─────────────────────────────────────────────────
	st, err := store.NewFileStore(cfg.StoreRoot)
	if err != nil {
		return fmt.Errorf("supervisor: open store at %s: %w", cfg.StoreRoot, err)
	}

	// ── 1b. Take ownership of the builder cache-disk slot leases ─────────────
	// D-HSH-07: the lease must expire with the VM, not with the CLI that
	// selected the slot. On this path the CLI passed the already-locked
	// descriptors through ExtraFiles, so adopting them is instantaneous and
	// leaves no window in which the slot reads free. See cachedisk_lease.go.
	cacheLeases, err := acquireCacheDiskLeases(ctx, cfg.CacheDiskSlots, cfg.CacheDiskLeaseFDs, cacheDiskAdoptLeaseTimeout)
	if err != nil {
		return fmt.Errorf("supervisor: %w", err)
	}
	defer builder.ReleaseCacheDiskLeases(cacheLeases)

	// ── 2. Construct per-sandbox driver ───────────────────────────────────────
	extraDisks := make([]cloudhypervisor.ExtraDisk, 0, len(cfg.ExtraDisks))
	for _, p := range cfg.ExtraDisks {
		extraDisks = append(extraDisks, cloudhypervisor.ExtraDisk{Path: p})
	}
	// Derive MemoryMaxMiB and VCPUMax from GovBounds so the supervisor's boot
	// reserves the same VirtioMem hotplug region and vCPU headroom as the
	// initial CLI boot. Without these, MemoryMaxMiB=0 → no hotplug region →
	// vm.resize returns HTTP 204 but guest MemTotal never moves (Spike Leg 6).
	// VCPUMax=0 → CH starts with max_vcpus=BootVCPUs, blocking vCPU hotplug.
	var memMaxMiB uint32
	if cfg.GovBounds.MemMaxBytes > 0 {
		memMaxMiB = uint32(cfg.GovBounds.MemMaxBytes / (1024 * 1024)) //nolint:gosec // bytes→MiB; fits uint32 for any sane ceiling
	}
	vcpuMax := uint32(cfg.GovBounds.VCPUMax) //nolint:gosec // int32→uint32; VCPUMax is always non-negative by construction
	drv, err := cloudhypervisor.New(buildSupervisorDriverConfig(cfg, memMaxMiB, vcpuMax, extraDisks))
	if err != nil {
		return fmt.Errorf("supervisor: init driver: %w", err)
	}

	// ── 3. Build service with credential broker ───────────────────────────────
	svc := service.New(st, drv, lifecycle.New())
	broker := cred.NewBroker()
	svc = svc.WithBroker(broker)

	// Build Refresher-backed credential sources for the agent egress hosts
	// (api.anthropic.com, platform.claude.com). Each Refresher loads the
	// dedicated OAuth credential store; it maintains a live access token via
	// lockedToken + oauthRefreshBase under a cross-process flock, and pushes the
	// real token into broker via broker.SetRealToken whenever the access token
	// rotates.
	//
	// Graceful degradation: if the creds file is absent or unreadable the broker
	// starts with no real tokens and the perimeter still enforces network ACLs;
	// HTTPS auth headers will carry the placeholder (bearer will be invalid)
	// until the operator provisions the credential store.
	var refreshers []*cred.Refresher
	if cfg.CredsFile != "" {
		for _, host := range service.AgentEgressHosts(cred.ClaudeCodeProfile) {
			r, rErr := cred.NewRefresher(cfg.CredsFile, host, broker)
			if errors.Is(rErr, cred.ErrStoreAbsent) {
				slog.Info("supervisor.creds_absent", "path", cfg.CredsFile)
				break // same file for all hosts; no point trying others
			}
			if rErr != nil {
				slog.Warn("supervisor.refresher_init_failed", "host", host, "err", rErr)
				continue
			}
			refreshers = append(refreshers, r)
			slog.Info("supervisor.refresher_ready", "host", host, "path", cfg.CredsFile)
		}
	}

	// ── 3b. Resolve sandbox ID for agent client ───────────────────────────────
	// Resolve the sandbox record now (before VM boot) so we can construct the
	// agent client. agent.NewClient does not dial immediately; the vsock
	// connection is established on the first RPC, which happens after the VM is
	// running. The client is used in the background CA seeder goroutine (5b).
	//
	// Graceful degradation: if the sandbox cannot be resolved, CA seeding is
	// skipped; the perimeter still enforces egress ACLs.
	var agentClient *agent.Client
	preSB, resolveErr := st.ResolveByPrefix(ctx, cfg.SandboxRef)
	if resolveErr != nil {
		slog.Warn("supervisor.sandbox_resolve_failed", "ref", cfg.SandboxRef, "err", resolveErr)
	} else {
		// CHDriver implements driver.GuestDialer; pass drv directly.
		agentClient = agent.NewClient(drv, preSB.ID)
		slog.Info("supervisor.agent_client_ready", "sandboxID", preSB.ID)

		// Record which cache-disk slot(s) this VM occupies BEFORE it boots, so
		// a supervisor that dies mid-boot still leaves behind the one fact an
		// adopting or re-acquiring supervisor needs in order to take the SAME
		// slot back (D-HSH-07). Without this the replacement would select a
		// fresh slot and collide with the CH write lock the live VM still
		// holds on the old one.
		if len(cacheLeases) > 0 {
			slots := builder.EncodeCacheDiskSlots(builder.CacheDiskSlotPaths(cacheLeases))
			if setErr := svc.SetCacheDiskSlot(ctx, preSB.ID, slots); setErr != nil {
				return fmt.Errorf("supervisor: persist cache-disk slot: %w", setErr)
			}
		}
	}

	// Wire the static credential source for file-based agents (e.g. cursor-agent)
	// via buildSeedEgressOpts → WireAgentEgress so the profile-generic seam has a
	// live production caller (D-MAC-16). OAuth agents (ClaudeCodeProfile) get a
	// nil AgentCredSource; they push credentials via Refresher.ForcePush instead.
	// Graceful degradation: if the credential file is absent or unreadable,
	// AgentCredSource is nil and the broker starts with no real token (HTTPS auth
	// carries the placeholder; operator re-runs agent login to fix it).
	var egressWire service.CreateAndBootOptions
	if resolveErr == nil {
		if wired, wireErr := buildSeedEgressOpts(preSB, broker); wireErr != nil {
			slog.Warn("supervisor.static_cred_source_failed",
				"agent", resolveSeedProfile(preSB).Name, "err", wireErr)
		} else {
			egressWire = wired
		}
	}

	// ── 4. Bind IPC socket (before VM boot so early stop requests are handled) ─
	sockPath := SockPath(cfg.StateDir)
	_ = os.Remove(sockPath) // remove stale socket from a crash
	binaryHash, hashErr := computeBinaryHash()
	if hashErr != nil {
		// Non-fatal: `nexus3 supervisor-upgrade`'s "already on the current
		// binary" check degrades to "unknown, proceed" rather than blocking
		// every other IPC verb over a hash failure.
		slog.Warn("supervisor.binary_hash_failed", "err", hashErr)
	}
	// perimSupPtr is set after svc.Start returns and the perimeter supervisor is
	// live. The IPC egress-allow and /supervisor/handoff handlers read it
	// atomically; a nil load means the perimeter is not yet ready.
	var perimSupPtr atomic.Pointer[perimeter.PerimeterSupervisor]
	// sbPtr is set after svc.Start returns. The /supervisor/handoff handler
	// needs sb.ID's boot bounds for Payload.Governor; a nil load means the
	// sandbox is not yet running and handoff must refuse rather than offer a
	// payload describing a VM that does not exist yet.
	var sbPtr atomic.Pointer[domain.Sandbox]
	allowEgressFn := allowEgressFunc(func(host string) error {
		sup := perimSupPtr.Load()
		if sup == nil {
			return fmt.Errorf("perimeter not yet ready")
		}
		return sup.AllowEgress(host)
	})
	handoffFn := handoffFunc(func(hctx context.Context, peerSock string) (bool, string, error) {
		sup := perimSupPtr.Load()
		sb := sbPtr.Load()
		if sup == nil || sb == nil {
			return false, "perimeter not yet ready", nil
		}
		bootVCPUs := cfg.BootVCPUs
		if bootVCPUs == 0 {
			bootVCPUs = 1 // matches cloudhypervisor driver default
		}
		// Payload AND the "is CA mandatory" predicate both come from the LIVE
		// supervisor, never from the store record — see
		// [handoffFromLiveSupervisor] (ticket 14).
		return handoffFromLiveSupervisor(hctx, peerSock, sup, cfg.SandboxRef, bootVCPUs, cfg.MemoryMiB)
	})
	// agentHealthFn probes the guest agent's control/data planes live, using
	// the SAME drv this process dials every RPC through. resolveErr == nil is
	// required for preSB.ID to be meaningful; a failed resolve degrades to
	// AgentChannelUnknown (never to Healthy) rather than skipping the probe.
	agentHealthFn := agentHealthFunc(func(hctx context.Context) AgentHealth {
		if resolveErr != nil {
			return AgentHealth{State: AgentChannelUnknown, ControlErr: fmt.Sprintf("sandbox not resolved: %v", resolveErr)}
		}
		// drv (*cloudhypervisor.CHDriver) implements driver.GuestDialer
		// unconditionally (see ch_vsock.go's compile-time assertion) — no
		// comma-ok needed here, unlike agentClientFor's interface-typed driver.
		return checkAgentHealth(hctx, drv, preSB.ID)
	})
	ipcH, err := serveIPC(ctx, sockPath, svc, cfg.SandboxRef, allowEgressFn, handoffFn, agentHealthFn, binaryHash)
	if err != nil {
		return fmt.Errorf("supervisor: bind IPC socket %s: %w", sockPath, err)
	}
	stopCh := ipcH.StopCh
	detachCh := ipcH.DetachCh
	// removeOwnSocket (not a bare os.Remove) fixes D-HSH-09: a replacement
	// supervisor can rebind sockPath before this process's defers run, and an
	// unconditional Remove would unlink the replacement's freshly bound
	// socket instead of this process's now-stale one.
	defer removeOwnSocket(sockPath, ipcH.BindStat)

	// ── 4b. Parent watchdog (ephemeral mode only) ─────────────────────────────
	// In ephemeral (builder) mode the supervisor is expected to exit when the
	// CLI sends POST /supervisor/stop. If the CLI is SIGKILL'd mid-build, no
	// defers run — neither the CLI's st.Delete nor its Stop IPC call. Without a
	// watchdog the supervisor (and its cloud-hypervisor child) run forever.
	//
	// Fix: SpawnDetached passes the read end of a pipe to the supervisor via fd
	// ParentPipeFD; the CLI holds the write end. When the CLI process exits for
	// any reason, the OS closes the write end and the supervisor reads EOF here.
	// EOF triggers cancel(), which causes awaitShutdown to return
	// shutdownBySignal and the normal graceful-shutdown path to execute.
	if cfg.Ephemeral && cfg.ParentPipeFD > 0 {
		pipeR := os.NewFile(uintptr(cfg.ParentPipeFD), "parent-watchdog-pipe") //nolint:gosec // fd provided by SpawnDetached; range-checked by caller
		startParentWatchdog(pipeR, cfg.SandboxRef, cancel)
	}

	// ── 5. Boot VM + start in-process perimeter ───────────────────────────────
	//
	// IMPORTANT: use the long-lived supervisor ctx, NOT a cancellable
	// sub-context. StartNetnsRuntime uses exec.CommandContext(ctx, nexus3Binary)
	// for the netns child that hosts cloud-hypervisor. If that context is
	// cancelled (e.g. a 5m boot-timeout sub-ctx cancelled right after Start
	// returns), the goroutine installed by exec.CommandContext wakes up and
	// kills the netns child — taking the VM and vsock socket with it. The
	// CHDriver already has its own per-call StartTimeout for the API-socket
	// readiness poll; SpawnDetached's ReadyTimeout gates the integration test.
	slog.Info("supervisor.starting", "sandboxRef", cfg.SandboxRef)
	sb, err := svc.Start(ctx, cfg.SandboxRef)
	if err != nil {
		return fmt.Errorf("supervisor: start sandbox %s: %w", cfg.SandboxRef, err)
	}
	slog.Info("supervisor.vm_running", "sandboxRef", cfg.SandboxRef)
	sbPtr.Store(&sb)

	// Wire the perimeter supervisor into the IPC egress-allow handler. The
	// supervisor was created inside svc.Start; retrieve it via GetPerimeterSupervisor.
	// nil means AllowAll / no-perimeter mode — AllowEgress handles that case.
	if sup := svc.GetPerimeterSupervisor(sb.ID); sup != nil {
		perimSupPtr.Store(sup)
	}

	// ── 5a. Start auto-resize governor ───────────────────────────────────────
	// The governor is single-tenant (D-DC-12): one per supervisor, for this
	// sandbox's full lifetime. It polls the guest over vsock (TelemetryVsockPort
	// 3002) and calls ResizeMemory when the memory control law warrants it.
	// Host-headroom admission control (fail-conservative) prevents the governor
	// from amplifying host OOM during nested builds.
	//
	// bootVCPUs: apply the driver default (1) when the field is unset so that
	// SandboxResizer.CurrentVCPUs() reflects the actual running count before the
	// first resize call. Without this the CPU axis seeds currentVCPUs to 0 and
	// the atomic accounting is wrong even though cpu.go:135 papers over it by
	// falling back to minVCPUs.
	bootVCPUs := int32(cfg.BootVCPUs) //nolint:gosec // uint32→int32; vCPU counts always fit int32
	if bootVCPUs == 0 {
		bootVCPUs = 1 // matches cloudhypervisor driver: Config.VCPUs=0 → 1 vCPU
	}
	resizer := cloudhypervisor.NewSandboxResizer(drv, sb.ID, cfg.GovBounds, int64(cfg.MemoryMiB)*1024*1024, bootVCPUs)
	gov := govern.New(govern.Config{
		Resizer:   resizer,
		Telemetry: govern.NewVsockTelemetry(drv, sb.ID),
		Bounds:    cfg.GovBounds,
	})
	// Effective disk indices: prefer ResizableDiskIndices when non-empty;
	// fall back to the legacy HasWorkspaceDisk/WorkspaceDiskIndex pair.
	diskIndices := cfg.ResizableDiskIndices
	if len(diskIndices) == 0 && cfg.HasWorkspaceDisk {
		diskIndices = []int{cfg.WorkspaceDiskIndex}
	}
	wireGovernorAxes(gov, resizer, resizer, cfg.GovBounds, diskIndices)
	go gov.Run(ctx)

	// ── 5b-mcp. Wire MCP OAuth Refreshers ───────────────────────────────────
	// Build and register per-server OAuth refreshers for shared HTTP MCP
	// servers. Uses the same Broker and lifecycle as the Anthropic refreshers;
	// the returned []*cred.Refresher are appended to refreshers so they ride
	// the same Register / ticker / ForcePush path below.
	//
	// Unlike the Anthropic refresher, MCP OAuth tokens cannot flow through
	// ResolveEnvelopeSecrets: the bind's Env name is a synthetic placeholder
	// (e.g. NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION) that is not set in the
	// detached supervisor's host environment, so the bind is silently dropped
	// before applySecrets can call RegisterPlaceholder. We register the broker
	// scope here directly from the refresh config so that:
	//   (a) ForcePush / SetRealToken succeeds — the scope (sandboxID, host)
	//       must exist in the broker before step 5d calls r.ForcePush, and
	//   (b) the MITM proxy can resolve the broker placeholder to the real token.
	// mcpOAuthSeedMap is serverName→placeholder hex for each successfully
	// registered MCP OAuth scope. It is consumed in step 5d to seed the guest
	// env so the MCP client header expands to "Bearer <placeholder>".
	var mcpOAuthSeedMap map[string]string
	if len(cfg.MCPOAuthRefreshConfigs) > 0 {
		mcpOAuthSeedMap = registerMCPOAuthPlaceholders(broker, sb.ID, cfg.MCPOAuthRefreshConfigs)
		mcpStoreRoot := service.DefaultMCPOAuthStoreRoot()
		mcpRefreshers, mcpErr := service.StartMCPOAuthRefreshers(ctx, broker, mcpStoreRoot, cfg.MCPOAuthRefreshConfigs)
		if mcpErr != nil {
			slog.Warn("supervisor.mcp_oauth_refreshers_failed", "err", mcpErr)
		} else {
			for _, r := range mcpRefreshers {
				slog.Info("supervisor.mcp_oauth_refresher_ready", "host", r.Host())
			}
			refreshers = append(refreshers, mcpRefreshers...)
		}
	}

	// ── 5b. Wire Refreshers to the running sandbox ───────────────────────────
	// Register each Refresher with the sandbox so its Token() call can invoke
	// broker.SetRealToken(sb.ID, host, realToken) after seeding mints the
	// placeholder. For the Anthropic refresher, RegisterPlaceholder is
	// delegated to SeedGuestAgent in step 5d so the guest and the broker always
	// hold the same placeholder. For MCP OAuth refreshers, RegisterPlaceholder
	// is called above in step 5b-mcp (the envelope-secret path cannot supply
	// the token). The initial real-token push happens after seeding (step 5d).
	//
	// Graceful degradation: failures are logged but do not stop the supervisor.
	for _, r := range refreshers {
		r.Register(sb.ID)
		slog.Info("supervisor.refresher_registered", "host", r.Host(), "path", cfg.CredsFile)
	}

	// ── 5c. Background token refresh ─────────────────────────────────────────
	// Poll each Refresher every minute; lockedToken re-uses the in-process
	// cachedTok when the token is not near expiry and only calls oauthRefreshBase
	// (an HTTP round-trip) when within refreshExpiryDelta of expiry. Token() may
	// return an error if SeedGuestAgent has not yet run (scope not yet registered
	// in the broker); the goroutine logs and retries each tick.
	for _, r := range refreshers {
		go func(r *cred.Refresher) {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, _, tokErr := r.Token(ctx); tokErr != nil {
						slog.Warn("supervisor.token_refresh_failed", "host", r.Host(), "err", tokErr)
					}
				}
			}
		}(r)
	}

	// ── 5d. Seed MITM CA cert + agent placeholder creds (before READY) ──────
	// Seed (a) the MITM CA certificate into the guest trust store and (b) the
	// agent placeholder payload (CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_AUTH_TOKEN
	// + NODE_EXTRA_CA_CERTS + CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC) so
	// Node.js tools (claude) trust the proxy and send the placeholder in their
	// Authorization header.
	//
	// D-PP-04 zero-cred-in-guest: SeedGuestAgent writes ONLY the placeholder
	// string minted by broker.RegisterPlaceholder — the real bearer token is
	// held exclusively in the in-process Broker and is structurally unreachable
	// from the seed payload.
	//
	// The pidfile (READY signal) is written AFTER seeding (or after the attempt
	// cap is exhausted — see maxSeedAttempts) so that a spawning CLI that sees
	// READY can rely on the trust anchor and placeholder being in the guest.
	// Retry with 2-second backoff; the guest agent may not respond immediately
	// after driver.Start returns.
	if agentClient != nil {
		// ── 5d-probe + shell-profile seed (D-J14 / D-J11) ───────────────────
		// probeAndSeedGuest runs the unconditional liveness gate (30 s window)
		// and the login-shell credential drop-in, both extracted into a testable
		// helper so deleting either call turns the unit suite RED (D-M4).
		profileSeeder := service.NewGuestFileSeeder(agentClient, service.GuestShellProfilePath)
		onboardingExecer := service.NewAgentExecer(agentClient)
		// Resolve the guest project directory: the first live-mount guest path,
		// then the first mounted-volume guest path, then "" (no projects entry).
		// This mirrors herdrShellCwd's priority and ensures the onboarding seed
		// records the directory where the operator's source is mounted.
		projectDir := ""
		for _, m := range sb.LiveMounts {
			if m.GuestPath != "" {
				projectDir = m.GuestPath
				break
			}
		}
		if projectDir == "" {
			for _, v := range sb.MountedVolumes {
				if v.GuestPath != "" {
					projectDir = v.GuestPath
					break
				}
			}
		}
		// A-MOUNT: detect the agentcfg lower dir by its well-known guest path.
		// If the sandbox was created with --agent and sharing enabled, the CLI
		// staged curated host config into a per-sandbox dir and added it as a RO
		// live mount at this path. The supervisor re-attaches it on every reboot;
		// probeAndSeedGuest mounts the overlay before any other seed writes.
		agentCfgLowerGuestPath := ""
		var mcpServersForSeed map[string]json.RawMessage
		var userMountsForSeed *service.UserMountManifest
		for _, lm := range cfg.LiveMounts {
			if lm.GuestPath == "/run/nexus3/agentcfg-lower" {
				agentCfgLowerGuestPath = lm.GuestPath
				// Read the MCP servers map staged by cmd_sandbox into the same
				// host-side config dir. Absent or malformed file is a silent skip:
				// the overlay/onboarding still succeed; only MCP injection is skipped.
				if data, readErr := os.ReadFile(filepath.Join(lm.HostPath, "mcp-servers.json")); readErr == nil {
					var m map[string]json.RawMessage
					if json.Unmarshal(data, &m) == nil {
						mcpServersForSeed = m
					}
				}
				// Read the user-mounts manifest staged alongside mcp-servers.json.
				// Absent or malformed file is a silent skip.
				if data, readErr := os.ReadFile(filepath.Join(lm.HostPath, "usermounts.json")); readErr == nil {
					var m service.UserMountManifest
					if json.Unmarshal(data, &m) == nil {
						userMountsForSeed = &m
					}
				}
				break
			}
		}
		seedInputs := guestSeedInputs{
			ID:                     sb.ID,
			Labels:                 sb.Labels,
			ProjectDir:             projectDir,
			SourcePaths:            service.SourceGuestPaths(cfg.WorkspaceGuestPath, sb.LiveMounts),
			ProfileSeeder:          profileSeeder,
			GitSeeder:              service.NewGuestFileSeeder(agentClient, service.GuestGitconfigPath),
			CredentialHelperSeeder: service.NewGuestFileSeeder(agentClient, service.GuestGitCredentialHelperPath),
			Execer:                 onboardingExecer,
			AgentCfgLowerGuestPath: agentCfgLowerGuestPath,
			MCPServers:             mcpServersForSeed,
			UserMounts:             userMountsForSeed,
			// IsHumanGitVM: true for human git-VM sandboxes (no agent). Enables
			// the SSH→HTTPS remote rewrite in probeAndSeedGuest so "git push"
			// routes through the MITM proxy on this boot and every restart.
			IsHumanGitVM: sb.AgentName == "",
		}
		if checkErr := probeAndSeedGuest(ctx, agentClient, seedInputs); checkErr != nil {
			slog.Error("supervisor.guest_agent_unreachable",
				"err", checkErr,
				"action", "refusing READY; sandbox unusable without a reachable guest agent")
			writeFailureReason(cfg.StateDir, checkErr)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer stopCancel()
			if cfg.Ephemeral {
				_ = svc.Remove(stopCtx, cfg.SandboxRef)
			} else {
				_, _ = svc.Stop(stopCtx, cfg.SandboxRef)
			}
			return checkErr
		}

		caSeeder := service.NewAgentCACopySeeder(agentClient)
		agentSeeder := service.NewAgentCopySeeder(agentClient)
		// M2c seed: append NEXUS3_MCP_<SERVER>_AUTHORIZATION=Bearer <placeholder>
		// lines for each MCP OAuth server registered in step 5b-mcp. The "Bearer "
		// prefix is required so the expanded MCP header value is exactly
		// "Bearer <placeholder>", matching swapAuthorization's input format.
		// Real tokens are never written; the broker holds them host-side.
		if len(mcpOAuthSeedMap) > 0 {
			agentSeeder = wrapSeederWithExtra(agentSeeder, buildMCPOAuthCredPayload(mcpOAuthSeedMap))
		}
		cert := svc.GetPerimeterCACert(sb.ID)
		// D-PD-33 / D-PD-36: seed human secrets whenever SecretHosts is non-empty.
		// OpenEgress=true (human open-egress) AND OpenEgress=false (--egress closed)
		// both carry SecretHosts that require MITM credential swap; the supervisor
		// must mint placeholders in either posture. The original guard on OpenEgress
		// silently skipped GH_TOKEN seeding for --egress closed sandboxes.
		route := chooseSeedRoute(sb)
		seedDone, guestEverResponded := runSeedRoute(ctx, route, buildSeedRouteInputs(
			sb, cert, caSeeder, agentSeeder, agentClient, broker, refreshers, egressWire, svc,
		))
		// routeNone skips both branches below: there is no seed failure to warn
		// about, and no CA in the guest to activate.
		if route != routeNone && !seedDone {
			if ctx.Err() != nil {
				slog.Warn("supervisor.seed_skipped", "reason", "context cancelled before seeding complete")
			} else if !guestEverResponded {
				// The guest agent was reachable at the probe (above) but did not
				// complete a single CA-seed round-trip across all seeding attempts
				// — most likely the guest died during init. The sandbox is
				// structurally unusable (D-J14).
				slog.Error("supervisor.guest_died_during_seeding",
					"max_attempts", maxSeedAttempts,
					"action", "refusing READY; guest was alive at probe but died before seeding completed")
				failErr := fmt.Errorf("supervisor: guest agent stopped responding during seeding after %d attempts: sandbox unusable", maxSeedAttempts)
				writeFailureReason(cfg.StateDir, failErr)
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer stopCancel()
				if cfg.Ephemeral {
					_ = svc.Remove(stopCtx, cfg.SandboxRef)
				} else {
					_, _ = svc.Stop(stopCtx, cfg.SandboxRef)
				}
				return failErr
			} else {
				// Guest was alive (at least one CA seed succeeded) but the full
				// seed did not complete — e.g. a specific secret write failed.
				// Perimeter is live; the sandbox is usable for some things.
				// This degradation is deliberate (D-J14: preserve existing behaviour).
				slog.Warn("supervisor.seed_cap_exhausted",
					"max_attempts", maxSeedAttempts,
					"action", "writing READY anyway; perimeter live, guest may lack placeholder+CA")
			}
		} else if route != routeNone {
			// Activate the CA cert in the system trust store so that non-Node.js
			// HTTPS clients (git, wget, curl, gh) trust the MITM proxy CA without
			// explicit per-process configuration.
			ucCtx, ucCancel := context.WithTimeout(ctx, 30*time.Second)
			defer ucCancel()
			if _, ucErr := agentClient.Exec(ucCtx, agent.ExecOptions{
				Argv: []string{"/usr/sbin/update-ca-certificates"},
			}); ucErr != nil {
				slog.Warn("supervisor.update_ca_certs_failed", "err", ucErr)
			} else {
				slog.Info("supervisor.update_ca_certs_done")
			}
		}
	}

	// ── 6. Write pidfile (READY signal) ──────────────────────────────────────
	pid := os.Getpid()
	pidfile := PidfilePath(cfg.StateDir)
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)+"\n"), statedir.FileMode); err != nil {
		return fmt.Errorf("supervisor: write pidfile %s: %w", pidfile, err)
	}
	// removeOwnPidfile mirrors the inode-checked removeOwnSocket: a
	// replacement supervisor may write its own PID to the same path before
	// this process's defers run. Read the file at cleanup time and only
	// unlink when it still names our own PID — not the replacement's.
	defer func() {
		data, readErr := os.ReadFile(pidfile)
		if readErr != nil {
			return // already gone
		}
		if !bytes.Equal(bytes.TrimRight(data, "\n"), []byte(strconv.Itoa(pid))) {
			// A replacement supervisor has already written its own PID.
			// Removing the file here would destroy its READY signal.
			slog.Debug("supervisor.pidfile_not_ours", "sandboxRef", cfg.SandboxRef,
				"pidfile", pidfile, "action", "skip removal")
			return
		}
		_ = os.Remove(pidfile)
	}()

	slog.Info("supervisor.ready",
		"sandboxRef", cfg.SandboxRef,
		"pid", pid,
		"sock", sockPath,
	)

	// ── 7. Block until shutdown ───────────────────────────────────────────────
	// Wire the VM-death channel: closed by watchParentOwnedDeath /
	// watchAdoptedDeath in ch_netns.go when the netns child exits. A nil
	// channel (returned when no runtime is registered yet) is safe — nil is
	// never ready in a select.
	vmDeadCh := drv.RuntimeDeathCh(sb.ID)
	cause := awaitShutdown(ctx, stopCh, detachCh, vmDeadCh)
	switch {
	case cfg.Ephemeral && cause == shutdownByStopVerb:
		// Builder finished: the caller sent POST /supervisor/stop to signal
		// that the build is complete. This is the normal exit path in ephemeral
		// mode — not an emergency stop.
		slog.Info("supervisor.build_complete", "sandboxRef", cfg.SandboxRef)
	case cause == shutdownByStopVerb:
		slog.Info("supervisor.stop_requested", "sandboxRef", cfg.SandboxRef)
	case cause == shutdownByDetach:
		slog.Info("supervisor.detach_requested", "sandboxRef", cfg.SandboxRef)
	case cause == shutdownByVMDeath:
		slog.Warn("supervisor.vm_died", "sandboxRef", cfg.SandboxRef)
	default:
		slog.Info("supervisor.signal_received", "sandboxRef", cfg.SandboxRef)
	}

	// shutdownByDetach: exit WITHOUT tearing the VM down. This is the entire
	// point of /supervisor/detach and a confirmed /supervisor/handoff — the VM
	// and perimeter must keep running for a replacement supervisor to adopt.
	// Deliberately returns before the UNI-TEARDOWN block below: svc.Stop and
	// svc.Remove both call driver.Stop, which is exactly what must NOT happen
	// here. Only defers already registered above (pidfile, IPC socket via
	// removeOwnSocket, signal context cancel) run on the way out.
	if cause == shutdownByDetach {
		slog.Info("supervisor.detached", "sandboxRef", cfg.SandboxRef,
			"action", "VM and perimeter left running for a replacement supervisor")
		return nil
	}

	// shutdownByVMDeath: the netns child exited unexpectedly — the VM is
	// already gone. Reconcile the store record to Stopped/MemoryLost so that
	// `nexus3 sandbox list` and `nexus3 recover` see the honest state rather
	// than a forever-running ghost. Skip the UNI-TEARDOWN below (svc.Stop /
	// svc.Remove) — calling driver.Stop on a dead pgid is a no-op but would
	// overwrite StopReason with "clean". Defers (pidfile, socket, ctx cancel)
	// still run on the way out.
	if cause == shutdownByVMDeath {
		slog.Warn("supervisor.vm_died", "sandboxRef", cfg.SandboxRef)
		reconCtx, reconCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer reconCancel()
		if err := reconcileVMDeath(reconCtx, st, sb.ID); err != nil {
			slog.Warn("supervisor.vm_died_record_update_failed",
				"sandboxRef", cfg.SandboxRef, "err", err)
		}
		slog.Info("supervisor.exited", "sandboxRef", cfg.SandboxRef, "cause", "vm_died")
		return nil
	}

	// ── 8. Graceful shutdown ──────────────────────────────────────────────────
	// # Teardown ordering — UNI-TEARDOWN
	//
	// This is the single authoritative teardown site for the supervisor. See
	// docs/teardown-ordering.md for the full analysis; the invariants are:
	//
	//   Ephemeral (builder) mode:
	//     svc.Remove is called instead of svc.Stop. Remove: (a) stops the VM
	//     under the per-sandbox flock, (b) deletes the transient __builder
	//     store record. This means the record is cleaned up even when the CLI
	//     was SIGKILL'd and its defer st.Delete never ran.
	//     If the record was already deleted by the CLI's defer (normal path),
	//     Remove's inner st.Delete gets ErrNotFound and returns an error here;
	//     the VM has already been stopped by the CHDriver call inside
	//     store.Update, which succeeded before Delete tried to clean up.
	//     The error is logged and the supervisor exits cleanly.
	//
	//   Persistent (orca) mode:
	//     svc.Stop leaves the record alive (State=Stopped) so that
	//     `nexus3 sandbox list` continues to show the sandbox.
	//
	//   Flock invariant:
	//     Both svc.Stop and svc.Remove call driver.Stop inside store.Update,
	//     which holds the per-sandbox exclusive flock. If the user concurrently
	//     runs `nexus3 sandbox stop <id>`, the CLI's service.Stop also acquires
	//     the flock via store.Update. Only one call proceeds; the other either
	//     waits or — if the record is already Stopped — hits the lifecycle fast-
	//     path rejection before touching the driver. CHDriver.Stop is idempotent
	//     (absent VM is not an error), so the worst outcome is a logged warning.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if cfg.Ephemeral {
		// Ephemeral: remove the transient __builder record AND stop the VM.
		// ErrNotFound on the record is expected when the CLI's defer already
		// deleted it; svc.Remove still called driver.Stop inside store.Update
		// before the delete failed, so the VM is already stopped.
		if removeErr := svc.Remove(stopCtx, cfg.SandboxRef); removeErr != nil {
			slog.Warn("supervisor.remove_failed", "sandboxRef", cfg.SandboxRef, "err", removeErr)
		}
	} else {
		if _, stopErr := svc.Stop(stopCtx, cfg.SandboxRef); stopErr != nil {
			slog.Warn("supervisor.stop_failed", "sandboxRef", cfg.SandboxRef, "err", stopErr)
		}
	}
	slog.Info("supervisor.exited", "sandboxRef", cfg.SandboxRef)
	return nil
}

// shutdownCause identifies how RunDetached was asked to exit.
type shutdownCause int

const (
	// shutdownBySignal means SIGTERM or SIGINT cancelled the signal context.
	shutdownBySignal shutdownCause = iota
	// shutdownByStopVerb means a POST /supervisor/stop IPC request closed
	// stopCh. In ephemeral mode this is the normal "build finished" path.
	// Stop means "tear the VM down" — see ipcStopPath's doc comment.
	shutdownByStopVerb
	// shutdownByDetach means a POST /supervisor/detach request, or a
	// POST /supervisor/handoff request whose replacement confirmed, closed
	// detachCh. Detach means "exit WITHOUT tearing the VM down" — the VM and
	// perimeter are left running for a replacement supervisor to adopt. This
	// must never be treated the same as shutdownByStopVerb; RunDetached's
	// teardown switch skips svc.Stop/svc.Remove entirely for this cause.
	shutdownByDetach
	// shutdownByVMDeath means the netns child exited unexpectedly — the VM
	// died without an operator stop request. The supervisor must reconcile
	// the store record to Stopped/MemoryLost and exit without calling
	// svc.Stop (the VM is already gone; calling driver.Stop on a dead pgid
	// is a no-op but would overwrite StopReason with "clean"). This cause
	// is distinct from shutdownBySignal so the teardown switch can skip the
	// UNI-TEARDOWN driver call entirely and write the honest reason instead.
	shutdownByVMDeath
)

// awaitShutdown blocks until the OS signal context is cancelled (SIGTERM /
// SIGINT), a /supervisor/stop IPC request closes stopCh, a
// /supervisor/detach (or confirmed /supervisor/handoff) request closes
// detachCh, or the netns child exits (vmDeadCh). It is extracted from
// RunDetached for unit-testability.
//
// detachCh and vmDeadCh may be nil (a nil channel never becomes ready in a
// select, so either degrades gracefully for callers that don't need them).
func awaitShutdown(ctx context.Context, stopCh, detachCh, vmDeadCh <-chan struct{}) shutdownCause {
	select {
	case <-stopCh:
		return shutdownByStopVerb
	case <-detachCh:
		return shutdownByDetach
	case <-vmDeadCh:
		return shutdownByVMDeath
	case <-ctx.Done():
		return shutdownBySignal
	}
}

// vmDeathReconciler is the subset of store.Store used by reconcileVMDeath.
// A narrow interface keeps reconcileVMDeath unit-testable without a full
// store.Store fake implementation in tests.
type vmDeathReconciler interface {
	Update(ctx context.Context, id domain.SandboxID, fn func(*domain.Sandbox) error) error
}

// reconcileVMDeath writes State=Stopped/StopReasonMemoryLost to the store for
// a sandbox whose VM died unexpectedly, and clears the netns adoption fields
// so that a future AdoptNetnsRuntime cannot target a recycled pid group.
//
// It is extracted from RunDetached (and RunAdopt) so that tests can drive it
// directly against a fake, proving the body itself — not a hand-copy of it.
// The call site in RunDetached is not reached from unit tests (booting a VM
// is required); that gap is acknowledged and accepted.
func reconcileVMDeath(ctx context.Context, r vmDeathReconciler, id domain.SandboxID) error {
	return r.Update(ctx, id, func(rec *domain.Sandbox) error {
		rec.State = domain.Stopped
		rec.StopReason = domain.StopReasonMemoryLost
		// Clear netns adoption fields: a stale record must not cause a
		// future AdoptNetnsRuntime to target a recycled pid group.
		rec.NetnsChildPID = 0
		rec.NetnsChildPGID = 0
		rec.NetnsChildStartTime = 0
		rec.GuestTapName = ""
		rec.CHAPISocket = ""
		return nil
	})
}

// wireGovernorAxes attaches the CPU and disk AxisEvaluators to gov based on
// bounds and disk configuration. The memory axis is always wired by govern.New;
// this function adds only CPU and disk. Must be called before gov.Run.
//
// CPU axis: registered when both VCPUMin and VCPUMax are non-zero. A partial
// bounds (min-only or max-only) leaves the axis unregistered — the axis itself
// also guards on the bounds, but skipping registration avoids polling overhead.
//
// Disk axis: registered for each index in diskIndices when DiskMaxBytes > 0.
// diskIndices must contain only indices present in the supervisor's CHDriver
// ExtraDisks list. A wrong diskIndex causes GrowDisk to truncate the wrong
// backing file — data loss, not a build failure. Default-off (diskIndices nil or empty)
// is the safe configuration when no workspace disks are attached.
func wireGovernorAxes(
	gov *govern.Governor,
	cpuR resize.CPUResizer,
	diskR resize.DiskResizer,
	bounds resize.Bounds,
	diskIndices []int,
) {
	if bounds.VCPUMin != 0 && bounds.VCPUMax != 0 {
		govern.NewCPUAxis(gov, cpuR)
	}
	if bounds.DiskMaxBytes != 0 {
		for _, idx := range diskIndices {
			govern.NewDiskAxis(gov, diskR, idx)
		}
	}
}

// supervisorErrFile is the filename written by RunDetached when it refuses
// READY. SpawnDetached reads it to surface the reason in the spawner's error.
const supervisorErrFile = "supervisor.err"

// writeFailureReason writes err.Error() to cfg.StateDir/supervisor.err so that
// SpawnDetached can surface the supervisor's reason in the spawner error.
// Errors are silently ignored — this is best-effort diagnostics.
func writeFailureReason(stateDir string, err error) {
	path := filepath.Join(stateDir, supervisorErrFile)
	_ = os.WriteFile(path, []byte(err.Error()), statedir.FileMode)
}

// GuestProber is the subset of *agent.Client needed by ProbeGuestAgent.
// Extracted as an interface so the probe logic is testable without a live VM.
type GuestProber interface {
	Ping(ctx context.Context) error
}

// ProbeGuestAgent retries prober.Ping at retryDelay intervals until ctx expires
// or Ping returns nil. Returns nil on first success, ctx.Err() on timeout.
// Exported so unit tests can verify both dead-guest and alive-guest behaviour
// without starting a VM (D-J14).
func ProbeGuestAgent(ctx context.Context, prober GuestProber, retryDelay time.Duration) error {
	for {
		if err := prober.Ping(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay):
		}
	}
}

// seedShellProfileFn is the function called by probeAndSeedGuest to write the
// login-shell credential drop-in. Default is service.SeedGuestShellProfile;
// tests replace it with a spy to verify the call without a live VM (D-M4).
var seedShellProfileFn = service.SeedGuestShellProfile

// seedAgentOnboardingFn is the function called by probeAndSeedGuest to write
// the claude CLI onboarding state. Default is service.SeedGuestAgentOnboarding;
// tests replace it with a spy to verify the call without a live VM (D-J10).
var seedAgentOnboardingFn = service.SeedGuestAgentOnboarding

// seedBypassConsentFn is the function called by probeAndSeedGuest to seed the
// bypass-permissions consent state into ~/.claude/settings.json. Default is
// service.SeedGuestBypassConsent; tests replace it with a spy (D-J12 mutation guard).
var seedBypassConsentFn = service.SeedGuestBypassConsent

// seedUserMountsFn is the function called by probeAndSeedGuest to apply the
// operator tool-dir overlay mounts and home symlink inside the guest. Default
// is service.SeedGuestUserMounts; tests replace it with a spy.
var seedUserMountsFn = service.SeedGuestUserMounts

// seedOverlayClaudeConfigFn is the function called by probeAndSeedGuest to
// mount a writable overlay onto /root/.claude. Default is seedOverlayClaudeConfig;
// tests replace it with a spy to verify the call without a live VM (A-MOUNT).
var seedOverlayClaudeConfigFn = seedOverlayClaudeConfig

// agentCfgUpperDir is the persistent overlayfs upper dir for the /root/.claude
// overlay. It lives on a sandbox-scoped named ext4 volume (mounted at
// /var/lib/nexus3/agentcfg by herdrWorktreeSandboxCreateArgs) so it is
// governor-visible and can grow when Claude session state fills the upper
// layer. Only removed when the sandbox itself is removed.
const agentCfgUpperDir = "/var/lib/nexus3/agentcfg/upper"

// agentCfgWorkDir is the overlayfs work dir for the /root/.claude overlay.
// The kernel requires upper and work to share ONE filesystem — both live on
// the named ext4 volume mounted at /var/lib/nexus3/agentcfg, satisfying that
// constraint without pinning either to the root ext4 disk. It must be empty
// at mount time. It is recreated fresh on every boot — it holds only
// kernel-internal overlayfs state, not user data.
const agentCfgWorkDir = "/var/lib/nexus3/agentcfg/work"

// agentCfgMountedMarker is a volume-independent sentinel written on the root
// ext4 disk by Branch 1 of seedOverlayClaudeConfig on every successful named-
// volume mount. Branch 3 reads it to distinguish "attach failure for a sandbox
// that has successfully mounted before" from "brand-new sandbox without a
// volume" — the former is a hard error; the latter is a configuration defect
// (D-RAM-09 must have failed to provision).
const agentCfgMountedMarker = "/var/lib/nexus3/.agentcfg-mounted"

// errAgentCfgDegraded is returned by seedOverlayClaudeConfig when Branch 2
// fires: the named volume is absent but pre-existing data was found on the
// root ext4 disk (D-RAM-11). The caller emits a structured slog.Warn so an
// operator can list affected sandboxes; boot continues. No non-destructive
// drain exists: a real one requires an API to add a volume attachment to an
// existing sandbox record, which does not exist today (D-RAM-15).
var errAgentCfgDegraded = errors.New("agentcfg: degraded to root ext4 (D-RAM-11)")

// seedOverlayClaudeConfig mounts a writable overlay onto /root/.claude in the
// guest. lowerGuestPath is the guest path of the RO virtiofs share (the curated
// host config staged by AssembleCuratedConfig). The upper dir lives on a named
// ext4 volume (agentCfgUpperDir) so Claude session transcripts, todos, and
// stats written by the in-guest agent survive sandbox stop/start and
// crash+recover, and the governor can grow the volume if it fills.
// The work dir (agentCfgWorkDir) is recreated empty on every boot — overlayfs
// requires it empty at mount time and uses it only for internal kernel state.
//
// The named volume is provisioned on ALL bootable sandbox create paths
// (D-RAM-09), so this function always runs against a governor-visible disk.
//
// Must be the FIRST seed step so onboarding writes (seedAgentOnboarding,
// seedBypassConsent) land in the upper layer rather than failing against the
// RO lower.
func seedOverlayClaudeConfig(ctx context.Context, id domain.SandboxID, lowerGuestPath string, execer service.GuestExecer) error {
	// Use bash; /bin/sh in the base image is dash which does not support pipefail.
	// Three-way guard (D-RAM-08 / D-RAM-11):
	//   Branch 1: named volume mounted → happy path; migrate legacy data if present.
	//   Branch 2: volume absent but pre-existing data at old/root path → degrade
	//             gracefully so pre-existing sandboxes keep their Claude session
	//             state. Mounting from root ext4 violates D-RAM-08's memory-safety
	//             goal but is preferable to losing user data on restart (D-RAM-11).
	//   Branch 3: volume absent and no prior data → fail closed. This is a new
	//             sandbox; the named volume must have been provisioned at create time
	//             (D-RAM-09). Any other outcome is a configuration defect.
	script := fmt.Sprintf(`set -eu
mkdir -p /root/.claude
# D-RAM-08: detect whether the named ext4 volume is mounted at
# /var/lib/nexus3/agentcfg. Use stat device-number comparison — more portable
# than mountpoint(1), which may be absent in the base image.
_mp_dev=$(stat -c '%%d' /var/lib/nexus3/agentcfg 2>/dev/null) || _mp_dev=""
_par_dev=$(stat -c '%%d' /var/lib/nexus3 2>/dev/null) || { echo 'agentcfg: stat /var/lib/nexus3 failed' >&2; exit 1; }
if [ -n "$_mp_dev" ] && [ "$_mp_dev" != "$_par_dev" ]; then
    # Branch 1: named volume mounted — happy path.
    mkdir -p %s
    # D-RAM-09 one-shot migration: move legacy agentcfg-upper (root ext4) into
    # the governor-visible named volume. Idempotent: old dir is removed after
    # copy+sync so subsequent boots skip this block.
    if [ -d /var/lib/nexus3/agentcfg-upper ]; then
        cp -a /var/lib/nexus3/agentcfg-upper/. %s/
        sync
        rm -rf /var/lib/nexus3/agentcfg-upper
    fi
    # Work dir must be empty at mount time (overlayfs kernel-internal state).
    rm -rf %s
    mkdir %s
    mount -t overlay overlay -o lowerdir=%s,upperdir=%s,workdir=%s /root/.claude
    # D-RAM-13: write a volume-independent marker on the root disk so Branch 3
    # can distinguish attach-failure (marker present) from a new sandbox (absent).
    touch %s
elif [ -d /var/lib/nexus3/agentcfg-upper ] || [ -d %s ]; then
    # Branch 2: volume absent but pre-existing data found — degrade to root ext4.
    # Pre-existing sandboxes created before D-RAM-09 have no named volume; losing
    # their /root/.claude overlay on restart would silently strand session state.
    if [ -d /var/lib/nexus3/agentcfg-upper ]; then
        _fb_upper=/var/lib/nexus3/agentcfg-upper
    else
        _fb_upper=%s
    fi
    _fb_work=/var/lib/nexus3/agentcfg-work
    rm -rf "$_fb_work"
    mkdir -p "$_fb_upper" "$_fb_work"
    echo "agentcfg: named volume absent; degrading to root ext4 at $_fb_upper (D-RAM-11)" >&2
    mount -t overlay overlay -o lowerdir=%s,upperdir="$_fb_upper",workdir="$_fb_work" /root/.claude
    exit 2
else
    # Branch 3: volume absent, no prior data.
    if [ -f %s ]; then
        echo 'agentcfg: named volume was previously mounted but is now absent — attach failed; refusing to boot without named volume' >&2
    else
        echo 'agentcfg: /var/lib/nexus3/agentcfg is not a mountpoint — named volume not attached; refusing to fall back to root ext4' >&2
    fi
    exit 1
fi
`, agentCfgUpperDir, agentCfgUpperDir, agentCfgWorkDir, agentCfgWorkDir, lowerGuestPath, agentCfgUpperDir, agentCfgWorkDir, agentCfgMountedMarker, agentCfgUpperDir, agentCfgUpperDir, lowerGuestPath, agentCfgMountedMarker)
	code, err := execer(ctx, id, []string{"/bin/bash", "-c", script}, nil)
	if err != nil {
		return fmt.Errorf("overlay mount: %w", err)
	}
	switch code {
	case 0:
		return nil
	case 2:
		// Branch 2: degraded to root ext4 — non-fatal; caller emits slog.Warn.
		return errAgentCfgDegraded
	default:
		return fmt.Errorf("overlay mount script exited %d", code)
	}
}

// seedGitIdentityFn is the function called by probeAndSeedGuest to write the
// guest gitconfig (operator identity, safe.directory for every source path,
// per-sandbox branch). Default is service.SeedGitIdentity; tests replace it
// with a spy (D-J13 mutation guard).
var seedGitIdentityFn = service.SeedGitIdentity

// seedGitCredentialHelperFn is the function called by probeAndSeedGuest to
// write the credential-helper script to GuestGitCredentialHelperPath.
// Default is service.SeedGitCredentialHelper; tests replace it with a spy.
var seedGitCredentialHelperFn = service.SeedGitCredentialHelper

// guestSeedInputs carries everything probeAndSeedGuest writes into a freshly
// booted guest. It is a struct rather than a parameter list because the seeds
// are an open-ended set — five already, each with its own path, payload and
// seeder — and every addition was otherwise widening the signature at both the
// call site and every test.
type guestSeedInputs struct {
	ID                     domain.SandboxID
	Labels                 map[string]string
	ProjectDir             string   // guest dir the agent works in; "" when nothing is mounted
	SourcePaths            []string // every guest dir holding source, for safe.directory
	ProfileSeeder          service.GuestSeeder
	GitSeeder              service.GuestSeeder
	CredentialHelperSeeder service.GuestSeeder
	Execer                 service.GuestExecer
	// AgentCfgLowerGuestPath is the guest path of the RO virtiofs share
	// that backs the /root/.claude overlay (A-MOUNT). Empty when sharing is
	// disabled or the sandbox carries no agentcfg live mount.
	AgentCfgLowerGuestPath string
	// MCPServers is the sanitized mcpServers map to merge into the guest's
	// /root/.claude.json. Read from mcp-servers.json inside the agentcfg
	// staging dir written by cmd_sandbox at create time. Nil when sharing is
	// disabled, no servers are configured, or the file is absent.
	MCPServers map[string]json.RawMessage
	// UserMounts is the manifest of operator tool dirs to seed into the guest.
	// Read from usermounts.json inside the agentcfg staging dir. Nil when
	// sharing is disabled or the file is absent.
	UserMounts *service.UserMountManifest
	// IsHumanGitVM is true when the sandbox is the human git-VM (AgentName ==
	// ""). When set, probeAndSeedGuest rewrites the "origin" remote of the
	// workspace from SSH form to HTTPS form so that "git push" routes through
	// the MITM proxy, which intercepts HTTPS traffic only.
	IsHumanGitVM bool
}

// probeAndSeedGuest runs the liveness probe (D-J14), login-shell credential
// drop-in seed (D-J11), and claude onboarding seed (D-J10) for a newly-booted
// guest. Returns non-nil only when the probe fails (guest unreachable);
// shell-profile and onboarding seed failures are non-fatal.
//
// Extracted from RunDetached for unit-testability: tests exercise this function
// directly with stub probers instead of launching a real VM (D-M4 mutation guard).
// RunDetached calls this immediately after VM boot; on non-nil return the caller
// must tear down the VM and return the error so supervisor.pid is never written.
func probeAndSeedGuest(ctx context.Context, prober GuestProber, in guestSeedInputs) error {
	id := in.ID
	const probeTimeout = 30 * time.Second
	probeCtx, probeCancel := context.WithTimeout(ctx, probeTimeout)
	probeErr := ProbeGuestAgent(probeCtx, prober, 500*time.Millisecond)
	probeCancel()
	if probeErr != nil {
		return fmt.Errorf("supervisor: guest agent unreachable after %s: %w", probeTimeout, probeErr)
	}

	// A-MOUNT overlay setup (FIRST seed step). Establishes a writable overlayfs
	// on /root/.claude before any other seed writes so that seedAgentOnboarding
	// and seedBypassConsent land in the tmpfs upper layer. Without this ordering
	// those seeds would either fail (writing to the RO lower) or be lost on
	// sandbox exit.
	if in.AgentCfgLowerGuestPath != "" {
		ovlErr := seedOverlayClaudeConfigFn(ctx, id, in.AgentCfgLowerGuestPath, in.Execer)
		switch {
		case ovlErr == nil:
			slog.Info("supervisor.overlay_claude_config_seeded", "sandbox", id, "lower", in.AgentCfgLowerGuestPath)
		case errors.Is(ovlErr, errAgentCfgDegraded):
			// D-RAM-11 Branch 2: named volume absent; degraded to root ext4.
			// Non-fatal: pre-existing session state is preserved on the root disk.
			// No non-destructive drain exists: a real one requires an API to add a
			// volume attachment to an existing sandbox record, which does not exist
			// today (D-RAM-15).
			slog.Warn("supervisor.agentcfg_degraded",
				"sandbox", id,
				"action", "agentcfg volume absent; overlayfs on root ext4 (D-RAM-11); no non-destructive drain exists (D-RAM-15)")
		default:
			// D-RAM-13: Branch 3 or attach error — fail closed, boot aborts.
			return fmt.Errorf("supervisor: agentcfg overlay mount failed (fail-closed): %w", ovlErr)
		}
	}

	// Login-shell credential drop-in (D-J11). Written unconditionally before the
	// proxy/seeding split so whether a shell picks up a credential does not depend
	// on which seeding branch this sandbox takes. The drop-in carries no credential
	// itself; it is harmless when cred.env never arrives (no MITM proxy).
	if profErr := seedShellProfileFn(ctx, id, in.ProfileSeeder); profErr != nil {
		slog.Warn("supervisor.shell_profile_seed_failed",
			"sandbox", id, "path", service.GuestShellProfilePath, "err", profErr,
			"action", "interactively started agents in this guest will lack credentials")
	} else {
		slog.Info("supervisor.shell_profile_seeded",
			"sandbox", id, "path", service.GuestShellProfilePath)
	}

	// Claude CLI onboarding seed (D-J10). Writes ~/.claude.json so an
	// interactively started claude skips the theme-picker, login, and
	// folder-trust wizards and reaches its prompt directly. Also carries the
	// mcpServers map (host-side Go merge, no in-guest node dependency) so
	// shared host MCP server definitions are immediately available. Non-fatal:
	// the agent still works without the seed, it just opens first-run wizards
	// and loses shared MCP server definitions.
	if onbErr := seedAgentOnboardingFn(ctx, id, in.ProjectDir, in.MCPServers, in.Execer); onbErr != nil {
		slog.Warn("supervisor.agent_onboarding_seed_failed",
			"sandbox", id, "path", service.GuestAgentOnboardingPath, "err", onbErr,
			"action", "interactively started agent will open first-run wizards and shared MCP servers will be absent")
	} else {
		slog.Info("supervisor.agent_onboarding_seeded",
			"sandbox", id, "path", service.GuestAgentOnboardingPath,
			"mcp_servers_count", len(in.MCPServers))
	}

	// MCP servers are written host-side inside seedAgentOnboardingFn as part of
	// the initial /root/.claude.json payload. Log when servers were requested so
	// the supervisor trace shows the intent.
	if len(in.MCPServers) > 0 {
		slog.Info("supervisor.mcp_servers_included_in_onboarding",
			"sandbox", id, "count", len(in.MCPServers))
	}

	// User-mount seed (usermount-guest-seed): home symlink, PATH drop-in, and
	// overlay mounts for operator tool dirs (from user-global config).
	// Must run AFTER the /root/.claude overlay (A-MOUNT) so the plugins overlay
	// layers on top of the already-mounted /root/.claude overlayfs. Non-fatal.
	if in.UserMounts != nil {
		if umErr := seedUserMountsFn(ctx, id, *in.UserMounts, in.Execer); umErr != nil {
			slog.Warn("supervisor.usermount_seed_failed",
				"sandbox", id, "err", umErr,
				"action", "guest will not see operator tool dirs or home symlink")
		} else {
			slog.Info("supervisor.usermount_seeded", "sandbox", id, "count", len(in.UserMounts.Mounts))
		}
	}

	// Bypass-permissions consent seed (D-J12). skipDangerousModePermissionPrompt
	// must reach ~/.claude/settings.json so the shell-function `claude` (which
	// always adds --dangerously-skip-permissions) does not stall on the consent
	// wizard. Two paths:
	//
	//  • Sharing ON (AgentCfgLowerGuestPath != ""): AssembleCuratedConfig already
	//    injected the key into the staged lower settings.json, which the overlay
	//    presents as the effective file. Writing an upper-layer file here would
	//    shadow the ENTIRE lower settings.json, silently dropping enabledPlugins
	//    and extraKnownMarketplaces (overlayfs is file-granular, not key-granular).
	//    Skip the upper write; the lower layer is sufficient.
	//
	//  • Sharing OFF (AgentCfgLowerGuestPath == ""): no lower settings.json
	//    exists, so the upper write is the only source of the key and is required.
	if in.AgentCfgLowerGuestPath == "" {
		if bypassErr := seedBypassConsentFn(ctx, id, in.Execer); bypassErr != nil {
			slog.Warn("supervisor.bypass_consent_seed_failed",
				"sandbox", id, "err", bypassErr,
				"action", "guest claude will stop on bypass-permissions consent dialog")
		} else {
			slog.Info("supervisor.bypass_consent_seeded", "sandbox", id)
		}
	} else {
		slog.Info("supervisor.bypass_consent_in_lower_layer",
			"sandbox", id, "lower", in.AgentCfgLowerGuestPath,
			"action", "skipDangerousModePermissionPrompt carried in staged settings.json; no upper write needed")
	}
	// Guest gitconfig (D-PD-29 + safe.directory). Seeded here, unconditionally,
	// rather than on the human-secrets branch it used to live on.
	//
	// That gating was wrong twice over. It keyed the gitconfig off
	// `len(sb.Envelope.SecretHosts) > 0` — whether the sandbox holds a PUSH
	// CREDENTIAL — but the gitconfig answers two entirely different questions:
	// can git read this directory at all (safe.directory), and whose name is on
	// a commit (identity). Neither depends on being able to push. The visible
	// symptom was that every `--agent` sandbox, which is exactly what this
	// motive creates, failed `git log` in its own mounted source with "detected
	// dubious ownership". It also sat inside the proxy-dependent seeding block,
	// so an open-egress sandbox with no MITM proxy skipped it too.
	//
	// Safe to write unconditionally: the payload carries the operator's
	// name/email and nothing else — no token, no key, no credential path. See
	// the security invariant on service.SeedGitIdentity.
	if len(in.SourcePaths) > 0 {
		// Credential-helper script: must be seeded before the gitconfig,
		// because the gitconfig references the script by path
		// (helper = !sh /usr/local/bin/nexus3-git-credential).
		// Non-fatal: git push will fail with "credential helper not found"
		// rather than with a sandbox-fatal error.
		if helperErr := seedGitCredentialHelperFn(ctx, id, in.CredentialHelperSeeder); helperErr != nil {
			slog.Warn("supervisor.git_credential_helper_seed_failed",
				"sandbox", id, "path", service.GuestGitCredentialHelperPath, "err", helperErr,
				"action", "in-guest git push will fail; credential helper script not present")
		} else {
			slog.Info("supervisor.git_credential_helper_seeded",
				"sandbox", id, "path", service.GuestGitCredentialHelperPath)
		}
		if _, gitErr := seedGitIdentityFn(ctx, id, in.Labels, in.SourcePaths, in.GitSeeder); gitErr != nil {
			slog.Warn("supervisor.git_identity_seed_failed",
				"sandbox", id, "err", gitErr,
				"action", "in-guest git will report dubious ownership and commits will lack an identity; "+
					"configure host git user.name/user.email")
		} else {
			slog.Info("supervisor.git_identity_seeded",
				"sandbox", id, "path", service.GuestGitconfigPath, "sources", in.SourcePaths)
		}
		// Remote rewrite (SSH→HTTPS): human git-VM only. Rewrites the "origin"
		// remote from SSH form (git@ or ssh://) to HTTPS so that "git push"
		// travels through the MITM proxy, which intercepts HTTPS traffic only.
		// Co-located with git identity seeding so both git configuration steps
		// happen in one place. Non-fatal: a missing or already-HTTPS remote is
		// silently accepted; a rewrite failure logs and continues boot.
		if in.IsHumanGitVM {
			if remErr := service.SeedGitRemoteHTTPS(ctx, id, in.SourcePaths[0], in.Execer); remErr != nil {
				slog.Warn("supervisor.git_remote_https_rewrite_failed",
					"sandbox", id, "path", in.SourcePaths[0], "err", remErr,
					"action", "git push may not route through the MITM proxy")
			} else {
				slog.Info("supervisor.git_remote_https_rewritten",
					"sandbox", id, "path", in.SourcePaths[0])
			}
		}
	}

	return nil
}

// PerimeterCAGetter is the subset of *service.Service needed by SeedLoop to
// fetch the MITM CA certificate. Nil is permitted; if nil, GetPerimeterCACert
// is never called (the caller must pre-populate cert).
type PerimeterCAGetter interface {
	GetPerimeterCACert(id domain.SandboxID) *x509.Certificate
}

// seedAgentAndHumanSecrets seeds MITM CA + both agent credential vars and human
// secret placeholders (e.g. GH_TOKEN) for a sandbox that has BOTH an attached
// agent and secret hosts. It composes both credential sets into one payload via
// [service.SeedGuestAgentAndSecrets] to prevent a second write from silently
// overwriting the first. After a successful seed it re-pushes real tokens to all
// refreshers, mirroring the behaviour of [SeedLoop] on the agent path.
func seedAgentAndHumanSecrets(
	ctx context.Context,
	sb domain.Sandbox,
	cert *x509.Certificate,
	caSeeder, credSeeder service.GuestSeeder,
	broker *cred.Broker,
	refreshers []*cred.Refresher,
	svc PerimeterCAGetter,
	profile cred.AgentProfile,
	credFileSeeder service.GuestSeeder,
) (ok bool, guestEverResponded bool) {
	for attempt := range maxSeedAttempts {
		if ctx.Err() != nil {
			return false, guestEverResponded
		}
		if cert == nil && svc != nil {
			cert = svc.GetPerimeterCACert(sb.ID)
		}
		if cert != nil {
			if caErr := service.SeedCA(ctx, cert, sb.ID, caSeeder); caErr != nil {
				slog.Debug("supervisor.seed_ca_retry", "attempt", attempt, "err", caErr)
			} else {
				guestEverResponded = true
				records, combErr := service.SeedGuestAgentAndSecretsForProfile(ctx, broker, sb.ID, sb.Envelope.SecretSpecs, credSeeder, profile)
				if combErr == nil {
					combErr = service.SeedGuestCredFile(ctx, sb.ID, records, profile, credFileSeeder)
				}
				if combErr != nil {
					slog.Debug("supervisor.seed_combined_retry", "attempt", attempt, "err", combErr)
				} else {
					slog.Info("supervisor.agent_and_secrets_complete", "sandbox", sb.ID,
						"secret_hosts", sb.Envelope.SecretHosts)
					// ForcePush re-pushes the real token to the placeholder freshly
					// minted by SeedGuestAgentAndSecrets. A plain Token() call would
					// skip the push because lastToken has not changed since the
					// previous rotation; ForcePush bypasses that guard.
					//
					// READY is correct even when a post-seed push fails. The sandbox
					// is running with egress live, and the failure is recorded in
					// lastPushErrs so the next Token tick repairs it automatically
					// (see refresher.go ForcePush and vend). Blocking READY on push
					// outcome would delay sandbox availability indefinitely on
					// transient broker errors.
					for _, r := range refreshers {
						if fpErr := r.ForcePush(ctx, sb.ID); fpErr != nil {
							slog.Warn("supervisor.post_seed_token_push_failed",
								"host", r.Host(), "err", fpErr)
						} else {
							slog.Info("supervisor.real_token_pushed",
								"host", r.Host(), "sandbox", sb.ID)
						}
					}
					return true, true
				}
			}
		}
		select {
		case <-ctx.Done():
			return false, guestEverResponded
		case <-time.After(2 * time.Second):
		}
	}
	return false, guestEverResponded
}

// SeedLoop attempts up to maxAttempts rounds of CA + agent-placeholder seeding.
// It returns (ok, guestEverResponded): ok is true only when all seeds succeed;
// guestEverResponded is true when the guest agent answered at least one CA-seed
// round-trip (caErr == nil ≥ once) even if the full seed never completed.
// Callers must distinguish the two failure modes: a dead guest (guestEverResponded
// false) is a hard failure; a partially-seeded live guest is a soft degradation.
// It is called from RunDetached and also exported for testing so the bounded-
// retry behaviour can be verified without booting a real VM.
//
// On success it re-pushes the real token from each Refresher to the new
// placeholder minted by SeedGuestAgent, then returns true.
// On cap exhaustion or ctx cancellation it returns false — the caller writes
// READY anyway (perimeter is live, guest may lack placeholder/CA).
//
// cert is a pointer-to-pointer so SeedLoop can refresh the CA cert on each
// attempt if it was nil at call time (requires svc != nil for that path).
// startParentWatchdog starts a goroutine that blocks on the read end of the
// parent-watchdog pipe. When the write end closes (because the CLI parent
// exited for any reason, including SIGKILL), the goroutine reads EOF and calls
// cancel, which causes awaitShutdown to return shutdownBySignal and the
// graceful-shutdown path to execute.
//
// Only called when cfg.Ephemeral && cfg.ParentPipeFD > 0. The persistent
// supervisor deliberately has no parent watchdog because it is meant to
// outlive the CLI that spawned it.
func startParentWatchdog(pipeR *os.File, sandboxRef string, cancel context.CancelFunc) {
	go func() {
		defer pipeR.Close()
		buf := make([]byte, 1)
		_, _ = pipeR.Read(buf) // blocks until CLI closes write end or dies
		slog.Info("supervisor.parent_pipe_closed", "sandboxRef", sandboxRef)
		cancel() // trigger awaitShutdown → svc.Remove shutdown path
	}()
}

// registerMCPOAuthPlaceholders registers a broker placeholder for each
// MCPOAuthRefreshConfig so that (a) ForcePush / SetRealToken can update the
// scope after seeding, and (b) the MITM proxy can resolve the placeholder to
// the real token. Configs with an empty AccessToken or Host are logged and
// skipped; all others are registered unconditionally (RegisterPlaceholder is
// idempotent: a second call for the same scope replaces the old placeholder).
// registerMCPOAuthPlaceholders registers a broker placeholder for each
// MCPOAuthRefreshConfig so that (a) ForcePush / SetRealToken can update the
// scope after seeding, and (b) the MITM proxy can resolve the placeholder to
// the real token. Configs with an empty AccessToken or Host are logged and
// skipped; all others are registered unconditionally (RegisterPlaceholder is
// idempotent: a second call for the same scope replaces the old placeholder).
//
// Returns a serverName→placeholder hex map for the successfully registered
// servers; callers pass this to buildMCPOAuthCredPayload to seed the guest env.
func registerMCPOAuthPlaceholders(broker *cred.Broker, sandboxID domain.SandboxID, configs []service.MCPOAuthRefreshConfig) map[string]string {
	seeds := make(map[string]string, len(configs))
	for _, cfg := range configs {
		if cfg.AccessToken == "" || cfg.Host == "" {
			slog.Warn("supervisor.mcp_oauth_placeholder_skip",
				"server", cfg.ServerName, "reason", "empty access_token or host")
			continue
		}
		rec, err := broker.RegisterPlaceholder(sandboxID, cfg.Host, cfg.AccessToken)
		if err != nil {
			slog.Warn("supervisor.mcp_oauth_placeholder_failed",
				"host", cfg.Host, "server", cfg.ServerName, "err", err)
			continue
		}
		slog.Info("supervisor.mcp_oauth_placeholder_registered",
			"host", cfg.Host, "server", cfg.ServerName)
		seeds[cfg.ServerName] = rec.Placeholder
	}
	return seeds
}

// buildMCPOAuthCredPayload builds KEY=Bearer <placeholder> lines for each
// MCP OAuth server whose placeholder was minted by registerMCPOAuthPlaceholders.
// The "Bearer " prefix is included so the expanded MCP header value
// (Authorization: ${NEXUS3_MCP_<SERVER>_AUTHORIZATION}) is exactly
// "Bearer <placeholder>", matching the format swapAuthorization expects in the
// MITM proxy. Real tokens are never present; the broker holds them host-side.
//
// The value is single-quoted because it is the only cred.env value that
// contains a space ("Bearer <placeholder>"). GuestCredEnvPath is consumed by
// POSIX `. file` sourcing in BOTH launchCredSourcedArgv and
// guestShellProfileScript; an unquoted `KEY=Bearer <hex>` line is parsed as the
// assignment `KEY=Bearer` followed by the command `<hex>`, so the variable is
// never exported and the agent sends an empty Authorization header (Linear 401).
// The placeholder hex is [0-9a-f]+ and never contains a single quote, so
// single-quoting is a complete, injection-safe escaping.
func buildMCPOAuthCredPayload(seeds map[string]string) []byte {
	if len(seeds) == 0 {
		return nil
	}
	names := make([]string, 0, len(seeds))
	for name := range seeds {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, serverName := range names {
		ph := seeds[serverName]
		envVar := service.MCPOAuthVarName(serverName, "Authorization")
		fmt.Fprintf(&buf, "%s='Bearer %s'\n", envVar, ph)
	}
	return buf.Bytes()
}

// wrapSeederWithExtra wraps base to append extra bytes to every payload before
// writing. It is a no-op when extra is empty. Used to inject MCP OAuth
// placeholder env vars (step 5d) into the agent credential seed payload.
func wrapSeederWithExtra(base service.GuestSeeder, extra []byte) service.GuestSeeder {
	if len(extra) == 0 {
		return base
	}
	return func(ctx context.Context, id domain.SandboxID, payload []byte) error {
		combined := make([]byte, len(payload)+len(extra))
		copy(combined, payload)
		copy(combined[len(payload):], extra)
		return base(ctx, id, combined)
	}
}

func SeedLoop(
	ctx context.Context,
	id domain.SandboxID,
	cert **x509.Certificate,
	caSeeder service.GuestSeeder,
	agentSeeder service.GuestSeeder,
	broker *cred.Broker,
	refreshers []*cred.Refresher,
	maxAttempts int,
	retryDelay time.Duration,
	svc PerimeterCAGetter,
	seedAgentCreds bool,
	profile cred.AgentProfile,
	credFileSeeder service.GuestSeeder,
) (ok bool, guestEverResponded bool) {
	for attempt := range maxAttempts {
		if ctx.Err() != nil {
			return false, guestEverResponded
		}
		if *cert == nil && svc != nil {
			*cert = svc.GetPerimeterCACert(id)
		}
		if *cert != nil {
			caErr := service.SeedCA(ctx, *cert, id, caSeeder)
			if caErr == nil {
				// At least one vsock round-trip to the guest agent succeeded.
				guestEverResponded = true
			}
			var agentErr error
			if caErr == nil && seedAgentCreds {
				var records []cred.PlaceholderRecord
				records, agentErr = service.SeedGuestAgentForProfile(ctx, broker, id, agentSeeder, profile)
				if agentErr == nil {
					agentErr = service.SeedGuestCredFile(ctx, id, records, profile, credFileSeeder)
				}
			}
			if caErr == nil && agentErr == nil {
				slog.Info("supervisor.seeds_complete", "sandbox", id,
					"cert_path", service.GuestCACertPath,
					"env_path", service.GuestCredEnvPath)
				// ForcePush re-pushes the real token to the placeholder freshly
				// minted by SeedGuestAgent (which revoked the previous placeholder
				// and minted a new one for the same scope). A plain Token() call
				// would skip the push because lastToken has not changed since the
				// previous rotation; ForcePush bypasses that guard.
				//
				// READY is correct even when a post-seed push fails. The sandbox
				// is running with egress live, and the failure is recorded in
				// lastPushErrs so the next Token tick repairs it automatically
				// (see refresher.go ForcePush and vend). Blocking READY on push
				// outcome would delay sandbox availability indefinitely on
				// transient broker errors.
				for _, r := range refreshers {
					if fpErr := r.ForcePush(ctx, id); fpErr != nil {
						slog.Warn("supervisor.post_seed_token_push_failed",
							"host", r.Host(), "err", fpErr)
					} else {
						slog.Info("supervisor.real_token_pushed",
							"host", r.Host(), "sandbox", id)
					}
				}
				return true, true
			}
			if caErr != nil {
				slog.Debug("supervisor.seed_ca_retry", "attempt", attempt, "err", caErr)
			}
			if agentErr != nil {
				slog.Debug("supervisor.seed_agent_retry", "attempt", attempt, "err", agentErr)
			}
		}
		select {
		case <-ctx.Done():
			return false, guestEverResponded
		case <-time.After(retryDelay):
		}
	}
	return false, guestEverResponded
}

// seedHumanSecrets seeds the MITM CA plus GH_TOKEN / --secret placeholders
// for a human sandbox (AllowAll + SecretHosts). The supervisor-owned broker
// re-resolves tokens from `gh auth token` / process env (D-PD-26).
func seedHumanSecrets(
	ctx context.Context,
	sb domain.Sandbox,
	cert *x509.Certificate,
	caSeeder, secretSeeder service.GuestSeeder,
	broker *cred.Broker,
	svc PerimeterCAGetter,
) (ok bool, guestEverResponded bool) {
	for attempt := range maxSeedAttempts {
		if ctx.Err() != nil {
			return false, guestEverResponded
		}
		if cert == nil && svc != nil {
			cert = svc.GetPerimeterCACert(sb.ID)
		}
		if cert != nil {
			if caErr := service.SeedCA(ctx, cert, sb.ID, caSeeder); caErr != nil {
				slog.Debug("supervisor.seed_ca_retry", "attempt", attempt, "err", caErr)
			} else {
				// CA seed succeeded: at least one vsock round-trip to the guest agent.
				guestEverResponded = true
				if secErr := service.SeedGuestSecrets(ctx, broker, sb.ID, sb.Envelope.SecretSpecs, secretSeeder); secErr != nil {
					slog.Debug("supervisor.seed_secrets_retry", "attempt", attempt, "err", secErr)
				} else {
					slog.Info("supervisor.human_secrets_complete", "sandbox", sb.ID,
						"secret_hosts", sb.Envelope.SecretHosts)
					return true, true
				}
			}
		}
		select {
		case <-ctx.Done():
			return false, guestEverResponded
		case <-time.After(2 * time.Second):
		}
	}
	return false, guestEverResponded
}

// buildSupervisorDriverConfig assembles the cloudhypervisor.Config the detached
// supervisor boots its VM with.
//
// It exists as a separate function so a test can observe the config WITHOUT
// booting a VM. That matters because of the bug this function was extracted to
// prevent: VCPUs was simply absent from this literal, so the driver fell back
// to its 1-vCPU default while VCPUMax still advertised the hotplug ceiling.
// Every supervisor-backed sandbox therefore booted with exactly one CPU and
// N-1 empty slots (/sys/devices/system/cpu present=0, possible=0-15) no matter
// what --vcpus asked for.
//
// It stayed invisible because BootVCPUs WAS forwarded over argv as
// --boot-vcpus and WAS consumed by the resize governor, so the argv test kept
// passing: it asserted the value was TRANSPORTED, never that it reached the VM.
// A test over this function closes the config half only — that the supervisor
// actually calls it is proven by booting a sandbox and reading nproc.
func buildSupervisorDriverConfig(
	cfg Config,
	memMaxMiB, vcpuMax uint32,
	extraDisks []cloudhypervisor.ExtraDisk,
) cloudhypervisor.Config {
	return cloudhypervisor.Config{
		BinaryPath:        cfg.CHBin,
		SocketDir:         cfg.SocketDir,
		KernelPath:        cfg.KernelPath,
		DiskImagePath:     cfg.DiskPath,
		StartTimeout:      30 * time.Second,
		MemoryMiB:         cfg.MemoryMiB,
		MemoryMaxMiB:      memMaxMiB,
		VCPUs:             cfg.BootVCPUs, // boot_vcpus — see doc comment above
		VCPUMax:           vcpuMax,
		ExtraDisks:        extraDisks,
		Cmdline:           cfg.Cmdline,
		LiveMounts:        cfg.LiveMounts,
		VirtiofsdPath:     cfg.VirtiofsdPath,
		FreePageReporting: true,
		NestedVirt:        cfg.NestedVirt,
		// ConsoleLogPath persists guest virtio-console output alongside supervisor.log.
		// The netns child receives this via NEXUS3_NETNS_CONSOLE_LOG and drains CH
		// stdout to this file, capped at 16 MiB to prevent unbounded growth.
		ConsoleLogPath: filepath.Join(cfg.StateDir, "console.log"),
	}
}
