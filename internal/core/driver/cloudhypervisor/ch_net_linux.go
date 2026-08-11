//go:build linux

package cloudhypervisor

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openHostTap opens /dev/net/tun and binds it to an existing TAP interface by
// name using TUNSETIFF. The interface must already exist (created by
// createTapBridge). Requires CAP_NET_ADMIN.
func openHostTap(name string) (*os.File, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	ifreq, err := unix.NewIfreq(name)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("NewIfreq(%s): %w", name, err)
	}
	// IFF_TAP: layer-2 (Ethernet frames); IFF_NO_PI: no prepended packet-info header.
	// With IFF_NO_PI, one Read returns exactly one raw Ethernet frame.
	ifreq.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifreq); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("TUNSETIFF(%s): %w", name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}
