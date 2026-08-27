package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
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
)

// ReapEntry describes one host resource and its classification.
type ReapEntry struct {
	Resource       HostResource
	Status         ReapStatus
	Reason         string
	AllocatedBytes int64 // syscall.Stat_t.Blocks * 512; 0 for dirs
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

	report := &ReapReport{
		Entries: make([]ReapEntry, 0, len(resources)),
	}

	for _, res := range resources {
		var entry ReapEntry
		if res.Kind == KindDiskShadow || res.Kind == KindShadowIntent {
			// Shadow disks and their intents use handle-based correlation,
			// not ULID/liveness.
			entry = classifyShadowDisk(res, shadowHandleMap, recordMap, inFlightShadow)
		} else {
			entry = classifyResource(ctx, res, recordMap, inFlight, socketDir, opt.ProcDir)
		}
		if entry.Status == ReapStatusOrphan {
			report.ReclaimableBytes += entry.AllocatedBytes
			if apply {
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
	if res.Kind == KindBuilderSupervisor {
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

