package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/statedir"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/supervisor/handoff"
)

const adoptHandoffAcceptTimeout = 20 * time.Second
const adoptWaitOldExitTimeout = 15 * time.Second

// RunAdopt runs a detached supervisor in adopt mode. It listens on handoffSockPath,
// accepts one handoff offer, and installs the acquired netns runtime into the driver.
// /** Safety (D-HSH-08): fail-closed. Every error returns WITHOUT calling [handoff.Confirm],
// leaving ownership with the outgoing side unchanged. */
func RunAdopt(cfg Config, handoffSockPath string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := statedir.Ensure(cfg.StateDir); err != nil {
		return fmt.Errorf("supervisor: adopt: mkdir state dir %s: %w", cfg.StateDir, err)
	}

	st, err := store.NewFileStore(cfg.StoreRoot)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: open store at %s: %w", cfg.StoreRoot, err)
	}
	sb, err := st.ResolveByPrefix(ctx, cfg.SandboxRef)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: resolve sandbox %s: %w", cfg.SandboxRef, err)
	}

	if sb.NetnsChildPID <= 0 || sb.NetnsChildPGID <= 0 || sb.NetnsChildStartTime == 0 ||
		sb.GuestTapName == "" || sb.CHAPISocket == "" {
		return fmt.Errorf("supervisor: adopt: sandbox %s has an incomplete netns identity; refusing to adopt", sb.ID)
	}

	extraDisks := make([]cloudhypervisor.ExtraDisk, 0, len(cfg.ExtraDisks))
	for _, p := range cfg.ExtraDisks {
		extraDisks = append(extraDisks, cloudhypervisor.ExtraDisk{Path: p})
	}
	var memMaxMiB uint32
	if cfg.GovBounds.MemMaxBytes > 0 {
		memMaxMiB = uint32(cfg.GovBounds.MemMaxBytes / (1024 * 1024)) //nolint:gosec // bytes→MiB; fits uint32 for any sane ceiling
	}
	vcpuMax := uint32(cfg.GovBounds.VCPUMax) //nolint:gosec // int32→uint32; VCPUMax is always non-negative by construction
	drv, err := cloudhypervisor.New(buildSupervisorDriverConfig(cfg, memMaxMiB, vcpuMax, extraDisks))
	if err != nil {
		return fmt.Errorf("supervisor: adopt: init driver: %w", err)
	}

	svc := service.New(st, drv, lifecycle.New())
	broker := cred.NewBroker()
	svc = svc.WithBroker(broker)

	var refreshers []*cred.Refresher

	// ── Listen for the handoff offer ──────────────────────────────────────
	_ = os.Remove(handoffSockPath)
	rawLn, err := net.Listen("unix", handoffSockPath)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: listen handoff socket %s: %w", handoffSockPath, err)
	}
	hln, ok := rawLn.(*net.UnixListener)
	if !ok {
		rawLn.Close()
		return fmt.Errorf("supervisor: adopt: unexpected handoff listener type %T", rawLn)
	}
	defer func() {
		hln.Close()
		_ = os.Remove(handoffSockPath)
	}()

	if err := hln.SetDeadline(time.Now().Add(adoptHandoffAcceptTimeout)); err != nil {
		return fmt.Errorf("supervisor: adopt: set accept deadline: %w", err)
	}
	rawConn, err := hln.Accept()
	if err != nil {
		return fmt.Errorf("supervisor: adopt: accept handoff connection: %w", err)
	}
	conn, isUnix := rawConn.(*net.UnixConn)
	if !isUnix {
		rawConn.Close()
		return fmt.Errorf("supervisor: adopt: unexpected handoff conn type %T", rawConn)
	}
	defer conn.Close()

	payload, fdFile, err := handoff.Accept(conn)
	if err != nil {
		return fmt.Errorf("supervisor: adopt: accept payload: %w", err)
	}
	if payload.Version != handoff.CurrentVersion {
		reason := fmt.Sprintf("unsupported handoff version %d (this binary understands %d)", payload.Version, handoff.CurrentVersion)
		if fdFile != nil {
			fdFile.Close()
		}
		_ = handoff.Refuse(conn, reason)
		return fmt.Errorf("supervisor: adopt: %s", reason)
	}
	if !payload.Perimeter.Present || fdFile == nil {
		if fdFile != nil {
			fdFile.Close()
		}
		const reason = "payload carries no perimeter fd"
		_ = handoff.Refuse(conn, reason)
		return fmt.Errorf("supervisor: adopt: %s", reason)
	}

	rt, err := cloudhypervisor.AdoptNetnsRuntime(ctx,
		sb.NetnsChildPID, sb.NetnsChildPGID, sb.NetnsChildStartTime,
		sb.GuestTapName, sb.CHAPISocket, fdFile,
	)
	if err != nil {
		_ = handoff.Refuse(conn, err.Error())
		return fmt.Errorf("supervisor: adopt: adopt netns runtime: %w", err)
	}
	if err := drv.AdoptRuntime(sb.ID, rt); err != nil {
		// Do NOT rt.Stop() — that SIGKILLs the only live VM copy.
		_ = handoff.Refuse(conn, err.Error())
		return fmt.Errorf("supervisor: adopt: install runtime: %w", err)
	}

	if err := handoff.Confirm(conn); err != nil {
		return fmt.Errorf("supervisor: adopt: confirm: %w", err)
	}
	slog.Info("supervisor.adopted", "sandboxRef", cfg.SandboxRef, "sandbox", sb.ID)

	// /** Seed with the outgoing supervisor's CA so the guest's TLS trust survives.
	// A fresh CA would break in-guest TLS until guest re-import. */
	var seedCA *service.CASeed
	if len(payload.CA.CertPEM) > 0 && len(payload.CA.KeyPEM) > 0 {
		seedCA = &service.CASeed{CertPEM: payload.CA.CertPEM, KeyPEM: payload.CA.KeyPEM}
	}

	return serveAdoptedSupervisor(ctx, serveAdoptedInput{
		cfg:        cfg,
		st:         st,
		svc:        svc,
		drv:        drv,
		sb:         sb,
		seedCA:     seedCA,
		refreshers: refreshers,
		waitForPID: sb.SupervisorPID,
		logPrefix:  "supervisor.adopt",
	})
}
