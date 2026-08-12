package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// EnsureCacheDisk returns a CacheDiskSpec for the given ecosystemKey. On first
// call it creates a sparse ext4 image at {dataDir}/caches/<key>.ext4; on
// subsequent calls it reuses the existing image without recreating it.
//
// The returned spec carries ImagePath, MountPath, and Subpaths as needed by G3
// to attach and (on teardown) sync the disk.
func EnsureCacheDisk(ctx context.Context, dataDir, ecosystemKey string) (CacheDiskSpec, error) {
	entry, ok := ecosystemRegistry[ecosystemKey]
	if !ok {
		return CacheDiskSpec{}, fmt.Errorf("cachedisk: unknown ecosystem key %q (valid: npm pnpm yarn pip cargo go apt buildkit)", ecosystemKey)
	}

	cacheDir := filepath.Join(dataDir, "caches")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return CacheDiskSpec{}, fmt.Errorf("cachedisk: mkdir %s: %w", cacheDir, err)
	}

	imgPath := filepath.Join(cacheDir, ecosystemKey+".ext4")

	// Reuse: if the image already exists, return immediately — do NOT recreate.
	if _, err := os.Stat(imgPath); err == nil {
		return CacheDiskSpec{
			EcosystemKey: ecosystemKey,
			ImagePath:    imgPath,
			MountPath:    entry.mountPath,
			Subpaths:     entry.subpaths,
		}, nil
	}

	// First use: create a sparse ext4 disk from an empty temp directory so
	// mke2fs has a valid source tree to populate the image from.
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

	return CacheDiskSpec{
		EcosystemKey: ecosystemKey,
		ImagePath:    imgPath,
		MountPath:    entry.mountPath,
		Subpaths:     entry.subpaths,
	}, nil
}

// SelectCacheDisks returns CacheDiskSpec values for exactly the requested
// ecosystem keys, creating each cache disk on first use. Unknown keys cause an
// immediate error; the caller receives no partial results.
func SelectCacheDisks(ctx context.Context, dataDir string, keys []string) ([]CacheDiskSpec, error) {
	// Validate all keys upfront so the caller gets a clean error before any
	// disk creation occurs.
	for _, k := range keys {
		if _, ok := ecosystemRegistry[k]; !ok {
			return nil, fmt.Errorf("cachedisk: unknown ecosystem key %q (valid: npm pnpm yarn pip cargo go apt buildkit)", k)
		}
	}

	specs := make([]CacheDiskSpec, 0, len(keys))
	for _, k := range keys {
		spec, err := EnsureCacheDisk(ctx, dataDir, k)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
