package builder

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// TestWipe_PreservesGrownSize: wipe-recreate must not shrink a grown disk.
func TestWipe_PreservesGrownSize(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available")
	}

	ctx := context.Background()
	dataDir := t.TempDir()

	specs, release, err := SelectCacheDisks(ctx, dataDir, []string{"npm"})
	if err != nil {
		t.Fatalf("SelectCacheDisks (create): %v", err)
	}
	spec := specs[0]

	grownSize := int64(20 * 1024 * 1024 * 1024)
	if err := os.Truncate(spec.ImagePath, grownSize); err != nil {
		t.Fatalf("simulate GrowDisk: %v", err)
	}

	if !cacheDiskIsDirty(spec.ImagePath) {
		t.Fatal("test setup: disk must be fenced dirty on first lease")
	}
	ReleaseCacheDiskLeases(release)

	specs2, leases2, err := SelectCacheDisks(ctx, dataDir, []string{"npm"})
	if err != nil {
		t.Fatalf("SelectCacheDisks (reuse after unclean death): %v", err)
	}
	defer ReleaseCacheDiskLeases(leases2)

	fi, err := os.Stat(specs2[0].ImagePath)
	if err != nil {
		t.Fatalf("stat recreated disk: %v", err)
	}
	if fi.Size() < grownSize {
		t.Errorf("wipe-recreate shrank disk: got %d want >= %d", fi.Size(), grownSize)
	}
}
