package volumestore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func debugfsAvailable() bool {
	_, err := exec.LookPath("debugfs")
	return err == nil
}

func writeExt4File(t *testing.T, imgPath, hostSrc, imgDest string) {
	t.Helper()
	cmd := exec.Command("debugfs", "-w", "-R", "write "+hostSrc+" "+imgDest, imgPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("debugfs write %s: %v\n%s", imgDest, err, out)
	}
}

func catExt4File(t *testing.T, imgPath, imgSrc string) string {
	t.Helper()
	cmd := exec.Command("debugfs", "-R", "cat "+imgSrc, imgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat %s: %v\n%s", imgSrc, err, out)
	}
	return string(out)
}

var freeBlocksRE = regexp.MustCompile(`Free blocks:\s+(\d+)`)

func ext4FreeBlocks(t *testing.T, imgPath string) int64 {
	t.Helper()
	out, err := exec.Command("dumpe2fs", "-h", imgPath).CombinedOutput()
	if err != nil {
		t.Fatalf("dumpe2fs -h %s: %v\n%s", imgPath, err, out)
	}
	m := freeBlocksRE.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("dumpe2fs -h %s: no Free blocks line in:\n%s", imgPath, out)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		t.Fatalf("parse free blocks %q: %v", m[1], err)
	}
	return n
}

func newTestExt4Image(t *testing.T, sizeBytes int64) string {
	t.Helper()
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not on PATH")
	}
	path := filepath.Join(t.TempDir(), "disk.ext4")
	if err := preallocateFile(path, sizeBytes); err != nil {
		t.Fatalf("preallocateFile: %v", err)
	}
	if err := formatExt4(context.Background(), path); err != nil {
		t.Fatalf("formatExt4: %v", err)
	}
	return path
}

func TestShrinkExt4ToMinimum_ReclaimsBytes(t *testing.T) {
	if !ShrinkToolsAvailable() {
		t.Skip("e2fsck/resize2fs not on PATH")
	}
	const origSize = 256 * 1024 * 1024
	path := newTestExt4Image(t, origSize)

	newSize, err := shrinkExt4ToMinimum(context.Background(), path)
	if err != nil {
		t.Fatalf("shrinkExt4ToMinimum: %v", err)
	}
	if newSize >= origSize {
		t.Fatalf("shrunk size %d not smaller than original %d", newSize, origSize)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != newSize {
		t.Fatalf("file size on disk %d does not match reported shrunk size %d", fi.Size(), newSize)
	}
}

func TestShrinkExt4ToMinimum_Idempotent(t *testing.T) {
	if !ShrinkToolsAvailable() {
		t.Skip("e2fsck/resize2fs not on PATH")
	}
	path := newTestExt4Image(t, 256*1024*1024)

	// resize2fs's minimum estimate tightens each pass; run enough rounds to
	// reach the fixed point, then assert the last two rounds agree.
	var prev int64 = 1 << 62
	var last, secondLast int64
	for i := 0; i < 6; i++ {
		size, err := shrinkExt4ToMinimum(context.Background(), path)
		if err != nil {
			t.Fatalf("shrink round %d: %v", i, err)
		}
		if size > prev {
			t.Fatalf("shrink round %d grew the file: %d -> %d", i, prev, size)
		}
		prev = size
		secondLast = last
		last = size
	}
	if last != secondLast {
		t.Fatalf("shrink did not converge to a fixed point: %d -> %d", secondLast, last)
	}
}

func TestShrinkExt4ToMinimum_ToolsUnavailable(t *testing.T) {
	path := newTestExt4Image(t, 64*1024*1024)

	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath) //nolint:errcheck
	os.Setenv("PATH", "")            //nolint:errcheck

	if _, err := shrinkExt4ToMinimum(context.Background(), path); err != ErrE2fsckUnavailable {
		t.Fatalf("expected ErrE2fsckUnavailable, got: %v", err)
	}
}

func TestShrinkThenGrow_RoundTrip(t *testing.T) {
	if !ShrinkToolsAvailable() {
		t.Skip("e2fsck/resize2fs not on PATH")
	}
	const origSize = 256 * 1024 * 1024
	path := newTestExt4Image(t, origSize)

	shrunkSize, err := shrinkExt4ToMinimum(context.Background(), path)
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}

	// Mirrors driver_resize.go GrowDisk's host leg: truncate up, then grow.
	const grownSize = 512 * 1024 * 1024
	if err := os.Truncate(path, grownSize); err != nil {
		t.Fatalf("truncate up: %v", err)
	}
	if err := runResize2fs(context.Background(), path); err != nil {
		t.Fatalf("grow resize2fs: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != grownSize {
		t.Fatalf("file size %d != expected grown size %d", fi.Size(), grownSize)
	}

	// A post-grow e2fsck proves the filesystem the guest would mount is
	// consistent after having gone through shrink -> truncate-up -> grow.
	if err := runE2fsckClean(context.Background(), path); err != nil {
		t.Fatalf("post-grow e2fsck: %v", err)
	}
	if shrunkSize >= grownSize {
		t.Fatalf("shrunk size %d unexpectedly >= grown size %d", shrunkSize, grownSize)
	}
}

// TestReclaimExt4_PreservesDataFreeSpaceAndFurtherGrowth is Bug 1's
// regression test: a data-filled image must come out of reclaimExt4 at its
// original declared size (not resize2fs -M's compacted minimum), with the
// data intact and real free space available, and still growable beyond that
// declared size via the normal grow path.
func TestReclaimExt4_PreservesDataFreeSpaceAndFurtherGrowth(t *testing.T) {
	if !ShrinkToolsAvailable() {
		t.Skip("e2fsck/resize2fs not on PATH")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not on PATH")
	}
	const origSize = 256 * 1024 * 1024
	path := newTestExt4Image(t, origSize)

	payloadHost := filepath.Join(t.TempDir(), "payload.txt")
	const payload = "reclaim must not lose this data"
	if err := os.WriteFile(payloadHost, []byte(payload), 0o644); err != nil {
		t.Fatalf("write host payload: %v", err)
	}
	writeExt4File(t, path, payloadHost, "payload.txt")

	if err := reclaimExt4(context.Background(), path, origSize); err != nil {
		t.Fatalf("reclaimExt4: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != origSize {
		t.Fatalf("file size %d != declared size %d (reclaim must preserve declared capacity)", fi.Size(), origSize)
	}

	if got := catExt4File(t, path, "payload.txt"); !strings.Contains(got, payload) {
		t.Fatalf("payload.txt content = %q, want to contain %q", got, payload)
	}

	if free := ext4FreeBlocks(t, path); free == 0 {
		t.Fatalf("free blocks after reclaim = 0, want > 0 (declared capacity must have real free space)")
	}

	if err := runE2fsckClean(context.Background(), path); err != nil {
		t.Fatalf("post-reclaim e2fsck: %v", err)
	}

	const grownSize = 512 * 1024 * 1024
	if err := os.Truncate(path, grownSize); err != nil {
		t.Fatalf("truncate beyond declared size: %v", err)
	}
	if err := runResize2fs(context.Background(), path); err != nil {
		t.Fatalf("grow beyond declared size: %v", err)
	}
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat after further grow: %v", err)
	}
	if fi.Size() != grownSize {
		t.Fatalf("file size after further grow %d != %d", fi.Size(), grownSize)
	}
	if got := catExt4File(t, path, "payload.txt"); !strings.Contains(got, payload) {
		t.Fatalf("payload.txt content after further grow = %q, want to contain %q", got, payload)
	}
}
