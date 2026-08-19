package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// intentFileSyncer is the seam used to fsync the intent file after writing.
// It is a package-level variable so tests can inject a recording spy to verify
// the sync call without relying on filesystem inspection (which would succeed
// from page cache regardless of whether Sync was called).
// Production code must never replace this outside of tests.
var intentFileSyncer = func(f *os.File) error { return f.Sync() }

// intentDirSyncer is the seam used to fsync the directory containing the
// intent file after the file is closed. Syncing only the file guarantees data
// durability but NOT directory-entry durability: after a power loss the inode
// may survive while the directory entry does not, making the file invisible to
// the reaper's directory scan. Both syncs together close this gap.
// Production code must never replace this outside of tests.
var intentDirSyncer = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	_ = d.Close()
	return syncErr
}

// createIntent is the durable marker written to <diskDir>/<id>.create-intent.json
// BEFORE any host resource is materialized by CreateAndBoot. Its purpose is to
// make the disk resources visible to the R1 reaper if the creating process dies
// between resource materialisation and the durable store.Create call.
//
// The reaper identifies orphaned intents by checking whether a corresponding
// sandbox record exists: an intent without a record means the create did not
// complete, and the listed disk paths are safe to reclaim.
//
// On a clean exit (error or success) CreateAndBoot removes the intent file in
// its deferred cleanup, so intents only survive after unclean termination.
type createIntent struct {
	// ID is the sandbox ULID. Stored as a string so the file is self-describing.
	ID string `json:"id"`

	// DiskCopyPath is the absolute path of the per-sandbox CoW disk copy
	// (<diskDir>/<id>.raw). Empty when the sandbox was created with --rootfs
	// (RootfsPath mode) and therefore has no per-sandbox disk copy.
	DiskCopyPath string `json:"disk_copy_path,omitempty"`

	// WorkspaceDiskPath is the absolute path of the workspace ext4 disk
	// (<diskDir>/<id>-workspace.ext4). Empty when no Workspace was specified.
	WorkspaceDiskPath string `json:"workspace_disk_path,omitempty"`
}

// createIntentLease is an in-flight-create lease: an open file descriptor
// holding an exclusive flock(2) on the create-intent file for as long as the
// creating process is inside the create window.
//
// # Why a lease is needed
//
// CreateAndBoot materialises the per-sandbox disk (a multi-second cp for a
// multi-GiB image) BEFORE it commits the store record. During that window the
// disk exists with no record, and no process anywhere carries the ULID in its
// cmdline — cloud-hypervisor is not launched until after the record write — so
// the reaper's /proc liveness gate (reap.go, scanProcForULID) has nothing to
// match. Without a lease the reaper correctly concludes "no record, no live
// process" and unlinks the disk of a create that is still running.
//
// # Why flock
//
// The kernel releases flock(2) locks when the holding process dies, including
// on SIGKILL, and a reboot releases everything. So a dead creator can NEVER
// block reclamation: the next reaper acquires the lease and reclaims. That is
// the property that makes this safe to add to a reaper whose entire purpose is
// reclaiming leaked disks. It is the same primitive, and the same reasoning,
// that store.Lock already uses for sandbox records (see store/lock.go, "Crash
// safety guarantee" and "Lease equivalence").
//
// A lease is held from before the intent file is discoverable (see
// writeCreateIntent) until CreateAndBoot's deferred cleanup calls release,
// which happens after store.Create has committed the record. There is
// therefore no instant at which a create's disks are neither leased nor owned.
type createIntentLease struct {
	path string
	f    *os.File
}

// Path returns the intent file path this lease covers.
func (l *createIntentLease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// release removes the intent file and drops the lease. It is safe to call on a
// nil lease and safe to call more than once.
//
// Order matters: the file is removed BEFORE the descriptor is closed, so the
// intent is never visible to a reaper in an unleased state.
func (l *createIntentLease) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = os.Remove(l.path)
	// Closing the descriptor releases the flock.
	_ = l.f.Close()
	l.f = nil
}

// abort tears down a partially constructed lease, removing the staging file.
func (l *createIntentLease) abort(stagePath string) {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Close()
	l.f = nil
	_ = os.Remove(stagePath)
}

// intentLeaseState is the three-way result of probing a create-intent file's
// lease. Ambiguity resolves to keep, matching the reaper's N-AC2 rule — but it
// is reported distinctly from a genuine live lease, because the two have very
// different operational meanings (see leaseUnknown).
type intentLeaseState int

const (
	// leaseFree means nobody holds the lease: the creator is gone (crashed,
	// killed, or the host rebooted) or the intent predates leases. The
	// resources are reclaimable exactly as they were before leases existed.
	leaseFree intentLeaseState = iota
	// leaseHeld means a live creator holds the lease: a create is in flight.
	// This state ends when the creator dies, so it cannot persist.
	leaseHeld
	// leaseUnknown means the intent file could not be read, so a live creator
	// cannot be ruled out. Unlike leaseHeld this state does NOT expire on its
	// own: an intent file left mode-0600 by another uid keeps its sandbox's
	// disks unreclaimable until an operator intervenes. It therefore gets its
	// own reason string in the reap report so a permanent keep is legible as
	// one rather than masquerading as a create that is still running.
	leaseUnknown
)

// probeIntentLease reports whether the intent file at path is currently leased
// by a live creator, i.e. whether a create is in flight for that sandbox.
//
// It probes with a non-blocking exclusive flock:
//
//   - EWOULDBLOCK  → leaseHeld.
//   - acquired     → leaseFree.
//   - ENOENT       → leaseFree (the intent vanished; the creator finished).
//   - other errors → leaseUnknown; the caller keeps the resources.
//
// The probe lock is held only for the duration of the call. A second reaper
// probing concurrently may observe the first reaper's probe lock and treat the
// sandbox as in flight; that is conservative, not a leak — the reaper holding
// the lock is itself about to reclaim the resource, and any leftover is swept
// by the next reap.
//
// probeIntentLease never creates the file: probing must not resurrect an intent
// that a concurrent reaper just unlinked.
func probeIntentLease(path string) intentLeaseState {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return leaseFree
		}
		// Unreadable (e.g. EACCES): cannot rule out a live creator.
		return leaseUnknown
	}
	defer func() { _ = f.Close() }()

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		// No live holder. Drop the probe lock immediately; the close in the
		// defer would do it too, but releasing early narrows the window in
		// which a concurrent reaper sees our probe lock.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return leaseFree
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
		return leaseHeld
	default:
		return leaseUnknown
	}
}

// writeCreateIntent writes a create-intent file for id into diskDir durably and
// returns the lease that protects the in-flight create. The caller MUST call
// release on the returned lease when the create window closes (CreateAndBoot
// does this in its deferred cleanup).
//
// It creates diskDir if it does not exist.
//
// Durability contract: the function performs a write→Sync→rename→dir-Sync
// sequence before returning. Both syncs are required for true power-loss
// durability:
//
//   - f.Sync() makes the file data durable (survives ext4 journal replay).
//   - dir.Sync() makes the directory entry durable so the reaper can discover
//     the file by scanning diskDir. Without the directory sync the inode may
//     survive a power loss while the directory entry does not, leaving the
//     reaper unable to find the intent and the orphaned disk invisible.
//
// Lease-before-visibility: the file is written under a staging name
// ("<ULID>.create-intent.json.tmp") and flocked BEFORE being renamed into
// place. The reaper's index matches only the exact ".create-intent.json"
// suffix (resource_index.go), so the staging name is invisible to it. The
// rename is atomic and carries the flock with the inode, so the intent becomes
// discoverable only in the already-leased state — there is no window in which
// a reaper can see an unleased intent for a live create.
//
// The descriptor deliberately stays open after this function returns: closing
// it would drop the lease. It is closed by release.
//
// The intent must be fully durable before cowExt4 materialises the .raw disk,
// or the failure window this function is designed to close simply moves.
// Callers (create.go) invoke writeCreateIntent before cowExt4; together the
// write+sync sequence inside this function and that call-site ordering make the
// contract hold.
//
// Residual limit: fsync guarantees are only as strong as the storage hardware
// and driver stack honour them. True power-loss behaviour is not tested here;
// see docs/site/operations/resource-lifecycle.md for the full durability contract
// and its unverified residuals.
//
// diskCopyPath and workspaceDiskPath are the planned disk paths; either may be
// empty if the corresponding resource will not be created.
func writeCreateIntent(diskDir string, id domain.SandboxID, diskCopyPath, workspaceDiskPath string) (*createIntentLease, error) {
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		return nil, fmt.Errorf("create intent: mkdir %s: %w", diskDir, err)
	}
	intentPath := IntentPath(diskDir, id)
	data, err := json.Marshal(createIntent{
		ID:                id.String(),
		DiskCopyPath:      diskCopyPath,
		WorkspaceDiskPath: workspaceDiskPath,
	})
	if err != nil {
		return nil, fmt.Errorf("create intent: marshal: %w", err)
	}
	return publishLeasedIntent(diskDir, intentPath, data, "create intent")
}

// publishLeasedIntent writes data to intentPath durably and returns a held
// lease, performing the stage → flock → write → fsync → rename → dir-fsync
// sequence documented on writeCreateIntent.
//
// It is shared by the ULID-keyed create intent and the handle-keyed shadow
// intent so the two cannot drift: the lease-before-visibility property is the
// whole reason either file is safe to leave lying around, and re-deriving it
// per intent kind is how one of them ends up publishing an unleased file.
//
// label prefixes error messages so a failure names the intent kind.
func publishLeasedIntent(dir, intentPath string, data []byte, label string) (*createIntentLease, error) {
	// Stage under a name the reaper's index does not match, so the intent is
	// never discoverable before its lease is held.
	stagePath := intentPath + ".tmp"
	f, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%s: open %s: %w", label, stagePath, err)
	}
	lease := &createIntentLease{path: intentPath, f: f}

	// Take the lease. Non-blocking: the staging path is unique to this owner,
	// so a conflict means something is badly wrong and blocking would hang a
	// create.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lease.abort(stagePath)
		return nil, fmt.Errorf("%s: lease %s: %w", label, stagePath, err)
	}
	if _, err := f.Write(data); err != nil {
		lease.abort(stagePath)
		return nil, fmt.Errorf("%s: write %s: %w", label, stagePath, err)
	}
	// Sync file data before it becomes visible. intentFileSyncer is a
	// package-level seam so tests can assert this call without relying on
	// page-cache reads.
	if err := intentFileSyncer(f); err != nil {
		lease.abort(stagePath)
		return nil, fmt.Errorf("%s: sync %s: %w", label, stagePath, err)
	}
	// Atomic publish. The flock follows the inode, so the intent appears
	// already leased.
	if err := os.Rename(stagePath, intentPath); err != nil {
		lease.abort(stagePath)
		return nil, fmt.Errorf("%s: publish %s: %w", label, intentPath, err)
	}
	// Sync the directory entry. intentDirSyncer is a seam for the same reason.
	if err := intentDirSyncer(dir); err != nil {
		lease.release()
		return nil, fmt.Errorf("%s: sync dir %s: %w", label, dir, err)
	}
	return lease, nil
}

// readCreateIntent reads and parses the intent file at intentPath.
// Returns ErrNotExist (via os package) if the file is absent.
func readCreateIntent(intentPath string) (createIntent, error) {
	data, err := os.ReadFile(intentPath)
	if err != nil {
		return createIntent{}, err
	}
	var ci createIntent
	if err := json.Unmarshal(data, &ci); err != nil {
		return createIntent{}, fmt.Errorf("create intent: parse %s: %w", intentPath, err)
	}
	return ci, nil
}

// IntentPath returns the canonical path of the create-intent file for id in diskDir.
// Exported so the R1 reaper can scan diskDir for orphaned intents without re-deriving
// the naming convention.
func IntentPath(diskDir string, id domain.SandboxID) string {
	return filepath.Join(diskDir, id.String()+".create-intent.json")
}
