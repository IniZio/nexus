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
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
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

	// CredsFile is the optional path to creds.json for real-token seeding.
	// When empty, the broker is wired but holds no real tokens.
	CredsFile string
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
	drv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:    cfg.CHBin,
		SocketDir:     cfg.SocketDir,
		KernelPath:    cfg.KernelPath,
		DiskImagePath: cfg.DiskPath,
		StartTimeout:  30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("supervisor: init driver: %w", err)
	}

	// ── 3. Build service with credential broker ───────────────────────────────
	svc := service.New(st, drv, lifecycle.New())
	broker := cred.NewBroker()
	svc = svc.WithBroker(broker)

	// Build Refresher-backed credential sources for the agent egress hosts
	// (api.anthropic.com, platform.claude.com). Each Refresher loads the
	// dedicated OAuth credential store and uses oauth2 ReuseTokenSourceWithExpiry
	// to maintain a live access token; it pushes the real token into broker via
	// broker.SetRealToken whenever the access token rotates.
	//
	// Graceful degradation: if the creds file is absent or unreadable the broker
	// starts with no real tokens and the perimeter still enforces network ACLs;
	// HTTPS auth headers will carry the placeholder (bearer will be invalid)
	// until the operator provisions the credential store.
	var refreshers []*cred.Refresher
	if cfg.CredsFile != "" {
		for _, host := range service.AgentEgressHosts() {
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
	// Poll each Refresher every minute; oauth2 ReuseTokenSourceWithExpiry only
	// actually fetches when the cached token is near expiry. Token() may return
	// an error if SeedGuestAgent has not yet run (scope not yet registered in
	// the broker); the goroutine logs and retries each tick.
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
		caSeeder := service.NewAgentCACopySeeder(agentClient)
		agentSeeder := service.NewAgentCopySeeder(agentClient)
		cert := svc.GetPerimeterCACert(sb.ID)
		seedDone := SeedLoop(ctx, sb.ID, &cert, caSeeder, agentSeeder, broker, refreshers,
			maxSeedAttempts, 2*time.Second, svc)
		if !seedDone {
			if ctx.Err() != nil {
				slog.Warn("supervisor.seed_skipped", "reason", "context cancelled before seeding complete")
			} else {
				slog.Warn("supervisor.seed_cap_exhausted",
					"max_attempts", maxSeedAttempts,
					"action", "writing READY anyway; perimeter live, guest may lack placeholder+CA")
			}
		} else {
			// Activate the CA cert in the system trust store so that non-Node.js
			// HTTPS clients (git, wget, curl) trust the MITM proxy CA without
			// explicit per-process configuration. GuestCACertPath is already in
			// /usr/local/share/ca-certificates/ (written by SeedCA), so a single
			// update-ca-certificates call incorporates it into /etc/ssl/certs/.
			// Failure is non-fatal — claude still works via NODE_EXTRA_CA_CERTS.
			ucCtx, ucCancel := context.WithTimeout(ctx, 30*time.Second)
			defer ucCancel()
			if _, ucErr := agentClient.Exec(ucCtx, agent.ExecOptions{
				// Full path: exec.Command does LookPath in the agent's own
				// environment (PID-1 init, PATH may be unset), so a bare name
				// would not be found even with Env["PATH"] set.
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
	select {
	case <-stopCh:
		slog.Info("supervisor.stop_requested", "sandboxRef", cfg.SandboxRef)
	case <-ctx.Done():
		slog.Info("supervisor.signal_received", "sandboxRef", cfg.SandboxRef)
	}

	// ── 8. Graceful shutdown ──────────────────────────────────────────────────
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if _, stopErr := svc.Stop(stopCtx, cfg.SandboxRef); stopErr != nil {
		slog.Warn("supervisor.stop_failed", "sandboxRef", cfg.SandboxRef, "err", stopErr)
	}
	slog.Info("supervisor.exited", "sandboxRef", cfg.SandboxRef)
	return nil
}

// PerimeterCAGetter is the subset of *service.Service needed by SeedLoop to
// fetch the MITM CA certificate. Nil is permitted; if nil, GetPerimeterCACert
// is never called (the caller must pre-populate cert).
type PerimeterCAGetter interface {
	GetPerimeterCACert(id domain.SandboxID) *x509.Certificate
}

// SeedLoop attempts up to maxAttempts rounds of CA + agent-placeholder seeding.
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
) bool {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return false
		}
		if *cert == nil && svc != nil {
			*cert = svc.GetPerimeterCACert(id)
		}
		if *cert != nil {
			caErr := service.SeedCA(ctx, *cert, id, caSeeder)
			var agentErr error
			if caErr == nil {
				_, agentErr = service.SeedGuestAgent(ctx, broker, id, agentSeeder)
			}
			if caErr == nil && agentErr == nil {
				slog.Info("supervisor.seeds_complete", "sandbox", id,
					"cert_path", service.GuestCACertPath,
					"env_path", service.GuestCredEnvPath)
				// Re-push real tokens to the placeholder freshly minted by
				// SeedGuestAgent (which revoked the step-5b placeholder and
				// minted a new one for the same scope).
				for _, r := range refreshers {
					if _, _, tokErr := r.Token(ctx); tokErr != nil {
						slog.Warn("supervisor.post_seed_token_push_failed",
							"host", r.Host(), "err", tokErr)
					} else {
						slog.Info("supervisor.real_token_pushed",
							"host", r.Host(), "sandbox", id)
					}
				}
				return true
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
			return false
		case <-time.After(retryDelay):
		}
	}
	return false
}
