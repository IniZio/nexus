//go:build linux

// nexus-tap-helper is a small privileged helper binary that creates and deletes
// TAP network interfaces on behalf of the Firecracker VM manager.
//
// It requires cap_net_admin=ep set once at install time:
//
//	sudo setcap cap_net_admin=ep /usr/local/bin/nexus-tap-helper
//
// Usage:
//
//	nexus-tap-helper create <tapname> <bridge>
//	nexus-tap-helper delete <tapname>
package main

import (
	"fmt"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: nexus-tap-helper create <tapname> <bridge>\n")
		fmt.Fprintf(os.Stderr, "       nexus-tap-helper delete <tapname>\n")
		os.Exit(1)
	}
	subcmd := os.Args[1]
	switch subcmd {
	case "create":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "usage: nexus-tap-helper create <tapname> <bridge>\n")
			os.Exit(1)
		}
		tapName := os.Args[2]
		bridge := os.Args[3]
		if err := createTAP(tapName, bridge); err != nil {
			fmt.Fprintf(os.Stderr, "nexus-tap-helper create: %v\n", err)
			os.Exit(1)
		}
	case "delete":
		tapName := os.Args[2]
		if err := deleteTAP(tapName); err != nil {
			fmt.Fprintf(os.Stderr, "nexus-tap-helper delete: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", subcmd)
		os.Exit(1)
	}
}

const (
	tunsetiff   = 0x400454ca
	iffTAP      = 0x0002
	iffNoPI     = 0x1000
	siocBrAddIf = 0x89a2
)

// ifreqFlags is the layout for TUNSETIFF / SIOCGIFFLAGS / SIOCSIFFLAGS.
type ifreqFlags struct {
	Name  [unix.IFNAMSIZ]byte
	Flags uint16
	_     [22]byte
}

// ifreqIndex is the layout for bridge ioctls that pass an ifindex.
type ifreqIndex struct {
	Name  [unix.IFNAMSIZ]byte
	Index int32
	_     [20]byte
}

// createTAP creates a persistent TAP device and attaches it to the given bridge.
func createTAP(tapName, bridge string) error {
	if len(tapName) >= unix.IFNAMSIZ {
		return fmt.Errorf("tap name %q exceeds max length %d", tapName, unix.IFNAMSIZ-1)
	}

	// Open /dev/net/tun and issue TUNSETIFF to create the tap.
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /dev/net/tun: %w", err)
	}

	var req ifreqFlags
	copy(req.Name[:], tapName)
	req.Flags = iffTAP | iffNoPI
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), tunsetiff, uintptr(unsafe.Pointer(&req))); errno != 0 {
		unix.Close(fd)
		return fmt.Errorf("TUNSETIFF %s: %w", tapName, errno)
	}

	// Make the tap persist after we close the fd.
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETPERSIST, 1); errno != 0 {
		unix.Close(fd)
		return fmt.Errorf("TUNSETPERSIST %s: %w", tapName, errno)
	}
	unix.Close(fd)

	// Bring the interface up.
	if err := setLinkUp(tapName); err != nil {
		return fmt.Errorf("bring up %s: %w", tapName, err)
	}

	if err := attachToBridge(tapName, bridge); err != nil {
		return fmt.Errorf("attach %s to bridge %s: %w", tapName, bridge, err)
	}

	return nil
}

// deleteTAP removes a TAP device.
func deleteTAP(tapName string) error {
	if len(tapName) >= unix.IFNAMSIZ {
		return fmt.Errorf("tap name %q exceeds max length %d", tapName, unix.IFNAMSIZ-1)
	}

	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /dev/net/tun: %w", err)
	}
	defer unix.Close(fd)

	var req ifreqFlags
	copy(req.Name[:], tapName)
	req.Flags = iffTAP | iffNoPI
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), tunsetiff, uintptr(unsafe.Pointer(&req))); errno != 0 {
		return fmt.Errorf("TUNSETIFF %s: %w", tapName, errno)
	}

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETPERSIST, 0); errno != 0 {
		return fmt.Errorf("TUNSETPERSIST(0) %s: %w", tapName, errno)
	}

	return nil
}

func attachToBridge(tapName, bridge string) error {
	if len(bridge) >= unix.IFNAMSIZ {
		return fmt.Errorf("bridge name %q exceeds max length %d", bridge, unix.IFNAMSIZ-1)
	}

	tapIface, err := net.InterfaceByName(tapName)
	if err != nil {
		return fmt.Errorf("lookup tap %s: %w", tapName, err)
	}

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("socket: %w", err)
	}
	defer unix.Close(fd)

	var req ifreqIndex
	copy(req.Name[:], bridge)
	req.Index = int32(tapIface.Index)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), siocBrAddIf, uintptr(unsafe.Pointer(&req))); errno != 0 {
		return errno
	}

	return nil
}

// setLinkUp brings a network interface up by name using SIOCGIFFLAGS/SIOCSIFFLAGS.
func setLinkUp(ifName string) error {
	// Look up current interface flags.
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifName, err)
	}

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("socket: %w", err)
	}
	defer unix.Close(fd)

	var req ifreqFlags
	copy(req.Name[:], iface.Name)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&req))); errno != 0 {
		return fmt.Errorf("SIOCGIFFLAGS: %w", errno)
	}
	req.Flags |= unix.IFF_UP
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&req))); errno != 0 {
		return fmt.Errorf("SIOCSIFFLAGS: %w", errno)
	}
	return nil
}
