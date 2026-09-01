package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

// netnsRunEnv and netnsEnvAPISocket mirror
// cloudhypervisor.NetnsRunEnv / the unexported netnsEnvAPISocket
// (internal/core/driver/cloudhypervisor/ch_netns.go). They cannot be
// imported directly: internal/core/driver/cloudhypervisor's test package
// imports internal/core/recovery, which imports internal/core/service — so
// service importing cloudhypervisor would close an import cycle at test-build
// time. Values are the wire contract StartNetnsRuntime sets on every netns
// child's own environ (ch_netns.go:227-233) and are effectively frozen ABI
// between the two packages; NetnsEnvAPISocket is separately re-exported from
// the same package for the same reason by internal/supervisor/netns_backfill.go.
const (
	netnsRunEnv       = "NEXUS3_NETNS_RUN"
	netnsEnvAPISocket = "NEXUS3_NETNS_API_SOCKET"
)

// ReapStatus classifies a host resource for reclamation purposes.
type ReapStatus string

const (
	// ReapStatusOrphan means no record and no live process: resource is safe
	// to delete.
	ReapStatusOrphan ReapStatus = "orphan"
	// ReapStatusLive means a live process or responsive socket was found for
	// this ULID: keep it (likely a sandbox mid-create).
	ReapStatusLive ReapStatus = "live"
	// ReapStatusOwned means a store record exists for this ULID: keep it
	// (stale-record cleanup is R2's job).
	ReapStatusOwned ReapStatus = "owned"
	// ReapStatusSuspect means the resource could NOT be definitively
	// classified as orphan, owned, or live — reap does not know enough to
	// rule out that this is a live orphan. This is the fail-closed rail
	// (ticket 10): "cannot determine" must be reported LOUDLY, not folded
	// into a clean report. Distinct from ReapStatusLive (which means reap
	// affirmatively confirmed liveness) and from ReapStatusOrphan (which
	// means reap affirmatively confirmed no owner exists). --apply never
	// touches a Suspect entry.
	ReapStatusSuspect ReapStatus = "suspect"
)

// ReapEntry describes one host resource and its classification.
type ReapEntry struct {
	Resource       HostResource
	Status         ReapStatus
	Reason         string
	AllocatedBytes int64 // syscall.Stat_t.Blocks * 512; 0 for dirs

	// ProcessPID and ProcessPGID identify the live process behind a
	// Resource.Kind == KindNetnsProcess entry (see sweepOrphanNetnsProcesses).
	// Zero for every other kind. --apply reclaims these by killing the
	// process group (ProcessPGID), not by removing a file.
	ProcessPID  int
	ProcessPGID int
}

// ReapFailure records an orphan that --apply tried and failed to reclaim.
//
// Before TBD-PD-37 these were dropped on the floor: the resource simply did
// not appear in Deleted, with no message, no report line and no non-zero exit,
// which made `--apply` unverifiable from its own output. A caller could not
// tell "129 orphans, all deleted" from "129 orphans, 128 deleted".
type ReapFailure struct {
	Path   string
	Kind   ResourceKind
	Reason string
}

// ReapReport is the result of a Reap call.
type ReapReport struct {
	Entries          []ReapEntry
	ReclaimableBytes int64    // sum of orphan AllocatedBytes
	Deleted          []string // paths removed (non-empty only when apply=true)

	// Failed lists orphans that --apply could not reclaim. Two things land
	// here: a delete that returned an error, and a delete that returned
	// success while the path was still present on the verify pass. The second
	// case exists because a reported success is not evidence of a removal.
	Failed []ReapFailure

	// KilledPIDs lists the process-group leader pids that --apply killed to
	// reclaim a KindNetnsProcess orphan (see sweepOrphanNetnsProcesses).
	// Empty when apply=false or no netns-process orphans were found.
	KilledPIDs []int

	// ZombieProcesses is the count of zombie processes encountered during the
	// netns sweep. A zombie has no mm, no tap, and no netns — it cannot be a
	// live netns child. The dominant real-world source is virtiofsd and nexus3
	// child processes whose parent holds a missing Wait() call; counting these
	// separately keeps the structural leak visible without raising a false alarm.
	// Does NOT affect the exit code.
	ZombieProcesses int

	// UninspectableProcesses is the count of non-zombie, own-uid processes
	// skipped during the netns sweep because /proc/<pid> or
	// /proc/<pid>/environ was unreadable. The common causes are a cleared
	// dumpable flag (sshd, systemd --user session leaders after setuid exec)
	// and a process that vanished between ReadDir and stat. This is NOT a
	// ptrace-scope restriction: Yama only gates PTRACE_MODE_ATTACH;
	// /proc/<pid>/environ uses PTRACE_MODE_READ_FSCREDS, which own-uid
	// non-descendant processes can read just fine under ptrace_scope=1. These
	// processes are NOT ReapEntries; the count does NOT affect the exit code.
	UninspectableProcesses int
}

// ReapOptions provides injectable overrides for Reap. All fields are optional;
// the zero value selects production defaults. Use in tests to avoid real /proc
// and real sockets.
type ReapOptions struct {
	// ProcDir overrides the directory scanned for process cmdlines.
	// Empty → "/proc". In tests pass a temp dir containing synthetic PID entries.
	ProcDir string

	// VerifyStat overrides the existence probe used by the post-apply verify
	// pass. Empty → os.Lstat.
	//
	// It exists because the survivor observed on 2026-08-19 — a delete that
	// reported success while the file remained — has no known cause and so
	// cannot be reproduced by arranging real files. This seam lets a test
	// drive the DETECTION end-to-end through Reap even though the mechanism
	// that produces a survivor is unexplained.
	VerifyStat func(path string) error

	// ProcOwnerLookup overrides the uid lookup for /proc/<pid>. Empty →
	// defaultProcPIDOwnerUID (real os.Stat). Tests inject a function that
	// returns a foreign uid to verify that foreign-uid processes produce no
	// report entry at all — not Suspect, not Orphan, just silently skipped.
	ProcOwnerLookup func(procDir string, pid int) (uint32, error)

	// AllowRealProcKill must be set to true when apply=true is used with the
	// real /proc (i.e. ProcDir is empty, which defaults to "/proc"). It acts as
	// an explicit opt-in so that a test which forgets to set ProcDir gets a
	// clear error instead of silently sending SIGKILL to live host processes.
	// The production CLI path (runReapWith) sets this; tests must not.
	AllowRealProcKill bool

	// NetnsKillFn, when non-nil, is called in place of the package-level
	// killNetnsProcessFn when reclaiming a KindNetnsProcess orphan. It MUST be
	// set whenever ProcDir is not "/proc" (a synthetic ProcDir). This is the
	// structural guard that prevents a test from accidentally reaching the real
	// syscall.Kill via a pgid read from a synthetic /proc entry that happens to
	// match a live host process group: with a synthetic ProcDir the only path to
	// killing is through the fn the caller explicitly provides. Tests that need
	// to exercise a real kill (e.g. against their own spawned child) pass their
	// own syscall.Kill wrapper here. Tests that only need to prove the Orphan
	// classification path use a no-op or recording stub.
	//
	// When nil and ProcDir == "/proc" the package-level killNetnsProcessFn
	// (which calls syscall.Kill) is used — that is the production path.
	NetnsKillFn func(pgid int) error
}

// defaultProcPIDOwnerUID stats /proc/<pid> and returns its owner uid, which
// is the process's real uid on Linux.
func defaultProcPIDOwnerUID(procDir string, pid int) (uint32, error) {
	info, err := os.Stat(filepath.Join(procDir, strconv.Itoa(pid)))
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected Sys() type %T for /proc/%d", info.Sys(), pid)
	}
	return st.Uid, nil
}

// Reap enumerates host resources via idx, classifies each one against the
// store, and (when apply=true) deletes orphans.
//
// Classification rules for ULID-keyed resources:
//
//   - ReapStatusOwned: a store record exists → skip (R2's job).
//   - ReapStatusLive:  no record, but a liveness check detects a create in
//     flight (leased intent file), a running process, or a responsive API
//     socket → skip.
//   - ReapStatusOrphan: no record and all liveness checks definitive-dead →
//     reclaimable.
//
// Classification rules for KindDiskShadow resources (§4.4 supplementary):
//
//   - Legacy format (ShadowHandle == ""): unconditionally ReapStatusOrphan.
//   - B1 format: ReapStatusOwned when ShadowHandle matches a sandbox record;
//     ReapStatusOrphan otherwise.
func Reap(ctx context.Context, st store.Store, idx *ResourceIndex, apply bool, opts ...ReapOptions) (*ReapReport, error) {
	var opt ReapOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.ProcDir == "" {
		opt.ProcDir = "/proc"
	}
	if opt.ProcOwnerLookup == nil {
		opt.ProcOwnerLookup = defaultProcPIDOwnerUID
	}
	if apply && opt.ProcDir == "/proc" && !opt.AllowRealProcKill {
		return nil, fmt.Errorf("reap: apply=true with real /proc requires AllowRealProcKill=true in ReapOptions; set it only on the production CLI path, never in tests")
	}
	resources, err := idx.List()
	if err != nil {
		return nil, fmt.Errorf("reap: enumerate resources: %w", err)
	}

	// Identify sandboxes whose create is still in flight. This MUST happen
	// before the store snapshot below — the order is the correctness argument,
	// not an optimisation.
	//
	// A create materialises its disks seconds before it commits the store
	// record (create.go: writeCreateIntent → cowExt4 → … → store.Create), and
	// during that window nothing carries the ULID in its cmdline, so the /proc
	// gate cannot see it. The flock lease on the intent file is the signal that
	// closes that window; the kernel releases it if the creator dies, so a
	// crashed create is still reclaimable on the next reap.
	//
	// # Why probe BEFORE st.List
	//
	// The reaper cannot observe the filesystem and the store atomically, so the
	// order decides whether the two observations can BOTH miss a live sandbox.
	// The creator's release sequence is: store.Create commits → CreateAndBoot
	// returns → the deferred release unlinks the intent and drops the lease.
	// Release therefore strictly FOLLOWS the record becoming visible.
	//
	// With probe-then-list, every outcome is safe:
	//
	//   - probe says leased → in flight → keep. Safe.
	//   - probe says not leased → the creator either never published a lease
	//     (crashed create, or a legacy intent) or already released it. Release
	//     follows store.Create, so if it had released, the record was already
	//     committed BEFORE this probe — and st.List runs AFTER the probe, so it
	//     must see that record → Owned → keep. Safe.
	//
	// Listing records first inverts this: the record snapshot could be taken
	// before the commit while the lease probe happens after the release, so a
	// sandbox that is live throughout appears in neither set and its disk is
	// unlinked. That is the residual race this ordering closes.
	//
	// The set is keyed by ULID because a leased intent protects ALL of that
	// sandbox's resources (intent, .raw, workspace disk), each of which is
	// enumerated as a separate HostResource.
	// Maps ULID → the reason to report for keeping it. A leased intent and an
	// unreadable one both keep, but they are reported differently: the first
	// expires when the creator dies, the second does not (see intentLeaseState).
	inFlight := make(map[domain.SandboxID]string)
	for _, res := range resources {
		if res.Kind != KindCreateIntent {
			continue
		}
		switch probeIntentLease(res.Path) {
		case leaseHeld:
			inFlight[res.OwnerID] = fmt.Sprintf("create in flight: intent lease held for %s", res.OwnerID)
		case leaseUnknown:
			inFlight[res.OwnerID] = fmt.Sprintf(
				"intent file unreadable, cannot rule out a live creator — keeping indefinitely; "+
					"inspect %s (this keep does not expire on its own)", res.Path)
		}
	}

	// Shadow disks are correlated by HANDLE, not ULID, so the ULID-keyed
	// inFlight map above cannot answer "is a create for this handle running?".
	// Shadow intents carry that answer; probe their leases the same way
	// (TBD-PD-25). Maps safeHandle → the reason to report for keeping it.
	inFlightShadow := make(map[string]string)
	for _, res := range resources {
		if res.Kind != KindShadowIntent {
			continue
		}
		switch probeIntentLease(res.Path) {
		case leaseHeld:
			inFlightShadow[res.ShadowHandle] = fmt.Sprintf(
				"create in flight: shadow intent lease held for handle %q", res.ShadowHandle)
		case leaseUnknown:
			inFlightShadow[res.ShadowHandle] = fmt.Sprintf(
				"shadow intent unreadable, cannot rule out a live creator — keeping indefinitely; "+
					"inspect %s (this keep does not expire on its own)", res.Path)
		}
	}

	all, err := st.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("reap: list store records: %w", err)
	}
	recordMap := make(map[domain.SandboxID]domain.Sandbox, len(all))
	for _, sb := range all {
		recordMap[sb.ID] = sb
	}

	// Build safeHandle → sandbox ID map for shadow disk correlation (§4.4).
	// safeHandle = strings.ReplaceAll(sb.Handle(), "/", "_").
	shadowHandleMap := make(map[string]domain.SandboxID, len(all))
	for i := range all {
		sh := strings.ReplaceAll(all[i].Handle(), "/", "_")
		shadowHandleMap[sh] = all[i].ID
	}

	// Resolve the socket directory once so liveness helpers can locate .sock
	// files for non-socket resources.
	socketDir, err := idx.socketDir()
	if err != nil {
		return nil, fmt.Errorf("reap: resolve socket dir: %w", err)
	}

	// The state root IS the store root (IndexConfig.StateRoot defaults to
	// store.DefaultRoot(), the same resolver the FileStore is opened with on
	// the CLI path). classifySupervisorState needs it to answer "does this
	// sandbox's record DIRECTORY exist" without decoding the record.
	storeRoot, err := idx.stateRoot()
	if err != nil {
		return nil, fmt.Errorf("reap: resolve state root: %w", err)
	}

	report := &ReapReport{
		Entries: make([]ReapEntry, 0, len(resources)),
	}

	for _, res := range resources {
		var entry ReapEntry
		if res.Kind == KindDiskShadow || res.Kind == KindShadowIntent {
			// Shadow disks and their intents use handle-based correlation,
			// not ULID/liveness.
			entry = classifyShadowDisk(res, shadowHandleMap, recordMap, inFlightShadow)
		} else if res.Kind == KindSupervisorState {
			entry = classifySupervisorState(ctx, res, recordMap, inFlight, socketDir, opt.ProcDir, storeRoot)
		} else {
			entry = classifyResource(ctx, res, recordMap, inFlight, socketDir, opt.ProcDir)
		}
		if entry.Status == ReapStatusOrphan {
			report.ReclaimableBytes += entry.AllocatedBytes
			if entry.Status == ReapStatusOrphan && apply {
				switch err := deleteResourceFn(res); {
				case err == nil:
					report.Deleted = append(report.Deleted, res.Path)
				case os.IsNotExist(err):
					// Already gone — another reaper or a concurrent Remove got
					// there first. Reclamation is idempotent, so this is a
					// success, not a failure.
					report.Deleted = append(report.Deleted, res.Path)
				default:
					report.Failed = append(report.Failed, ReapFailure{
						Path:   res.Path,
						Kind:   res.Kind,
						Reason: err.Error(),
					})
				}
			}
		}
		report.Entries = append(report.Entries, entry)
	}

	// Independent process sweep (ticket 10: "reap is blind to exactly the
	// class it exists to catch").
	//
	// Every entry above is discovered by iterating `resources`, which comes
	// from idx.List() — a filesystem enumeration. That direction can only
	// ever ask "is the owner of this FILE still alive?"; it structurally
	// cannot see a process that owns no file at all. A netns child + CH pair
	// that survives its own cleanup (e.g. the nested-virt preflight leak
	// fixed in driver.go, or any future cleanup gap) has, by the time reap
	// runs, had its disk copy and API socket file already removed by the
	// create-path's own deferred cleanup — so it is invisible to every check
	// above no matter how thorough.
	//
	// This sweep goes the OTHER direction: process -> record. It enumerates
	// LIVE processes carrying the netns-child sentinel
	// (cloudhypervisor.NetnsRunEnv=1, the same marker
	// internal/supervisor/netns_backfill.go uses to identify a sandbox's own
	// netns child) and asks "does any record or in-flight create claim this
	// one?" — the reversed direction the ticket's hypothesis calls for.
	netnsEntries, zombies, inaccessible, sweepErr := sweepOrphanNetnsProcesses(opt.ProcDir, recordMap, inFlight, opt.ProcOwnerLookup)
	if sweepErr != nil {
		return nil, fmt.Errorf("reap: sweep netns processes: %w", sweepErr)
	}
	report.ZombieProcesses = zombies
	report.UninspectableProcesses = inaccessible
	// Resolve the effective kill function: opt.NetnsKillFn when the caller
	// provided one (required for synthetic ProcDir; optional for real /proc),
	// otherwise the package-level killNetnsProcessFn (production default).
	effectiveKillFn := killNetnsProcessFn
	if opt.NetnsKillFn != nil {
		effectiveKillFn = opt.NetnsKillFn
	}
	for _, entry := range netnsEntries {
		if entry.Status == ReapStatusOrphan && apply {
			// Guard: a synthetic ProcDir must supply its own kill function.
			// Without this check, the package-level killNetnsProcessFn (real
			// syscall.Kill) is reachable from any test that plants a directory
			// named after a live host pid under a temp dir, silently signaling
			// that process group. Requiring NetnsKillFn when ProcDir != "/proc"
			// makes the hazard structurally unreachable at the kill site: the
			// caller cannot reach the real Kill without explicitly providing it.
			// The guard fires here (not at Reap entry) so a synthetic ProcDir
			// with NO netns entries (common in non-netns tests) is not rejected.
			if opt.ProcDir != "/proc" && opt.NetnsKillFn == nil {
				return nil, fmt.Errorf("reap: apply=true with a synthetic ProcDir requires NetnsKillFn in ReapOptions; prevents accidentally reaching the real syscall.Kill via a pgid discovered from a synthetic /proc entry")
			}
			if err := effectiveKillFn(entry.ProcessPGID); err != nil {
				report.Failed = append(report.Failed, ReapFailure{
					Path:   entry.Resource.Path,
					Kind:   entry.Resource.Kind,
					Reason: err.Error(),
				})
			} else {
				report.KilledPIDs = append(report.KilledPIDs, entry.ProcessPID)
			}
		}
		report.Entries = append(report.Entries, entry)
	}

	if apply {
		statFn := opt.VerifyStat
		if statFn == nil {
			statFn = func(path string) error {
				_, err := os.Lstat(path)
				return err
			}
		}
		verifyDeletions(report, statFn)
	}

	return report, nil
}

// verifyDeletions re-stats every path Reap believes it deleted and moves any
// survivor into Failed.
//
// This exists because a successful os.Remove is not by itself evidence that
// the resource is gone. During the authorised cleanup on 2026-08-19 an --apply
// pass reported "Deleted 129 resource(s)" and a stub file survived it; a second
// pass removed the same file. Whatever the cause, the report asserted a
// reclamation that had not happened, and nothing in the output contradicted it.
// A verify pass makes that class of discrepancy visible at the moment it
// occurs instead of on the next run.
//
// Survivors are moved out of Deleted rather than merely added to Failed, so
// the two lists stay mutually exclusive and len(Deleted) means what it says.
func verifyDeletions(report *ReapReport, stat func(string) error) {
	if len(report.Deleted) == 0 {
		return
	}
	kept := report.Deleted[:0]
	for _, path := range report.Deleted {
		if err := stat(path); err == nil {
			report.Failed = append(report.Failed, ReapFailure{
				Path:   path,
				Reason: "delete reported success but the path still exists",
			})
			continue
		}
		kept = append(kept, path)
	}
	report.Deleted = kept
}

// classifyShadowDisk classifies a KindDiskShadow resource using handle-based
// supplementary correlation (spec §4.4).
//
// Phase separation: List() extracts the safeHandle from the filename (record-
// free). classifyShadowDisk receives the pre-built handleMap (which required
// record access) and applies the ownership decision here, in the classification
// phase. This keeps the record-free invariant on ResourceIndex.List().
func classifyShadowDisk(
	res HostResource,
	handleMap map[string]domain.SandboxID,
	recordMap map[domain.SandboxID]domain.Sandbox,
	inFlightShadow map[string]string,
) ReapEntry {
	entry := ReapEntry{
		Resource:       res,
		AllocatedBytes: allocatedBytes(res.Path),
	}
	// In-flight check FIRST. During a create the owning handle has no committed
	// record yet, so handleMap cannot see it and every subsequent branch would
	// conclude "orphan" — deleting a live sandbox's shadow disks mid-create,
	// which is the defect this ordering exists to prevent (TBD-PD-25).
	if reason, ok := inFlightShadow[res.ShadowHandle]; ok && res.ShadowHandle != "" {
		entry.Status = ReapStatusLive
		entry.Reason = reason
		return entry
	}
	if res.ShadowHandle == "" {
		// Legacy format (*.shadow.ext4): no embedded handle, no owning sandbox.
		// Unconditionally reclaimable.
		entry.Status = ReapStatusOrphan
		entry.Reason = "legacy shadow disk (no embedded sandbox handle)"
		return entry
	}
	noun := "shadow disk"
	if res.Kind == KindShadowIntent {
		noun = "shadow intent"
	}
	if ownerID, ok := handleMap[res.ShadowHandle]; ok {
		entry.Status = ReapStatusOwned
		entry.Reason = fmt.Sprintf("%s owned by sandbox %s (handle=%s)", noun, ownerID, res.ShadowHandle)
		return entry
	}
	// Fork child copy (RL-14). ForkFrom copies every parent extra disk, and
	// shadow disks ARE extra disks, so a child gets
	// <childULID>-<parentSafeHandle>.shadow.<name>.ext4. That composite matches
	// no sandbox handle, so the handle lookup above can never resolve it and
	// the copy would be orphaned for the child's whole life — deleting a live
	// forked sandbox's dependency tree. Resolve it by the ULID it carries.
	if childID, ok := forkChildShadowOwner(res.ShadowHandle); ok {
		if sb, live := recordMap[childID]; live {
			entry.Status = ReapStatusOwned
			entry.Reason = fmt.Sprintf("%s owned by fork child %s (copied from handle=%s)",
				noun, sb.ID, strings.TrimPrefix(res.ShadowHandle, childID.String()+"-"))
			return entry
		}
	}
	entry.Status = ReapStatusOrphan
	entry.Reason = fmt.Sprintf("%s handle %q matches no live sandbox", noun, res.ShadowHandle)
	return entry
}

// forkChildShadowOwner extracts the child sandbox ULID from a fork-copied
// shadow disk's safeHandle, which ForkFrom builds as
// "<childULID>-<parentSafeHandle>" (see ChildExtraDiskPath).
//
// The ULID's own string form contains a "-" ("sb-06G…"), and a parent handle
// may contain any number more, so the boundary cannot be found by splitting on
// the first or last separator. Instead every "-" position is offered to
// ParseSandboxID and the first that parses wins — the ULID is fixed-width and
// checksummed, so a false positive would require the parent handle to begin
// with a valid ULID.
//
// Returns ok=false for an ordinary (non-fork) safeHandle.
func forkChildShadowOwner(safeHandle string) (domain.SandboxID, bool) {
	for i, r := range safeHandle {
		if r != '-' {
			continue
		}
		if id, err := domain.ParseSandboxID(safeHandle[:i]); err == nil {
			return id, true
		}
	}
	return domain.SandboxID{}, false
}

// supervisorSockFile is the name of the supervisor's own control socket inside
// its state dir. It mirrors supervisor.SockPath (supervisor.go:246), which
// cannot be imported here: internal/supervisor imports internal/core/service.
// This is the same one-way dependency that keeps the state dir PATH in
// internal/core/statedir, and the same reason it is a frozen name rather than
// a shared constant — the socket name is wire contract between the supervisor
// and every CLI verb that dials it.
const supervisorSockFile = "supervisor.sock"

// classifySupervisorState classifies a KindSupervisorState directory
// (<stateRoot>/supervisors/<ULID>/).
//
// This is the fail-closed rail for D-HSH-18. The directory holds the state a
// future re-acquisition needs — spawn.json, and from the CA-persistence slice
// the MITM CA private key — so deleting one belonging to a sandbox that is
// live, or that `nexus3 recover` would classify adoptable, destroys the very
// thing recovery exists to use. When in doubt this KEEPS.
//
// # Why this reuses the reap classification instead of internal/core/recovery
//
// The ticket asks for the recover/adopt judgement, not a second weaker one.
// That judgement cannot be called from here — internal/core/recovery imports
// internal/core/service, so the dependency only runs one way — but it also
// does not need to be, because of what it is made of:
//
//   - recovery classifies STORE RECORDS. Recoverer.Recover iterates st.List
//     and every outcome kind, OutcomeAdoptable included, is reached only from
//     a record (recover.go: recoverByID → applySupervisorLiveness). An
//     adoptable sandbox therefore always HAS a record, and a record here is
//     already ReapStatusOwned — kept, unconditionally, before any liveness
//     question is asked. The live-VM/dead-supervisor class is covered by that
//     branch alone.
//   - the converse — a live VM whose record has been lost — is outside what
//     recovery can classify at all (no record, nothing to iterate), and is
//     covered by the liveness gates classifyResource already applies: the
//     ULID appears in the cloud-hypervisor process cmdline, and its API socket
//     answers.
//
// So the delegation below is the recover judgement, expressed in the terms
// this package can observe, plus two gates recovery has no analogue for.
//
// # Extra gate 1: the record directory on disk
//
// classifyResource's R1 asks "is this ULID in recordMap", and recordMap is
// built from store.List — which SILENTLY SKIPS every record it cannot decode:
// corrupt, half-written by an interrupted Create, or ErrSchemaTooNew
// (filestore.go, List). So R1 alone reads "cannot read the record" as "there
// is no record", which is fail-OPEN, and the failure is not rare: an older
// binary — this repo keeps a stale ./nexus3 in the tree — gets ErrSchemaTooNew
// for EVERY record, so a single `reap --apply` under it would collect the
// state dir of every stopped sandbox on the host at once.
//
// A stopped sandbox has no VM in /proc and no responsive socket, so none of
// the liveness gates catch that. This one does: if the record DIRECTORY exists
// under the store root, the owner record is present and merely unreadable by
// this binary, and a correct-version binary will read it fine. That is a KEEP.
// A stat error other than "not exist" (EACCES, EIO) is ambiguity, and
// ambiguity keeps too.
//
// This gate lives here rather than in the shared classifyResource on purpose:
// other resource kinds have their own risk calculus and are out of scope for
// D-HSH-18.
//
// # Extra gate 2: the supervisor control socket
//
// A supervisor can outlive its own VM socket — that is precisely the wedged
// state ticket 09 exists for. Its control socket lives INSIDE this directory,
// so a responsive supervisor.sock proves a live process is using this exact
// directory even when every ULID-keyed check has gone quiet. Removing the
// directory out from under it would strand a running supervisor. Ambiguity
// (timeout, unexpected error) resolves to KEEP, matching N-AC2 everywhere else
// in this file.
//
// With both gates in place the "ambiguity resolves to KEEP" claim holds of
// EVERY gate this function applies, the record gate included.
func classifySupervisorState(
	ctx context.Context,
	res HostResource,
	recordMap map[domain.SandboxID]domain.Sandbox,
	inFlight map[domain.SandboxID]string,
	socketDir string,
	procDir string,
	storeRoot string,
) ReapEntry {
	entry := classifyResource(ctx, res, recordMap, inFlight, socketDir, procDir)
	if entry.Status != ReapStatusOrphan {
		return entry
	}

	recordDir := store.RecordDir(storeRoot, res.OwnerID)
	switch _, err := os.Stat(recordDir); {
	case err == nil:
		entry.Status = ReapStatusLive
		entry.Reason = fmt.Sprintf(
			"owner record present but unreadable — keeping; record dir %s exists "+
				"(corrupt, half-written, or written by a newer schema than this binary)", recordDir)
		return entry
	case !os.IsNotExist(err):
		entry.Status = ReapStatusLive
		entry.Reason = fmt.Sprintf(
			"owner record dir %s could not be stat'd (%v) — cannot rule out a record, keeping", recordDir, err)
		return entry
	}

	sockPath := filepath.Join(res.Path, supervisorSockFile)
	alive, ambiguous := socketResponsive(ctx, sockPath)
	if ambiguous {
		entry.Status = ReapStatusLive
		entry.Reason = fmt.Sprintf("supervisor control socket %s check ambiguous — keeping", sockPath)
		return entry
	}
	if alive {
		entry.Status = ReapStatusLive
		entry.Reason = fmt.Sprintf("supervisor control socket %s is responsive", sockPath)
		return entry
	}

	entry.Reason = "supervisor state dir: no record dir on disk, no live process, no responsive VM or supervisor socket"
	return entry
}

// classifyResource determines the ReapStatus of a single host resource.
func classifyResource(
	ctx context.Context,
	res HostResource,
	recordMap map[domain.SandboxID]domain.Sandbox,
	inFlight map[domain.SandboxID]string,
	socketDir string,
	procDir string,
) ReapEntry {
	entry := ReapEntry{
		Resource:       res,
		AllocatedBytes: allocatedBytes(res.Path),
	}

	// R1: a store record exists → owned, skip.
	if sb, ok := recordMap[res.OwnerID]; ok {
		entry.Status = ReapStatusOwned
		entry.Reason = fmt.Sprintf("owner %s has record (state=%s)", sb.ID, sb.State)
		return entry
	}

	// Liveness gate, step 0: a create-intent lease held by a live creator.
	// This precedes the /proc scan because it is both cheaper and strictly more
	// informative during the create window — the window in which the /proc scan
	// is blind (create.go step 3.6 → step 6). Deleting here would destroy the
	// disk of a create that is still running.
	if reason, ok := inFlight[res.OwnerID]; ok {
		entry.Status = ReapStatusLive
		entry.Reason = reason
		return entry
	}

	idStr := res.OwnerID.String()

	// Liveness gate (TBD-PD-11) — three-way result; ambiguity → KEEP (N-AC2).
	// 1. /proc/*/cmdline scan: LIVE if found; AMBIGUOUS if scan incomplete.
	switch scanProcForULID(procDir, idStr) {
	case procScanLive:
		entry.Status = ReapStatusLive
		entry.Reason = "found ULID in /proc cmdline"
		return entry
	case procScanAmbiguous:
		// Scan was inconclusive (unreadable proc entry, truncated cmdline, etc.)
		// Ambiguity resolves to keep per N-AC2.
		entry.Status = ReapStatusLive
		entry.Reason = "proc scan inconclusive (unreadable entry or truncated cmdline) — keeping"
		return entry
	// procScanDead: continue to socket check.
	}

	// 3. API socket liveness (for non-socket resources, derive the socket path
	//    from the socket directory).
	sockPath := socketPathForID(res, socketDir)
	if sockPath != "" {
		alive, ambiguous := socketResponsive(ctx, sockPath)
		if ambiguous {
			entry.Status = ReapStatusLive
			entry.Reason = "socket check ambiguous — keeping"
			return entry
		}
		if alive {
			entry.Status = ReapStatusLive
			entry.Reason = fmt.Sprintf("API socket %s is responsive", sockPath)
			return entry
		}
	}

	// All checks definitive-dead.
	entry.Status = ReapStatusOrphan
	entry.Reason = "no record, no live process, no responsive socket"
	return entry
}

// socketPathForID returns the socket path to probe for the given resource's
// liveness check.
//
// For a socket-kind resource the probe target is the socket file itself, and
// resource_index.go populates res.Path with its real path for every socket
// kind (.sock, .vsock, .iid) — so return that directly. The earlier code
// fabricated "<socketDir>/<id>.sock" for the .vsock/.iid kinds, probing a path
// that never exists (TBD-PD-23).
//
// For a NON-socket resource (a raw disk, a create-intent, etc.) res.Path is
// that resource's own file, not a socket. The liveness gate still wants to
// probe the owner sandbox's API socket, so derive it from the socket directory
// and the owner id. Returning res.Path here would make the probe target the
// resource's own file (e.g. the .create-intent.json), which exists and thus
// wrongly reads as "live", stranding the resource from reclamation.
func socketPathForID(res HostResource, socketDir string) string {
	switch res.Kind {
	case KindSocketAPI, KindSocketVSock, KindSocketIID:
		return res.Path
	default:
		return filepath.Join(socketDir, res.OwnerID.String()+".sock")
	}
}

// deleteResourceFn is the indirection point for removal. Production always
// uses deleteResource; same-package tests replace it to drive branches that
// cannot be produced by arranging real files — notably the ENOENT branch,
// which requires the path to vanish between enumeration and deletion.
// Mirrors the DiskStatfs / intentFileSyncer seams elsewhere in this package.
var deleteResourceFn = deleteResource

// deleteResource removes a resource from disk.
func deleteResource(res HostResource) error {
	switch res.Kind {
	case KindBuilderSupervisor, KindSupervisorState:
		// Directories with contents; os.Remove would fail with ENOTEMPTY.
		return os.RemoveAll(res.Path)
	}
	return os.Remove(res.Path)
}

// allocatedBytes returns the number of bytes physically allocated on disk for
// path, computed as stat(2).Blocks * 512. Returns 0 for directories or on any
// error (directories are not meaningful for reclamation reporting).
func allocatedBytes(path string) int64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0
	}
	if st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		return 0
	}
	return st.Blocks * 512
}

// procScanResult is the three-way outcome of a /proc scan.
// Ambiguity always resolves to KEEP (N-AC2).
type procScanResult int

const (
	// procScanDead means every process in /proc was checked and none contained
	// the ULID. The scan was complete and definitive.
	procScanDead procScanResult = iota
	// procScanLive means at least one process' cmdline contained the ULID.
	procScanLive
	// procScanAmbiguous means the scan was incomplete or inconclusive:
	// ReadDir failed, a PID's cmdline was unreadable, or a read was truncated.
	// Ambiguity resolves to KEEP per N-AC2.
	procScanAmbiguous
)

// scanProcForULID scans procDir (usually "/proc") for any process whose
// cmdline contains ulidStr. It returns a three-way result:
//
//   - procScanLive:      ULID found in at least one process cmdline.
//   - procScanDead:      Every process scanned; ULID not found anywhere.
//   - procScanAmbiguous: Scan was incomplete (ReadDir failure, unreadable
//     cmdline, truncated read). Ambiguity resolves to KEEP per N-AC2.
//
// procDir is injectable for tests; production callers pass "/proc".
func scanProcForULID(procDir, ulidStr string) procScanResult {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		// Cannot read /proc at all — complete unknown state.
		return procScanAmbiguous
	}

	anyAmbiguous := false
	for _, e := range entries {
		if !isNumeric(e.Name()) {
			continue
		}
		cmdlinePath := filepath.Join(procDir, e.Name(), "cmdline")
		result := checkPIDCmdline(cmdlinePath, ulidStr)
		switch result {
		case procScanLive:
			return procScanLive
		case procScanAmbiguous:
			// Don't short-circuit: another PID might be definitively live.
			anyAmbiguous = true
		// procScanDead: this PID is not our process; continue.
		}
	}
	if anyAmbiguous {
		return procScanAmbiguous
	}
	return procScanDead
}

// maxCmdlineRead is the read limit per /proc/<pid>/cmdline. Cloud-hypervisor
// cmdlines can be long (many --device args), so we use a generous limit.
// If a read reaches this limit without finding the ULID, we treat the result
// as ambiguous because the ULID might appear beyond the truncation point.
const maxCmdlineRead = 512 * 1024 // 512 KiB

// checkPIDCmdline reads the cmdline file at path and reports whether it
// contains ulidStr.
//
//   - procScanLive:      cmdline contains ulidStr.
//   - procScanDead:      cmdline fully read; ulidStr absent.
//   - procScanAmbiguous: file unreadable (EACCES, ENOENT — PID may have been
//     live when enumerated), or read was truncated (ULID may be beyond cutoff).
func checkPIDCmdline(path, ulidStr string) procScanResult {
	f, err := os.Open(path)
	if err != nil {
		// Any open failure is ambiguous:
		//   EACCES: process exists but is owned by another user.
		//   ENOENT: process vanished between ReadDir and Open — it may have
		//           been our target and just exited (mid-create race).
		// In both cases we cannot rule out the process being ours.
		return procScanAmbiguous
	}
	defer f.Close()

	// Read up to maxCmdlineRead+1 bytes. LimitReader(n+1) lets us detect
	// truncation: if ReadAll returns exactly n+1 bytes, the cmdline is longer.
	lr := io.LimitReader(f, maxCmdlineRead+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		// Unexpected read error → ambiguous.
		return procScanAmbiguous
	}

	truncated := int64(len(data)) > maxCmdlineRead
	if truncated {
		data = data[:maxCmdlineRead]
	}

	if strings.Contains(string(data), ulidStr) {
		return procScanLive
	}
	if truncated {
		// We read the cmdline but it was cut off. The ULID might be in the
		// unread tail (e.g. the socket path appears after many disk args).
		return procScanAmbiguous
	}
	return procScanDead
}

// socketResponsive attempts an HTTP GET to http://unix/.version over the Unix
// socket at path, with a 500ms timeout.
//
// Returns:
//
//	(true,  false) — any 2xx or 4xx response (socket is alive)
//	(false, false) — connection refused, ENOENT, or socket does not exist
//	(false, true)  — timeout or unexpected error (ambiguous → caller keeps)
func socketResponsive(ctx context.Context, path string) (alive bool, ambiguous bool) {
	// If the socket file doesn't exist at all, it's definitively dead.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, false
		}
		// Unexpected stat error: ambiguous.
		return false, true
	}

	dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	sockPath := path // capture for closure
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   500 * time.Millisecond,
	}

	req, err := http.NewRequestWithContext(dialCtx, http.MethodGet, "http://unix/.version", nil)
	if err != nil {
		return false, true // unexpected
	}

	resp, err := client.Do(req)
	if err != nil {
		// Connection refused or file not found → definitively dead.
		errStr := err.Error()
		if strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "no such file") ||
			strings.Contains(errStr, "connect: no such file") {
			return false, false
		}
		// Timeout or any other error → ambiguous, keep.
		return false, true
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain a small body to allow connection reuse; ignore errors.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	// Any HTTP response (2xx or 4xx) means the socket is live.
	return true, false
}

// killNetnsProcessFn is the indirection point for reclaiming a
// KindNetnsProcess orphan. Production sends SIGKILL to the process group
// (mirroring NetnsRuntime.Stop's Kill(-ChildPGID, SIGKILL)); tests replace it
// to drive the Failed branch without actually killing a process.
var killNetnsProcessFn = func(pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("reap: refusing to kill process group %d (non-positive pgid)", pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", pgid, err)
	}
	return nil
}

// sweepOrphanNetnsProcesses independently enumerates LIVE processes carrying
// the netns-child sentinel (cloudhypervisor.NetnsRunEnv=1) and classifies
// each one against recordMap/inFlight — the reversed direction from every
// other check in Reap (process -> record, not record -> process; see the
// call site in Reap for why this direction is required).
//
// procDir is injectable for tests (mirrors scanProcForULID's ProcDir seam).
//
// Returns (entries, uninspectableCount, err). uninspectableCount is the
// number of processes that could not be classified (zombie, stat denied, or
// environ unreadable) — see ReapReport.UninspectableProcesses for why this
// is NOT an error.
//
// Classification:
//   - /proc itself is unreadable: one Suspect entry naming the failure
//     (single aggregate alarm, not per-process noise).
//   - a candidate is a zombie (stat field 3 == "Z"): counted as uninspectable,
//     NOT Suspect. A zombie has no mm, no tap, no netns — it cannot be a live
//     netns child. Its /proc/<pid>/environ is absent so it would fail the
//     environ read anyway; skipping early keeps the count honest.
//   - a candidate's /proc/<pid> stat is unreadable for a non-ENOENT reason:
//     counted as uninspectable (increments the returned count), NOT Suspect.
//     Reason: the process may have had its dumpable flag cleared (e.g. sshd,
//     systemd --user after setuid) or may have vanished between ReadDir and
//     stat — neither is evidence of an unrecorded netns child.
//   - a candidate is owned by a DIFFERENT uid than os.Geteuid(): silently
//     skipped — StartNetnsRuntime spawns the child under the SAME uid, so a
//     foreign-uid process cannot be ours. No entry is emitted, not even
//     Suspect.
//   - a candidate's /proc/<pid>/environ is unreadable for a non-ENOENT
//     reason AND the process is owned by our uid: counted as uninspectable
//     (increments the returned count), NOT Suspect.
//
//     WHY THIS IS NOT SUSPECT: an unreadable environ most commonly means the
//     process cleared its dumpable flag after exec (e.g. sshd, systemd --user
//     session leaders) or the process vanished between ReadDir and the read.
//     Neither is evidence that the process is an unrecorded netns child.
//     Emitting a Suspect per such process would produce O(N) bogus entries on
//     any normal host and completely bury the legitimate Suspect and Orphan
//     signals. The uninspectable count is rendered as a single aggregate line
//     (not a Suspect) so it is informative without raising a false alarm.
//
//     NOTE FOR FUTURE READERS: do NOT revert this to Suspect on the grounds
//     that "we cannot classify it, so it might be an orphan." That logic was
//     tried (round 2 of ticket 10) and produced 50 bogus Suspects per run
//     (42 zombie virtiofsd + 4 zombie nexus3 + sshd/systemd with dumpable
//     cleared), drowning 17 genuine orphans in noise. The dominant cause was
//     own-uid zombies — unwaited nexus3 children — not any ptrace policy.
//
//   - a candidate's socket path names a sandbox ID with a live record whose
//     CHAPISocket does not match what this live process is actually using:
//     Suspect — reap cannot certify this process belongs to that record.
//   - a candidate's socket path does not parse as a sandbox ID and no
//     record claims it by any other means: Suspect — "no record claims this"
//     only becomes confident ORPHAN when reap can also rule out an in-flight
//     create, which requires the ID.
//
// A candidate is ReapStatusOrphan only when its socket names a parseable
// sandbox ID, no live record's CHAPISocket matches it, and no in-flight
// create-intent lease is held for that ID.
func sweepOrphanNetnsProcesses(
	procDir string,
	recordMap map[domain.SandboxID]domain.Sandbox,
	inFlight map[domain.SandboxID]string,
	ownerLookup func(string, int) (uint32, error),
) (entries []ReapEntry, zombies int, inaccessible int, err error) {
	claimedSockets := make(map[string]domain.SandboxID, len(recordMap))
	for _, sb := range recordMap {
		if sb.CHAPISocket != "" {
			claimedSockets[sb.CHAPISocket] = sb.ID
		}
	}

	dirEntries, readErr := os.ReadDir(procDir)
	if readErr != nil {
		return []ReapEntry{{
			Status: ReapStatusSuspect,
			Reason: fmt.Sprintf("netns process sweep: cannot read %s: %v — cannot rule out a live orphan", procDir, readErr),
		}}, 0, 0, nil
	}

	var out []ReapEntry
	for _, e := range dirEntries {
		if !isNumeric(e.Name()) {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}

		// Gate on process ownership before reading environ. StartNetnsRuntime
		// always spawns the netns child under the SAME uid as the calling process
		// (ch_netns.go: Cloneflags does not include CLONE_NEWUSER and no uid
		// mapping is applied). A process owned by a DIFFERENT uid therefore
		// cannot be a netns child we spawned — EACCES is positive evidence of
		// non-ownership, not an ambiguous case. The fail-closed Suspect rail is
		// reserved for OWN-uid processes whose classification genuinely cannot
		// be resolved, where the ambiguity is real. Mixing foreign-uid processes
		// into the Suspect bucket would produce hundreds of bogus entries on any
		// normal multi-user host and destroy the "0 orphans → exit 0" contract.
		ownerUID, uidErr := ownerLookup(procDir, pid)
		if uidErr != nil {
			if os.IsNotExist(uidErr) {
				continue // process vanished between ReadDir and stat: ordinary churn
			}
			// Cannot stat /proc/<pid>: count as inaccessible, not Suspect.
			// The process may have had its dumpable flag cleared (sshd, systemd)
			// or may have vanished between ReadDir and stat — neither is evidence
			// of an unrecorded netns child.
			inaccessible++
			continue
		}
		if uint32(os.Geteuid()) != ownerUID {
			continue // not our uid: cannot be a netns child we spawned — skip silently
		}

		// Zombies have no mm, no tap, no netns — skip explicitly rather than
		// letting the environ read fail and falling into inaccessible. A zombie's
		// /proc/<pid>/stat is still readable, so this is a positive classification.
		if state, stErr := readProcState(procDir, pid); stErr == nil && state == "Z" {
			zombies++
			continue
		}

		env, envErr := readProcEnviron(procDir, pid)
		if envErr != nil {
			if os.IsNotExist(envErr) {
				continue // vanished between ReadDir and read: ordinary churn, not our process
			}
			// Cannot read /proc/<pid>/environ: count as uninspectable, NOT Suspect.
			//
			// WHY: the process likely cleared its dumpable flag after exec (sshd,
			// systemd session leaders do this), or vanished between ReadDir and the
			// read. Neither is evidence that the process is an unrecorded netns
			// child. Emitting a Suspect per uninspectable process would produce
			// O(N) bogus entries on any normal host and bury genuine signals.
			//
			// NOTE FOR FUTURE READERS: do NOT revert this to Suspect on the grounds
			// that "we cannot rule out the process being an orphan." That logic was
			// tried in round 2 of ticket 10 and produced 50 bogus Suspects per run
			// (42 zombie virtiofsd + 4 zombie nexus3 + sshd/systemd with dumpable
			// cleared), drowning 17 genuine orphans in noise. The dominant cause was
			// own-uid zombies, not any ptrace policy.
			inaccessible++
			continue
		}
		if env[netnsRunEnv] != "1" {
			continue // not a netns child
		}

		apiSocket := env[netnsEnvAPISocket]
		if ownerID, ok := claimedSockets[apiSocket]; ok {
			_ = ownerID
			continue // matches a live record's own socket — expected, not orphaned
		}

		id, idErr := sandboxIDFromSocketPath(apiSocket)
		if idErr == nil {
			if _, ok := inFlight[id]; ok {
				continue // create in flight for this id — expected during the create window
			}
			if sb, ok := recordMap[id]; ok {
				out = append(out, ReapEntry{
					Status: ReapStatusSuspect,
					Resource: HostResource{
						Kind:    KindNetnsProcess,
						Path:    apiSocket,
						OwnerID: id,
					},
					Reason: fmt.Sprintf("pid %d: live netns child (api socket %s) names sandbox %s but the record's CHAPISocket=%q does not match — cannot verify ownership", pid, apiSocket, sb.ID, sb.CHAPISocket),
				})
				continue
			}
		}

		// StartNetnsRuntime spawns the netns child with Setpgid:true, so
		// pgid == pid at creation (ch_netns.go:257-259), and CH inherits that
		// same group via spawnVMMInGroup's Setpgid:false — nothing later in
		// either process's life calls setpgid again. pid doubles as pgid here
		// without a second /proc read.
		pgid := pid
		if livePgid, statErr := readProcPGID(procDir, pid); statErr == nil && livePgid > 0 {
			pgid = livePgid
		}

		if idErr != nil {
			out = append(out, ReapEntry{
				Status: ReapStatusSuspect,
				Resource: HostResource{
					Kind: KindNetnsProcess,
					Path: apiSocket,
				},
				Reason:      fmt.Sprintf("pid %d: live netns child (api socket %q) does not parse as a sandbox id and matches no record — cannot verify ownership", pid, apiSocket),
				ProcessPID:  pid,
				ProcessPGID: pgid,
			})
			continue
		}

		out = append(out, ReapEntry{
			Status: ReapStatusOrphan,
			Resource: HostResource{
				Kind:    KindNetnsProcess,
				Path:    apiSocket,
				OwnerID: id,
			},
			Reason:      fmt.Sprintf("pid %d: live netns child (api socket %s) matches no store record and no in-flight create intent — orphaned by a prior failed/retried create; holds memfd-backed guest RAM, a tap, and a netns", pid, apiSocket),
			ProcessPID:  pid,
			ProcessPGID: pgid,
		})
	}
	return out, zombies, inaccessible, nil
}

// sandboxIDFromSocketPath extracts the sandbox ID embedded in a CH API
// socket's filename (<socketDir>/<id>.sock — see cloudhypervisor.socketPath),
// so a candidate process can be cross-checked against records and in-flight
// leases even after its own resource files have already been removed.
func sandboxIDFromSocketPath(path string) (domain.SandboxID, error) {
	if path == "" {
		return domain.SandboxID{}, fmt.Errorf("empty socket path")
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".sock")
	return domain.ParseSandboxID(stem)
}

// readProcEnviron reads /proc/<pid>/environ (NUL-separated KEY=VALUE
// records) into a map. Mirrors internal/supervisor/netns_backfill.go's
// unexported helper of the same shape; duplicated here (rather than
// exported and imported) because that package's version is scoped to a
// single already-known-alive supervisor's children, not a system-wide sweep,
// and because internal/supervisor already depends on internal/core/service's
// sibling packages in the other direction.
func readProcEnviron(procDir string, pid int) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil, err
	}
	env := make(map[string]string)
	for _, kv := range strings.Split(string(data), "\x00") {
		if kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[k] = v
	}
	return env, nil
}

// readProcState reads field 3 (state) of /proc/<pid>/stat and returns the
// single-character state string (e.g. "R", "S", "Z"). Returns an error if the
// stat file cannot be read or does not have the expected shape.
func readProcState(procDir string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	line := strings.TrimRight(string(data), "\n")
	idx := strings.LastIndex(line, ")")
	if idx < 0 {
		return "", fmt.Errorf("parse /proc/%d/stat: no ')' found", pid)
	}
	fields := strings.Fields(line[idx+1:])
	if len(fields) < 1 {
		return "", fmt.Errorf("parse /proc/%d/stat: no state field after ')'", pid)
	}
	return fields[0], nil // fields[0] = state (field 3 in /proc/<pid>/stat)
}

// readProcPGID reads field 5 (pgid) of /proc/<pid>/stat. It exists as a
// belt-and-suspenders re-check alongside the pid==pgid assumption documented
// at its call site; a read failure is non-fatal there (falls back to pid).
//
// This duplicates the field-5 slice of cloudhypervisor.ReadProcStat's
// parsing rather than importing it, for the same import-cycle reason
// documented at netnsRunEnv above.
func readProcPGID(procDir string, pid int) (int, error) {
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	line := strings.TrimRight(string(data), "\n")
	idx := strings.LastIndex(line, ")")
	if idx < 0 {
		return 0, fmt.Errorf("parse /proc/%d/stat: no ')' found", pid)
	}
	fields := strings.Fields(line[idx+1:])
	if len(fields) < 3 {
		return 0, fmt.Errorf("parse /proc/%d/stat: too few fields after ')': got %d, want >=3", pid, len(fields))
	}
	return strconv.Atoi(fields[2]) // fields[2] = pgid (field 5)
}

// isNumeric returns true if s consists entirely of ASCII digits.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

