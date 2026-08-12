package main

// network.go — guest network initialisation for PID 1 startup.
//
// When nexus3-agent runs as PID 1 (in-guest init), it configures the guest
// virtio-net interface with a static IP and DNS server.  The nexus3 perimeter
// virtual network stack (gvproxy/netstack) assigns:
//
//	Guest IP  : 192.168.127.2/24
//	Gateway   : 192.168.127.1
//	DNS server: 192.168.127.1 (same as gateway — gvproxy serves DNS there)
//
// Using static assignment (not DHCP) keeps the agent lean — no DHCP client
// binary is required.  The addresses are fixed constants in the netstack
// package (netstackGatewayIP / netstackDeviceIP), so hardcoding them here is
// intentional and stable.

import (
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	guestNetworkIP      = "192.168.127.2/24"
	guestNetworkGateway = "192.168.127.1"
	guestNetworkDNS     = "192.168.127.1"
)

// setupLoopback brings the loopback interface (lo) UP via ioctl SIOCSIFFLAGS.
// The ip(8) binary is absent from the guest image, so we use a direct syscall.
// This must be called before setupNetwork so that 127.0.0.1 is reachable
// regardless of egress-interface state.  Errors are logged and non-fatal.
func setupLoopback(con *os.File) {
	// Open an AF_INET socket purely to issue the ioctl.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		consoleLog(con, "nexus3-agent: network: lo: socket: %v\n", err)
		return
	}
	defer unix.Close(fd)

	// Use x/sys/unix typed Ifreq so the struct is correctly sized (40 bytes on
	// linux/amd64); hand-rolling a [32]byte would undersize the kernel struct.
	ifreq, err := unix.NewIfreq("lo")
	if err != nil {
		consoleLog(con, "nexus3-agent: network: lo: NewIfreq: %v\n", err)
		return
	}
	// Read current flags.
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifreq); err != nil {
		consoleLog(con, "nexus3-agent: network: lo: SIOCGIFFLAGS: %v\n", err)
		return
	}
	// Set IFF_UP and write back.
	ifreq.SetUint16(ifreq.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifreq); err != nil {
		consoleLog(con, "nexus3-agent: network: lo: SIOCSIFFLAGS: %v\n", err)
		return
	}
	consoleLog(con, "nexus3-agent: network: lo: up\n")
}

// setupNetwork configures the guest virtio-net interface for the nexus3
// perimeter.  Called only when running as PID 1 (after mountGuestFS).
// All errors are logged and non-fatal: if network setup fails, the agent
// still serves vsock traffic for non-network workloads.
func setupNetwork(con *os.File) {
	// Bring loopback up first so 127.0.0.1 is reachable regardless of
	// egress-interface state.  Uses ioctl directly — ip(8) is absent.
	setupLoopback(con)

	// Write /etc/resolv.conf before bringing up the interface so DNS
	// resolves immediately once the interface is up.
	dns := "nameserver " + guestNetworkDNS + "\n"
	if err := os.WriteFile("/etc/resolv.conf", []byte(dns), 0o644); err != nil {
		consoleLog(con, "nexus3-agent: network: write /etc/resolv.conf: %v\n", err)
		// Non-fatal: continue anyway.
	}

	// Discover the first non-loopback interface (virtio-net appears as eth0
	// on kernels without udev/systemd predictable names — which is the case
	// when nexus3-agent is PID 1).
	iface := firstNonLoIface()
	if iface == "" {
		consoleLog(con, "nexus3-agent: network: no non-loopback interface found; skipping\n")
		return
	}
	consoleLog(con, "nexus3-agent: network: configuring %s: ip=%s gw=%s\n",
		iface, guestNetworkIP, guestNetworkGateway)

	// Locate the ip(8) binary. Paths differ by distro:
	//   Debian/Ubuntu: /usr/sbin/ip (iproute2 package)
	//   Alpine Linux:  /sbin/ip     (busybox applet)
	// nexus3-agent runs as PID 1, so kernel init PATH may omit these dirs;
	// we probe absolute paths instead of relying on PATH.
	ipBin := ""
	for _, candidate := range []string{"/usr/sbin/ip", "/sbin/ip", "/bin/ip"} {
		if _, err := os.Stat(candidate); err == nil {
			ipBin = candidate
			break
		}
	}
	if ipBin == "" {
		consoleLog(con, "nexus3-agent: network: ip(8) not found at /usr/sbin/ip, /sbin/ip, /bin/ip; skipping\n")
		return
	}

	// Bring interface up.
	runNetCmd(con, ipBin, "link", "set", iface, "up")
	// Assign static IP.
	runNetCmd(con, ipBin, "addr", "add", guestNetworkIP, "dev", iface)
	// Set default route.
	runNetCmd(con, ipBin, "route", "add", "default", "via", guestNetworkGateway)
	consoleLog(con, "nexus3-agent: network: %s configured\n", iface)
}

// firstNonLoIface returns the name of the first non-loopback network interface
// from /sys/class/net, or "" if none is found.
func firstNonLoIface() string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Name() != "lo" {
			return e.Name()
		}
	}
	return ""
}

// runNetCmd executes a network-configuration command and logs any error.
// Failures are non-fatal: some commands may legitimately fail (e.g. "ip addr
// add" when the address is already configured after a re-exec).
func runNetCmd(con *os.File, name string, args ...string) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		consoleLog(con, "nexus3-agent: network: %s %s: %v: %s\n",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}
