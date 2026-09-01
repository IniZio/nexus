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
	// MITM CA because none could be recovered, and every in-guest TLS session
	// therefore breaks until the guest re-imports it.
	//
	// [ReacquirePerimeterForSandbox] leaves this at the FAIL-CLOSED value
	// (true) because it does not read the CA; [RunReacquire] overwrites it
	// from [reacquireSeedInput], which is the single place the CA decision is
	// made. The default is deliberately the pessimistic one so any future
	// caller that forgets to load a CA reports the truth rather than claiming
	// a trust continuity it does not have.
	CALost bool
}

// reacquireSeedInput loads the persisted MITM CA for sb and assembles the
// [serveAdoptedInput] the crash path serves with. It returns the input and
// whether the CA was LOST (i.e. could not be recovered and a fresh one will be
// minted by StartPerimeterOnly).
//
// # Why the load and the wiring live in ONE function
//
// This slice exists because the crash path passed seedCA: nil while the whole
// seeding mechanism sat there unused. A mechanism with no caller is the defect
// class this motive has already shipped once (53ac4a8). Loading the CA in one
// function and wiring it in another would leave a link that no test can reach,
// so both happen here: this is the function whose output RunReacquire hands
// straight to serveAdoptedSupervisor, and a test can assert on it.
//
// # Fail closed and loud
//
// Absent, corrupt, truncated, half-written, or expired CA material all take
// the same path: seedCA stays nil, caLost is true, and a WARN names the cause
// and the path. statedir.LoadCA validates the pair with exactly the checks
// mitm.New applies to a seed, so a damaged file can never become a supervisor
// start-up failure — a crash-recovery path that DIED on a truncated CA file
// would turn a recoverable sandbox into an unrecoverable one, which is the
// outcome this whole rail exists to prevent.
func reacquireSeedInput(
	cfg Config,
	st store.Store,
	svc *service.Service,
	drv *cloudhypervisor.CHDriver,
	sb domain.Sandbox,
	refreshers []*cred.Refresher,
) (serveAdoptedInput, bool) {
	// cfg.StoreRoot is the same root the writer resolves: the CLI opens its
	// FileStore at store.DefaultRoot() and passes that path down as StoreRoot,
	// and service.startSupervisor — which runs INSIDE this process and does
	// the writing — calls store.DefaultRoot() itself, inheriting the same
	// XDG_STATE_HOME. Both ends also go through statedir.SupervisorDir, so the
	// path cannot drift even if the roots ever did.
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
		// waitForPID is 0: the previous supervisor is already dead — that is
		// the premise of this whole mode — so there is no exit to wait for.
		waitForPID: 0,
		logPrefix:  "supervisor.reacquire",
	}, seedCA == nil
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
// This function does not read the CA, so the CALost it returns is the
// fail-closed default (true). The CA decision belongs to
// [reacquireSeedInput], which loads the CA persisted by the perimeter when it
// minted it (statedir.SaveCA, D-HSH-18 / ticket 13) and is the single place
// that decides whether the guest's TLS trust survives; [RunReacquire]
// overwrites CALost from it.
//
// Historical note, because it explains the shape of this rail: before
// s15-ca-persistence the CA existed only in the crashed supervisor's memory
// and travelled only via handoff.Payload.CA, which a crashed process never
// sent — so CALost was unconditionally true and crash recovery was
// non-destructive but never transparent.
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

	slog.Info("supervisor.reacquired",
		"sandbox", sb.ID,
		"childPID", sb.NetnsChildPID,
		"note", "perimeter re-acquired without rebooting the guest; whether TLS trust survived is decided by reacquireSeedInput and logged separately")

	// CALost defaults to the fail-closed value: this function never read a CA.
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
// # TLS across this path
//
// The MITM CA is persisted to the per-sandbox supervisor state dir when the
// perimeter mints it (statedir.SaveCA, D-HSH-18 / ticket 13), so this mode
// re-seeds it and the replacement perimeter keeps signing leaf certificates
// the guest already trusts: recovery is transparent to a RUNNING in-guest
// process, which is the whole point — a long-running Node process reads
// NODE_EXTRA_CA_CERTS once at startup, so no guest-side re-import could have
// reached it.
//
// If the CA cannot be recovered — absent, unreadable, corrupt, truncated,
// half-written, or expired — this path FAILS CLOSED and LOUD: it mints a fresh
// CA, reports CALost, and logs the cause at WARN, rather than papering over
// it. In that case every in-guest TLS session breaks until the guest
// re-imports, while plain networking is fully restored. Reported, because a
// caller who believes TLS survived will diagnose the resulting failures as a
// network fault instead of as the cost of a lost CA.
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
	// Load the persisted MITM CA and build the serve input in one step. The
	// WARN/INFO naming the CA outcome is emitted inside reacquireSeedInput.
	in, caLost := reacquireSeedInput(cfg, st, svc, drv, sb, refreshers)
	res.CALost = caLost

	slog.Info("supervisor.reacquire.acquired",
		"sandboxRef", cfg.SandboxRef, "sandbox", sb.ID, "netnsChildPID", sb.NetnsChildPID,
		"caLost", res.CALost)

	// Record the CA outcome where the spawning CLI can read it back. This must
	// happen BEFORE serveAdoptedSupervisor writes the pidfile, because the
	// pidfile is the readiness signal SpawnReacquireDetached returns on: written
	// after it, there would be a window in which `recover` sees a ready
	// supervisor and no outcome, and would report "undetermined" for a
	// re-acquisition that in fact succeeded.
	//
	// `recover` is a short-lived CLI that only SPAWNS this process; the CA
	// decision is made here, in reacquireSeedInput, so this is the only place
	// that can state it as fact. The alternative — the CLI re-running the load
	// to guess the answer — would be a checker sharing the mechanism it checks,
	// and would agree with itself even when the mechanism broke.
	recordReacquireCAOutcome(cfg.StateDir, res.CALost)

	// Persist this process's supervisor identity. recover cleared the dead
	// supervisor's pid/socket when it classified the sandbox adoptable, so
	// without this write the record would name no supervisor at all and
	// later commands (stop, egress allow, upgrade) could not find this one.
	sockPath := SockPath(cfg.StateDir)
	if setErr := svc.SetSupervisor(ctx, sb.ID, os.Getpid(), sockPath); setErr != nil {
		return fmt.Errorf("supervisor: reacquire: persist supervisor identity: %w", setErr)
	}

	return serveAdoptedSupervisor(ctx, in)
}
