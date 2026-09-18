package builder

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func makeDirtyFakeImage(t *testing.T, dir string) (imgPath string, ino uint64) {
	t.Helper()
	imgPath = filepath.Join(dir, "npm.ext4")
	if err := os.WriteFile(imgPath, []byte("fake ext4 content"), 0o644); err != nil {
		t.Fatalf("write fake image: %v", err)
	}
	if err := markCacheDiskDirty(imgPath); err != nil {
		t.Fatalf("markCacheDiskDirty: %v", err)
	}
	fi, err := os.Stat(imgPath)
	if err != nil {
		t.Fatalf("stat fake image: %v", err)
	}
	return imgPath, fi.Sys().(*syscall.Stat_t).Ino
}

func overrideFsck(t *testing.T, fn func(string) error) {
	t.Helper()
	orig := fsckCacheDisk
	fsckCacheDisk = fn
	t.Cleanup(func() { fsckCacheDisk = orig })
}

func TestFsck_DirtyRecovered_ReusesDisk(t *testing.T) {
	dir := t.TempDir()
	_, ino1 := makeDirtyFakeImage(t, dir)

	overrideFsck(t, func(string) error { return nil })

	ctx := context.Background()
	spec, err := ensureCacheDiskAt(ctx, dir, "npm", ecosystemRegistry["npm"], 0)
	if err != nil {
		t.Fatalf("ensureCacheDiskAt: %v", err)
	}

	fi2, err := os.Stat(spec.ImagePath)
	if err != nil {
		t.Fatalf("stat after fsck-ok: %v", err)
	}
	if ino2 := fi2.Sys().(*syscall.Stat_t).Ino; ino2 != ino1 {
		t.Errorf("fsck-ok: disk was recreated (inode %d → %d), want same inode (reuse)", ino1, ino2)
	}
	// Inode numbers can be recycled, so also prove the bytes survived. Size
	// first: a recreated image is a 10 GiB sparse ext4, never read it whole.
	if fi2.Size() != int64(len("fake ext4 content")) {
		t.Fatalf("fsck-ok: image size %d, want %d (reuse, not recreate)", fi2.Size(), len("fake ext4 content"))
	}
	if got, err := os.ReadFile(spec.ImagePath); err != nil || string(got) != "fake ext4 content" {
		t.Errorf("fsck-ok: image content = %q, %v; want original fake content", got, err)
	}
	if !cacheDiskIsDirty(spec.ImagePath) {
		t.Error("dirty marker must remain set after fsck recovery")
	}
}

func TestFsck_DirtyFailed_WipesAndRecreates(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available")
	}

	dir := t.TempDir()
	makeDirtyFakeImage(t, dir)

	overrideFsck(t, func(string) error { return errors.New("e2fsck: filesystem errors (exit 4)") })

	ctx := context.Background()
	spec, err := ensureCacheDiskAt(ctx, dir, "npm", ecosystemRegistry["npm"], 0)
	if err != nil {
		t.Fatalf("ensureCacheDiskAt: %v", err)
	}

	fi2, err := os.Stat(spec.ImagePath)
	if err != nil {
		t.Fatalf("stat after fsck-fail wipe: %v", err)
	}
	// Inode numbers can be recycled by remove+create, so prove the wipe by
	// size: the fake image is a few bytes, a recreated ext4 is cacheDiskSizeBytes.
	if fi2.Size() != cacheDiskSizeBytes {
		t.Errorf("fsck-fail: image size %d, want %d (fresh ext4); old file was reused", fi2.Size(), cacheDiskSizeBytes)
	}
	if !cacheDiskIsDirty(spec.ImagePath) {
		t.Error("recreated disk must be fenced dirty")
	}
}

func TestFsck_Missing_WipesAndRecreates(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available")
	}

	dir := t.TempDir()
	makeDirtyFakeImage(t, dir)

	overrideFsck(t, func(string) error { return ErrE2fsckUnavailable })

	ctx := context.Background()
	spec, err := ensureCacheDiskAt(ctx, dir, "npm", ecosystemRegistry["npm"], 0)
	if err != nil {
		t.Fatalf("ensureCacheDiskAt: %v", err)
	}

	fi2, err := os.Stat(spec.ImagePath)
	if err != nil {
		t.Fatalf("stat after fsck-missing wipe: %v", err)
	}
	// Inode numbers can be recycled by remove+create, so prove the wipe by
	// size: the fake image is a few bytes, a recreated ext4 is cacheDiskSizeBytes.
	if fi2.Size() != cacheDiskSizeBytes {
		t.Errorf("fsck-missing: image size %d, want %d (fresh ext4); old file was reused", fi2.Size(), cacheDiskSizeBytes)
	}
	if !cacheDiskIsDirty(spec.ImagePath) {
		t.Error("recreated disk must be fenced dirty")
	}
}
