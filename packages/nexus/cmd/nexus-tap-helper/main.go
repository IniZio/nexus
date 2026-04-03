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
	"os/exec"
	"strings"
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
	tunsetiff = 0x400454ca
	iffTAP    = 0x0002
	iffNoPI   = 0x1000
)

// ifreqFlags is the layout for TUNSETIFF / SIOCGIFFLAGS / SIOCSIFFLAGS.
type ifreqFlags struct {
	Name  [unix.IFNAMSIZ]byte
	Flags uint16
	_     [22]byte
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

	// Attach tap to bridge.
	out, err := exec.Command("ip", "link", "set", tapName, "master", bridge).CombinedOutput()
	if err != nil {
		return fmt.Errorf("attach %s to bridge %s: %w: %s", tapName, bridge, err, strings.TrimSpace(string(out)))
	}

	return nil
}

// deleteTAP removes a TAP device.
func deleteTAP(tapName string) error {
	out, err := exec.Command("ip", "link", "del", tapName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip link del %s: %w: %s", tapName, err, strings.TrimSpace(string(out)))
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
