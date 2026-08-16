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
	projected := int64(count) * per

	// Fall back to parent dir if diskDir does not yet exist.
	checkDir := diskDir
	if _, serr := os.Stat(diskDir); os.IsNotExist(serr) {
		checkDir = filepath.Dir(diskDir)
	}

	free, err := DiskStatfs(checkDir)
	if err != nil {
		// Fail closed: cannot measure free space, refuse to proceed.
		return nil, fmt.Errorf("%w: cannot stat free space on %s: %v",
			ErrInsufficientDisk, checkDir, err)
	}

	r := &DiskPreflightResult{
		Count:           count,
		PerSandboxBytes: per,
		ProjectedBytes:  projected,
		FreeBytes:       free,
	}

	if projected > free {
		return r, fmt.Errorf(
			"%w: %d sandbox(es) × %.2f GiB = %.2f GiB projected, only %.2f GiB free on %s"+
				"; remove unused sandboxes or use --force to override",
			ErrInsufficientDisk,
			count,
			float64(per)/(1<<30),
			float64(projected)/(1<<30),
			float64(free)/(1<<30),
			checkDir,
		)
	}
	return r, nil
}
