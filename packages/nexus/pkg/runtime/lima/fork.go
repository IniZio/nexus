package lima

import "fmt"

// btrfsForkScript returns a shell script that creates a btrfs subvolume snapshot
// of parentPath at childPath. Both paths must be btrfs subvolumes on the same
// btrfs filesystem (the workspace data volume).
//
// The snapshot is read-write. Parent and child diverge independently from this
// point via btrfs copy-on-write semantics.
func btrfsForkScript(parentPath, childPath string) string {
	return fmt.Sprintf("sudo -n btrfs subvolume snapshot %s %s", parentPath, childPath)
}
