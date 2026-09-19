package volumestore_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/IniZio/nexus/internal/core/volumestore"
)

func hostBlocks(t *testing.T, fi os.FileInfo) int64 {
	t.Helper()
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("FileInfo.Sys() is not *syscall.Stat_t on this platform")
	}
	return int64(st.Blocks)
}

func debugfsAvailable(t *testing.T) bool {
	t.Helper()
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

// TestDetach_ReclaimsHostBlocksButPreservesDeclaredCapacity is Bug 1's
// regression test at the store layer: full detachment must reclaim host disk
// blocks synchronously (Detach runs reclaimExt4 in-process before returning)
// without touching the volume's declared SizeBytes or its on-disk logical
// size.
func TestDetach_ReclaimsHostBlocksButPreservesDeclaredCapacity(t *testing.T) {
	if !volumestore.ShrinkToolsAvailable() || !volumestore.Mke2fsAvailable() {
		t.Skip("e2fsprogs not on PATH")
	}
	s := newStore(t)
	ctx := context.Background()

	const sizeBytes = 256 * 1024 * 1024
	rec, err := s.Create(ctx, "shrink-vol", volumestore.KindDisk, sizeBytes, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	origSize := rec.SizeBytes

	if err := s.Attach(ctx, "shrink-vol", "sandbox-a"); err != nil {
		t.Fatalf("Attach a: %v", err)
	}
	if err := s.Attach(ctx, "shrink-vol", "sandbox-b"); err != nil {
		t.Fatalf("Attach b: %v", err)
	}

	if err := s.Detach(ctx, "shrink-vol", "sandbox-a"); err != nil {
		t.Fatalf("Detach a: %v", err)
	}
	fi, err := os.Stat(s.DiskPath("shrink-vol"))
	if err != nil {
		t.Fatalf("stat after partial detach: %v", err)
	}
	if fi.Size() != origSize {
		t.Fatalf("partially-detached volume changed size (still attached to sandbox-b): %d != %d", fi.Size(), origSize)
	}
	blocksBefore := hostBlocks(t, fi)

	if err := s.Detach(ctx, "shrink-vol", "sandbox-b"); err != nil {
		t.Fatalf("Detach b: %v", err)
	}

	fi, err = os.Stat(s.DiskPath("shrink-vol"))
	if err != nil {
		t.Fatalf("stat after full detach: %v", err)
	}
	if fi.Size() != origSize {
		t.Fatalf("fully-detached volume's logical size changed (declared capacity must survive reclaim): %d != %d", fi.Size(), origSize)
	}
	if blocksAfter := hostBlocks(t, fi); blocksAfter >= blocksBefore {
		t.Fatalf("reclaim did not reduce host blocks: %d >= %d", blocksAfter, blocksBefore)
	}

	got, err := s.Get("shrink-vol")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SizeBytes != origSize {
		t.Fatalf("meta.json size_bytes changed by reclaim: %d != %d", got.SizeBytes, origSize)
	}

	// Volume must still be usable after reclaim: re-attaching must succeed.
	if err := s.Attach(ctx, "shrink-vol", "sandbox-c"); err != nil {
		t.Fatalf("Attach after reclaim: %v", err)
	}
}

// TestDetach_NoOpDetachDoesNotTriggerReclaim is Bug 3's regression test: a
// Detach call that removes nothing (sandboxID absent, or the volume is
// already fully detached) must not fire the reclaim, evidenced by the
// backing file being left completely untouched (identical mtime).
func TestDetach_NoOpDetachDoesNotTriggerReclaim(t *testing.T) {
	if !volumestore.ShrinkToolsAvailable() || !volumestore.Mke2fsAvailable() {
		t.Skip("e2fsprogs not on PATH")
	}
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "shrink-vol2", volumestore.KindDisk, 128*1024*1024, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	fiBefore, err := os.Stat(s.DiskPath("shrink-vol2"))
	if err != nil {
		t.Fatalf("stat before no-op detach: %v", err)
	}

	// No sandbox was ever attached — this Detach removes nothing and must be
	// a pure no-op, not a trigger for reclaim.
	if err := s.Detach(ctx, "shrink-vol2", "never-attached"); err != nil {
		t.Fatalf("Detach (no-op): %v", err)
	}

	fiAfter, err := os.Stat(s.DiskPath("shrink-vol2"))
	if err != nil {
		t.Fatalf("stat after no-op detach: %v", err)
	}
	if !fiAfter.ModTime().Equal(fiBefore.ModTime()) {
		t.Fatalf("no-op detach touched the backing file: mtime %v -> %v", fiBefore.ModTime(), fiAfter.ModTime())
	}

	// Attach then detach once — reclaim fires. A second, redundant Detach for
	// the same sandboxID must not fire it again.
	if err := s.Attach(ctx, "shrink-vol2", "sandbox-a"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := s.Detach(ctx, "shrink-vol2", "sandbox-a"); err != nil {
		t.Fatalf("Detach 1: %v", err)
	}
	fi1, err := os.Stat(s.DiskPath("shrink-vol2"))
	if err != nil {
		t.Fatalf("stat 1: %v", err)
	}

	if err := s.Detach(ctx, "shrink-vol2", "sandbox-a"); err != nil {
		t.Fatalf("Detach 2 (redundant no-op): %v", err)
	}
	fi2, err := os.Stat(s.DiskPath("shrink-vol2"))
	if err != nil {
		t.Fatalf("stat 2: %v", err)
	}
	if !fi2.ModTime().Equal(fi1.ModTime()) {
		t.Fatalf("redundant no-op detach re-triggered reclaim: mtime %v -> %v", fi1.ModTime(), fi2.ModTime())
	}
}

// TestVolumeLifecycle_ReclaimPreservesDataCapacityAndFreeSpace is the
// end-to-end wiring test: create -> attach -> write real data -> detach
// (reclaim fires) -> re-attach must leave declared SizeBytes unchanged, real
// free space available, and the data intact.
func TestVolumeLifecycle_ReclaimPreservesDataCapacityAndFreeSpace(t *testing.T) {
	if !volumestore.ShrinkToolsAvailable() || !volumestore.Mke2fsAvailable() {
		t.Skip("e2fsprogs not on PATH")
	}
	if !debugfsAvailable(t) {
		t.Skip("debugfs not on PATH")
	}
	s := newStore(t)
	ctx := context.Background()

	const sizeBytes = 256 * 1024 * 1024
	created, err := s.Create(ctx, "lifecycle-vol", volumestore.KindDisk, sizeBytes, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Attach(ctx, "lifecycle-vol", "sandbox-a"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	payloadHost := filepath.Join(t.TempDir(), "payload.txt")
	const payload = "end-to-end reclaim must not lose this"
	if err := os.WriteFile(payloadHost, []byte(payload), 0o644); err != nil {
		t.Fatalf("write host payload: %v", err)
	}
	diskPath := s.DiskPath("lifecycle-vol")
	writeExt4File(t, diskPath, payloadHost, "payload.txt")

	if err := s.Detach(ctx, "lifecycle-vol", "sandbox-a"); err != nil {
		t.Fatalf("Detach: %v", err)
	}

	if err := s.Attach(ctx, "lifecycle-vol", "sandbox-b"); err != nil {
		t.Fatalf("re-Attach: %v", err)
	}

	got, err := s.Get("lifecycle-vol")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SizeBytes != created.SizeBytes {
		t.Fatalf("declared SizeBytes changed across reclaim: %d != %d", got.SizeBytes, created.SizeBytes)
	}

	fi, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != sizeBytes {
		t.Fatalf("on-disk logical size changed across reclaim: %d != %d", fi.Size(), sizeBytes)
	}

	if content := catExt4File(t, diskPath, "payload.txt"); !strings.Contains(content, payload) {
		t.Fatalf("payload.txt content after reclaim+re-attach = %q, want to contain %q", content, payload)
	}
}

func dumpe2fsAvailable(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("dumpe2fs")
	return err == nil
}

func freeBlockCount(t *testing.T, imgPath string) int64 {
	t.Helper()
	out, err := exec.Command("dumpe2fs", "-h", imgPath).CombinedOutput()
	if err != nil {
		t.Fatalf("dumpe2fs -h %s: %v\n%s", imgPath, err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Free blocks:") {
			n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "Free blocks:")), 10, 64)
			if err != nil {
				t.Fatalf("parse free block count from %q: %v", line, err)
			}
			return n
		}
	}
	t.Fatalf("no Free blocks line in dumpe2fs -h output:\n%s", out)
	return 0
}

// TestDetach_SyncReclaimVisibleImmediatelyNoProductionDrainHook is the
// regression test for the goroutine-detach bug: production has no
// synchronization hook (DrainReclaims was removed), so this asserts the
// reclaimed state — logical file size restored to the declared capacity, and
// real ext4 free blocks available — directly on the return of Detach, with
// no wait of any kind in between. It exercises the same call Remove ->
// detachVolumeLocked -> VolumeStore.Detach makes in production.
func TestDetach_SyncReclaimVisibleImmediatelyNoProductionDrainHook(t *testing.T) {
	if !volumestore.ShrinkToolsAvailable() || !volumestore.Mke2fsAvailable() {
		t.Skip("e2fsprogs not on PATH")
	}
	if !dumpe2fsAvailable(t) {
		t.Skip("dumpe2fs not on PATH")
	}
	s := newStore(t)
	ctx := context.Background()

	const sizeBytes = 256 * 1024 * 1024
	created, err := s.Create(ctx, "sync-reclaim-vol", volumestore.KindDisk, sizeBytes, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Attach(ctx, "sync-reclaim-vol", "sandbox-a"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := s.Detach(ctx, "sync-reclaim-vol", "sandbox-a"); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	// No drain, no sleep, no poll: the assertions below run the instant
	// Detach returns, matching what a short-lived `nexus sandbox rm` process
	// sees before it exits.

	diskPath := s.DiskPath("sync-reclaim-vol")
	fi, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != created.SizeBytes {
		t.Fatalf("logical size after Detach returns = %d, want declared SizeBytes %d", fi.Size(), created.SizeBytes)
	}
	if free := freeBlockCount(t, diskPath); free <= 0 {
		t.Fatalf("free block count after Detach returns = %d, want > 0", free)
	}
}
