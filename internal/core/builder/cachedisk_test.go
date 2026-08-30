package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// skipIfInGuest skips the test when running inside a nexus3 KVM guest.
//
// In-guest: the rootfs is a 5 GiB ext4 image with limited free space.
// The cachedisk tests create 10 GiB sparse ext4 disks; each requires
// ~300 MiB of real inode-table + journal writes to the guest filesystem.
// Four such operations (TestEnsureCacheDisk_CreateThenReuse,
// TestSelectCacheDisks_ExactSet ×2, TestWarmReuse_MarkerSurvives) cause
// residual page-cache pressure that triggers OOM in the 8 GiB guest during
// subsequent heavy compilations (e.g. the cloudhypervisor test binary).
//
// Detection: /dev/vda is the virtio-blk rootfs disk; it is present in every
// nexus3 KVM guest and absent on the development host.
func skipIfInGuest(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/vda"); err == nil {
		t.Skip("skipping heavy mke2fs test in-guest (page-cache pressure; see cachedisk_test.go skipIfInGuest)")
	}
}

// TestEnsureCacheDisk_CreateThenReuse verifies that EnsureCacheDisk creates
// the ext4 image on the first call and reuses it (without recreating) on the
// second call, confirmed via inode stability.
func TestEnsureCacheDisk_CreateThenReuse(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available; skipping cache disk creation test")
	}

	ctx := context.Background()
	dataDir := t.TempDir()

	spec1, err := EnsureCacheDisk(ctx, dataDir, "npm")
	if err != nil {
		t.Fatalf("first EnsureCacheDisk: %v", err)
	}

	if spec1.EcosystemKey != "npm" {
		t.Errorf("EcosystemKey: got %q want %q", spec1.EcosystemKey, "npm")
	}
	if spec1.MountPath != "/root/.npm" {
		t.Errorf("MountPath: got %q want %q", spec1.MountPath, "/root/.npm")
	}
	if spec1.ImagePath == "" {
		t.Fatal("ImagePath is empty")
	}

	// Capture inode and mtime to detect any recreation.
	fi1, err := os.Stat(spec1.ImagePath)
	if err != nil {
		t.Fatalf("stat after first create: %v", err)
	}
	ino1 := fi1.Sys().(*syscall.Stat_t).Ino
	mtime1 := fi1.ModTime()

	// Second call must reuse the existing image.
	spec2, err := EnsureCacheDisk(ctx, dataDir, "npm")
	if err != nil {
		t.Fatalf("second EnsureCacheDisk: %v", err)
	}
	if spec2.ImagePath != spec1.ImagePath {
		t.Errorf("ImagePath changed between calls: %q vs %q", spec1.ImagePath, spec2.ImagePath)
	}

	fi2, err := os.Stat(spec2.ImagePath)
	if err != nil {
		t.Fatalf("stat after second call: %v", err)
	}
	ino2 := fi2.Sys().(*syscall.Stat_t).Ino
	mtime2 := fi2.ModTime()

	if ino2 != ino1 {
		t.Errorf("inode changed (image was recreated): %d → %d", ino1, ino2)
	}
	if !mtime2.Equal(mtime1) {
		t.Errorf("mtime changed (image was modified): %v → %v", mtime1, mtime2)
	}
}

// TestSelectCacheDisks_ExactSet verifies that SelectCacheDisks returns exactly
// the requested keys and errors on unknown keys.
func TestSelectCacheDisks_ExactSet(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available; skipping select cache disks test")
	}

	ctx := context.Background()
	dataDir := t.TempDir()

	keys := []string{"pip", "cargo"}
	specs, release, err := SelectCacheDisks(ctx, dataDir, keys)
	if err != nil {
		t.Fatalf("SelectCacheDisks: %v", err)
	}
	defer release()
	if len(specs) != len(keys) {
		t.Fatalf("got %d specs, want %d", len(specs), len(keys))
	}
	for i, k := range keys {
		if specs[i].EcosystemKey != k {
			t.Errorf("specs[%d].EcosystemKey = %q, want %q", i, specs[i].EcosystemKey, k)
		}
		if specs[i].ImagePath == "" {
			t.Errorf("specs[%d].ImagePath is empty", i)
		}
		if _, err := os.Stat(specs[i].ImagePath); err != nil {
			t.Errorf("specs[%d].ImagePath not on disk: %v", i, err)
		}
	}

	// Unknown key must error.
	_, _, err = SelectCacheDisks(ctx, dataDir, []string{"ruby"})
	if err == nil {
		t.Fatal("expected error for unknown key 'ruby', got nil")
	}
}

// TestSelectCacheDisks_UnknownKey verifies that EnsureCacheDisk also errors
// for an unknown key directly.
func TestSelectCacheDisks_UnknownKey(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	_, err := EnsureCacheDisk(ctx, dataDir, "maven")
	if err == nil {
		t.Fatal("expected error for unknown key 'maven', got nil")
	}
}

// TestAllEcosystemKeys verifies that all 8 required ecosystem keys are present
// in the registry with non-empty mount paths.
func TestAllEcosystemKeys(t *testing.T) {
	required := []struct {
		key       string
		mountPath string
	}{
		{"npm", "/root/.npm"},
		{"pnpm", "/root/.local/share/pnpm/store"},
		{"yarn", "/root/.cache/yarn"},
		{"pip", "/root/.cache/pip"},
		{"cargo", "/root/.cargo"},
		{"go", "/root/.cache/go-build"},
		{"apt", "/var/cache/apt"},
		{"buildkit", "/var/lib/buildkit"},
	}

	for _, r := range required {
		entry, ok := ecosystemRegistry[r.key]
		if !ok {
			t.Errorf("ecosystem %q missing from registry", r.key)
			continue
		}
		if entry.mountPath != r.mountPath {
			t.Errorf("ecosystem %q: mountPath = %q, want %q", r.key, entry.mountPath, r.mountPath)
		}
	}

	if len(ecosystemRegistry) < len(required) {
		t.Errorf("registry has %d entries, want at least %d", len(ecosystemRegistry), len(required))
	}
}

// TestCacheDiskDirtyMarker_RoundTrip is a fast, mke2fs-free unit test of the
// fencing-marker primitives themselves: a fresh path is not dirty, marking it
// dirty makes cacheDiskIsDirty true, and marking it clean clears it again.
func TestCacheDiskDirtyMarker_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "fake.ext4")
	if err := os.WriteFile(imgPath, []byte("not a real ext4 image"), 0o644); err != nil {
		t.Fatalf("write fake image: %v", err)
	}

	if cacheDiskIsDirty(imgPath) {
		t.Fatal("fresh image path reported dirty before any marker was written")
	}

	if err := markCacheDiskDirty(imgPath); err != nil {
		t.Fatalf("markCacheDiskDirty: %v", err)
	}
	if !cacheDiskIsDirty(imgPath) {
		t.Fatal("cacheDiskIsDirty false after markCacheDiskDirty")
	}
	if _, err := os.Stat(dirtyMarkerPath(imgPath)); err != nil {
		t.Fatalf("dirty marker file missing on disk: %v", err)
	}

	if err := markCacheDiskClean(imgPath); err != nil {
		t.Fatalf("markCacheDiskClean: %v", err)
	}
	if cacheDiskIsDirty(imgPath) {
		t.Fatal("cacheDiskIsDirty true after markCacheDiskClean")
	}

	// markCacheDiskClean must be a no-op (not an error) when no marker is
	// present — e.g. a second, redundant clean-teardown call.
	if err := markCacheDiskClean(imgPath); err != nil {
		t.Fatalf("markCacheDiskClean on already-clean image: %v", err)
	}
}

// TestCacheDisk_DirtyLease_WipesOnNextReuse is the TBD-1 mutation-proof
// regression test. It reproduces the exact scenario from the D-DC-31 debugfs
// forensics: a builder VM writes real data into an ecosystem cache disk, then
// dies without ever confirming a clean sync (the guest-SIGKILL case — no
// in-guest or host teardown code runs). The NEXT lease of that same slot must
// detect the fencing marker left dirty and wipe the disk rather than reuse it,
// so a later build can never observe the poisoned pre-crash state as a warm
// cache.
//
// The contrasting clean-teardown case is exercised in the same test to prove
// the mechanism does not simply wipe on every reuse (which would defeat
// caching and pass trivially): once markCacheDiskClean is called — the signal
// BuildInVM only sends after lifecycle.SyncAndStop confirms success — the next
// lease must preserve the existing data.
func TestCacheDisk_DirtyLease_WipesOnNextReuse(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available; skipping cache disk wipe test")
	}
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs not available (install e2fsprogs)")
	}

	ctx := context.Background()
	writeMarker := func(t *testing.T, imgPath string) {
		t.Helper()
		markerHost := filepath.Join(t.TempDir(), "payload.txt")
		if err := os.WriteFile(markerHost, []byte("pre-crash cache payload"), 0o644); err != nil {
			t.Fatalf("write marker payload: %v", err)
		}
		cmd := exec.CommandContext(ctx, "debugfs", "-w", "-R",
			"write "+markerHost+" payload.txt", imgPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("debugfs write: %v\n%s", err, out)
		}
	}
	markerPresent := func(t *testing.T, imgPath string) bool {
		t.Helper()
		cmd := exec.CommandContext(ctx, "debugfs", "-R", "cat payload.txt", imgPath)
		out, _ := cmd.CombinedOutput()
		return strings.Contains(string(out), "pre-crash cache payload")
	}

	t.Run("dirty lease is wiped on next reuse", func(t *testing.T) {
		dataDir := t.TempDir()

		spec, err := EnsureCacheDisk(ctx, dataDir, "npm")
		if err != nil {
			t.Fatalf("EnsureCacheDisk (create): %v", err)
		}
		// EnsureCacheDisk's first-use path already leaves the disk fenced
		// dirty (this lease has not yet confirmed a clean sync).
		if !cacheDiskIsDirty(spec.ImagePath) {
			t.Fatal("newly created cache disk must be fenced dirty until a clean sync is confirmed")
		}

		writeMarker(t, spec.ImagePath)
		if !markerPresent(t, spec.ImagePath) {
			t.Fatal("test setup failed: marker payload not readable back")
		}

		fi1, err := os.Stat(spec.ImagePath)
		if err != nil {
			t.Fatalf("stat before reuse: %v", err)
		}
		ino1 := fi1.Sys().(*syscall.Stat_t).Ino

		// Simulate the guest-SIGKILL exit path: nothing ever calls
		// markCacheDiskClean. The NEXT lease of this slot must see the
		// dirty marker and wipe rather than hand back the poisoned image.
		spec2, err := EnsureCacheDisk(ctx, dataDir, "npm")
		if err != nil {
			t.Fatalf("EnsureCacheDisk (reuse after unclean death): %v", err)
		}
		if spec2.ImagePath != spec.ImagePath {
			t.Fatalf("ImagePath changed: %q vs %q", spec.ImagePath, spec2.ImagePath)
		}
		fi2, err := os.Stat(spec2.ImagePath)
		if err != nil {
			t.Fatalf("stat after reuse: %v", err)
		}
		ino2 := fi2.Sys().(*syscall.Stat_t).Ino
		if ino2 == ino1 {
			t.Error("image inode unchanged: dirty lease was NOT wiped on next reuse")
		}
		if markerPresent(t, spec2.ImagePath) {
			t.Error("pre-crash payload survived reuse: dirty lease was not wiped — poisoned cache would be served as a hit")
		}
		// The fresh lease handed out by the wipe path must itself be fenced
		// dirty again, not silently treated as already clean.
		if !cacheDiskIsDirty(spec2.ImagePath) {
			t.Error("re-created disk after wipe must be fenced dirty until its own clean sync is confirmed")
		}
	})

	t.Run("clean lease is preserved on next reuse", func(t *testing.T) {
		dataDir := t.TempDir()

		spec, err := EnsureCacheDisk(ctx, dataDir, "npm")
		if err != nil {
			t.Fatalf("EnsureCacheDisk (create): %v", err)
		}
		writeMarker(t, spec.ImagePath)

		// Simulate BuildInVM's confirmed-clean-sync signal.
		if err := markCacheDiskClean(spec.ImagePath); err != nil {
			t.Fatalf("markCacheDiskClean: %v", err)
		}

		spec2, err := EnsureCacheDisk(ctx, dataDir, "npm")
		if err != nil {
			t.Fatalf("EnsureCacheDisk (reuse after clean sync): %v", err)
		}
		if spec2.ImagePath != spec.ImagePath {
			t.Fatalf("ImagePath changed: %q vs %q", spec.ImagePath, spec2.ImagePath)
		}
		if !markerPresent(t, spec2.ImagePath) {
			t.Error("cache payload lost on reuse after a confirmed clean sync — should have been preserved")
		}
	})
}

// TestWarmReuse_MarkerSurvives is the warm-reuse proof: it writes a marker
// file into the ext4 cache disk via debugfs (offline), then a second
// EnsureCacheDisk call returns the same disk, and we verify the marker is
// still present. This proves that consecutive runs reuse disk state.
func TestWarmReuse_MarkerSurvives(t *testing.T) {
	skipIfInGuest(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available")
	}
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs not available (install e2fsprogs)")
	}

	ctx := context.Background()
	dataDir := t.TempDir()

	// First call: create the go cache disk.
	spec, err := EnsureCacheDisk(ctx, dataDir, "go")
	if err != nil {
		t.Fatalf("EnsureCacheDisk (create): %v", err)
	}

	// Write a marker file into the ext4 image using debugfs write command.
	// We create a temp marker file on the host, then inject it into the image.
	markerHost := filepath.Join(t.TempDir(), "g5-marker.txt")
	markerContent := "g5-warm-reuse-proof"
	if err := os.WriteFile(markerHost, []byte(markerContent), 0o644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	// debugfs -w -R "write <host> <guest>" <image>
	writeCmd := exec.CommandContext(ctx, "debugfs", "-w", "-R",
		"write "+markerHost+" g5-marker.txt",
		spec.ImagePath,
	)
	if out, err := writeCmd.CombinedOutput(); err != nil {
		t.Fatalf("debugfs write marker: %v\n%s", err, out)
	}

	// Second call: must reuse the same disk (no recreation).
	spec2, err := EnsureCacheDisk(ctx, dataDir, "go")
	if err != nil {
		t.Fatalf("EnsureCacheDisk (reuse): %v", err)
	}
	if spec2.ImagePath != spec.ImagePath {
		t.Fatalf("ImagePath changed: %q → %q", spec.ImagePath, spec2.ImagePath)
	}

	// Read the marker back from the ext4 image using debugfs cat.
	catCmd := exec.CommandContext(ctx, "debugfs", "-R",
		"cat g5-marker.txt",
		spec2.ImagePath,
	)
	out, err := catCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat marker: %v\n%s", err, out)
	}
	// debugfs prefixes output with a version banner line; strip it.
	got := strings.TrimPrefix(string(out), "debugfs "+strings.SplitN(string(out), "\n", 2)[0]+"\n")
	// More robust: find the last line that contains the marker content.
	outStr := string(out)
	if !strings.Contains(outStr, markerContent) {
		t.Errorf("warm-reuse marker not found in debugfs output: got %q want to contain %q", outStr, markerContent)
	}
	_ = got
}
