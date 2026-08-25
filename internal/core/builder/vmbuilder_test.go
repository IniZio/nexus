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

	t.Run("buildkit snapshots path", func(t *testing.T) {
		orig := errors.New("failed to write to /var/lib/buildkit/snapshots: disk quota exceeded")
		got := wrapOutOfSpaceErr(orig)
		if !strings.Contains(got.Error(), "buildkit cache disk (/var/lib/buildkit) is full") {
			t.Fatalf("expected legible cache-disk message, got: %v", got)
		}
		if !errors.Is(got, orig) {
			t.Fatalf("errors.Is must reach original error through wrap chain")
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
