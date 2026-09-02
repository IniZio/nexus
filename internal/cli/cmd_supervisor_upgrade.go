package cli

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "supervisor-upgrade",
		Summary: "Replace a running sandbox's supervisor with the current binary, without rebooting the guest",
		Run:     runSupervisorUpgrade,
	})
}

// These are package-level variables so unit tests can replace them with stubs
// that avoid spawning a real supervisor process. The production paths always
// use the real supervisor package implementations.
var doSpawnAdoptDetached = supervisor.SpawnAdoptDetached
var doRequestHandoff = supervisor.RequestHandoff

// Error codes for the refusal paths below. Each one is returned before any
// mutation — no store write, no process spawn, no IPC call that could change
// ownership of anything.
const (
	supervisorUpgradeNotRunningCode      = "supervisor_upgrade_not_running"
	supervisorUpgradeNoSupervisorCode    = "supervisor_upgrade_no_supervisor"
	supervisorUpgradeIncompleteNetnsCode = "supervisor_upgrade_incomplete_netns_identity"
	supervisorUpgradeNoopCode            = "supervisor_upgrade_noop_same_binary"
	supervisorUpgradeNoSpawnSpecCode     = "supervisor_upgrade_no_spawn_spec"
	supervisorUpgradeSpawnFailedCode     = "supervisor_upgrade_spawn_failed"
	supervisorUpgradeHandoffFailedCode   = "supervisor_upgrade_handoff_failed"
	supervisorUpgradeHandoffRefusedCode  = "supervisor_upgrade_handoff_refused"
)

func runSupervisorUpgrade(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("supervisor-upgrade", flag.ContinueOnError)
	forceFlag := fs.Bool("force", false, "upgrade even when the running supervisor already reports the current binary")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}
	positionals := fs.Args()
	if len(positionals) != 1 {
		return &UsageError{Msg: "supervisor-upgrade: usage: supervisor-upgrade [--force] <sandbox>"}
	}
	ref := positionals[0]

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "supervisor-upgrade: " + err.Error(), Err: err}
	}
	return runSupervisorUpgradeWith(ctx, ref, *forceFlag, out, svc)
}

// supervisorSockLooksAlive reports whether pid is alive AND sockPath accepts
// a connection, without mutating anything on disk — deliberately more
// conservative than [supervisor.CheckAndReconcile], which also cleans up
// stale artifact files as a side effect. This verb's refusal paths must not
// touch the filesystem at all, so the liveness check here is read-only.
func supervisorSockLooksAlive(pid int, sockPath string) bool {
	if pid <= 0 || !supervisor.PidAlive(pid) {
		return false
	}
	if sockPath == "" {
		return false
	}
	conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// runSupervisorUpgradeWith is the testable entry point for
// "supervisor-upgrade <sandbox>". It performs a planned supervisor
// replacement over a VM that is already running: spawn the current binary
// in adopt mode, drive the handoff, and report the new supervisor's pid and
// binary identity.
//
// # Refusals
//
// Every check up to and including the spawn spec read is read-only —
// nothing is mutated until [supervisor.SpawnAdoptDetached] is called. Each
// refusal below returns a non-nil error and touches neither the sandbox
// record nor any process:
//
//   - sandbox does not exist / cannot be resolved
//   - sandbox is not domain.Running
//   - the supervisor socket is absent, or its pid is dead, or its pid is
//     alive but the socket does not accept a connection (stale/PID-recycled)
//   - the persisted netns identity is incomplete: a zero, absent, or (by
//     construction, since these are typed ints/strings on the record — an
//     unreadable record fails earlier at ResolveRef) unreadable
//     NetnsChildPID, NetnsChildPGID, or NetnsChildStartTime, or an empty
//     GuestTapName/CHAPISocket, REFUSES. There is no "skip when absent, for
//     old records" branch — a sandbox whose record predates these fields
//     looks identical to one where they were lost, and both must refuse.
//   - the running supervisor already reports the same binary-identity hash
//     as this CLI process's own executable (nothing to upgrade) AND its agent
//     RPC channel probes healthy. If the running supervisor cannot be asked
//     (predates /supervisor/version, or the request errors), the
//     binary-identity check degrades to "unknown, proceed" rather than
//     refusing — an old supervisor is exactly the case this verb exists to
//     upgrade. The health probe added by this slice does NOT get a
//     symmetrical "predates the endpoint → assume healthy" escape hatch: a
//     supervisor whose agent-health probe cannot be reached is treated as
//     "not proven healthy", same as force, and the upgrade proceeds — a
//     redundant upgrade against a healthy-but-old supervisor is harmless,
//     while silently trusting an unreachable health probe would recreate
//     exactly the "wedged supervisor with no escape but a reboot" defect
//     this flag exists to fix.
//   - forceFlag bypasses BOTH of the above and always proceeds — the
//     explicit escape hatch for an operator who already knows the
//     supervisor needs replacing.
//   - no persisted spawn spec exists for the sandbox (spawnPersistedSupervisor
//     already refuses the same way for a boot-mode respawn)
func runSupervisorUpgradeWith(ctx context.Context, ref string, force bool, out *Output, svc *service.Service) error {
	sb, err := svc.ResolveRef(ctx, ref)
	if err != nil {
		return errSandbox("supervisor-upgrade", err)
	}

	if sb.State != domain.Running {
		return &CodedError{
			Code: supervisorUpgradeNotRunningCode,
			Msg:  fmt.Sprintf("supervisor-upgrade: sandbox %s is not running", sb.ID),
		}
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "supervisor-upgrade: resolve state directory: " + err.Error(), Err: err}
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	sockPath := supervisor.SockPath(stateDir)

	if !supervisorSockLooksAlive(sb.SupervisorPID, sockPath) {
		return &CodedError{
			Code: supervisorUpgradeNoSupervisorCode,
			Msg:  fmt.Sprintf("supervisor-upgrade: sandbox %s has no live supervisor (is it running?)", sb.ID),
		}
	}

	// Fail-closed netns identity guard (this motive's most commonly broken
	// rail): a zero, absent, or otherwise incomplete value REFUSES. There is
	// no compatibility branch that treats an absent field as "skip the
	// check" — that has shipped three fail-open defects in this motive.
	if sb.NetnsChildPID <= 0 || sb.NetnsChildPGID <= 0 || sb.NetnsChildStartTime == 0 ||
		sb.GuestTapName == "" || sb.CHAPISocket == "" {
		return &CodedError{
			Code: supervisorUpgradeIncompleteNetnsCode,
			Msg:  fmt.Sprintf("supervisor-upgrade: sandbox %s has an incomplete netns identity; refusing to adopt", sb.ID),
		}
	}

	// HashOwnBinary is called unconditionally so myHash is available for the
	// success line below: SpawnAdoptDetached always installs the current binary,
	// so its hash IS the post-upgrade answer regardless of what the new
	// supervisor reports back (which may race with socket rebind).
	myHash, myHashErr := supervisor.HashOwnBinary()

	if !force {
		if myHashErr == nil {
			if runningHash, verErr := supervisor.RequestSupervisorVersion(ctx, sockPath); verErr == nil && runningHash == myHash {
				// Same binary by hash. That alone is not enough to call this a
				// true no-op: a supervisor can be running the right binary and
				// still have a dead agent RPC channel (D-HSH incident,
				// 2026-08-31 — a transient vsock timeout left an adopted
				// supervisor's control plane wedged with no reconnect path and
				// no way to force a re-adopt). Ask the running supervisor to
				// actually probe its own channel before trusting the hash
				// match.
				health, healthErr := supervisor.RequestAgentHealth(ctx, sockPath)
				if healthErr == nil && health.Healthy() {
					return &CodedError{
						Code: supervisorUpgradeNoopCode,
						Msg:  fmt.Sprintf("supervisor-upgrade: sandbox %s is already served by the current binary and its agent channel is healthy; nothing to upgrade", sb.ID),
					}
				}
				// healthErr != nil (probe unreachable — old supervisor,
				// transport failure) or health.Healthy() == false (probe ran
				// and did NOT prove healthy, including AgentChannelUnknown):
				// in both cases this is deliberately NOT treated as "assume
				// healthy, refuse to upgrade" — proceed to replace the
				// supervisor instead.
			}
			// verErr != nil: the running supervisor predates /supervisor/version
			// or the request failed transiently. Proceed — refusing here would
			// make an old, unresponsive supervisor permanently un-upgradeable.
		}
	}

	spawnSpec, err := supervisor.ReadSpawnSpec(stateDir)
	if err != nil {
		return &CodedError{
			Code: supervisorUpgradeNoSpawnSpecCode,
			Msg:  fmt.Sprintf("supervisor-upgrade: no spawn spec for %s: %v", sb.ID, err),
		}
	}

	// ── Everything above is read-only. Mutation starts here. ──────────────

	handoffSock := filepath.Join(stateDir, fmt.Sprintf("handoff-%d.sock", os.Getpid()))
	newPid, spawnErr := doSpawnAdoptDetached(supervisor.SpawnConfig{
		Config:           spawnSpec,
		ReadyTimeout:     30 * time.Second,
		AdoptHandoffSock: handoffSock,
	})
	if spawnErr != nil {
		return &CodedError{
			Code: supervisorUpgradeSpawnFailedCode,
			Msg:  fmt.Sprintf("supervisor-upgrade: spawn replacement: %v", spawnErr),
		}
	}

	ok, reqErr := doRequestHandoff(ctx, sockPath, handoffSock)
	if reqErr != nil {
		// Transport/protocol failure talking to the OUTGOING supervisor — it
		// never received (or never answered) the handoff request, so it is
		// still the sole owner. The spawned replacement is left waiting on
		// its own accept deadline and will exit on its own; give it a
		// SIGTERM nudge so it does not sit around for the full timeout.
		_ = terminateAdoptSpawn(newPid)
		return &CodedError{
			Code: supervisorUpgradeHandoffFailedCode,
			Msg:  fmt.Sprintf("supervisor-upgrade: request handoff: %v", reqErr),
		}
	}
	if !ok {
		// Clean refusal per D-HSH-08: the outgoing supervisor remains sole
		// owner and nothing changed. The spawned replacement independently
		// reaches the same conclusion (its Accept either never fires or its
		// own validation refuses) and exits on its own.
		_ = terminateAdoptSpawn(newPid)
		return &CodedError{
			Code: supervisorUpgradeHandoffRefusedCode,
			Msg:  fmt.Sprintf("supervisor-upgrade: handoff refused; sandbox %s remains on the previous supervisor", sb.ID),
		}
	}

	// Handoff confirmed: ownership has moved. Persist the new supervisor
	// identity so future commands (stop, egress allow, another upgrade) find
	// it. sockPath is unchanged — the new process rebinds the same path.
	if setErr := svc.SetSupervisor(ctx, sb.ID, newPid, sockPath); setErr != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "supervisor-upgrade: persist new supervisor: " + setErr.Error(), Err: setErr}
	}

	// myHash is the hash of THIS process's binary — the one SpawnAdoptDetached
	// just installed. Asking the new supervisor to identify itself via
	// RequestSupervisorVersion races with its socket rebind and the error was
	// previously silently discarded, leaving the field blank. Use the known
	// value instead; fall back to "unknown" only if HashOwnBinary itself
	// failed (fail-closed rail: never render a legitimately-blank value).
	newHash := myHash
	if myHashErr != nil {
		newHash = "unknown"
	}

	out.EmitSuccess("supervisor.upgrade",
		map[string]string{"sandbox": sb.ID.String(), "pid": fmt.Sprintf("%d", newPid), "binary_hash": newHash},
		fmt.Sprintf("sandbox %s now served by supervisor pid %d (binary %s)", sb.ID, newPid, newHash))
	return nil
}

// terminateAdoptSpawn kills a spawned adopt-mode process that the CLI has
// decided will never receive (or already failed) a confirmed handoff. This
// is always safe to do unconditionally with SIGKILL: this function is only
// called on a path where RequestHandoff did not return (true, nil), so per
// D-HSH-08 the outgoing supervisor never received a positive Ack and the
// spawned process — whatever internal state it reached — has not bound the
// canonical IPC socket or taken any externally visible ownership action
// (see RunAdopt's doc comment). It is also not the netns child's parent, so
// killing it cannot cascade to the VM's process group.
//
// Best-effort: the process also times out and exits on its own via
// adoptHandoffAcceptTimeout if this signal is lost or fails to deliver.
func terminateAdoptSpawn(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
