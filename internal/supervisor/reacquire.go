package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/statedir"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ErrNotReacquirable is returned when the persisted record does not carry a
// complete enough netns identity to attempt a crash-path re-acquisition. It
// is distinct from a refusal by the child: this failure happens before any
// contact is made, so nothing anywhere was touched.
var ErrNotReacquirable = errors.New("supervisor: sandbox is not re-acquirable")

// ReacquireResult carries what a successful re-acquisition produced: the
// adopted runtime, and the fact that the guest's TLS trust did NOT survive.
type ReacquireResult struct {
	// Runtime is the adopted netns runtime, already installed in the driver.
	Runtime *cloudhypervisor.NetnsRuntime

	// CALost is true when the perimeter had to be brought up with a FRESH
	// MITM CA because none could be recovered. On the crash path this is
	// always true today: the CA lives only in the dead supervisor's memory
	// and travels only in handoff.Payload.CA, which a crashed process never
	// sent. See ReacquirePerimeterForSandbox's doc comment.
	CALost bool
}

// reacquirePreflight is the fail-closed gate that decides whether a crash-path
// re-acquisition may be ATTEMPTED at all.
//
// Every value it checks must be positively present. A zero, absent, or empty
// value REFUSES — it never falls back to "skip that check", for the same
// reason AdoptNetnsRuntime refuses a zero starttime: these are precisely the
// signals that decide whether the perimeter gets attached to the right VM,
// and a decorative guard is worse than none because it reads as working.
//
// This deliberately duplicates the check [RunReacquire]'s spawner (recovery,
// via the adopt-spawn callback) has already made. A check must never be
// satisfied by "the caller already verified it".
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

// runtimeAdopter is the subset of *cloudhypervisor.CHDriver that
// ReacquirePerimeterForSandbox needs, so the fail-closed behaviour can be
// exercised against a driver that refuses installation.
type runtimeAdopter interface {
	AdoptRuntime(id domain.SandboxID, rt *cloudhypervisor.NetnsRuntime) error
}

// ReacquirePerimeterForSandbox is the crash-path counterpart to RunAdopt's
// handoff.Accept: it rebuilds the perimeter for a sandbox whose supervisor
// DIED, where no live sender exists to pass the perimeter fd.
//
// It asks the surviving netns child, over that child's authenticated control
// socket, for a FRESH socketpair pump end (the child swaps it into its live
// frame pump), keeps the perimeter end, and hands that end to
// AdoptNetnsRuntime — converging with the planned-upgrade path at exactly
// the same seam. No tap fd moves and the guest is never rebooted.
//
// # Fail-closed contract
//
// A replacement that cannot FULLY re-acquire the perimeter REFUSES and
// leaves the VM alone. Concretely, on every error path here:
//
//   - the preflight refuses before any contact is made, or
//   - ReacquirePerimeter refuses, having closed both ends of its fresh
//     socketpair, with the child's pump left untouched, or
//   - AdoptNetnsRuntime refuses (pid recycled), and the perimeter fd is
//     closed here without ever being installed, or
//   - AdoptRuntime refuses, and rt is dropped WITHOUT calling rt.Stop() —
//     stopping would SIGKILL the process group of the only live copy of the
//     VM, which is the destructive outcome this rail exists to prevent.
//
// In every one of those cases the caller has acquired nothing and the VM is
// exactly as it was. A partially-rebuilt perimeter — one that carries frames
// but does not enforce egress policy — is never produced, because the only
// way to obtain the perimeter end at all is a positive response from the
// child, and the child only responds positively after it has completed the
// swap.
//
// # CA / TLS (answers ticket 09 Q3)
//
// CALost is always true on this path. The MITM CA material exists only in
// the crashed supervisor's memory and is transferred only via
// handoff.Payload.CA (buildHandoffPayload reads it from the live
// perimeter.PerimeterSupervisor via CAKeyPair); it is never written to disk.
// A crashed supervisor sent no payload, so the replacement necessarily mints
// a fresh CA. The guest has already imported and pinned the OLD CA this
// boot, so every in-guest TLS session breaks until the guest re-imports.
// Plain (non-TLS) guest networking is fully restored. Crash recovery is
// therefore NON-DESTRUCTIVE but NOT TRANSPARENT — this is reported, not
// papered over, because a caller that believes TLS survived would diagnose
// the resulting failures as a network fault rather than as expected.
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
		// AdoptNetnsRuntime does not close perimFile on its error paths
		// (its documented contract), so disposing of it is ours to do.
		perimFile.Close()
		return ReacquireResult{}, fmt.Errorf("supervisor: reacquire %s: adopt netns runtime: %w", sb.ID, err)
	}

	if err := drv.AdoptRuntime(sb.ID, rt); err != nil {
		// Deliberately NOT rt.Stop(): that would SIGKILL the process group
		// of the only live copy of the VM. Dropping rt un-stopped leaves the
		// VM running exactly as it was, which is the fail-closed outcome.
		return ReacquireResult{}, fmt.Errorf("supervisor: reacquire %s: install runtime: %w", sb.ID, err)
	}

	slog.Warn("supervisor.reacquired",
		"sandbox", sb.ID,
		"childPID", sb.NetnsChildPID,
		"caLost", true,
		"note", "guest TLS sessions break until the guest re-imports the new MITM CA; plain networking is restored")

	return ReacquireResult{Runtime: rt, CALost: true}, nil
}

// compile-time assertion that the real driver satisfies runtimeAdopter.
var _ runtimeAdopter = (*cloudhypervisor.CHDriver)(nil)

// RunReacquire runs a detached supervisor in RE-ACQUIRE mode: the crash-path
// counterpart to [RunAdopt].
//
// Like RunAdopt it never calls driver.Start and never boots a VM. Unlike
// RunAdopt there is no handoff to accept — the previous supervisor is DEAD,
// which is the whole reason this mode exists. It rebuilds the perimeter by
// asking the surviving netns child, over that child's authenticated control
// socket, for a fresh socketpair pump end.
//
// # Why this must be a long-lived process (the hazard this mode exists for)
//
// The perimeter — gvproxy, the MITM proxy, the netfilter rules — runs INSIDE
// this process. Re-acquiring from a short-lived CLI and then exiting would
// tear the perimeter straight back down and leave the guest network-dead a
// second time: a fix that undoes itself on exit. So this mode does what
// RunDetached and RunAdopt do, and serves for the VM's whole lifetime via
// [serveAdoptedSupervisor].
//
// # Fail-closed
//
// Every failure path returns a non-nil error WITHOUT having disturbed the VM:
// the preflight refuses before any contact; a refusal by the child leaves its
// pump untouched; and a failure to install the runtime drops it WITHOUT
// calling rt.Stop(), which would SIGKILL the process group of the only live
// copy of the VM. See [ReacquirePerimeterForSandbox] for the full contract.
//
// # TLS is broken across this path
//
// There is no seed CA to pass: the crashed supervisor's CA existed only in
// its memory. StartPerimeterOnly therefore mints a FRESH CA, and every
// in-guest TLS session breaks until the guest re-imports it. Plain networking
// is fully restored. This is logged at WARN rather than papered over — a
// caller who believes TLS survived will diagnose the resulting failures as a
// network fault instead of as the expected cost of crash recovery.
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

	// Fail-closed identity guard, re-checked here rather than trusted from
	// the spawner, per this motive's rail that a check must never be
	// satisfied by "the caller already verified it".
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
	if cfg.CredsFile != "" {
		for _, host := range service.AgentEgressHosts(cred.ClaudeCodeProfile) {
			r, rErr := cred.NewRefresher(cfg.CredsFile, host, broker)
			if errors.Is(rErr, cred.ErrStoreAbsent) {
				break // same file for all hosts; no point trying others
			}
			if rErr != nil {
				slog.Warn("supervisor.reacquire.refresher_init_failed", "host", host, "err", rErr)
				continue
			}
			refreshers = append(refreshers, r)
		}
	}

	// ── Re-acquire the perimeter from the surviving netns child ───────────
	res, err := ReacquirePerimeterForSandbox(ctx, sb, drv)
	if err != nil {
		return fmt.Errorf("supervisor: reacquire: %w", err)
	}
	slog.Info("supervisor.reacquire.acquired",
		"sandboxRef", cfg.SandboxRef, "sandbox", sb.ID, "netnsChildPID", sb.NetnsChildPID)

	if res.CALost {
		slog.Warn("supervisor.reacquire.ca_lost",
			"sandboxRef", cfg.SandboxRef,
			"sandbox", sb.ID,
			"impact", "in-guest TLS sessions will FAIL until the guest re-imports the new MITM CA; plain (non-TLS) guest networking is restored",
			"cause", "the crashed supervisor's CA existed only in its memory and travels only in handoff.Payload.CA, which a crashed process never sent")
	}

	// Persist this process's supervisor identity. recover cleared the dead
	// supervisor's pid/socket when it classified the sandbox adoptable, so
	// without this write the record would name no supervisor at all and
	// later commands (stop, egress allow, upgrade) could not find this one.
	sockPath := SockPath(cfg.StateDir)
	if setErr := svc.SetSupervisor(ctx, sb.ID, os.Getpid(), sockPath); setErr != nil {
		return fmt.Errorf("supervisor: reacquire: persist supervisor identity: %w", setErr)
	}

	// waitForPID is 0: the previous supervisor is already dead — that is the
	// premise of this whole mode — so there is no exit to wait for.
	return serveAdoptedSupervisor(ctx, serveAdoptedInput{
		cfg:        cfg,
		st:         st,
		svc:        svc,
		drv:        drv,
		sb:         sb,
		seedCA:     nil, // crash path: no payload, so no CA to seed (res.CALost)
		refreshers: refreshers,
		waitForPID: 0,
		logPrefix:  "supervisor.reacquire",
	})
}
