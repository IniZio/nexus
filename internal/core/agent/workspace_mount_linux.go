//go:build linux

package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// EnsureHostHomeSymlink creates a symlink at hostHome pointing to /root so that
// host-absolute paths inside config files (e.g. /home/alice/.claude/plugins/...)
// resolve correctly inside the guest.
//
// Skip conditions (any one → no-op):
//   - hostHome is empty, "/root", not absolute, or contains ".."
//   - hostHome already exists as a symlink (leave as-is)
//   - hostHome exists as a non-empty real directory (log warning, leave as-is)
//
// If hostHome exists as an empty real directory it is removed and replaced with
// the symlink. If the path does not exist its parent is created first.
func EnsureHostHomeSymlink(hostHome string) error {
	if hostHome == "" || hostHome == "/root" {
		return nil
	}
	if !filepath.IsAbs(hostHome) || strings.Contains(hostHome, "..") {
		return nil
	}

	fi, err := os.Lstat(hostHome)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil // already a symlink — leave it
		}
		// Exists as a real entry (dir or file).
		entries, readErr := os.ReadDir(hostHome)
		if readErr != nil {
			return fmt.Errorf("hosthome: readdir %s: %w", hostHome, readErr)
		}
		if len(entries) > 0 {
			slog.Warn("hosthome: exists as non-empty directory; leaving it", "path", hostHome)
			return nil
		}
		// Empty real directory — remove it so we can create the symlink.
		if rmErr := os.Remove(hostHome); rmErr != nil {
			return fmt.Errorf("hosthome: rmdir %s: %w", hostHome, rmErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("hosthome: lstat %s: %w", hostHome, err)
	} else {
		// Path does not exist — create parent directories.
		if mkErr := os.MkdirAll(filepath.Dir(hostHome), 0o755); mkErr != nil {
			return fmt.Errorf("hosthome: mkdir parent of %s: %w", hostHome, mkErr)
		}
	}

	if symErr := os.Symlink("/root", hostHome); symErr != nil {
		return fmt.Errorf("hosthome: symlink %s -> /root: %w", hostHome, symErr)
	}
	return nil
}

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
