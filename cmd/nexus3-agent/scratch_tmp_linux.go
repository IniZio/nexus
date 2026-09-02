//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const scratchDiskGuestMount = "/tmp"

// agentScratchDisk is set by main() once — before any goroutines call
// guestBaselineEnv — to record whether a scratch disk was provided via the
// --scratch-disk= kernel cmdline argument. It controls the TMPDIR rider
// emitted by guestBaselineEnv (D-SD-04).
var agentScratchDisk bool

// parseScratchDiskArg parses a "--scratch-disk=<dev>" kernel cmdline token.
// Returns the device path and true on success, ("", false) when absent or malformed.
func parseScratchDiskArg(arg string) (dev string, ok bool) {
	const prefix = "--scratch-disk="
	if !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	dev = strings.TrimPrefix(arg, prefix)
	if dev == "" {
		return "", false
	}
	return dev, true
}

// wipeMountScratchDisk wipes, reformats, and mounts the scratch disk at /tmp.
func wipeMountScratchDisk(dev string, con *os.File) error {
	consoleLog(con, "nexus3-agent: scratch disk %s: wiping and mounting as %s (D-SD-01)\n", dev, scratchDiskGuestMount)
	out, err := exec.Command("mkfs.ext4", "-q", "-F", dev).CombinedOutput()
	if err != nil {
		return fmt.Errorf("scratch disk %s: mkfs.ext4: %w: %s", dev, err, out)
	}
	if err := syscall.Unmount(scratchDiskGuestMount, 0); err != nil && err != syscall.EINVAL {
		// EINVAL = not mounted; safe to ignore (first boot after mountGuestFS skipped /tmp)
		return fmt.Errorf("scratch disk: unmount %s: %w", scratchDiskGuestMount, err)
	}
	if err := syscall.Mount(dev, scratchDiskGuestMount, "ext4", 0, ""); err != nil {
		return fmt.Errorf("scratch disk %s: mount %s: %w", dev, scratchDiskGuestMount, err)
	}
	if err := os.Chmod(scratchDiskGuestMount, 0o1777); err != nil {
		return fmt.Errorf("scratch disk: chmod %s: %w", scratchDiskGuestMount, err)
	}
	consoleLog(con, "nexus3-agent: scratch disk %s: mounted as %s\n", dev, scratchDiskGuestMount)
	return nil
}
