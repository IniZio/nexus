package lima

import "fmt"

// namespaceWrapCommand wraps a shell command string with a per-process mount
// namespace so the workspace process sees /workspace as its canonical path,
// regardless of the actual subvolume mount at /workspace/<workspaceID>.
//
// Uses unshare(1) to create a private mount namespace, bind-mounts the
// workspace subvolume to /workspace within that namespace, then execs the
// inner command.
//
// Requires: either CAP_SYS_ADMIN or user namespaces enabled in the guest kernel
// (default on Ubuntu 22+: /proc/sys/user/max_user_namespaces > 0).
func namespaceWrapCommand(innerCmd, workspaceID string) string {
	subvolPath := guestWorkdirForID(workspaceID)
	return fmt.Sprintf(
		`sudo -n unshare --mount -- sh -c 'sudo -n mount --bind %s /workspace && cd /workspace && exec %s'`,
		subvolPath,
		innerCmd,
	)
}
