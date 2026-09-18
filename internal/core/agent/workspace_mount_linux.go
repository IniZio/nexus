//go:build linux

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func (m GuestMount) mountFlags() uintptr {
	if m.ReadOnly {
		return syscall.MS_RDONLY
	}
	return 0
}

// planMountOrder returns a copy of mounts sorted so that every parent path
// appears before any of its descendants. Within the same depth level, mounts
// are sorted lexicographically by Target for full determinism.
//
// Invariant (D-DC-10): if mount A's Target is a path-prefix of mount B's
// Target, A appears before B. This guarantees that a shadow mount
// (e.g. /workspace/repo/node_modules) is never attempted before its parent
// (/workspace/repo) has been established, so the shadow is never hidden.
//
// planMountOrder does NOT mutate the input slice.
func planMountOrder(mounts []GuestMount) []GuestMount {
	out := make([]GuestMount, len(mounts))
	copy(out, mounts)
	sort.SliceStable(out, func(i, j int) bool {
		di := strings.Count(out[i].Target, "/")
		dj := strings.Count(out[j].Target, "/")
		if di != dj {
			return di < dj
		}
		return out[i].Target < out[j].Target
	})
	return out
}

// MountWorkspace mounts each entry in mounts in dependency-correct order
// (parents before children). For each mount it:
//  1. Creates the target directory with os.MkdirAll if it does not exist.
//  2. Calls mount(2) via syscall.Mount.
//
// Ordering is determined by planMountOrder; input order is irrelevant.
//
// On failure the returned error names both the device and the target so the
// caller can identify which mount step failed. Previously-completed mounts
// remain in place — the VM is responsible for overall cleanup on exit.
var guestBindMountFn = func(src, dst string, flags uintptr) error {
	if err := syscall.Mount(src, dst, "", syscall.MS_BIND|flags, ""); err != nil {
		return err
	}
	if flags&syscall.MS_RDONLY != 0 {
		return syscall.Mount("", dst, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY, "")
	}
	return nil
}

var fileMountScratchBase = "/run/nexus/filemounts"

var guestVirtiofsTagMountFn = func(device, target string, flags uintptr) error {
	return syscall.Mount(device, target, "virtiofs", flags, "")
}

func MountWorkspace(mounts []GuestMount) error {
	ordered := planMountOrder(mounts)
	for _, m := range ordered {
		if m.IsFile && m.FSType == "virtiofs" {
			if err := mountFileVirtiofs(m); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(m.Target, 0o755); err != nil {
			return fmt.Errorf("workspace mount: mkdir %s: %w", m.Target, err)
		}
		if err := syscall.Mount(m.Device, m.Target, m.FSType, m.mountFlags(), ""); err != nil {
			return fmt.Errorf("workspace mount: mount %s → %s (%s): %w",
				m.Device, m.Target, m.FSType, err)
		}
	}
	return nil
}

func mountFileVirtiofs(m GuestMount) error {
	scratch := filepath.Join(fileMountScratchBase, m.Device)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return fmt.Errorf("workspace mount: mkdir scratch %s: %w", scratch, err)
	}
	if err := guestVirtiofsTagMountFn(m.Device, scratch, m.mountFlags()); err != nil {
		return fmt.Errorf("workspace mount: mount virtiofs tag %s → %s: %w", m.Device, scratch, err)
	}
	src := filepath.Join(scratch, m.FileName)

	if err := os.MkdirAll(filepath.Dir(m.Target), 0o755); err != nil {
		return fmt.Errorf("workspace mount: mkdir parent of %s: %w", m.Target, err)
	}
	if _, statErr := os.Stat(m.Target); os.IsNotExist(statErr) {
		f, err := os.OpenFile(m.Target, os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("workspace mount: create bind target %s: %w", m.Target, err)
		}
		f.Close()
	}
	if err := guestBindMountFn(src, m.Target, m.mountFlags()); err != nil {
		return fmt.Errorf("workspace mount: bind-mount %s → %s: %w", src, m.Target, err)
	}
	return nil
}
