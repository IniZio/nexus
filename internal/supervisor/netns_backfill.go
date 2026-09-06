// netns_backfill.go — verified reconstruction of the netns identity fields
// (NetnsChildPID, NetnsChildPGID, NetnsChildStartTime, GuestTapName,
// CHAPISocket) for a sandbox that was already running before slice 04 landed
// (ticket 11, nexus3-host-supervisor-hotswap).
//
// # Why reconstruction, not supervisor self-registration
//
// The ticket ranked a new supervisor IPC endpoint (the running supervisor
// writing its own known-good identity into the record) as the strongest
// option, because the identity would come from the process that owns it
// rather than from inference. That option is RULED OUT here: the supervisor
// holding the identity for a pre-slice-04 sandbox is the OLD binary, and its
// IPC routes (serveIPC, internal/supervisor/ipc.go) are whatever was
// compiled into it at the version it was launched with. A new endpoint only
// ships in a future binary — no already-running old supervisor is executing
// that binary, so self-registration cannot reach a single sandbox in the gap
// it exists to close. It only helps sandboxes booted AFTER the endpoint
// exists, which never had the gap in the first place.
//
// Verified reconstruction is the fallback the ticket ranked weaker, but it
// is stronger in practice than the ticket assumed: every value below is
// recovered FIRST-HAND from live process state — not inferred from the
// process tree's shape — because StartNetnsRuntime puts GuestTapName and
// CHAPISocket into the netns child's OWN environment
// (ch_netns.go:217-232), and the kernel exposes ppid/pgid/starttime for any
// pid via /proc/<pid>/stat.
package supervisor

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
)

// NetnsIdentity is a verified netns child identity, ready to persist onto
// domain.Sandbox in a single write. Every field was read from /proc of one
// uniquely-identified process — see BackfillNetnsIdentity.
type NetnsIdentity struct {
	ChildPID       int
	ChildPGID      int
	ChildStartTime uint64
	GuestTapName   string
	APISocket      string
}

// BackfillNetnsIdentity reconstructs a VERIFIED netns child identity for a
// sandbox whose record predates the persisted identity fields.
//
// supervisorPID must already be known alive by the caller (e.g. via
// supervisorSockLooksAlive in internal/cli) — pid liveness alone is not a
// sufficient standalone check, so this function additionally requires each
// candidate's own ppid to equal supervisorPID.
//
// expectedAPISocket is the CH API socket path this sandbox is expected to
// use (deterministic: SocketDir/<sandboxID>.sock — see
// cloudhypervisor.(*CHDriver).socketPath). It is what binds a matched child
// to THIS sandbox rather than to some other sandbox's netns child that
// happens to also be a child of the same supervisor process (a supervisor
// process is 1:1 with a sandbox today, but this predicate does not rely on
// that staying true).
//
// Every ambiguity REFUSES rather than guesses — this is the acceptance gate
// ticket 11 sets: a backfilled identity must be indistinguishable in
// trustworthiness from one written at start by StartNetnsRuntime, or the
// pid-reuse guard in AdoptNetnsRuntime is only as strong as the weakest way
// a field got there.
//   - zero matching candidates: refuse.
//   - more than one matching candidate: refuse — never pick-the-first.
//   - a candidate's /proc/<pid>/environ is unreadable (EACCES) or the
//     process exits mid-read: refuse the WHOLE backfill, not just that
//     candidate. Silently excluding an unreadable candidate would let
//     whichever candidate happens to stay readable win by default, even
//     though the unreadable one could have been the true (or a colliding)
//     match.
func BackfillNetnsIdentity(supervisorPID int, expectedAPISocket string) (NetnsIdentity, error) {
	if supervisorPID <= 0 {
		return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: supervisorPID=%d must be positive", supervisorPID)
	}
	if expectedAPISocket == "" {
		return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: expectedAPISocket is empty")
	}

	pids, err := listProcPIDs()
	if err != nil {
		return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: list /proc: %w", err)
	}

	type candidate struct {
		pid int
		tap string
	}
	var matched []candidate
	for _, pid := range pids {
		stat, statErr := cloudhypervisor.ReadProcStat(pid)
		if statErr != nil {
			// The pid was listed under /proc but is gone (or was a
			// transient kernel thread entry) by the time we stat it. This is
			// ordinary process-table churn, not ambiguity about identity —
			// skip it rather than refusing the whole backfill.
			continue
		}
		if stat.PPID != supervisorPID {
			continue
		}
		env, envErr := readProcEnviron(pid)
		if envErr != nil {
			return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: candidate pid %d (child of %d): read /proc/%d/environ: %w — refusing rather than silently excluding an unreadable candidate", pid, supervisorPID, pid, envErr)
		}
		if env[cloudhypervisor.NetnsRunEnv] != "1" {
			continue
		}
		if env[cloudhypervisor.NetnsEnvAPISocket] != expectedAPISocket {
			continue
		}
		matched = append(matched, candidate{pid: pid, tap: env[cloudhypervisor.NetnsEnvGuestTap]})
	}

	if len(matched) == 0 {
		return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: no child of supervisor pid %d matches netns identity (api socket %s); refusing to guess", supervisorPID, expectedAPISocket)
	}
	if len(matched) > 1 {
		pids := make([]int, len(matched))
		for i, c := range matched {
			pids[i] = c.pid
		}
		return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: %d children of supervisor pid %d match netns identity (api socket %s); ambiguous, refusing to pick one: pids=%v", len(matched), supervisorPID, expectedAPISocket, pids)
	}

	pid := matched[0].pid
	tap := matched[0].tap
	if tap == "" {
		return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: candidate pid %d has no %s in its environ", pid, cloudhypervisor.NetnsEnvGuestTap)
	}

	// Settle pgid and starttime from ONE /proc read taken NOW, after
	// selection — not reused from the enumeration pass above — so the
	// pid+starttime pair persisted together is exactly as fresh as the pair
	// StartNetnsRuntime captures immediately after spawning the child
	// (ch_netns.go:287). Re-check ppid too: if the pid was recycled by an
	// unrelated process between enumeration and now, that new process is
	// almost certainly not our supervisor's child, and this catches it.
	stat, statErr := cloudhypervisor.ReadProcStat(pid)
	if statErr != nil {
		return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: candidate pid %d vanished before its identity could be settled: %w", pid, statErr)
	}
	if stat.PPID != supervisorPID {
		return NetnsIdentity{}, fmt.Errorf("supervisor: backfill netns identity: candidate pid %d's ppid changed between selection and settling (was %d, now %d) — likely pid reuse; refusing", pid, supervisorPID, stat.PPID)
	}

	return NetnsIdentity{
		ChildPID:       pid,
		ChildPGID:      stat.PGID,
		ChildStartTime: stat.StartTime,
		GuestTapName:   tap,
		APISocket:      expectedAPISocket,
	}, nil
}

// listProcPIDs returns every numeric entry directly under /proc.
func listProcPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// readProcEnviron reads /proc/<pid>/environ (NUL-separated KEY=VALUE
// records) into a map. Returns an error — never a partial map — on EACCES or
// a process that exits mid-read: BackfillNetnsIdentity's contract requires
// the caller to refuse the whole backfill on this, not treat the candidate
// as a silent non-match.
func readProcEnviron(pid int) (map[string]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	env := make(map[string]string)
	for kv := range strings.SplitSeq(string(data), "\x00") {
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
