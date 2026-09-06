package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Regression tests for the concurrent builder-VM spawn failure.
//
// Symptom: launching 3 builder VMs at once left 2 of them dead within a second
// with an empty supervisor state dir. Root cause: every builder VM was handed
// the SAME host-shared writable disk images — the cached builder rootfs
// template (vda) and caches/buildkit.ext4 (vdd). cloud-hypervisor takes an
// exclusive write lock on every image it attaches, so the second and third VM
// were refused at vm.boot with "The file is already locked" and their
// supervisors died before writing supervisor.pid.
//
// Both tests below assert the property that fixes it: no two concurrent
// builder VMs are handed the same image path.

// TestPrivateRootfs_NotTheSharedTemplate proves the builder rootfs handed to a
// VM is a private clone in the build work dir, not the shared cache template,
// and that two concurrent builds get two distinct files.
//
// Mutation proof: make PrivateRootfs return sharedImage unchanged and this
// test fails on the first assertion.
func TestPrivateRootfs_NotTheSharedTemplate(t *testing.T) {
	ctx := context.Background()

	stateDir := t.TempDir()
	shared := filepath.Join(stateDir, "nexus-builder-shared.ext4")
	content := []byte("pretend-ext4-image-bytes")
	if err := os.WriteFile(shared, content, 0o644); err != nil {
		t.Fatalf("write shared template: %v", err)
	}

	workA := t.TempDir()
	workB := t.TempDir()

	rootfsA, err := PrivateRootfs(ctx, shared, workA)
	if err != nil {
		t.Fatalf("PrivateRootfs (build A): %v", err)
	}
	rootfsB, err := PrivateRootfs(ctx, shared, workB)
	if err != nil {
		t.Fatalf("PrivateRootfs (build B): %v", err)
	}

	if rootfsA == shared {
		t.Errorf("build A boots the SHARED template %s; concurrent builder VMs "+
			"collide on cloud-hypervisor's exclusive image write lock", shared)
	}
	if rootfsA == rootfsB {
		t.Errorf("build A and build B share rootfs %s; the second VM cannot boot", rootfsA)
	}

	// The clone must be a faithful copy, otherwise the VM boots a broken rootfs.
	for _, p := range []string{rootfsA, rootfsB} {
		got, readErr := os.ReadFile(p)
		if readErr != nil {
			t.Fatalf("read clone %s: %v", p, readErr)
		}
		if string(got) != string(content) {
			t.Errorf("clone %s content = %q, want %q", p, got, content)
		}
	}

	// Writing to a clone must not touch the template — the template is reused
	// by every later build.
	if err := os.WriteFile(rootfsA, []byte("guest-wrote-here"), 0o644); err != nil {
		t.Fatalf("write clone A: %v", err)
	}
	after, err := os.ReadFile(shared)
	if err != nil {
		t.Fatalf("re-read shared template: %v", err)
	}
	if string(after) != string(content) {
		t.Errorf("shared template mutated by a build: got %q, want %q", after, content)
	}
}

// TestSelectCacheDisks_ConcurrentLeasesAreDistinct proves that a second
// concurrent lease of the same ecosystem key gets a DIFFERENT image path while
// the first lease is still held, and that the slot is reused once released.
//
// Mutation proof: drop the flock in leaseCacheDiskSlot (always return slot 0)
// and the "distinct" assertion fails.
func TestSelectCacheDisks_ConcurrentLeasesAreDistinct(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available:", err)
	}
	ctx := context.Background()
	dataDir := t.TempDir()

	first, leasesFirst, err := SelectCacheDisks(ctx, dataDir, []string{"buildkit"})
	if err != nil {
		t.Fatalf("first SelectCacheDisks: %v", err)
	}
	defer ReleaseCacheDiskLeases(leasesFirst)

	second, leasesSecond, err := SelectCacheDisks(ctx, dataDir, []string{"buildkit"})
	if err != nil {
		t.Fatalf("second SelectCacheDisks while first held: %v", err)
	}

	if first[0].ImagePath == second[0].ImagePath {
		t.Fatalf("concurrent builds both leased %s; cloud-hypervisor refuses the "+
			"second VM's boot because the image is already write-locked",
			first[0].ImagePath)
	}
	// Slot 0 keeps the historical path so serial builds reuse their warm cache.
	wantSlot0 := filepath.Join(dataDir, "caches", "buildkit.ext4")
	if first[0].ImagePath != wantSlot0 {
		t.Errorf("first lease ImagePath = %s, want slot-0 path %s", first[0].ImagePath, wantSlot0)
	}
	if _, statErr := os.Stat(second[0].ImagePath); statErr != nil {
		t.Errorf("second lease image not created: %v", statErr)
	}

	// Releasing the second lease must free its slot for the next build.
	ReleaseCacheDiskLeases(leasesSecond)
	third, leasesThird, err := SelectCacheDisks(ctx, dataDir, []string{"buildkit"})
	if err != nil {
		t.Fatalf("third SelectCacheDisks after release: %v", err)
	}
	defer ReleaseCacheDiskLeases(leasesThird)
	if third[0].ImagePath != second[0].ImagePath {
		t.Errorf("released slot not reused: third = %s, want %s",
			third[0].ImagePath, second[0].ImagePath)
	}
}
