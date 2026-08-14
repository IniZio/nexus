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

// captureFreeSpaceFraction is the fraction of host free disk space that the
// auto capture guard considers safe to use for the projected ext4 image. The
// 20 % reserve exists because the builder VM also writes a 4 GiB artifact
// disk and buildkit cache disks to the same workdir as the context image (see
// cmd_sandbox.go). A projected image size exceeding this fraction of available
// space on the outExt4 filesystem is rejected before any bytes are written.
const captureFreeSpaceFraction = 0.8

// statfsAvail returns the number of bytes available to unprivileged users on
// the filesystem containing path. It is a package-level variable so tests can
// inject a stub without writing real gigabytes to disk; production code uses
// the real syscall.Statfs.
var statfsAvail = func(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	// Bavail (not Bfree) counts blocks available to unprivileged users.
	// Both Bavail and Bsize are converted to int64 explicitly: Bsize is
	// uint32 on darwin and int64 on linux (same portability note as st.Dev
	// in deviceIDOf).
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// walkDirFn is the directory walker used by preflightCaptureSize. It is a
// package-level variable so tests can inject synthetic walk errors (e.g.
// ENOENT) without relying on OS-level race conditions. Production code uses
// filepath.WalkDir directly.
var walkDirFn func(string, fs.WalkDirFunc) error = filepath.WalkDir

// isVanishedEntry reports whether err indicates that a filesystem entry has
// ceased to exist since the directory was read. Such entries contribute zero
// bytes: they are gone, not merely unmeasurable, so they do not cause an
// undercount.
//
// ENOENT: the entry was present in the directory listing but was deleted before
// WalkDir could stat it. This is routine in a live working tree: editors remove
// temp files, build tools churn output directories, git rewrites index lock files.
//
// ENOTDIR: a path component was renamed away while the walk was in progress so a
// component that appeared to be a directory no longer is. The net effect is the
// same: the entry is gone and its bytes do not exist on disk.
//
// Contrast with EACCES / EPERM / EIO: those errors mean the entry EXISTS but we
// cannot measure its size. That IS a true undercount and must keep the guard
// fail-closed — the same entry could be a multi-GiB directory invisible to the
// measurement but very much present on disk when mke2fs writes.
func isVanishedEntry(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

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
// Size guard: if maxBytes > 0, the total byte count of included files must not
// exceed maxBytes. If maxBytes <= 0, the guard is derived automatically from
// free space on the filesystem that will hold outExt4: the projected ext4
// image size (total * 2 + 64 MiB) must fit within [captureFreeSpaceFraction]
// of available bytes. Both modes fail immediately — before any disk image is
// written — with an actionable error naming the largest contributing
// directories. Pass 0 for automatic free-space-based protection.
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
	// included content would exceed maxBytes (explicit) or if the projected
	// ext4 image would exceed the free-space safety fraction (auto).
	if err := preflightCaptureSize(srcDir, outExt4, combinedPM, maxBytes); err != nil {
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
// returns an error if the capture would be unsafe. Two modes:
//
//   - maxBytes > 0 (explicit cap): the total included byte count must not
//     exceed maxBytes.
//   - maxBytes <= 0 (auto): the projected ext4 image size
//     (total * imageSizeHeadroomFactor + imageMinSizeBytes) must fit within
//     captureFreeSpaceFraction of the bytes available on the filesystem that
//     will hold outExt4. If statfs fails the guard fails closed.
//
// Both modes fail before any disk image is written, naming the top-5 largest
// top-level contributors so the operator knows what to add to .dockerignore.
//
// Error-skip policy: walk errors are split into two classes.
//
// Vanished-entry errors (ENOENT, ENOTDIR — see [isVanishedEntry]): the entry
// was in the directory listing but has since been deleted or renamed away. It
// contributes genuinely zero bytes — there is no hidden content and therefore no
// undercount. These are silently ignored; they do NOT trigger fail-closed. They
// are routine in a live working tree (editor temp files, build outputs, git lock
// files). See [isVanishedEntry] for the full rationale.
//
// All other I/O errors (EACCES, EPERM, EIO, …): the entry EXISTS but its size
// could not be measured. Those bytes are absent from total, making it an
// undercount. A guard built on an undercount cannot guarantee that mke2fs will
// not exhaust disk space — the same reasoning that drives the statfs fail-closed
// policy. preflightCaptureSize fails closed if any such entry is encountered.
//
// Type-based skips (sockets, device files, named pipes) and exclusion-policy
// skips (.dockerignore, nexus3AlwaysExclude) are legitimate and expected; they
// do NOT trigger this check.
func preflightCaptureSize(srcDir, outExt4 string, combinedPM *patternmatcher.PatternMatcher, maxBytes int64) error {
	topDirBytes := map[string]int64{}
	var total int64

	// errSkipCount counts entries that exist but whose size could not be measured
	// (EACCES, EPERM, EIO, and similar). Their bytes are absent from total —
	// a true undercount. errSkipFirstPath is the first such path, for use in
	// the error message. Vanished-entry errors (ENOENT, ENOTDIR) are NOT counted
	// here; see [isVanishedEntry] for the rationale.
	var errSkipCount int
	var errSkipFirstPath string
	recordUnmeasurableSkip := func(path string) {
		errSkipCount++
		if errSkipFirstPath == "" {
			errSkipFirstPath = path
		}
	}

	walkErr := walkDirFn(srcDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// Error reaching this entry. Split on the error class:
			//
			// Vanished (ENOENT, ENOTDIR): the entry has been deleted or renamed
			// away since the directory was read. It contributes zero bytes — no
			// hidden content, no undercount. Skip silently; this is routine in a
			// live working tree. See [isVanishedEntry].
			//
			// All other errors (EACCES, EPERM, EIO, …): the entry EXISTS but we
			// cannot measure it. Its bytes are absent from total — a true
			// undercount. Record for the fail-closed check below.
			if isVanishedEntry(werr) {
				return nil
			}
			recordUnmeasurableSkip(path)
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
			// Same split as werr: ENOENT/ENOTDIR means the entry vanished between
			// ReadDir and Info (contributes zero bytes, not an undercount); any
			// other error means the entry exists but is unmeasurable (undercount,
			// fail closed).
			if isVanishedEntry(err) {
				return nil
			}
			recordUnmeasurableSkip(path)
			return nil
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

	// Fail closed if any entries were unmeasurable (error class other than
	// ENOENT/ENOTDIR). Their bytes are absent from total, so the measurement is
	// an undercount. A guard built on an undercount cannot guarantee safety: a
	// large unreadable subtree on a nearly-full disk projects a tiny image, sails
	// through the guard, and then mke2fs writes until ENOSPC — the exact hazard
	// this guard exists to prevent. Vanished entries (ENOENT, ENOTDIR) are NOT
	// counted here; see [isVanishedEntry] and the comment above. Fix file
	// permissions or exclude the path via .dockerignore to proceed.
	if errSkipCount > 0 {
		return fmt.Errorf(
			"worktreedisk: preflight: %d path(s) could not be read during size measurement (first: %s); "+
				"cannot guarantee the capture fits within safe bounds — fix file permissions or exclude "+
				"the path via .dockerignore",
			errSkipCount, errSkipFirstPath)
	}

	// Determine whether the capture is safe.
	var header string
	if maxBytes > 0 {
		// Explicit cap: check raw file total.
		if total <= maxBytes {
			return nil
		}
		header = fmt.Sprintf("worktreedisk: capture would be %s (limit %s); the workspace is too large to capture safely.",
			formatCaptureBytes(total), formatCaptureBytes(maxBytes))
	} else {
		// Auto mode: check projected image size against available disk space.
		projectedBytes := total*imageSizeHeadroomFactor + imageMinSizeBytes
		avail, statErr := statfsAvail(filepath.Dir(outExt4))
		if statErr != nil {
			// Cannot measure available space — fail closed rather than
			// proceeding unguarded. A guard that cannot measure cannot
			// guarantee safety: mke2fs will happily write a large image
			// until it hits ENOSPC, which is exactly the hazard this guard
			// exists to prevent. Fail-closed is the only policy that upholds
			// the guard's contract; fail-open would be a silent no-op on any
			// host where the output path is inaccessible at preflight time.
			return fmt.Errorf("worktreedisk: preflight: %w", statErr)
		}
		safeAvail := int64(float64(avail) * captureFreeSpaceFraction)
		if projectedBytes <= safeAvail {
			return nil
		}
		dir := filepath.Dir(outExt4)
		header = fmt.Sprintf(
			"worktreedisk: capture is %s; projected ext4 image %s exceeds %.0f%% of %s available on %s",
			formatCaptureBytes(total),
			formatCaptureBytes(projectedBytes),
			captureFreeSpaceFraction*100,
			formatCaptureBytes(avail),
			dir,
		)
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
	fmt.Fprintf(&sb, "%s\n", header)
	fmt.Fprintf(&sb, "Add entries to .dockerignore to exclude large directories, or pass --capture-max <size> to override. Largest contributors:\n")
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
// fallback would move all captured file data into memory-backed storage, risking
// host OOM. filteredWorktreeDir detects a cross-device staging target and returns
// an actionable error rather than silently falling back.
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
	// fallback would write all captured file data into whatever backing store
	// tmpDir sits on (often a tmpfs) — the exact host-OOM hazard this package
	// exists to prevent.
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
