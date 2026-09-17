//go:build linux

package main

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
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
			if !strings.Contains(c.data, "mode=1777") {
				t.Fatalf("mountGuestFS: /tmp tmpfs options %q lack mode=1777; "+
					"a /tmp without the sticky bit is rejected as a socket dir "+
					"by Claude Code cross-session messaging", c.data)
			}
			return // PASS: tmpfs on /tmp with a size option found
		}
	}
	t.Fatalf("mountGuestFS did not mount tmpfs on /tmp; captured mounts: %v", calls)
}

// TestWipeMountScratchDisk_TmpIsSticky1777 drives the real wipeMountScratchDisk
// with mkfs/unmount/mount stubbed and the mount point redirected to a temp dir,
// then stats the directory: the observed symptom was a 0777 (no sticky bit)
// /tmp on every scratch-disk sandbox, because os.Chmod(path, 0o1777) masks
// off the raw sticky bit.
func TestWipeMountScratchDisk_TmpIsSticky1777(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	origMount, origMkfs, origUnmount, origMountFn := scratchDiskGuestMount, scratchMkfsFunc, scratchUnmountFunc, scratchMountFunc
	t.Cleanup(func() {
		scratchDiskGuestMount, scratchMkfsFunc, scratchUnmountFunc, scratchMountFunc = origMount, origMkfs, origUnmount, origMountFn
	})
	scratchDiskGuestMount = dir
	scratchMkfsFunc = func(string) ([]byte, error) { return nil, nil }
	scratchUnmountFunc = func(string, int) error { return syscall.EINVAL }
	scratchMountFunc = func(string, string, string, uintptr, string) error { return nil }

	if err := wipeMountScratchDisk("/dev/vdz", nil); err != nil {
		t.Fatalf("wipeMountScratchDisk: %v", err)
	}

	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		t.Fatal(err)
	}
	if got := st.Mode & 0o7777; got != 0o1777 {
		t.Fatalf("scratch /tmp mode = %04o, want 1777 (sticky bit required)", got)
	}
}

// TestMountGuestFS_includesTmpfsDevShm verifies that mountGuestFS mounts a
// world-writable tmpfs on /dev/shm.
//
// devtmpfs does not create /dev/shm, and without it glibc's sem_open/shm_open
// fail with ENOENT because they resolve names under that directory. The
// observed symptom in a worktree sandbox was djlint crashing, and the minimal
// reproducer was `python3 -c "import multiprocessing; multiprocessing.Semaphore()"`
// raising "FileNotFoundError: [Errno 2] No such file or directory".
//
// mode=1777 is asserted because a /dev/shm that only root may write is no more
// usable than a missing one for any non-root process in the guest.
func TestMountGuestFS_includesTmpfsDevShm(t *testing.T) {
	type mountCall struct {
		source, target, fstype, data string
	}

	var calls []mountCall
	orig := guestMountFn
	defer func() { guestMountFn = orig }()
	guestMountFn = func(source, target, fstype string, flags uintptr, data string) error {
		calls = append(calls, mountCall{source, target, fstype, data})
		return nil
	}
	mountGuestFS()

	for _, c := range calls {
		if c.target != "/dev/shm" {
			continue
		}
		if c.fstype != "tmpfs" {
			t.Fatalf("mountGuestFS: /dev/shm mounted as %q; want tmpfs", c.fstype)
		}
		if !strings.Contains(c.data, "mode=1777") {
			t.Fatalf("mountGuestFS: /dev/shm options %q lack mode=1777; a "+
				"root-only /dev/shm still breaks POSIX shm for non-root guests", c.data)
		}
		if !strings.Contains(c.data, "size=") {
			t.Fatalf("mountGuestFS: /dev/shm options %q have no size cap; an "+
				"unbounded RAM-backed tmpfs can starve the guest memory governor", c.data)
		}
		return // PASS
	}
	t.Fatalf("mountGuestFS did not mount tmpfs on /dev/shm; "+
		"POSIX semaphores (multiprocessing, djlint) will fail with ENOENT; "+
		"captured mounts: %v", calls)
}
