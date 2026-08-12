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
	specs, err := SelectCacheDisks(ctx, dataDir, keys)
	if err != nil {
		t.Fatalf("SelectCacheDisks: %v", err)
	}
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
	_, err = SelectCacheDisks(ctx, dataDir, []string{"ruby"})
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
