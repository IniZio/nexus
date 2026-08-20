package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// perSandboxAllocatedBytesDefault is the conservative per-sandbox workspace
// disk allocation estimate used when diskDir contains no existing workspace
// disks. Derived from a measured example-app pilot sandbox:
// 9,583,184 blocks × 512 = 4,906,590,208 bytes ≈ 4.57 GiB.
const perSandboxAllocatedBytesDefault = int64(9_583_184) * 512

// ErrInsufficientDisk is the sentinel error returned when the disk preflight
// fails. Callers should use errors.Is to detect it.
var ErrInsufficientDisk = errors.New("service: insufficient disk space")

// DiskPreflightResult holds the arithmetic from a successful CheckDiskSpace
// call. Returned alongside nil when the check passes.
type DiskPreflightResult struct {
	Count           int   // number of sandboxes requested
	PerSandboxBytes int64 // estimated allocated bytes per workspace disk
	ProjectedBytes  int64 // Count × PerSandboxBytes
	FreeBytes       int64 // host available bytes (syscall.Statfs Bavail × Bsize)
}

// DiskStatfs is the injectable free-space probe. The production implementation
// calls syscall.Statfs and returns Bavail × Bsize.
//
// Exported so that tests in other packages (e.g. internal/cli) can override it
// without requiring a live filesystem. Same-package tests also use it directly.
// Replace before calling CheckDiskSpace and restore (defer) after.
var DiskStatfs = func(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Bavail: blocks available to unprivileged users (df "available" column).
	// Bsize:  filesystem block size.
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// diskAllocatedBytes returns the allocated bytes for path using
// stat(2).Blocks * 512. Returns 0 on any error (directories, missing files,
// or stat failure). This mirrors the allocatedBytes function in reap.go —
// same contract, different package.
//
// Use this instead of os.Stat().Size() to avoid the sparse-disk trap: sparse
// ext4 images report inflated apparent sizes (os.Stat().Size()) while their
// actual on-disk footprint (Blocks * 512) is orders of magnitude smaller.
func diskAllocatedBytes(path string) int64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0
	}
	return st.Blocks * 512
}

// estimatePerSandbox scans diskDir for existing workspace disk images
// (*-workspace.ext4) and returns their mean allocated size
// (stat(2).Blocks * 512). Falls back to perSandboxAllocatedBytesDefault when
// no workspace disks are present or diskDir is unreadable.
//
// Critically, the measurement uses Blocks * 512 (allocated bytes), not
// os.Stat().Size() (apparent size). Sparse ext4 images have apparent sizes
// many times larger than their actual footprint — using apparent size would
// cause the preflight to project far too much disk usage and refuse valid
// creates.
func estimatePerSandbox(diskDir string) int64 {
	entries, err := os.ReadDir(diskDir)
	if err != nil {
		return perSandboxAllocatedBytesDefault
	}

	const wsSuffix = "-workspace.ext4"
	var total, count int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if !strings.HasSuffix(e.Name(), wsSuffix) {
			continue
		}
		n := diskAllocatedBytes(filepath.Join(diskDir, e.Name()))
		if n > 0 {
			total += n
			count++
		}
	}
	if count == 0 {
		return perSandboxAllocatedBytesDefault
	}
	return total / count
}

// CheckDiskSpace verifies that diskDir has enough free space for count new
// sandbox workspace disks.
//
// # Estimate scope (read before trusting the number)
//
// The per-sandbox figure comes from estimatePerSandbox, which samples ONLY
// existing *-workspace.ext4 files. It does NOT sample the root .raw disk,
// which is usually the larger of the two. Callers that know exactly what they
// are about to write should use ProjectCreateBytes / ProjectForkBytes with
// CheckDiskSpaceBytes instead; this count-based form is the coarse fallback.
//
// # Sparse-disk contract
//
// The per-sandbox estimate is derived from existing workspace disks in diskDir
// using stat(2).Blocks * 512 (allocated bytes). Apparent file sizes — as
// reported by os.Stat().Size() or du --apparent-size — are never used.
// Nexus3 workspace disks are created with os.Truncate + mke2fs -E nodiscard
// (sparse ext4), so their apparent size is wildly larger than their actual
// footprint. A real audit measured 101 GiB apparent versus 11.2 GiB actually
// allocated. Using apparent size would either refuse valid creates (false
// negative) or pass when the disk is genuinely full (false positive).
//
// When no existing workspace disks are present, the default estimate
// (perSandboxAllocatedBytesDefault, ≈ 4.57 GiB) is used.
//
// # Return values
//
// Returns (*DiskPreflightResult, nil) when sufficient space is available.
// Returns (result, ErrInsufficientDisk) when the check fails; the error
// message is actionable (names free/projected figures and the --force flag).
func CheckDiskSpace(diskDir string, count int) (*DiskPreflightResult, error) {
	per := estimatePerSandbox(diskDir)
	detail := fmt.Sprintf("%d sandbox(es) × %.2f GiB", count, float64(per)/(1<<30))
	r, err := CheckDiskSpaceBytes(diskDir, int64(count)*per, detail)
	if r != nil {
		r.Count = count
		r.PerSandboxBytes = per
	}
	return r, err
}

// CheckDiskSpaceBytes is the byte-exact form of the preflight: the caller has
// already computed how many allocated bytes it is about to write, so no
// sampling heuristic is involved. detail is a short human phrase naming where
// the projection came from (e.g. "root disk 4.44 GiB + workspace 0.12 GiB");
// it is interpolated into the refusal message so the operator can see which
// component dominates.
//
// # Reflink caveat
//
// The projection assumes the copy actually allocates. On btrfs and xfs,
// cowExt4's `cp --reflink=auto` clones extents and the true cost is near zero,
// so on those filesystems this check can refuse a create that would have
// succeeded. That is the reason every caller exposes a --force override.
//
// Returns (*DiskPreflightResult, nil) when sufficient space is available.
// Returns (result, ErrInsufficientDisk) when the check fails. Fails CLOSED:
// if free space cannot be measured at all, the check refuses.
func CheckDiskSpaceBytes(diskDir string, projected int64, detail string) (*DiskPreflightResult, error) {
	checkDir := existingAncestor(diskDir)
	free, err := DiskStatfs(checkDir)
	if err != nil {
		// Fail closed: cannot measure free space, refuse to proceed.
		return nil, fmt.Errorf("%w: cannot stat free space on %s: %v",
			ErrInsufficientDisk, checkDir, err)
	}

	r := &DiskPreflightResult{
		Count:           1,
		PerSandboxBytes: projected,
		ProjectedBytes:  projected,
		FreeBytes:       free,
	}

	if projected > free {
		return r, fmt.Errorf(
			"%w: %s = %.2f GiB projected, only %.2f GiB free on %s"+
				"; remove unused sandboxes (nexus3 reap --apply) or pass --force to override",
			ErrInsufficientDisk,
			detail,
			float64(projected)/(1<<30),
			float64(free)/(1<<30),
			checkDir,
		)
	}
	return r, nil
}

// existingAncestor walks up from path until it finds a directory that exists,
// and returns that. statfs reports the containing filesystem, so any existing
// ancestor answers the free-space question for a path that does not exist yet.
//
// Walking up ONE level is not enough. On a machine that has never run nexus3,
// neither <root>/disks nor <root> itself exists, so a single-level fallback
// statfs'd a missing directory, failed closed, and refused the create — the
// preflight would have blocked every first-ever create on a fresh host. The
// walk terminates at "/" or ".", both of which always exist.
func existingAncestor(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

// ProjectCreateBytes returns a conservative UPPER BOUND on the allocated bytes
// one CreateAndBoot is about to write into diskDir, plus a human phrase naming
// the components.
//
// The root figure is the source artifact's own allocated size. That is an
// upper bound, not an exact cost: cowExt4 runs `cp --sparse=always`, which
// punches holes for zero runs the source had allocated, so the copy can only
// ever be smaller. Measured on a real host, a 6.00 GiB artifact produced a
// 2.64 GiB copy — the projection over-charges by ~2.3x. It is still far better
// than the sampled estimate it replaces (which ignored root disks entirely),
// and it errs toward refusing rather than toward filling the disk, but it is
// the reason --force exists on every caller.
//
// The workspace disk cannot be measured before capture runs, so it falls back
// to estimatePerSandbox's sample of existing workspace disks.
//
// sourceArtifact is empty when --rootfs is used (the image is booted in place,
// no copy is made). withWorkspace is false when no capture was requested.
// Both empty means nothing will be allocated and the projection is zero.
func ProjectCreateBytes(diskDir, sourceArtifact string, withWorkspace bool) (int64, string) {
	var projected int64
	var parts []string
	if sourceArtifact != "" {
		n := diskAllocatedBytes(sourceArtifact)
		projected += n
		parts = append(parts, fmt.Sprintf("root disk %.2f GiB", float64(n)/(1<<30)))
	}
	if withWorkspace {
		n := estimatePerSandbox(diskDir)
		projected += n
		parts = append(parts, fmt.Sprintf("workspace ~%.2f GiB", float64(n)/(1<<30)))
	}
	return projected, strings.Join(parts, " + ")
}

// ProjectForkBytes returns the allocated bytes a fork of parent into count
// children is about to write into diskDir, plus a human phrase.
//
// Fork copies every one of the parent's disks per child — the root .raw and
// each extra disk (workspace, shadow disks) — so the projection is the
// parent's measured on-disk footprint multiplied by the child count. Unlike
// the create path there is no estimate here: every file being copied already
// exists and is measured directly.
//
// A parent with no disks in diskDir (e.g. a --rootfs sandbox) projects zero.
func ProjectForkBytes(diskDir, parentID string, count int) (int64, string) {
	entries, err := os.ReadDir(diskDir)
	if err != nil {
		return 0, ""
	}
	var per int64
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), parentID) {
			continue
		}
		// Intent files are metadata, not copied by fork.
		if strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		per += diskAllocatedBytes(filepath.Join(diskDir, e.Name()))
	}
	projected := int64(count) * per
	return projected, fmt.Sprintf("%d child(ren) × %.2f GiB parent footprint",
		count, float64(per)/(1<<30))
}
