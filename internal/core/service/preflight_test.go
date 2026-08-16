package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// makeSparseWorkspaceDisk creates a file at path that is sparse: it has a small
// allocated footprint (one filesystem block written at the start) but a large
// apparent size (10 GiB via os.Truncate). This mimics a real workspace disk
// that has been pre-allocated but not fully written, and is the M3-AC2
// sparse-image fixture.
//
// Returns (allocatedBytes, apparentSize). Calls t.Skip if the filesystem does
// not support sparse files.
func makeSparseWorkspaceDisk(t *testing.T, path string) (allocated, apparent int64) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Write one 4 KiB block at offset 0 so stat.Blocks > 0.
	if _, err := f.Write(make([]byte, 4096)); err != nil {
		f.Close()
		t.Fatalf("write block: %v", err)
	}
	// Truncate to a large apparent size without writing the bulk of the data.
	const apparentSize = int64(10 << 30) // 10 GiB
	if err := f.Truncate(apparentSize); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	f.Close()

	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatalf("stat: %v", err)
	}
	alloc := st.Blocks * 512
	if alloc >= apparentSize {
		t.Skipf("filesystem does not support sparse files (allocated %d >= apparent %d); skipping", alloc, apparentSize)
	}
	t.Logf("sparse file: apparent=%d bytes, allocated=%d bytes (ratio %.0fx)", apparentSize, alloc, float64(apparentSize)/float64(alloc))
	return alloc, apparentSize
}

// TestDiskAllocatedBytes_SparseVsApparent proves that diskAllocatedBytes
// returns the on-disk allocated size, not the apparent (os.Stat().Size()) size.
// This is the unit-level proof that we use Blocks * 512, not file.Size().
func TestDiskAllocatedBytes_SparseVsApparent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sparse.ext4")

	allocated, apparent := makeSparseWorkspaceDisk(t, p)

	got := diskAllocatedBytes(p)
	if got == apparent {
		t.Errorf("diskAllocatedBytes returned apparent size %d; should return allocated size %d",
			apparent, allocated)
	}
	if got != allocated {
		t.Errorf("diskAllocatedBytes = %d, want %d (stat.Blocks*512)", got, allocated)
	}
}

// TestEstimatePerSandbox_SparseImage verifies that estimatePerSandbox uses
// allocated bytes (not apparent size) when scanning existing workspace disks.
func TestEstimatePerSandbox_SparseImage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sb-01ABCDEFGHIJ-workspace.ext4")

	allocated, apparent := makeSparseWorkspaceDisk(t, p)

	est := estimatePerSandbox(dir)

	// If the implementation mistakenly used apparent size, est would be ~10 GiB.
	// Correct implementation returns the small allocated size.
	if est >= apparent {
		t.Errorf("estimatePerSandbox = %d bytes; appears to use apparent size (%d); should use allocated size (%d)",
			est, apparent, allocated)
	}
	t.Logf("estimatePerSandbox = %d bytes (allocated, not apparent %d)", est, apparent)
}

// TestCheckDiskSpace_SparseImage is the M3-AC2 regression test.
//
// It creates a sparse workspace disk — small allocated footprint, 10 GiB
// apparent size — and verifies that CheckDiskSpace PASSES when 1 GiB is free.
//
// A naive check using apparent size would refuse (10 GiB > 1 GiB) — a false
// rejection that prevents valid creates on any machine with < 10 GiB free.
// The correct implementation uses stat(2).Blocks * 512 (allocated bytes),
// which is tiny for a sparse file, so 1 GiB free is more than enough.
func TestCheckDiskSpace_SparseImage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sb-01ABCDEFGHIJ-workspace.ext4")

	allocated, apparent := makeSparseWorkspaceDisk(t, p)

	// Inject 1 GiB free — enough for the tiny allocated estimate but NOT for
	// the 10 GiB apparent size. If the check uses apparent size it would refuse.
	const freeSpace = int64(1 << 30) // 1 GiB
	old := DiskStatfs
	DiskStatfs = func(_ string) (int64, error) { return freeSpace, nil }
	defer func() { DiskStatfs = old }()

	r, err := CheckDiskSpace(dir, 1)
	if err != nil {
		t.Errorf("CheckDiskSpace failed when it should pass (sparse disk with %d allocated bytes, 1 GiB free): %v",
			allocated, err)
	}
	if r != nil && r.PerSandboxBytes >= apparent {
		t.Errorf("PerSandboxBytes = %d; used apparent size (%d); must use allocated size (%d)",
			r.PerSandboxBytes, apparent, allocated)
	}
	if r != nil {
		t.Logf("preflight passed: %d sandbox × %d bytes allocated = %d bytes projected, %d bytes free",
			r.Count, r.PerSandboxBytes, r.ProjectedBytes, r.FreeBytes)
	}
}

// TestCheckDiskSpace_Insufficient verifies that CheckDiskSpace returns
// ErrInsufficientDisk with an actionable message when projected > free.
func TestCheckDiskSpace_Insufficient(t *testing.T) {
	dir := t.TempDir()

	old := DiskStatfs
	DiskStatfs = func(_ string) (int64, error) { return 1 << 20, nil } // 1 MiB free
	defer func() { DiskStatfs = old }()

	r, err := CheckDiskSpace(dir, 1)
	if !errors.Is(err, ErrInsufficientDisk) {
		t.Errorf("want ErrInsufficientDisk, got %v", err)
	}
	if r == nil {
		t.Error("result should be non-nil even on failure (contains arithmetic for diagnostics)")
	}
	if err != nil {
		msg := err.Error()
		for _, want := range []string{"GiB", "free", "--force"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error message missing %q: %s", want, msg)
			}
		}
	}
}

// TestCheckDiskSpace_Sufficient verifies the happy path: enough free space,
// no existing workspace disks (falls back to default estimate).
func TestCheckDiskSpace_Sufficient(t *testing.T) {
	dir := t.TempDir()

	old := DiskStatfs
	DiskStatfs = func(_ string) (int64, error) { return 1 << 40, nil } // 1 TiB free
	defer func() { DiskStatfs = old }()

	r, err := CheckDiskSpace(dir, 2)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("result is nil on success")
	}
	if r.Count != 2 {
		t.Errorf("Count = %d, want 2", r.Count)
	}
	want := 2 * perSandboxAllocatedBytesDefault
	if r.ProjectedBytes != want {
		t.Errorf("ProjectedBytes = %d, want %d (2 × default)", r.ProjectedBytes, want)
	}
	if r.FreeBytes != 1<<40 {
		t.Errorf("FreeBytes = %d, want %d", r.FreeBytes, int64(1<<40))
	}
}

// TestCheckDiskSpace_StatfsError_FailsClosed verifies that when DiskStatfs
// returns an error, CheckDiskSpace fails closed (returns an error, not success).
func TestCheckDiskSpace_StatfsError_FailsClosed(t *testing.T) {
	dir := t.TempDir()

	old := DiskStatfs
	DiskStatfs = func(_ string) (int64, error) { return 0, syscall.EPERM }
	defer func() { DiskStatfs = old }()

	_, err := CheckDiskSpace(dir, 1)
	if !errors.Is(err, ErrInsufficientDisk) {
		t.Errorf("want ErrInsufficientDisk on statfs error, got %v", err)
	}
}

// TestCheckDiskSpace_NoExistingDisks_UsesDefault verifies that when diskDir
// has no *-workspace.ext4 files, the default estimate is used.
func TestCheckDiskSpace_NoExistingDisks_UsesDefault(t *testing.T) {
	dir := t.TempDir()

	old := DiskStatfs
	DiskStatfs = func(_ string) (int64, error) { return 1 << 40, nil }
	defer func() { DiskStatfs = old }()

	r, err := CheckDiskSpace(dir, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.PerSandboxBytes != perSandboxAllocatedBytesDefault {
		t.Errorf("PerSandboxBytes = %d, want default %d", r.PerSandboxBytes, perSandboxAllocatedBytesDefault)
	}
}
