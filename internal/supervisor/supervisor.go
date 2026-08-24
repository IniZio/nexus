// Package supervisor implements the detached per-sandbox supervisor for
// nexus3's persistent-perimeter architecture.
//
// # Architecture
//
// Each orca sandbox has a corresponding supervisor process that:
//   - boots and owns the VM (via svc.Start → driver.Start),
//   - starts the network perimeter (gvproxy + MITM + netfilter) in-process,
//   - owns a long-lived credential Broker for host-side token injection,
//   - signals readiness by writing supervisor.pid and supervisor.sock,
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
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/govern"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
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
	SB          domain.Sandbox
	Cert        *x509.Certificate
	CASeeder    service.GuestSeeder
	AgentSeeder service.GuestSeeder
	Broker      *cred.Broker
	Refreshers  []*cred.Refresher
	Svc         PerimeterCAGetter
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
		return seedAgentAndHumanSecretsFn(ctx, in.SB, in.Cert, in.CASeeder, in.AgentSeeder, in.Broker, in.Refreshers, in.Svc)
	case routeHumanSecrets:
		return seedHumanSecretsFn(ctx, in.SB, in.Cert, in.CASeeder, in.AgentSeeder, in.Broker, in.Svc)
	default: // routeAgent
		agentSandbox := in.SB.AgentName != ""
		return seedLoopFn(ctx, in.SB.ID, &in.Cert, in.CASeeder, in.AgentSeeder, in.Broker, in.Refreshers,
			maxSeedAttempts, 2*time.Second, in.Svc, agentSandbox)
	}
}

func RunDetached(cfg Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return fmt.Errorf("supervisor: mkdir state dir %s: %w", cfg.StateDir, err)
	}

	// ── 1. Open sandbox store ─────────────────────────────────────────────────
	st, err := store.NewFileStore(cfg.StoreRoot)
	if err != nil {
		return fmt.Errorf("supervisor: open store at %s: %w", cfg.StoreRoot, err)
	}

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
	}

	// ── 4. Bind IPC socket (before VM boot so early stop requests are handled) ─
	sockPath := SockPath(cfg.StateDir)
	_ = os.Remove(sockPath) // remove stale socket from a crash
	stopCh, err := serveIPC(ctx, sockPath, svc, cfg.SandboxRef)
	if err != nil {
		return fmt.Errorf("supervisor: bind IPC socket %s: %w", sockPath, err)
	}
	defer os.Remove(sockPath)

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
	wireGovernorAxes(gov, resizer, resizer, cfg.GovBounds, cfg.HasWorkspaceDisk, cfg.WorkspaceDiskIndex)
	go gov.Run(ctx)

	// ── 5b. Wire Refreshers to the running sandbox ───────────────────────────
	// Register each Refresher with the sandbox so its Token() call can invoke
	// broker.SetRealToken(sb.ID, host, realToken) after seeding mints the
	// placeholder. We do NOT call broker.RegisterPlaceholder here; that is
	// delegated to SeedGuestAgent in step 5d so the guest and the broker always
	// hold the same placeholder. The initial real-token push happens after
	// seeding (step 5d), not here.
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
		cert := svc.GetPerimeterCACert(sb.ID)
		// D-PD-33 / D-PD-36: seed human secrets whenever SecretHosts is non-empty.
		// OpenEgress=true (human open-egress) AND OpenEgress=false (--egress closed)
		// both carry SecretHosts that require MITM credential swap; the supervisor
		// must mint placeholders in either posture. The original guard on OpenEgress
		// silently skipped GH_TOKEN seeding for --egress closed sandboxes.
		route := chooseSeedRoute(sb)
		seedDone, guestEverResponded := runSeedRoute(ctx, route, seedRouteInputs{
			SB:          sb,
			Cert:        cert,
			CASeeder:    caSeeder,
			AgentSeeder: agentSeeder,
			Broker:      broker,
			Refreshers:  refreshers,
			Svc:         svc,
		})
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
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return fmt.Errorf("supervisor: write pidfile %s: %w", pidfile, err)
	}
	defer os.Remove(pidfile)

	slog.Info("supervisor.ready",
		"sandboxRef", cfg.SandboxRef,
		"pid", pid,
		"sock", sockPath,
	)

	// ── 7. Block until shutdown ───────────────────────────────────────────────
	switch cause := awaitShutdown(ctx, stopCh); {
	case cfg.Ephemeral && cause == shutdownByStopVerb:
		// Builder finished: the caller sent POST /supervisor/stop to signal
		// that the build is complete. This is the normal exit path in ephemeral
		// mode — not an emergency stop.
		slog.Info("supervisor.build_complete", "sandboxRef", cfg.SandboxRef)
	case cause == shutdownByStopVerb:
		slog.Info("supervisor.stop_requested", "sandboxRef", cfg.SandboxRef)
	default:
		slog.Info("supervisor.signal_received", "sandboxRef", cfg.SandboxRef)
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
	shutdownByStopVerb
)

// awaitShutdown blocks until either the OS signal context is cancelled
// (SIGTERM / SIGINT) or a /supervisor/stop IPC request closes stopCh.
// It is extracted from RunDetached for unit-testability.
func awaitShutdown(ctx context.Context, stopCh <-chan struct{}) shutdownCause {
	select {
	case <-stopCh:
		return shutdownByStopVerb
	case <-ctx.Done():
		return shutdownBySignal
	}
}

// wireGovernorAxes attaches the CPU and disk AxisEvaluators to gov based on
// bounds and disk configuration. The memory axis is always wired by govern.New;
// this function adds only CPU and disk. Must be called before gov.Run.
//
// CPU axis: registered when both VCPUMin and VCPUMax are non-zero. A partial
// bounds (min-only or max-only) leaves the axis unregistered — the axis itself
// also guards on the bounds, but skipping registration avoids polling overhead.
//
// Disk axis: registered when DiskMaxBytes > 0 AND hasDisk is true. hasDisk
// must be true only when the supervisor's CHDriver has the workspace disk in
// ExtraDisks[diskIndex]. A wrong diskIndex causes GrowDisk to truncate the
// wrong backing file — data loss, not a build failure. Default-off (hasDisk
// false) is the safe configuration when no workspace disk is attached.
func wireGovernorAxes(
	gov *govern.Governor,
	cpuR resize.CPUResizer,
	diskR resize.DiskResizer,
	bounds resize.Bounds,
	hasDisk bool,
	diskIndex int,
) {
	if bounds.VCPUMin != 0 && bounds.VCPUMax != 0 {
		govern.NewCPUAxis(gov, cpuR)
	}
	if bounds.DiskMaxBytes != 0 && hasDisk {
		govern.NewDiskAxis(gov, diskR, diskIndex)
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
	_ = os.WriteFile(path, []byte(err.Error()), 0o644)
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

// seedMCPServersFn is the function called by probeAndSeedGuest to merge the
// MCP servers map into /root/.claude.json. Default is service.SeedGuestMCPServers;
// tests may replace it.
var seedMCPServersFn = service.SeedGuestMCPServers

// seedOverlayClaudeConfigFn is the function called by probeAndSeedGuest to
// mount a writable overlay onto /root/.claude. Default is seedOverlayClaudeConfig;
// tests replace it with a spy to verify the call without a live VM (A-MOUNT).
var seedOverlayClaudeConfigFn = seedOverlayClaudeConfig

// seedOverlayClaudeConfig mounts a writable overlay onto /root/.claude in the
// guest. lowerGuestPath is the guest path of the RO virtiofs share (the curated
// host config staged by AssembleCuratedConfig). Upper and work dirs land on a
// tmpfs so all writes are discarded on sandbox exit.
//
// Must be the FIRST seed step so onboarding writes (seedAgentOnboarding,
// seedBypassConsent) land in the tmpfs upper rather than failing against the
// RO lower.
func seedOverlayClaudeConfig(ctx context.Context, id domain.SandboxID, lowerGuestPath string, execer service.GuestExecer) error {
	// Use bash; /bin/sh in the base image is dash which does not support pipefail.
	script := fmt.Sprintf(`set -eu
mkdir -p /root/.claude
mkdir -p /run/nexus3/ovl
mount -t tmpfs tmpfs /run/nexus3/ovl
mkdir -p /run/nexus3/ovl/upper /run/nexus3/ovl/work
mount -t overlay overlay -o lowerdir=%s,upperdir=/run/nexus3/ovl/upper,workdir=/run/nexus3/ovl/work /root/.claude
`, lowerGuestPath)
	code, err := execer(ctx, id, []string{"/bin/bash", "-c", script}, nil)
	if err != nil {
		return fmt.Errorf("overlay mount: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("overlay mount script exited %d", code)
	}
	return nil
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
		if ovlErr := seedOverlayClaudeConfigFn(ctx, id, in.AgentCfgLowerGuestPath, in.Execer); ovlErr != nil {
			slog.Warn("supervisor.overlay_claude_config_failed",
				"sandbox", id, "lower", in.AgentCfgLowerGuestPath, "err", ovlErr,
				"action", "agent will not see shared host config; /root/.claude is unshared")
		} else {
			slog.Info("supervisor.overlay_claude_config_seeded", "sandbox", id, "lower", in.AgentCfgLowerGuestPath)
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
	// folder-trust wizards and reaches its prompt directly. Non-fatal:
	// the agent still works, it just opens first-run wizards.
	if onbErr := seedAgentOnboardingFn(ctx, id, in.ProjectDir, in.Execer); onbErr != nil {
		slog.Warn("supervisor.agent_onboarding_seed_failed",
			"sandbox", id, "path", service.GuestAgentOnboardingPath, "err", onbErr,
			"action", "an interactively started agent in this guest will open first-run wizards")
	} else {
		slog.Info("supervisor.agent_onboarding_seeded",
			"sandbox", id, "path", service.GuestAgentOnboardingPath)
	}

	// MCP servers injection (mcp-defs-inject). Merges the sanitized
	// mcpServers map into /root/.claude.json so the agent sees shared host
	// MCP server definitions. Must run AFTER onboarding so the file exists.
	// Gated on the same A-MOUNT share-settings signal (MCPServers non-nil).
	// Non-fatal: the agent still works, it just loses host MCP definitions.
	if mcpErr := seedMCPServersFn(ctx, id, in.MCPServers, in.Execer); mcpErr != nil {
		slog.Warn("supervisor.mcp_servers_seed_failed",
			"sandbox", id, "err", mcpErr,
			"action", "guest claude will not see shared host MCP server definitions")
	} else if len(in.MCPServers) > 0 {
		slog.Info("supervisor.mcp_servers_seeded", "sandbox", id, "count", len(in.MCPServers))
	}

	// Bypass-permissions consent seed (D-J12). Merges
	// skipDangerousModePermissionPrompt:true into ~/.claude/settings.json so
	// the shell-function `claude` (which always adds --dangerously-skip-permissions)
	// does not stop on the bypass-permissions wizard. Non-fatal: the agent
	// reaches its prompt after the wizard, it just blocks for operator input.
	if bypassErr := seedBypassConsentFn(ctx, id, in.Execer); bypassErr != nil {
		slog.Warn("supervisor.bypass_consent_seed_failed",
			"sandbox", id, "err", bypassErr,
			"action", "guest claude will stop on bypass-permissions consent dialog")
	} else {
		slog.Info("supervisor.bypass_consent_seeded", "sandbox", id)
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
				if _, combErr := service.SeedGuestAgentAndSecrets(ctx, broker, sb.ID, sb.Envelope.SecretSpecs, credSeeder); combErr != nil {
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
) (ok bool, guestEverResponded bool) {
	for attempt := 0; attempt < maxAttempts; attempt++ {
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
				_, agentErr = service.SeedGuestAgent(ctx, broker, id, agentSeeder)
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
	}
}
