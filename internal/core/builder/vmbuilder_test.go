package builder

import (
	"errors"
	"strings"
	"testing"
)

// TestWrapOutOfSpaceErr_VMBuild verifies the out-of-space helper.
func TestWrapOutOfSpaceErr_VMBuild(t *testing.T) {
	t.Run("no space left on device", func(t *testing.T) {
		orig := errors.New("runc-native/snapshots/ingestions/123: no space left on device")
		got := wrapOutOfSpaceErr(orig)
		if !strings.Contains(got.Error(), "buildkit cache disk (/var/lib/buildkit) is full") {
			t.Fatalf("expected legible cache-disk message, got: %v", got)
		}
		if !errors.Is(got, orig) {
			t.Fatalf("errors.Is must reach original error through wrap chain")
		}
	})

	t.Run("ResourceExhausted gRPC status", func(t *testing.T) {
		orig := errors.New("buildkit: solve: ResourceExhausted: context deadline exceeded")
		got := wrapOutOfSpaceErr(orig)
		if !strings.Contains(got.Error(), "buildkit cache disk (/var/lib/buildkit) is full") {
			t.Fatalf("expected legible cache-disk message, got: %v", got)
		}
		if !errors.Is(got, orig) {
			t.Fatalf("errors.Is must reach original error through wrap chain")
		}
	})

	t.Run("ENOSPC at buildkit snapshots path", func(t *testing.T) {
		// Realistic: kernel ENOSPC surfaced while buildkit writes to its cache disk.
		// Must still be classified as disk-full (the ENOSPC token is present).
		orig := errors.New("failed to write to /var/lib/buildkit/snapshots: no space left on device")
		got := wrapOutOfSpaceErr(orig)
		if !strings.Contains(got.Error(), "buildkit cache disk (/var/lib/buildkit) is full") {
			t.Fatalf("expected legible cache-disk message, got: %v", got)
		}
		if !errors.Is(got, orig) {
			t.Fatalf("errors.Is must reach original error through wrap chain")
		}
	})

	t.Run("hollow-export path not mislabeled disk-full", func(t *testing.T) {
		// Regression guard: an error whose message mentions the export scratch path
		// (/var/lib/buildkit/nexus3-export) but carries no ENOSPC signal must NOT
		// be classified as cache-disk-full. With the bare path clause present this
		// test fails (false positive); after removal it passes.
		orig := errors.New("rootfs hollow: /var/lib/buildkit/nexus3-export: no artifacts written")
		got := wrapOutOfSpaceErr(orig)
		if got != orig {
			t.Fatalf("hollow-export error must pass through unchanged; got: %v", got)
		}
	})

	t.Run("unrelated error passes through unchanged", func(t *testing.T) {
		orig := errors.New("exec builder role: exit status 1")
		got := wrapOutOfSpaceErr(orig)
		if got != orig {
			t.Fatalf("unrelated error must be returned as-is; got: %v", got)
		}
	})

	t.Run("nil passes through", func(t *testing.T) {
		if got := wrapOutOfSpaceErr(nil); got != nil {
			t.Fatalf("nil must return nil; got: %v", got)
		}
	})
}
