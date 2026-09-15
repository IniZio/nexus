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

const cacheDiskSizeBytes int64 = 10 * 1024 * 1024 * 1024 // default sparse size for new per-ecosystem cache disks

type ecosystemEntry struct { // canonical guest mount path and optional subpaths for one ecosystem cache
	mountPath string
	subpaths  []string
}

var ecosystemRegistry = map[string]ecosystemEntry{ // ecosystem keys to canonical guest mount paths
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

const MaxCacheDiskSlots = 8 // max concurrent builder VMs per ecosystem

func slotImagePath(cacheDir, ecosystemKey string, slot int) string { // image path for slot i
	if slot == 0 {
		return filepath.Join(cacheDir, ecosystemKey+".ext4")
	}
	return filepath.Join(cacheDir, fmt.Sprintf("%s-%d.ext4", ecosystemKey, slot))
}

func ensureCacheDiskAt(ctx context.Context, cacheDir, ecosystemKey string, entry ecosystemEntry, slot int) (CacheDiskSpec, error) {
	imgPath := slotImagePath(cacheDir, ecosystemKey, slot)

	spec := CacheDiskSpec{
		EcosystemKey: ecosystemKey,
		ImagePath:    imgPath,
		MountPath:    entry.mountPath,
		Subpaths:     entry.subpaths,
	}

	preservedSize := cacheDiskSizeBytes
	if _, err := os.Stat(imgPath); err == nil {
		if !cacheDiskIsDirty(imgPath) {
			// Clean reuse: fence dirty until occupant confirms sync
			if err := markCacheDiskDirty(imgPath); err != nil {
				return CacheDiskSpec{}, fmt.Errorf("cachedisk: %w", err)
			}
			return spec, nil
		}
		// Fenced dirty (D-DC-31): wipe to avoid poisoned layer data
		if fi, statErr := os.Stat(imgPath); statErr == nil && fi.Size() > cacheDiskSizeBytes {
			preservedSize = fi.Size()
		}
		log.Printf("cachedisk: %s slot %d left dirty by a prior unclean death; wiping cache (%s) and recreating at %d bytes",
			ecosystemKey, slot, imgPath, preservedSize)
		if err := os.Remove(imgPath); err != nil {
			return CacheDiskSpec{}, fmt.Errorf("cachedisk: wipe dirty %s: %w", ecosystemKey, err)
		}
	}

	tmpSrc, err := os.MkdirTemp("", "nexus3-cachedisk-src-*")
	if err != nil {
		return CacheDiskSpec{}, fmt.Errorf("cachedisk: create src tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpSrc)

	if err := runMke2fs(ctx, tmpSrc, imgPath, preservedSize); err != nil {
		_ = os.Remove(imgPath)
		return CacheDiskSpec{}, fmt.Errorf("cachedisk: create ext4 for %q: %w", ecosystemKey, err)
	}
	if err := markCacheDiskDirty(imgPath); err != nil {
		return CacheDiskSpec{}, fmt.Errorf("cachedisk: %w", err)
	}
	return spec, nil
}

func dirtyMarkerPath(imgPath string) string { // sidecar fencing-marker path for a cache disk image
	return imgPath + ".dirty"
}

func cacheDiskIsDirty(imgPath string) bool { // reports whether imgPath is fenced dirty
	_, err := os.Stat(dirtyMarkerPath(imgPath))
	return err == nil
}

// Fencing marker before builder VM attaches; only mechanism covering SIGKILL.
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

func markCacheDiskClean(imgPath string) error { // clears fencing marker; no-op if absent
	markerPath := dirtyMarkerPath(imgPath)
	if err := os.Remove(markerPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("clear dirty marker %s: %w", markerPath, err)
	}
	return fsyncDir(filepath.Dir(markerPath))
}

func fsyncDir(dir string) error { // makes prior create/remove within dir crash-durable
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

// Leases one free cache-disk slot per ecosystem key (D-HSH-07).
// Lease handed to supervisor to survive CLI termination.
func SelectCacheDisks(ctx context.Context, dataDir string, keys []string) ([]CacheDiskSpec, []*CacheDiskLease, error) {
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

func leaseCacheDiskSlot(ctx context.Context, cacheDir, ecosystemKey string, entry ecosystemEntry) (CacheDiskSpec, *CacheDiskLease, error) { // finds lowest free slot and takes lease
	for slot := 0; slot < MaxCacheDiskSlots; slot++ {
		imagePath := slotImagePath(cacheDir, ecosystemKey, slot)
		// allowOwnPin=false: don't reuse same image for concurrent builds
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

// ── Slot leases (D-HSH-07) ──
/** A lease is flock(LOCK_EX) on slot's <image>.lock sidecar (motive
nexus3-builder-supervisor-spawn-race, fixes b4489a5 / 95ba583):
1. NEVER unlink lease file: flock attached to inode; unlinking lets next opener
   create fresh inode and "hold" same slot at same time. Release only CLOSEs
   descriptor; see CacheDiskLease.Release why LOCK_UN is wrong on shared
   file description.
2. OWN-PIN BEFORE PROBE: flock belongs to file description not process, so
   second open(2)+LOCK_NB of file THIS process holds fails EWOULDBLOCK and
   reads as "another process has it". Every acquisition consults in-process
   pin registry below first. */

var ErrCacheDiskSlotBusy = errors.New("cachedisk: slot lease is held")

type slotPin struct { // process-wide record of one held lease
	f    *os.File
	refs int
}

var (
	pinMu  sync.Mutex
	pinned = map[string]*slotPin{} // own-pin registry (per rule 2 above)
)

type CacheDiskLease struct { // held lease on one cache-disk slot
	imagePath string
	lockPath  string
	released  bool
}

func CacheDiskLockPath(imagePath string) string { return imagePath + ".lock" } // sidecar lock path

func (l *CacheDiskLease) ImagePath() string {
	if l == nil {
		return ""
	}
	return l.imagePath
}

// Returns open lock file. For passing descriptor to child via ExtraFiles;
// flock follows file description so child holds identical lock. Don't Close/flock directly.
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

func (l *CacheDiskLease) Release() { // drops reference; flock released only at last ref; idempotent and nil-safe
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
	// CLOSE, never LOCK_UN (D-HSH-07): flock belongs to file description.
	// After lease handed to child via ExtraFiles, parent and child share ONE
	// description. LOCK_UN would unlock slot from under supervisor now owning
	// it. Lock survives until EVERY descriptor is closed. Don't unlink (rule 1).
	_ = p.f.Close()
}

func ReleaseCacheDiskLeases(ls []*CacheDiskLease) { // releases every lease in ls
	for _, l := range ls {
		l.Release()
	}
}

func CacheDiskSlotPaths(ls []*CacheDiskLease) []string { // image paths of leased slots, in order
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.ImagePath())
	}
	return out
}

// Re-acquisition: adopting supervisor takes back exact slot.
func AcquireCacheDiskSlot(imagePath string) (*CacheDiskLease, error) {
	return acquireCacheDiskSlot(imagePath, true)
}

func AcquireCacheDiskSlotWait(ctx context.Context, imagePath string, timeout time.Duration) (*CacheDiskLease, error) { // AcquireCacheDiskSlot with bounded retry for adopt path
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
	// OWN-PIN BEFORE PROBE: probing first would flock-fail on our own lease
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

// Takes ownership of INHERITED fd holding slot's flock; fails closed if wrong fd.
func AdoptCacheDiskLeaseFD(fd int, imagePath string) (*CacheDiskLease, error) {
	if fd < 0 {
		return nil, fmt.Errorf("cachedisk: adopt lease fd for %s: invalid fd %d", imagePath, fd)
	}
	lockPath := CacheDiskLockPath(imagePath)

	// Validate before os.NewFile (which takes ownership)
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

	// Set FD_CLOEXEC: inherited descriptor arrives with it CLEARED (os/exec's
	// ExtraFiles behavior). Leaving it clear leaks lease through NEXT execve
	// (netns child, then cloud-hypervisor), so supervisor SIGKILL wouldn't free
	// slot and recover/supervisor-upgrade would block on lock nothing owns.
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, syscall.FD_CLOEXEC); errno != 0 {
		return nil, fmt.Errorf("cachedisk: adopt lease fd %d for %s: set FD_CLOEXEC: %w", fd, imagePath, errno)
	}

	pinMu.Lock()
	defer pinMu.Unlock()
	if p := pinned[lockPath]; p != nil {
		_ = syscall.Close(fd) // already pinned; close surplus descriptor
		p.refs++
		return &CacheDiskLease{imagePath: imagePath, lockPath: lockPath}, nil
	}
	pinned[lockPath] = &slotPin{f: os.NewFile(uintptr(fd), lockPath), refs: 1} //nolint:gosec // validated above
	return &CacheDiskLease{imagePath: imagePath, lockPath: lockPath}, nil
}

var syscallFstat = syscall.Fstat // var so tests can exercise fail-closed checks

func EncodeCacheDiskSlots(paths []string) string { return strings.Join(paths, ",") } // renders slot paths for domain.Sandbox.CacheDiskSlot

func DecodeCacheDiskSlots(s string) []string { // parses domain.Sandbox.CacheDiskSlot back into paths
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
