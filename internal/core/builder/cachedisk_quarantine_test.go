package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func makeDirtyFakeImage(t *testing.T, dir string) string {
	t.Helper()
	imgPath := filepath.Join(dir, "npm.ext4")
	if err := os.WriteFile(imgPath, []byte("fake ext4 content"), 0o644); err != nil {
		t.Fatalf("write fake image: %v", err)
	}
	if err := markCacheDiskDirty(imgPath); err != nil {
		t.Fatalf("markCacheDiskDirty: %v", err)
	}
	return imgPath
}

func quarantinedCopies(t *testing.T, imgPath string) []string {
	t.Helper()
	m, err := filepath.Glob(imgPath + ".quarantine-*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return m
}

func TestDirtySlot_QuarantinedNotReused(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available")
	}
	dir := t.TempDir()
	imgPath := makeDirtyFakeImage(t, dir)

	spec, err := ensureCacheDiskAt(context.Background(), dir, "npm", ecosystemRegistry["npm"], 0)
	if err != nil {
		t.Fatalf("ensureCacheDiskAt: %v", err)
	}
	fi, err := os.Stat(spec.ImagePath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != cacheDiskSizeBytes {
		t.Fatalf("image size %d, want fresh %d: dirty slot was reused", fi.Size(), cacheDiskSizeBytes)
	}
	q := quarantinedCopies(t, imgPath)
	if len(q) != 1 {
		t.Fatalf("quarantined copies = %v, want exactly 1", q)
	}
	if got, _ := os.ReadFile(q[0]); string(got) != "fake ext4 content" {
		t.Errorf("quarantined content = %q, want original bytes", got)
	}
	if !cacheDiskIsDirty(spec.ImagePath) {
		t.Error("fresh slot must be fenced dirty")
	}
}

func TestDirtySlot_KeepsAtMostOneQuarantine(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available")
	}
	dir := t.TempDir()
	imgPath := makeDirtyFakeImage(t, dir)
	old := imgPath + ".quarantine-1000"
	if err := os.WriteFile(old, []byte("older"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureCacheDiskAt(context.Background(), dir, "npm", ecosystemRegistry["npm"], 0); err != nil {
		t.Fatalf("ensureCacheDiskAt: %v", err)
	}
	q := quarantinedCopies(t, imgPath)
	if len(q) != 1 {
		t.Fatalf("quarantined copies = %v, want exactly 1", q)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("older quarantine %s not deleted (err=%v)", old, err)
	}
}
