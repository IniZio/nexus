//go:build linux

package main

import (
	"testing"
)

// TestMountGuestFS_includesTmpfsTmp verifies that mountGuestFS unconditionally
// mounts /tmp as a tmpfs with a non-empty size option.
//
// size=0/unlimited would give f_blocks=ULONG_MAX, making currentTmpCapBytes()
// enormous and preventing the resizer from ever growing /tmp.
//
// The test overrides guestMountFn to capture all mount calls without requiring
// root or a real mount namespace.
func TestMountGuestFS_includesTmpfsTmp(t *testing.T) {
	type mountCall struct {
		source, target, fstype, data string
	}

	captureMounts := func() []mountCall {
		var calls []mountCall
		orig := guestMountFn
		defer func() { guestMountFn = orig }()
		guestMountFn = func(source, target, fstype string, flags uintptr, data string) error {
			calls = append(calls, mountCall{source, target, fstype, data})
			return nil
		}
		mountGuestFS()
		return calls
	}

	calls := captureMounts()
	for _, c := range calls {
		if c.target == "/tmp" && c.fstype == "tmpfs" {
			if c.data == "" {
				t.Fatalf("mountGuestFS: /tmp tmpfs has no size option; "+
					"size=0 (unlimited) makes currentTmpCapBytes() huge and "+
					"the resizer can never grow it; calls: %v", calls)
			}
			return // PASS: tmpfs on /tmp with a size option found
		}
	}
	t.Fatalf("mountGuestFS did not mount tmpfs on /tmp; captured mounts: %v", calls)
}
