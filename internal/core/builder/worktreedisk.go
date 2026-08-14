package builder

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/moby/patternmatcher"
)

// WorkspaceMount describes a host git worktree that has been captured to an
// ext4 disk image for attachment to a nexus3 sandbox VM as a read-write
// workspace volume.
//
// WorkspaceMount is intentionally minimal: only the fields that current
// consumers actually need are present. Three later slices depend on this
// type's shape, so it must not grow without a concrete consumer requirement.
type WorkspaceMount struct {
	// SourcePath is the absolute path to the host git worktree root that was
	// captured. Used for display, logging, and diagnostics.
	SourcePath string

	// GuestPath is the absolute path inside the VM where the disk image will
	// be mounted (e.g. "/workspace/myrepo"). Convention keeps all workspace
	// mounts under /workspace.
	GuestPath string

	// DiskImage is the absolute path to the ext4 disk image on the host.
	// This file is attached as a virtio-blk device to the sandbox VM.
	DiskImage string
}

// DefaultCaptureMaxBytes is the default capture-size guard threshold for
// [WorktreeToDisk]. A capture larger than this threshold is rejected before
// any disk image is written. The value is a conservative 2 GiB because:
//
//   - Large untracked trees (node_modules, dist, .cache, build outputs) are
//     the most common cause of host OOM from ext4 page-cache pressure during
//     VM boot.
//   - Source trees that legitimately exceed 2 GiB are rare; most working
//     repos are well under this.
//
// Callers that have measured their workspaces and know they are safe may pass
// a larger value; this constant is the recommended default for unknown workspaces.
const DefaultCaptureMaxBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GiB

// WorktreeToDisk walks srcDir into a raw ext4 image at outExt4, capturing the
// full on-disk state of the working tree: dirty tracked files AND untracked
// files, subject to the standard exclusion policy.
//
// Exclusion policy (applied before measuring or copying any content):
//   - Paths matched by <srcDir>/.dockerignore are excluded.
//   - Names in [nexus3AlwaysExclude] (.claude, .agents, .groundwork,
//     .pnpm-store) are always excluded regardless of .dockerignore.
//   - Sockets, device files, named pipes, and irregular files are skipped:
//     they are not transferable to a guest filesystem.
//
// Symlinks are captured as symlinks (not followed). Regular files that share
// an inode on the host (hard links) are materialised as independent files in
// the image, because mke2fs reads each path independently.
//
// Size guard: if the total byte count of included files exceeds maxBytes, the
// call fails immediately — before any disk image is written — with an
// actionable error naming the largest contributing directories and suggesting
// .dockerignore entries. Pass [DefaultCaptureMaxBytes] for the recommended
// threshold.
//
// Returns [ErrMke2fsUnavailable] if mke2fs is not on the host PATH.
func WorktreeToDisk(ctx context.Context, srcDir string, outExt4 string, maxBytes int64) error {
	return WorktreeToDiskWithExtra(ctx, srcDir, outExt4, maxBytes, nil)
}

// WorktreeToDiskWithExtra is identical to [WorktreeToDisk] but also excludes
// any paths matched by extraExclude patterns before measuring or copying
// content. Patterns use the same syntax as .dockerignore.
//
// Use this instead of modifying the source tree's .dockerignore when callers
// need to inject transient exclusions (e.g. shadow-disk directories). The
// source tree is never written to.
func WorktreeToDiskWithExtra(ctx context.Context, srcDir, outExt4 string, maxBytes int64, extraExclude []string) error {
	pm, err := loadDockerIgnore(srcDir)
	if err != nil {
		return fmt.Errorf("worktreedisk: load .dockerignore: %w", err)
	}

	// Merge .dockerignore patterns with the nexus3-internal always-exclude list
	// and any caller-supplied extra patterns.
	allPatterns := slices.Clone(nexus3AlwaysExclude)
	if pm != nil {
		for _, p := range pm.Patterns() {
			allPatterns = append(allPatterns, p.String())
		}
	}
	allPatterns = append(allPatterns, extraExclude...)
	combinedPM, err := patternmatcher.New(allPatterns)
	if err != nil {
		return fmt.Errorf("worktreedisk: build combined ignore patterns: %w", err)
	}

	// Pre-flight size check: fail fast before writing any bytes if the
	// included content would exceed maxBytes. This is the primary defence
	// against host OOM from large untracked trees.
	if err := preflightCaptureSize(srcDir, combinedPM, maxBytes); err != nil {
		return err
	}

	filtered, cleanup, err := filteredWorktreeDir(srcDir, combinedPM, "")
	if err != nil {
		return fmt.Errorf("worktreedisk: filter worktree: %w", err)
	}
	defer cleanup()

	dataBytes, err := dirSizeBytes(filtered)
	if err != nil {
		return fmt.Errorf("worktreedisk: measure filtered dir: %w", err)
	}

	imageSizeBytes := dataBytes*imageSizeHeadroomFactor + imageMinSizeBytes
	const mib = 1024 * 1024
	imageSizeBytes = (imageSizeBytes + mib - 1) &^ (mib - 1)
	if imageSizeBytes < imageMinSizeBytes {
		imageSizeBytes = imageMinSizeBytes
	}

	if err := runMke2fs(ctx, filtered, outExt4, imageSizeBytes); err != nil {
		return fmt.Errorf("worktreedisk: pack ext4: %w", err)
	}
	return nil
}

// preflightCaptureSize walks srcDir (applying combinedPM exclusions) and
// returns an error if the total included byte count exceeds maxBytes. The
// error names the largest top-level contributors and suggests .dockerignore
// entries to bring the total under the limit.
func preflightCaptureSize(srcDir string, combinedPM *patternmatcher.PatternMatcher, maxBytes int64) error {
	topDirBytes := map[string]int64{}
	var total int64

	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// Non-fatal: skip unreadable entries rather than aborting the preflight.
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		excluded, err := combinedPM.MatchesOrParentMatches(rel)
		if err != nil {
			return fmt.Errorf("patternmatcher: %w", err)
		}
		if excluded {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil // directories have no byte cost of their own
		}

		typ := d.Type()
		// Count regular files and symlinks; skip sockets, devices, pipes, irregular files.
		if typ != 0 && typ&fs.ModeSymlink == 0 {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil // skip files we cannot stat
		}
		size := info.Size()
		total += size

		// Attribute the file to its top-level directory (or a synthetic
		// "(root files)" key for entries directly under srcDir).
		topKey := topLevelKey(rel)
		topDirBytes[topKey] += size

		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("worktreedisk: size preflight: %w", walkErr)
	}

	if total <= maxBytes {
		return nil
	}

	// Build an actionable error listing the largest contributors.
	type entry struct {
		name string
		size int64
	}
	entries := make([]entry, 0, len(topDirBytes))
	for name, sz := range topDirBytes {
		entries = append(entries, entry{name, sz})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].size > entries[j].size })

	const topN = 5
	n := min(topN, len(entries))

	var sb strings.Builder
	fmt.Fprintf(&sb, "worktreedisk: capture would be %s (limit %s); the workspace is too large to capture safely.\n",
		formatCaptureBytes(total), formatCaptureBytes(maxBytes))
	fmt.Fprintf(&sb, "Add entries to .dockerignore to exclude large directories. Largest contributors:\n")
	for _, e := range entries[:n] {
		fmt.Fprintf(&sb, "  %-40s  %s\n", e.name, formatCaptureBytes(e.size))
	}
	fmt.Fprintf(&sb, "Hint: echo '<name>' >> .dockerignore   (replace <name> with the directory to exclude)")
	return errors.New(sb.String())
}

// filteredWorktreeDir creates a temporary directory containing only the files
// from src that survive combinedPM exclusion. Entry types handled:
//
//   - Directories: recreated with matching permissions.
//   - Regular files: hardlinked for efficiency; copy fallback on same-device link failure.
//   - Symlinks: recreated as symlinks (target is not followed).
//   - Sockets, device files, named pipes, irregular files: skipped.
//
// stagingBase is passed as the first argument to os.MkdirTemp. An empty string
// uses the OS default (honouring TMPDIR). The staging directory MUST be on the
// same filesystem as src: hardlinking across devices is impossible, and the copy
// fallback would move up to DefaultCaptureMaxBytes of data into memory-backed
// storage, risking host OOM. filteredWorktreeDir detects a cross-device staging
// target and returns an actionable error rather than silently falling back.
//
// Multiple hard links to the same source inode are naturally materialised as
// independent files in the resulting ext4 image because mke2fs reads each path
// independently — no special tracking is required.
//
// The caller must invoke the returned cleanup func when done.
func filteredWorktreeDir(src string, combinedPM *patternmatcher.PatternMatcher, stagingBase string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp(stagingBase, "nexus3-wt-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	// Guard: refuse if the staging directory is on a different device than the
	// source tree. A cross-device layout makes os.Link impossible, and the copy
	// fallback would write up to DefaultCaptureMaxBytes of file data into
	// whatever backing store tmpDir sits on (often a tmpfs) — the exact host-OOM
	// hazard this package exists to prevent.
	srcDev, err := deviceIDOf(src)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("worktreedisk: stat source dir %q: %w", src, err)
	}
	tmpDev, err := deviceIDOf(tmpDir)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("worktreedisk: stat staging dir %q: %w", tmpDir, err)
	}
	if srcDev != tmpDev {
		cleanup()
		return "", nil, fmt.Errorf(
			"worktreedisk: staging temp dir is on a different device than the source tree.\n"+
				"  source:  %s (device %d)\n"+
				"  staging: %s (device %d)\n"+
				"Staging on a different device falls back to copying file data into memory-backed\n"+
				"storage, risking host OOM. Set TMPDIR to a path on the same filesystem as the\n"+
				"source tree (e.g. a parent directory of %s).",
			src, srcDev, tmpDir, tmpDev, src)
	}

	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		excluded, err := combinedPM.MatchesOrParentMatches(rel)
		if err != nil {
			return fmt.Errorf("patternmatcher: %w", err)
		}
		if excluded {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		typ := d.Type()
		dst := filepath.Join(tmpDir, rel)

		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(dst, info.Mode().Perm())

		case typ&fs.ModeSymlink != 0:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)

		case typ == 0: // regular file
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.Link(path, dst); err != nil {
				return copyFileWithMode(path, dst, d)
			}
			return nil

		default:
			// Sockets, device files, named pipes, and irregular files are
			// not transferable to a guest filesystem — skip silently.
			return nil
		}
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return tmpDir, cleanup, nil
}

// topLevelKey returns the top-level component of a relative path, used to
// group per-directory byte counts in the size-guard error message.
// Root-level files (no separator) are grouped under "(root files)".
func topLevelKey(rel string) string {
	sep := string(filepath.Separator)
	if i := strings.Index(rel, sep); i >= 0 {
		return rel[:i]
	}
	return "(root files)"
}

// formatCaptureBytes formats a byte count as a human-readable string (e.g.
// "1.50 GiB"). Used in capture-size guard error messages.
func formatCaptureBytes(n int64) string {
	const (
		kib = 1024
		mib = kib * kib
		gib = mib * kib
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.2f MiB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.2f KiB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// deviceIDOf returns the block device ID of the filesystem that contains path,
// using the st_dev field from stat(2). Used to detect cross-device staging
// targets in [filteredWorktreeDir].
func deviceIDOf(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	// st.Dev is uint64 on linux but int32 on darwin; convert explicitly so this
	// file stays portable across both.
	return uint64(st.Dev), nil
}
