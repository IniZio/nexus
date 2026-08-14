//go:build linux

package main

import (
	"testing"
)

// TestMountGuestFS_includesTmpfsTmp verifies the /tmp tmpfs mount behaviour of
// mountGuestFS for both auto-resize modes.
//
// Sub-test "autoResize=true": /tmp MUST be mounted as tmpfs with a non-empty
// size option (size=0/unlimited makes currentTmpCapBytes() enormous and the
// resizer can never grow it).
//
// Sub-test "autoResize=false" (opt-out regression): the escape hatch from a
// misbehaving governor is --no-auto-resize, which leaves /tmp disk-backed so
// the workload retains the full rootfs scratch area (≈5959 MiB). This case
// MUST NOT mount any tmpfs on /tmp.
//
// The test overrides guestMountFn to capture all mount calls without requiring
// root or a real mount namespace.
func TestMountGuestFS_includesTmpfsTmp(t *testing.T) {
	type mountCall struct {
		source, target, fstype, data string
	}

	capturesMounts := func(autoResize bool) []mountCall {
		var calls []mountCall
		orig := guestMountFn
		defer func() { guestMountFn = orig }()
		guestMountFn = func(source, target, fstype string, flags uintptr, data string) error {
			calls = append(calls, mountCall{source, target, fstype, data})
			return nil
		}
		mountGuestFS(autoResize)
		return calls
	}

	t.Run("autoResize=true mounts tmpfs on /tmp", func(t *testing.T) {
		calls := capturesMounts(true)
		for _, c := range calls {
			if c.target == "/tmp" && c.fstype == "tmpfs" {
				if c.data == "" {
					t.Fatalf("mountGuestFS(true): /tmp tmpfs has no size option; " +
						"size=0 (unlimited) makes currentTmpCapBytes() huge and " +
						"the resizer can never grow it")
				}
				return // PASS
			}
		}
		t.Fatalf("mountGuestFS(true) did not mount tmpfs on /tmp; captured mounts: %v", calls)
	})

	t.Run("autoResize=false skips tmpfs on /tmp", func(t *testing.T) {
		calls := capturesMounts(false)
		for _, c := range calls {
			if c.target == "/tmp" && c.fstype == "tmpfs" {
				t.Fatalf("mountGuestFS(false) mounted tmpfs on /tmp; "+
					"--no-auto-resize must keep /tmp disk-backed so the "+
					"escape hatch from a misbehaving governor remains usable; "+
					"captured mounts: %v", calls)
			}
		}
		// No tmpfs on /tmp found — PASS.
	})
}
