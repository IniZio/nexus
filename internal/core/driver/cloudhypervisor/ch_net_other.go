//go:build !linux

package cloudhypervisor

import "os"

// openHostTap is unsupported off Linux (no /dev/net/tun). The client never
// boots a VM locally; see errUnsupportedPlatform.
func openHostTap(name string) (*os.File, error) {
	return nil, errUnsupportedPlatform
}
