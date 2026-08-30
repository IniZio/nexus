package builder

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
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
func SelectCacheDisks(ctx context.Context, dataDir string, keys []string) ([]CacheDiskSpec, func(), error) {
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

	var held []*os.File
	release := func() {
		for _, f := range held {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		}
		held = nil
	}

	specs := make([]CacheDiskSpec, 0, len(keys))
	for _, k := range keys {
		entry := ecosystemRegistry[k]
		spec, lockFile, err := leaseCacheDiskSlot(ctx, cacheDir, k, entry)
		if err != nil {
			release() // never return partially held leases
			return nil, nil, err
		}
		held = append(held, lockFile)
		specs = append(specs, spec)
	}
	return specs, release, nil
}

// leaseCacheDiskSlot finds the lowest slot for ecosystemKey whose sidecar lock
// is free, creates the slot's image if needed, and returns the held lock file.
func leaseCacheDiskSlot(ctx context.Context, cacheDir, ecosystemKey string, entry ecosystemEntry) (CacheDiskSpec, *os.File, error) {
	for slot := 0; slot < MaxCacheDiskSlots; slot++ {
		lockPath := slotImagePath(cacheDir, ecosystemKey, slot) + ".lock"
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return CacheDiskSpec{}, nil, fmt.Errorf("cachedisk: open lease file %s: %w", lockPath, err)
		}
		if flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
			_ = f.Close() // slot busy: another builder VM holds this image
			continue
		}
		spec, ensureErr := ensureCacheDiskAt(ctx, cacheDir, ecosystemKey, entry, slot)
		if ensureErr != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			return CacheDiskSpec{}, nil, ensureErr
		}
		return spec, f, nil
	}
	return CacheDiskSpec{}, nil, fmt.Errorf(
		"cachedisk: all %d %q cache-disk slots are in use by concurrent builds; wait for one to finish",
		MaxCacheDiskSlots, ecosystemKey)
}
