package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/statedir"
	"github.com/IniZio/nexus/internal/core/store"
)

var ErrNotReacquirable = errors.New("supervisor: sandbox is not re-acquirable")

type ReacquireResult struct {
	Runtime *cloudhypervisor.NetnsRuntime
	CALost  bool
}

func reacquireSeedInput(
	cfg Config,
	st store.Store,
	svc *service.Service,
	drv *cloudhypervisor.CHDriver,
	sb domain.Sandbox,
	refreshers []*cred.Refresher,
) (serveAdoptedInput, bool) {
	caDir := statedir.SupervisorDir(cfg.StoreRoot, sb.ID)

	var seedCA *service.CASeed
	certPEM, keyPEM, caErr := statedir.LoadCA(caDir)
	switch {
	case caErr != nil:
		slog.Warn("supervisor.reacquire.ca_lost",
			"sandboxRef", cfg.SandboxRef,
			"sandbox", sb.ID,
			"path", statedir.CAPath(caDir),
			"cause", caErr.Error(),
			"impact", "in-guest TLS sessions will FAIL until the guest re-imports the FRESH MITM CA; plain (non-TLS) guest networking is restored")
	default:
		seedCA = &service.CASeed{CertPEM: certPEM, KeyPEM: keyPEM}
		slog.Info("supervisor.reacquire.ca_recovered",
			"sandboxRef", cfg.SandboxRef,
			"sandbox", sb.ID,
			"path", statedir.CAPath(caDir),
			"note", "the replacement perimeter keeps signing with the CA the guest already trusts; TLS survives this recovery")
	}

	return serveAdoptedInput{
		cfg:        cfg,
		st:         st,
		svc:        svc,
		drv:        drv,
		sb:         sb,
		seedCA:     seedCA,
		refreshers: refreshers,
		waitForPID: 0,
		logPrefix:  "supervisor.reacquire",
	}, seedCA == nil
}

// reacquirePreflight is the fail-closed gate for re-acquisition. Every value must be positively present.
func reacquirePreflight(sb domain.Sandbox) error {
	switch {
	case sb.NetnsChildPID <= 0:
		return fmt.Errorf("%w: %s has no netns child pid", ErrNotReacquirable, sb.ID)
	case sb.NetnsChildPGID <= 0:
		return fmt.Errorf("%w: %s has no netns child pgid", ErrNotReacquirable, sb.ID)
	case sb.NetnsChildStartTime == 0:
		return fmt.Errorf("%w: %s has no netns child starttime; refusing to re-acquire without a pid-reuse guard", ErrNotReacquirable, sb.ID)
	case sb.GuestTapName == "":
		return fmt.Errorf("%w: %s has no guest tap name", ErrNotReacquirable, sb.ID)
	case sb.CHAPISocket == "":
		return fmt.Errorf("%w: %s has no CH API socket", ErrNotReacquirable, sb.ID)
	case sb.NetnsControlSocket == "":
		return fmt.Errorf("%w: %s has no netns control socket; its VM predates the control-socket mechanism and is recoverable at the record level only", ErrNotReacquirable, sb.ID)
	case sb.NetnsControlToken == "":
		return fmt.Errorf("%w: %s has no netns control token", ErrNotReacquirable, sb.ID)
	}
	return nil
}

type runtimeAdopter interface {
	AdoptRuntime(id domain.SandboxID, rt *cloudhypervisor.NetnsRuntime) error
}

// ReacquirePerimeterForSandbox rebuilds the perimeter for a dead supervisor (D-HSH-18).
func ReacquirePerimeterForSandbox(ctx context.Context, sb domain.Sandbox, drv runtimeAdopter) (ReacquireResult, error) {
	if err := reacquirePreflight(sb); err != nil {
		return ReacquireResult{}, err
	}

	perimFile, err := cloudhypervisor.ReacquirePerimeter(
		sb.NetnsControlSocket, sb.NetnsControlToken, sb.ID.String(),
		sb.NetnsChildPID, sb.NetnsChildStartTime,
	)
	if err != nil {
		return ReacquireResult{}, fmt.Errorf("supervisor: reacquire %s: %w", sb.ID, err)
	}

	rt, err := cloudhypervisor.AdoptNetnsRuntime(ctx,
		sb.NetnsChildPID, sb.NetnsChildPGID, sb.NetnsChildStartTime,
		sb.GuestTapName, sb.CHAPISocket, perimFile,
	)
	if err != nil {
		perimFile.Close()
		return ReacquireResult{}, fmt.Errorf("supervisor: reacquire %s: adopt netns runtime: %w", sb.ID, err)
	}

	if err := drv.AdoptRuntime(sb.ID, rt); err != nil {
		return ReacquireResult{}, fmt.Errorf("supervisor: reacquire %s: install runtime: %w", sb.ID, err)
	}

	slog.Info("supervisor.reacquired",
		"sandbox", sb.ID,
		"childPID", sb.NetnsChildPID,
		"note", "perimeter re-acquired without rebooting the guest; whether TLS trust survived is decided by reacquireSeedInput and logged separately")
	return ReacquireResult{Runtime: rt, CALost: true}, nil
}

var _ runtimeAdopter = (*cloudhypervisor.CHDriver)(nil)

// RunReacquire runs the crash-path supervisor (D-HSH-18).
func RunReacquire(cfg Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := statedir.Ensure(cfg.StateDir); err != nil {
		return fmt.Errorf("supervisor: reacquire: mkdir state dir %s: %w", cfg.StateDir, err)
	}

	st, err := store.NewFileStore(cfg.StoreRoot)
	if err != nil {
		return fmt.Errorf("supervisor: reacquire: open store at %s: %w", cfg.StoreRoot, err)
	}
	sb, err := st.ResolveByPrefix(ctx, cfg.SandboxRef)
	if err != nil {
		return fmt.Errorf("supervisor: reacquire: resolve sandbox %s: %w", cfg.SandboxRef, err)
	}

	if err := reacquirePreflight(sb); err != nil {
		return fmt.Errorf("supervisor: reacquire: %w", err)
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
		return fmt.Errorf("supervisor: reacquire: init driver: %w", err)
	}

	svc := service.New(st, drv, lifecycle.New())
	broker := cred.NewBroker()
	svc = svc.WithBroker(broker)

	var refreshers []*cred.Refresher

	// ── Re-acquire the perimeter from the surviving netns child ───────────
	res, err := ReacquirePerimeterForSandbox(ctx, sb, drv)
	if err != nil {
		return fmt.Errorf("supervisor: reacquire: %w", err)
	}
	in, caLost := reacquireSeedInput(cfg, st, svc, drv, sb, refreshers)
	res.CALost = caLost

	slog.Info("supervisor.reacquire.acquired",
		"sandboxRef", cfg.SandboxRef, "sandbox", sb.ID, "netnsChildPID", sb.NetnsChildPID,
		"caLost", res.CALost)

	recordReacquireCAOutcome(cfg.StateDir, res.CALost)
	sockPath := SockPath(cfg.StateDir)
	if setErr := svc.SetSupervisor(ctx, sb.ID, os.Getpid(), sockPath); setErr != nil {
		return fmt.Errorf("supervisor: reacquire: persist supervisor identity: %w", setErr)
	}

	return serveAdoptedSupervisor(ctx, in)
}
