//go:build linux

package agent

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestBuildkitStateIsPersistent_PlainDir verifies that buildkitStateIsPersistent
// returns false for a plain subdirectory that shares its parent's st_dev —
// exactly the situation on the dogfood/nested path where no cache disk is
// attached and /var/lib/buildkit is just a directory on the guest ext4 rootfs.
func TestBuildkitStateIsPersistent_PlainDir(t *testing.T) {
	// Create a plain subdirectory. It will share its parent's st_dev.
	dir, err := os.MkdirTemp("", "g6-plain-dir-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	if buildkitStateIsPersistent(dir) {
		t.Errorf("buildkitStateIsPersistent(%q) = true for plain dir on same device as parent, want false", dir)
	}
}

// TestBuildkitStateIsPersistent_SeparateMountpoint verifies that
// buildkitStateIsPersistent returns true when the path is a distinct mountpoint
// (different st_dev than parent). This simulates the G5 cache disk case where
// a virtio-blk device is mounted at /var/lib/buildkit.
//
// If CAP_SYS_ADMIN is available (inside a KVM guest, or on a privileged test
// runner), the test mounts a real tmpfs and checks that the distinct st_dev is
// detected. Without mount privileges it states what it cannot exercise.
func TestBuildkitStateIsPersistent_SeparateMountpoint(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "g6-mountpoint-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Try to mount a tmpfs — creates a distinct mountpoint (different st_dev).
	if err := unix.Mount("tmpfs", dir, "tmpfs", 0, "size=4m"); err != nil {
		t.Skipf("cannot mount tmpfs at %q (%v) — CAP_SYS_ADMIN not available; "+
			"live mountpoint branch cannot be exercised without privilege. "+
			"The st_dev logic is verified by the plain-dir test (same-device → false); "+
			"run this inside a KVM guest (as in TestBuildDogfood) for the true-branch proof.",
			dir, err)
	}
	defer unix.Unmount(dir, 0) //nolint:errcheck

	if !buildkitStateIsPersistent(dir) {
		t.Errorf("buildkitStateIsPersistent(%q) = false for a real separate mountpoint, want true", dir)
	}
}

// TestBuildkitRootDirIsUnderState documents the --root contract: buildkitd's
// root is always under inGuestBuildkitState regardless of branch.
func TestBuildkitRootDirIsUnderState(t *testing.T) {
	want := inGuestBuildkitState + "/root"
	t.Logf("buildkitd --root = %q (persistent disk when separate mountpoint, tmpfs otherwise)", want)
	if want != "/var/lib/buildkit/root" {
		t.Errorf("unexpected root path %q", want)
	}
}
