//go:build linux

package agent

import (
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
)

// rootfsManifestMinBytes is the file-size threshold above which a file is
// included in the rootfs size manifest emitted after the buildkit Solve step.
//
// 1 MiB sits well below the 32 MiB truncation boundary (every affected file is
// ≥ 32 MiB) but high enough to keep the manifest compact for typical ubuntu /
// debian rootfs trees, which contain thousands of small files. A full manifest
// of all files would generate tens of thousands of log lines; restricting to
// ≥ 1 MiB yields tens to hundreds of lines for the files that actually matter.
const rootfsManifestMinBytes int64 = 1 << 20 // 1 MiB

// rootfsManifestEntry holds one file's path (relative to the rootfs root) and
// its on-disk byte count at the export seam — after buildkit Solve writes the
// directory and BEFORE mke2fs packs it into an ext4 image. Comparing this
// manifest against the final ext4 contents localises which stage introduces any
// truncation.
type rootfsManifestEntry struct {
	RelPath string
	Size    int64
}

// logRootfsSizeManifest walks rootfsDir and emits one log line per regular file
// whose size is ≥ rootfsManifestMinBytes. The log output reaches the host via
// the exec-pipe channel used by all other builder-VM log output (stderr →
// ring reader → host execBuf), so no new transport is needed.
//
// Diagnostic only — does NOT fail the build. If the walk itself errors, the
// partial manifest collected so far is logged and the function returns.
//
// The returned slice exists so unit tests can inspect the entries without
// parsing log output; production callers may ignore it.
func logRootfsSizeManifest(rootfsDir string) []rootfsManifestEntry {
	var entries []rootfsManifestEntry

	walkErr := filepath.WalkDir(rootfsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() < rootfsManifestMinBytes {
			return nil
		}
		rel, relErr := filepath.Rel(rootfsDir, path)
		if relErr != nil {
			rel = path // fallback: absolute path
		}
		entries = append(entries, rootfsManifestEntry{RelPath: rel, Size: info.Size()})
		return nil
	})

	if walkErr != nil {
		log.Printf("in-guest build: rootfs-size-manifest: walk error (partial manifest): %v", walkErr)
	}

	log.Printf("in-guest build: rootfs-size-manifest: %d file(s) >= %s in %s",
		len(entries), fmtBytes(rootfsManifestMinBytes), rootfsDir)
	for _, e := range entries {
		log.Printf("in-guest build: rootfs-size-manifest:   %-60s %d", e.RelPath, e.Size)
	}

	return entries
}

// fmtBytes formats n as a human-readable binary size string.
func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
