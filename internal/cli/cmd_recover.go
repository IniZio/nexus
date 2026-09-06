package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/recovery"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "recover",
		Summary: "Reconcile persisted sandbox records against the live substrate",
		Run:     runRecover,
	})
}

// runRecover is the implementation of the `nexus3 recover` subcommand.
//
// # Safety guarantee
//
// Recovery performs destructive reconciliation: it resolves paused→stopped,
// honours --rm, and may delete sandbox records. With a real substrate driver
// this is correct — Observe queries the live hypervisor before any write. With
// a fake or noop driver that always reports Absent it would destroy records for
// every running sandbox.
//
// This guarantee is structural, not conditional:
//   - SelectSubstrate() returns (nil, *SubstrateError) on any failure, and that
//     is the only path to a fake/noop driver. The noop driver is substituted
//     only in newSandboxService (for sandbox verbs), never here.
//   - runRecover returns immediately on a non-nil SubstrateError, before
//     recovery.New is ever called.
//   - Therefore recovery.New is only ever called with a real driver that can
//     observe actual VM state. The always-Absent noop driver is structurally
//     unreachable from this function.
func runRecover(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}

	// Substrate selection must succeed before we build a Recoverer. A failed
	// selection means the driver cannot observe actual VM state; running
	// recovery against such a driver would treat every sandbox as absent and
	// perform destructive reconciliation against live data.
	drv, serr := SelectSubstrate()
	if serr != nil {
		// Return CodedError so root.go emits the correct stable code. Do NOT
		// call out.EmitError here — that produces two envelopes on stdout in
		// --json mode, breaking json.load.
		return &CodedError{
			Code: sandboxErrCodeNoSubstrate,
			Msg:  serr.Msg,
			Err:  serr,
		}
	}

	root, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("recover: resolve state directory: %v", err),
		}
	}
	st, err := store.NewFileStore(root)
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("recover: open state directory: %v", err),
		}
	}

	return runRecoverWith(ctx, st, drv, out)
}

// runRecoverWith is the testable entry point for "recover": it performs the
// actual reconciliation against the given store and driver. runRecover is a
// thin wrapper around this that resolves the real substrate and store root;
// tests call runRecoverWith directly with a fake driver so they exercise the
// exact same wiring — including the supervisor-liveness cross-check
// (AC-8, OutcomeAdoptable) — that production runs, without needing a real
// hypervisor.
func runRecoverWith(ctx context.Context, st store.Store, drv driver.Driver, out *Output) error {
	rec := recovery.New(st, drv).
		WithSupervisorCheck(supervisor.CheckAndReconcile).
		WithAdoptSpawner(recoverAdoptSpawner)
	report, err := rec.Recover(ctx)
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("recover: %v", err),
		}
	}

	// AC-8 requires a live-VM/dead-supervisor sandbox to be REPORTED as
	// needing adoption, not merely classified correctly in the JSON envelope
	// — --json is opt-in, so the default surface is human mode, and
	// EmitSuccess's human-mode branch below prints only the bare summary
	// count. Without this, an operator running plain `nexus3 recover` against
	// the exact sandbox this ticket exists to fix sees only "examined 1
	// sandbox(es)" — the literal symptom the ticket quotes — with the
	// adoptable classification invisible unless they already know to pass
	// --json. Print one line per outcome worth a human's attention (every
	// kind except OutcomeUnchanged, the common case with nothing to act on)
	// before the summary line. The JSON envelope is untouched: this block is
	// skipped entirely in JSON mode, and out.Stdout() is never written to
	// there (writing anything else to stdout in JSON mode breaks json.load
	// per EmitSuccess's own doc comment).
	if !out.IsJSON() {
		for _, o := range report.Outcomes {
			if o.Kind == recovery.OutcomeUnchanged {
				continue
			}
			fmt.Fprintf(out.Stdout(), "%s: %s — %s\n", o.ID, o.Kind, o.Reason)
		}
	}

	out.EmitSuccess("recover", report,
		fmt.Sprintf("recovery complete: examined %d sandbox(es)", len(report.Outcomes)))
	return nil
}

// recoverAdoptSpawner is the adopt-spawn callback runRecoverWith wires into
// recovery. It is a package-level variable, not a direct reference, purely so
// tests can substitute a fake and assert that the PRODUCTION path reaches it
// — forking a real supervisor in a unit test is not possible, but leaving the
// wiring uncovered is how this motive shipped a mechanism with no caller in
// the first place. Production never reassigns it.
var recoverAdoptSpawner = spawnReplacementSupervisor

// doSpawnReacquireDetached is the re-acquire spawn function wired into
// spawnReplacementSupervisor. A package-level variable, not a direct
// reference, so tests can substitute a fake and assert that the PRODUCTION
// path forwards the correct SpawnConfig — in particular that GovBounds,
// MemoryMiB, and BootVCPUs from the persisted spawn spec are forwarded
// verbatim and not silently dropped or zeroed.
var doSpawnReacquireDetached = supervisor.SpawnReacquireDetached

// spawnReplacementSupervisor is the production adopt-spawn callback wired
// into recovery: it starts a long-lived RunReacquire supervisor for a
// sandbox whose VM is alive but whose supervisor died.
//
// It lives here, in the CLI, for the same reason supervisor.CheckAndReconcile
// does: internal/core/recovery must not import internal/supervisor (that
// closes an import cycle through the cloudhypervisor test package), and this
// package already imports both without cycling.
//
// The spawn config is reconstructed from the sandbox's own spawn.json, which
// the original create wrote — that is what carries the kernel, disk, ch-bin,
// governor bounds and mount set the replacement must boot with. Reading it
// rather than re-deriving those values means the replacement supervises the
// VM with exactly the configuration it was created under.
//
// # How the CA outcome is obtained (NOT re-derived here)
//
// Since s15-ca-persistence the crash path re-seeds the MITM CA persisted by
// the perimeter that minted it, so a re-acquisition USUALLY keeps the guest's
// TLS trust intact. This function must therefore not assert an outcome: it
// only spawns the replacement, and the CA decision is made afterwards, inside
// that detached process ([supervisor.RunReacquire] via reacquireSeedInput).
//
// The outcome is read back from the state dir the replacement wrote it to
// ([supervisor.ReadCAOutcome]), which the replacement records strictly before
// the pidfile this spawn waits on. Deliberately NOT re-loading the CA here to
// work out the answer: a checker that re-derives a result by copying the
// mechanism breaks identically when the mechanism breaks, and still agrees
// with itself.
//
// An unwritten or unreadable outcome yields [recovery.CAUnknown] and is
// reported as undetermined. It is never coerced to "recovered": claiming TLS
// survived when it did not is worse than the mis-report this replaced.
func spawnReplacementSupervisor(sb domain.Sandbox) (recovery.CAOutcome, error) {
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return recovery.CAUnknown, fmt.Errorf("resolve state directory: %w", err)
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)

	spawnSpec, err := supervisor.ReadSpawnSpec(stateDir)
	if err != nil {
		// Without the original spawn spec the replacement would have to guess
		// the kernel, disk and governor bounds. Refuse rather than boot a
		// supervisor against a configuration the VM was not created with.
		return recovery.CAUnknown, fmt.Errorf("read spawn spec for %s: %w", sb.ID, err)
	}

	if _, err := doSpawnReacquireDetached(supervisor.SpawnConfig{
		Config:       spawnSpec,
		ReadyTimeout: replacementSupervisorReadyTimeout,
	}); err != nil {
		return recovery.CAUnknown, err
	}
	// Read the outcome from the state dir the spawn config actually used, not
	// from a separately re-derived path, so the reader and the writer cannot
	// disagree about where the file lives.
	return caOutcomeFromSupervisor(supervisor.ReadCAOutcome(spawnSpec.StateDir)), nil
}

// caOutcomeFromSupervisor maps the supervisor package's CA outcome onto the
// recovery package's. Two enums exist only because internal/core/recovery must
// not import internal/supervisor; this function is the single seam that owns
// both imports.
//
// The default arm is CAUnknown, so an outcome value this build does not
// recognise degrades to "could not determine" rather than to either definite
// claim.
func caOutcomeFromSupervisor(o supervisor.CAOutcome) recovery.CAOutcome {
	switch o {
	case supervisor.CAOutcomeRecovered:
		return recovery.CARecovered
	case supervisor.CAOutcomeLost:
		return recovery.CALost
	default:
		return recovery.CAUnknown
	}
}

// replacementSupervisorReadyTimeout bounds how long recovery waits for a
// spawned replacement to report ready (its pidfile appearing, which happens
// only after it has re-acquired the perimeter and bound its IPC socket).
// Generous: the re-acquisition itself is a socket round-trip, but the
// perimeter bring-up behind it starts gvproxy and the MITM proxy.
const replacementSupervisorReadyTimeout = 60 * time.Second
