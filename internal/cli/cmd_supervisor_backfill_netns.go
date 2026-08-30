package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "supervisor-backfill-netns-identity",
		Summary: "Reconstruct and persist the netns identity for a sandbox created before slice 04, so supervisor-upgrade can adopt it",
		Run:     runSupervisorBackfillNetnsIdentity,
	})
}

// Error codes for the refusal paths below. As with supervisor-upgrade, every
// check up to the mutation boundary is read-only.
const (
	backfillNetnsNotRunningCode        = "backfill_netns_not_running"
	backfillNetnsNoSupervisorCode      = "backfill_netns_no_supervisor"
	backfillNetnsAlreadyPresentCode    = "backfill_netns_already_present"
	backfillNetnsNoSpawnSpecCode       = "backfill_netns_no_spawn_spec"
	backfillNetnsReconstructFailedCode = "backfill_netns_reconstruct_failed"
	backfillNetnsPersistFailedCode     = "backfill_netns_persist_failed"
)

func runSupervisorBackfillNetnsIdentity(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("supervisor-backfill-netns-identity", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}
	positionals := fs.Args()
	if len(positionals) != 1 {
		return &UsageError{Msg: "supervisor-backfill-netns-identity: usage: supervisor-backfill-netns-identity <sandbox>"}
	}
	ref := positionals[0]

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "supervisor-backfill-netns-identity: " + err.Error(), Err: err}
	}
	return runSupervisorBackfillNetnsIdentityWith(ctx, ref, out, svc)
}

// runSupervisorBackfillNetnsIdentityWith is the testable entry point for
// "supervisor-backfill-netns-identity <sandbox>".
//
// # Why a separate verb, not a supervisor-upgrade flag
//
// The ticket requires that identity backfill never happen as a silent side
// effect of another command. A flag on supervisor-upgrade (e.g.
// "--backfill-netns-identity") would satisfy "explicit" for the operator
// typing the command, but it would also mean supervisor-upgrade's refusal
// path — which today is provably read-only, see its own doc comment — grows
// a mutation branch reachable from the same verb whose entire safety
// argument rests on refusals never mutating anything. Keeping backfill in
// its own verb means:
//   - supervisor-upgrade's read-only refusal invariant needs no new caveat;
//   - a backfill failure and an upgrade failure are distinguishable error
//     codes/verbs in logs and scripts, rather than one verb's exit code
//     meaning two different things depending on a flag;
//   - the operator runs backfill once per pre-slice-04 sandbox, sees it
//     succeed, and then runs the ordinary supervisor-upgrade — the two
//     concerns (acquire an identity; use an identity) stay decomposed the
//     same way AdoptNetnsRuntime already decomposes "verify" from "use".
//
// # Refusals
//
// Every check up to and including the spawn spec read is read-only; nothing
// is mutated until svc.SetNetnsIdentity is called:
//   - sandbox does not exist / cannot be resolved
//   - sandbox is not domain.Running
//   - the sandbox record ALREADY has a complete netns identity (nothing to
//     backfill — this verb is not a way to silently overwrite a live
//     identity with a freshly reconstructed one)
//   - the supervisor socket is absent, or its pid is dead, or its pid is
//     alive but the socket does not accept a connection
//   - no persisted spawn spec exists for the sandbox (needed to compute the
//     expected CH API socket path)
//   - supervisor.BackfillNetnsIdentity itself refuses: zero, or more than
//     one, netns-child candidate under the live supervisor pid; an
//     unreadable /proc/<pid>/environ; or the candidate vanishing/changing
//     ppid between selection and settling
func runSupervisorBackfillNetnsIdentityWith(ctx context.Context, ref string, out *Output, svc *service.Service) error {
	sb, err := svc.ResolveRef(ctx, ref)
	if err != nil {
		return errSandbox("supervisor-backfill-netns-identity", err)
	}

	if sb.State != domain.Running {
		return &CodedError{
			Code: backfillNetnsNotRunningCode,
			Msg:  fmt.Sprintf("supervisor-backfill-netns-identity: sandbox %s is not running", sb.ID),
		}
	}

	if sb.NetnsChildPID > 0 && sb.NetnsChildPGID > 0 && sb.NetnsChildStartTime != 0 &&
		sb.GuestTapName != "" && sb.CHAPISocket != "" {
		return &CodedError{
			Code: backfillNetnsAlreadyPresentCode,
			Msg:  fmt.Sprintf("supervisor-backfill-netns-identity: sandbox %s already has a complete netns identity; nothing to backfill", sb.ID),
		}
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "supervisor-backfill-netns-identity: resolve state directory: " + err.Error(), Err: err}
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	sockPath := supervisor.SockPath(stateDir)

	if !supervisorSockLooksAlive(sb.SupervisorPID, sockPath) {
		return &CodedError{
			Code: backfillNetnsNoSupervisorCode,
			Msg:  fmt.Sprintf("supervisor-backfill-netns-identity: sandbox %s has no live supervisor (is it running?)", sb.ID),
		}
	}

	spawnSpec, err := supervisor.ReadSpawnSpec(stateDir)
	if err != nil {
		return &CodedError{
			Code: backfillNetnsNoSpawnSpecCode,
			Msg:  fmt.Sprintf("supervisor-backfill-netns-identity: no spawn spec for %s: %v", sb.ID, err),
		}
	}

	// Deterministic per-sandbox CH API socket path — mirrors
	// cloudhypervisor.(*CHDriver).socketPath (driver.go:508-510), the same
	// convention cmd_orca.go:747 (orcaSocketDir) already documents as a
	// duplicated-by-necessity formula rather than an importable helper. This
	// is what binds a matched netns child to THIS sandbox and not some other
	// sandbox's.
	expectedAPISocket := filepath.Join(spawnSpec.SocketDir, sb.ID.String()+".sock")

	identity, err := supervisor.BackfillNetnsIdentity(sb.SupervisorPID, expectedAPISocket)
	if err != nil {
		return &CodedError{
			Code: backfillNetnsReconstructFailedCode,
			Msg:  fmt.Sprintf("supervisor-backfill-netns-identity: %v", err),
		}
	}

	// ── Everything above is read-only. Mutation starts here. ──────────────

	if err := svc.SetNetnsIdentity(ctx, sb.ID, identity.ChildPID, identity.ChildPGID, identity.ChildStartTime, identity.GuestTapName, identity.APISocket); err != nil {
		return &CodedError{
			Code: backfillNetnsPersistFailedCode,
			Msg:  fmt.Sprintf("supervisor-backfill-netns-identity: persist identity: %v", err),
		}
	}

	out.EmitSuccess("supervisor.backfill_netns_identity",
		map[string]string{
			"sandbox": sb.ID.String(),
			"pid":     fmt.Sprintf("%d", identity.ChildPID),
			"pgid":    fmt.Sprintf("%d", identity.ChildPGID),
			"tap":     identity.GuestTapName,
		},
		fmt.Sprintf("sandbox %s netns identity backfilled: pid=%d pgid=%d tap=%s", sb.ID, identity.ChildPID, identity.ChildPGID, identity.GuestTapName))
	return nil
}
