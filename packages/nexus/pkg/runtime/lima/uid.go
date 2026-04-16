package lima

import "os"

// HostUID returns the numeric UID of the current process owner (the macOS host user).
// Used to provision the Lima guest user with a matching UID so that
// bind-mounted host directories are accessible without permission workarounds.
func HostUID() int {
	return os.Getuid()
}
