package builder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// cacheDiskSizeBytes is the default sparse size for a new per-ecosystem cache
// disk. 10 GiB is large enough for typical package caches while staying sparse
// (only populated blocks consume host space).
const cacheDiskSizeBytes int64 = 10 * 1024 * 1024 * 1024

// ecosystemEntry holds the canonical guest mount path and optional subpaths
// for a single ecosystem cache.
type ecosystemEntry struct {
	mountPath string
	subpaths  []string
}

// ecosystemRegistry maps ecosystem keys to their canonical guest mount paths.
// All 8 required ecosystems are registered here. The buildkit key is
// registered for G5's map contract; buildkitd wiring belongs to G6.
var ecosystemRegistry = map[string]ecosystemEntry{
	"npm": {
		mountPath: "/root/.npm",
	},
	"pnpm": {
		mountPath: "/root/.local/share/pnpm/store",
	},
	"yarn": {
		mountPath: "/root/.cache/yarn",
	},
	"pip": {
		mountPath: "/root/.cache/pip",
	},
	"cargo": {
		mountPath: "/root/.cargo",
		subpaths:  []string{"registry", "git"},
	},
	"go": {
		mountPath: "/root/.cache/go-build",
		subpaths:  []string{"/root/go/pkg/mod"},
	},
	"apt": {
		mountPath: "/var/cache/apt",
	},
	"buildkit": {
		mountPath: "/var/lib/buildkit",
	},
}

// MaxCacheDiskSlots bounds how many concurrent builder VMs can hold a cache
// disk of the same ecosystem. Slot 0 keeps the historical unsuffixed path so
// existing warm caches are not orphaned; slot i>0 uses <key>-<i>.ext4.
const MaxCacheDiskSlots = 8

// slotImagePath returns the image path for slot i of ecosystemKey. Slot 0 is
// the historical unsuffixed name.
func slotImagePath(cacheDir, ecosystemKey string, slot int) string {
	if slot == 0 {
		return filepath.Join(cacheDir, ecosystemKey+".ext4")
	}
	return filepath.Join(cacheDir, fmt.Sprintf("%s-%d.ext4", ecosystemKey, slot))
}

// ensureCacheDiskAt creates the slot's ext4 image if it does not exist yet and
// returns its spec.
func ensureCacheDiskAt(ctx context.Context, cacheDir, ecosystemKey string, entry ecosystemEntry, slot int) (CacheDiskSpec, error) {
	imgPath := slotImagePath(cacheDir, ecosystemKey, slot)

	spec := CacheDiskSpec{
		EcosystemKey: ecosystemKey,
		ImagePath:    imgPath,
		MountPath:    entry.mountPath,
		Subpaths:     entry.subpaths,
	}

	if _, err := os.Stat(imgPath); err == nil {
		if !cacheDiskIsDirty(imgPath) {
			// Clean reuse: fence the disk dirty before handing the spec to
			// its new occupant. The marker is cleared only once that
			// occupant's builder VM confirms a clean sync (BuildInVM, on
			// lifecycle.SyncAndStop success). An unclean death — including
			// guest SIGKILL, which runs no in-guest or host teardown code
			// at all — leaves the marker set for the next lease to see.
			if err := markCacheDiskDirty(imgPath); err != nil {
				return CacheDiskSpec{}, fmt.Errorf("cachedisk: %w", err)
			}
			return spec, nil
		}
		// Fenced dirty: the prior occupant of this slot never confirmed a
		// clean sync. The image may hold a committed buildkitd record whose
		// backing snapshot data never reached disk (D-DC-31 debugfs
		// forensics on caches/buildkit.ext4: a 0-byte agent layer was later
		// served as a cache HIT). Wipe rather than risk reuse — losing a
		// warm cache is cheap; serving poisoned layer data as good is not.
		log.Printf("cachedisk: %s slot %d left dirty by a prior unclean death; wiping cache (%s)",
			ecosystemKey, slot, imgPath)
		if err := os.Remove(imgPath); err != nil {
			return CacheDiskSpec{}, fmt.Errorf("cachedisk: wipe dirty %s: %w", ecosystemKey, err)
		}
	}

	// First use, or reuse after a wipe: create a sparse ext4 disk from an
	// empty temp directory so mke2fs has a valid source tree to populate the
	// image from.
	tmpSrc, err := os.MkdirTemp("", "nexus3-cachedisk-src-*")
	if err != nil {
		return CacheDiskSpec{}, fmt.Errorf("cachedisk: create src tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpSrc)

	if err := runMke2fs(ctx, tmpSrc, imgPath, cacheDiskSizeBytes); err != nil {
		// Remove partially written file so the next call retries cleanly.
		_ = os.Remove(imgPath)
		return CacheDiskSpec{}, fmt.Errorf("cachedisk: create ext4 for %q: %w", ecosystemKey, err)
	}
	if err := markCacheDiskDirty(imgPath); err != nil {
		return CacheDiskSpec{}, fmt.Errorf("cachedisk: %w", err)
	}
	return spec, nil
}

// dirtyMarkerPath returns the sidecar fencing-marker path for a cache disk
// image. The marker is a plain file, not data inside the ext4 image, so
// checking and clearing it never requires mounting or otherwise touching the
// image's filesystem contents on the host.
func dirtyMarkerPath(imgPath string) string {
	return imgPath + ".dirty"
}

// cacheDiskIsDirty reports whether imgPath is currently fenced dirty: leased
// to a builder VM that has not yet confirmed a clean sync.
func cacheDiskIsDirty(imgPath string) bool {
	_, err := os.Stat(dirtyMarkerPath(imgPath))
	return err == nil
}

// markCacheDiskDirty writes the fencing marker for imgPath before a builder
// VM is allowed to attach it. If the process now dies without the marker
// being cleared — including via a guest SIGKILL, where no in-guest or host
// cleanup code runs at all — the marker persists on disk and the next lease
// (ensureCacheDiskAt) detects the unclean death and wipes the image before
// reuse. This is the only mechanism in this file that needs no cooperation
// from the guest, so it is the only one that covers the SIGKILL case.
//
// The marker file itself is fsynced, and its directory entry is fsynced
// separately, so the marker's own presence is crash-durable.
func markCacheDiskDirty(imgPath string) error {
	markerPath := dirtyMarkerPath(imgPath)
	f, err := os.OpenFile(markerPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("write dirty marker %s: %w", markerPath, err)
	}
	if _, writeErr := f.WriteString("leased, sync not yet confirmed\n"); writeErr != nil {
		_ = f.Close()
		return fmt.Errorf("write dirty marker %s: %w", markerPath, writeErr)
	}
	if syncErr := f.Sync(); syncErr != nil {
		_ = f.Close()
		return fmt.Errorf("fsync dirty marker %s: %w", markerPath, syncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("close dirty marker %s: %w", markerPath, closeErr)
	}
	return fsyncDir(filepath.Dir(markerPath))
}

// markCacheDiskClean clears the fencing marker for imgPath. Callers must only
// do this once they have positive confirmation that the guest's writes to
// imgPath were flushed to the host (lifecycle.SyncAndStop returning nil). It
// is a no-op if no marker is present.
func markCacheDiskClean(imgPath string) error {
	markerPath := dirtyMarkerPath(imgPath)
	if err := os.Remove(markerPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("clear dirty marker %s: %w", markerPath, err)
	}
	return fsyncDir(filepath.Dir(markerPath))
}

// fsyncDir fsyncs a directory so that a prior create/remove of an entry
// within it is crash-durable, not just visible to readers.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s for fsync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir %s: %w", dir, err)
	}
	return nil
}

// SelectCacheDisks leases one free cache-disk slot per requested ecosystem key
// and returns the specs plus a release function that MUST be called once the
// builder VM using them has stopped.
//
// # Why leases
//
// A cache disk is attached to the builder VM as a virtio-blk device, and
// cloud-hypervisor takes an exclusive write lock on every image it attaches.
// While one builder VM runs, a second VM handed the same image path is refused
// at vm.boot with "Error locking disk images: Another instance likely holds a
// lock … The file is already locked", which kills its supervisor before it
// writes supervisor.pid. Handing every concurrent build the same
// caches/<key>.ext4 therefore made concurrent builder VMs impossible.
//
// Each slot is guarded by a sidecar <image>.lock file held under
// flock(LOCK_EX|LOCK_NB) for the lifetime of the build. The lock is on the
// sidecar, never on the image itself, because cloud-hypervisor needs its own
// exclusive lock on the image. Slot 0 keeps the historical unsuffixed path, so
// a serial build still reuses the warm cache it has always used.
//
// # Consequences of slotting
//
// Slot 0 carries the cache every serial build has always warmed. A build that
// starts while slot 0 is busy gets slot 1, which is COLD on its first use and
// warms independently from then on — so N concurrent builds converge on N
// separate caches rather than one, and the first build to land on a new slot
// pays a full cold-cache build. Concurrency is bought at the cost of cache
// locality; a build that can wait for slot 0 will be faster than one that
// cannot.
//
// Slot images are never pruned. They are ordinary cache disks (sparse, grown
// on demand by the disk governor), and nothing reclaims slots 1..N-1 when
// concurrency drops back to one. On a host that once ran N concurrent builds,
// N cache images persist until an operator removes them.
//
// Unknown keys cause an immediate error; the caller receives no partial
// results and no held leases.
// # Who owns the lease (D-HSH-07)
//
// SelectCacheDisks only SELECTS a free slot and takes the first lease on it.
// The returned [CacheDiskLease] values are handed to the supervisor process
// that owns the builder VM, and the CLI's own copies are dropped once that
// handoff has happened. Before D-HSH-07 the lease was released by a `defer`
// in the CLI, so it was CLI-scoped while cloud-hypervisor's write lock on the
// image is VM-scoped: a VM that outlived its CLI (spawn timeout escalating to
// SIGKILL) left the slot reading free while the image was still locked, and
// the next build failed to boot with an opaque CH "file is already locked"
// error. That mismatch is what the lease handoff closes — see
// [CacheDiskLease.File] and internal/supervisor's acquireCacheDiskLeases.
func SelectCacheDisks(ctx context.Context, dataDir string, keys []string) ([]CacheDiskSpec, []*CacheDiskLease, error) {
	// Validate all keys upfront so the caller gets a clean error before any
	// disk creation occurs.
	for _, k := range keys {
		if _, ok := ecosystemRegistry[k]; !ok {
			return nil, nil, fmt.Errorf("cachedisk: unknown ecosystem key %q (valid: npm pnpm yarn pip cargo go apt buildkit)", k)
		}
	}

	cacheDir := filepath.Join(dataDir, "caches")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("cachedisk: mkdir %s: %w", cacheDir, err)
	}

	var held []*CacheDiskLease
	specs := make([]CacheDiskSpec, 0, len(keys))
	for _, k := range keys {
		entry := ecosystemRegistry[k]
		spec, lease, err := leaseCacheDiskSlot(ctx, cacheDir, k, entry)
		if err != nil {
			ReleaseCacheDiskLeases(held) // never return partially held leases
			return nil, nil, err
		}
		held = append(held, lease)
		specs = append(specs, spec)
	}
	return specs, held, nil
}

// leaseCacheDiskSlot finds the lowest slot for ecosystemKey whose sidecar lock
// is free, creates the slot's image if needed, and returns the held lease.
func leaseCacheDiskSlot(ctx context.Context, cacheDir, ecosystemKey string, entry ecosystemEntry) (CacheDiskSpec, *CacheDiskLease, error) {
	for slot := 0; slot < MaxCacheDiskSlots; slot++ {
		imagePath := slotImagePath(cacheDir, ecosystemKey, slot)
		// allowOwnPin=false: SELECTION must treat a slot this process already
		// leases as busy and move on, otherwise two concurrent builds inside
		// one process would be handed the same image.
		lease, err := acquireCacheDiskSlot(imagePath, false)
		if errors.Is(err, ErrCacheDiskSlotBusy) {
			continue // slot busy: another builder VM holds this image
		}
		if err != nil {
			return CacheDiskSpec{}, nil, err
		}
		spec, ensureErr := ensureCacheDiskAt(ctx, cacheDir, ecosystemKey, entry, slot)
		if ensureErr != nil {
			lease.Release()
			return CacheDiskSpec{}, nil, ensureErr
		}
		return spec, lease, nil
	}
	return CacheDiskSpec{}, nil, fmt.Errorf(
		"cachedisk: all %d %q cache-disk slots are in use by concurrent builds; wait for one to finish",
		MaxCacheDiskSlots, ecosystemKey)
}

// ─────────────────────────────────────────────────────────────────────────────
// Slot leases (D-HSH-07)
//
// A lease is an flock(LOCK_EX) on the slot's <image>.lock sidecar. Two rules
// govern this file and were both learned the hard way in motive
// nexus3-builder-supervisor-spawn-race (fixes b4489a5 / 95ba583):
//
//  1. NEVER unlink a lease file. flock is attached to the inode; unlinking a
//     lock file another process holds lets the next opener create a fresh
//     inode and "hold" the same slot at the same time. Release therefore only
//     ever CLOSEs the descriptor — see CacheDiskLease.Release for why an
//     explicit LOCK_UN is wrong on a shared open file description.
//  2. OWN-PIN BEFORE PROBE. flock locks belong to the open file description,
//     not the process, so a second open(2)+LOCK_NB of a file THIS process
//     already holds fails with EWOULDBLOCK and reads as "another process has
//     it". Every acquisition consults the in-process pin registry below first.
//
// ─────────────────────────────────────────────────────────────────────────────

// ErrCacheDiskSlotBusy reports that a slot's lease is held by someone else —
// another process, or (when own-pins are not allowed) this process itself.
var ErrCacheDiskSlotBusy = errors.New("cachedisk: slot lease is held")

// slotPin is the process-wide record of one held lease. refs counts the
// outstanding CacheDiskLease values sharing it; the flock is dropped only when
// the last one is released.
type slotPin struct {
	f    *os.File
	refs int
}

var (
	pinMu sync.Mutex
	// pinned maps lock-file path → the lease this process holds on it. This
	// is the own-pin registry required by rule 2 above.
	pinned = map[string]*slotPin{}
)

// CacheDiskLease is a held lease on one cache-disk slot. Its lifetime — not
// the lifetime of whoever selected the slot — is what keeps a second builder
// VM off the same image, so it must be owned by the process whose lifetime
// matches the VM's.
type CacheDiskLease struct {
	imagePath string
	lockPath  string
	released  bool
}

// CacheDiskLockPath returns the sidecar lock path guarding a slot image.
func CacheDiskLockPath(imagePath string) string { return imagePath + ".lock" }

// ImagePath is the cache-disk image this lease guards.
func (l *CacheDiskLease) ImagePath() string {
	if l == nil {
		return ""
	}
	return l.imagePath
}

// File returns the open lock file whose flock this lease holds.
//
// It exists for ONE purpose: passing the descriptor to a child process via
// exec.Cmd.ExtraFiles. flock ownership follows the open file description, and
// an inherited descriptor refers to the SAME description — so the child holds
// the identical lock with no unlock/relock window in which a concurrent build
// could steal the slot. The lock survives until every descriptor referring to
// that description is closed, which is why the parent may (and should) drop
// its own copy afterwards. Callers must not Close or flock this file directly;
// use Release.
func (l *CacheDiskLease) File() *os.File {
	if l == nil {
		return nil
	}
	pinMu.Lock()
	defer pinMu.Unlock()
	if p := pinned[l.lockPath]; p != nil {
		return p.f
	}
	return nil
}

// Release drops this reference to the lease. The underlying flock is released
// only when the last reference goes; the lock FILE is never unlinked (rule 1).
// Release is idempotent and nil-safe.
func (l *CacheDiskLease) Release() {
	if l == nil || l.released {
		return
	}
	l.released = true
	pinMu.Lock()
	defer pinMu.Unlock()
	p := pinned[l.lockPath]
	if p == nil {
		return
	}
	p.refs--
	if p.refs > 0 {
		return
	}
	delete(pinned, l.lockPath)
	// CLOSE, never LOCK_UN. flock belongs to the open file description, and
	// after a lease has been handed to a child through ExtraFiles the parent
	// and the child share that ONE description: an explicit LOCK_UN here would
	// unlock the slot out from under the supervisor that now owns it, which is
	// precisely the CLI-scoped behaviour D-HSH-07 removes. Closing is the
	// correct operation — the lock survives until EVERY descriptor referring
	// to the description is closed, so this drops only this process's claim.
	//
	// The lock FILE is deliberately not unlinked; see rule 1 above.
	_ = p.f.Close()
}

// ReleaseCacheDiskLeases releases every lease in ls.
func ReleaseCacheDiskLeases(ls []*CacheDiskLease) {
	for _, l := range ls {
		l.Release()
	}
}

// CacheDiskSlotPaths returns the image paths of the leased slots, in order.
func CacheDiskSlotPaths(ls []*CacheDiskLease) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.ImagePath())
	}
	return out
}

// AcquireCacheDiskSlot takes the lease for one SPECIFIC slot image path.
//
// This is the re-acquisition entry point: a supervisor that did not perform
// the original selection — an adopting one, or one spawned by `nexus3 recover`
// after the previous supervisor was SIGKILLed — takes back the exact slot
// recorded on the sandbox (domain.Sandbox.CacheDiskSlot) rather than picking a
// new one and colliding with cloud-hypervisor's still-live write lock.
//
// Own-pinned slots succeed (returning an additional reference) instead of
// reporting busy; see rule 2 above.
func AcquireCacheDiskSlot(imagePath string) (*CacheDiskLease, error) {
	return acquireCacheDiskSlot(imagePath, true)
}

// AcquireCacheDiskSlotWait is AcquireCacheDiskSlot with a bounded retry. The
// adopt path needs it: the outgoing supervisor still holds the lease until its
// own shutdown runs, so a single LOCK_NB probe would lose a race it is
// guaranteed to win a moment later.
func AcquireCacheDiskSlotWait(ctx context.Context, imagePath string, timeout time.Duration) (*CacheDiskLease, error) {
	deadline := time.Now().Add(timeout)
	for {
		lease, err := acquireCacheDiskSlot(imagePath, true)
		if err == nil || !errors.Is(err, ErrCacheDiskSlotBusy) {
			return lease, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func acquireCacheDiskSlot(imagePath string, allowOwnPin bool) (*CacheDiskLease, error) {
	lockPath := CacheDiskLockPath(imagePath)
	pinMu.Lock()
	defer pinMu.Unlock()

	// OWN-PIN BEFORE PROBE. Probing first would flock-fail on our own lease.
	if p := pinned[lockPath]; p != nil {
		if !allowOwnPin {
			return nil, fmt.Errorf("%w: %s (held by this process)", ErrCacheDiskSlotBusy, imagePath)
		}
		p.refs++
		return &CacheDiskLease{imagePath: imagePath, lockPath: lockPath}, nil
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("cachedisk: open lease file %s: %w", lockPath, err)
	}
	if flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s (%v)", ErrCacheDiskSlotBusy, imagePath, flockErr)
	}
	pinned[lockPath] = &slotPin{f: f, refs: 1}
	return &CacheDiskLease{imagePath: imagePath, lockPath: lockPath}, nil
}

// AdoptCacheDiskLeaseFD takes ownership of an INHERITED descriptor that
// already holds the slot's flock, as passed through exec.Cmd.ExtraFiles by the
// process that selected the slot. No flock call is made: the lock is already
// held on this open file description and re-locking is neither needed nor
// possible without a window.
//
// It fails closed if fd does not refer to the slot's lock file — a wrong fd
// number would otherwise leave the supervisor believing it owns a slot it does
// not, which is exactly the silent-mismatch class this decision exists to end.
func AdoptCacheDiskLeaseFD(fd int, imagePath string) (*CacheDiskLease, error) {
	if fd < 0 {
		return nil, fmt.Errorf("cachedisk: adopt lease fd for %s: invalid fd %d", imagePath, fd)
	}
	lockPath := CacheDiskLockPath(imagePath)

	// Validate on the RAW fd, before wrapping it in an *os.File. os.NewFile
	// takes ownership: the returned value carries a finalizer that closes the
	// descriptor, so building one on a path that then returns an error would
	// close a descriptor this function does not own — silently breaking
	// whatever else in the process is using that fd number.
	var got, want syscall.Stat_t
	if err := syscallFstat(fd, &got); err != nil {
		return nil, fmt.Errorf("cachedisk: adopt lease fd %d for %s: fstat: %w", fd, imagePath, err)
	}
	if err := syscall.Stat(lockPath, &want); err != nil {
		return nil, fmt.Errorf("cachedisk: adopt lease fd %d for %s: stat %s: %w", fd, imagePath, lockPath, err)
	}
	if got.Dev != want.Dev || got.Ino != want.Ino {
		return nil, fmt.Errorf(
			"cachedisk: adopt lease fd %d does not refer to %s (inherited inode %d:%d, on-disk %d:%d)",
			fd, lockPath, got.Dev, got.Ino, want.Dev, want.Ino)
	}

	pinMu.Lock()
	defer pinMu.Unlock()
	if p := pinned[lockPath]; p != nil {
		// Already pinned here (the same lock reached this process twice).
		// Close the surplus descriptor rather than leaking it; the flock
		// stays held by the description already in the registry.
		_ = syscall.Close(fd)
		p.refs++
		return &CacheDiskLease{imagePath: imagePath, lockPath: lockPath}, nil
	}
	pinned[lockPath] = &slotPin{f: os.NewFile(uintptr(fd), lockPath), refs: 1} //nolint:gosec // validated above
	return &CacheDiskLease{imagePath: imagePath, lockPath: lockPath}, nil
}

// syscallFstat is a var so tests can exercise the fail-closed inode check.
var syscallFstat = syscall.Fstat

// EncodeCacheDiskSlots renders slot image paths for domain.Sandbox.CacheDiskSlot.
func EncodeCacheDiskSlots(paths []string) string { return strings.Join(paths, ",") }

// DecodeCacheDiskSlots parses domain.Sandbox.CacheDiskSlot back into paths.
// An empty record field decodes to no slots, which every caller must treat as
// "this VM leases no cache disk" rather than as an error.
func DecodeCacheDiskSlots(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
