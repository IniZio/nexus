package firecracker

import "fmt"

// btrfsForkScript returns a shell script to create a btrfs subvolume snapshot
// of parentPath at childPath inside the Firecracker guest VM.
//
// NOTE: Currently unused — the Firecracker kernel (vmlinux-5.10.239) does not have
// CONFIG_BTRFS_FS compiled in. This function is ready for when a btrfs-enabled
// kernel is available. See experiments/2026-04-16-btrfs-fork-poc/README.md.
//
// Requires btrfs-progs in the guest and the workspace data volume formatted as btrfs.
func btrfsForkScript(parentPath, childPath string) string {
	return fmt.Sprintf("btrfs subvolume snapshot %s %s", parentPath, childPath)
}
