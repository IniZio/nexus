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
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

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
	runNetCmd(con, ipBin, "route", "add", "default", "via", guestNetworkGateway)
	consoleLog(con, "nexus3-agent: network: %s configured\n", iface)

	// Probe egress immediately after configuration so any breakage (wrong
	// interface, perimeter down, DNS dead) is logged loudly on the serial
	// console rather than causing a silent downstream build timeout.
	checkEgress(iface, con)
}

// firstNonLoIface returns the name of the first non-loopback network interface
// from /sys/class/net that has a hardware device backing (i.e. has a
// /sys/class/net/<name>/device symlink). Virtual interfaces such as dummy0
// and veth* lack this symlink and are skipped.
//
// Kernels built with CONFIG_DUMMY=y (needed for Docker/compose networking)
// create a dummy0 interface at boot. Because "dummy" < "eth" alphabetically,
// a naive first-non-lo scan picks dummy0 before the virtio-net interface
// (eth0), assigning the guest IP to a black-hole device and breaking egress.
//
// Fallback: if no hardware-backed interface is found (e.g. an older test
// kernel where dummy0 is absent), the first non-loopback interface is
// returned, preserving previous behaviour.
func firstNonLoIface() string {
	return firstNonLoIfaceAt("/sys")
}

// firstNonLoIfaceAt is the testable implementation of firstNonLoIface.
// sysRoot is the root of the sysfs tree (production: "/sys"; tests: a tmpdir).
func firstNonLoIfaceAt(sysRoot string) string {
	netDir := sysRoot + "/class/net"
	entries, err := os.ReadDir(netDir)
	if err != nil {
		return ""
	}
	// Prefer hardware-backed interfaces. These have a "device"
	// symlink in <netDir>/<name>/device pointing to the PCI (or other bus)
	// device.  Virtual interfaces (dummy0, lo, veth*, bridge) do not.
	for _, e := range entries {
		name := e.Name()
		if name == "lo" {
			continue
		}
		if _, statErr := os.Stat(netDir + "/" + name + "/device"); statErr == nil {
			return name
		}
	}
	// Fallback: return the first non-loopback interface (original behaviour,
	// handles kernels that expose no sysfs device links).
	for _, e := range entries {
		if e.Name() != "lo" {
			return e.Name()
		}
	}
	return ""
}

// checkEgress probes the egress path immediately after interface configuration
// and logs the result to the serial console. It is NON-FATAL: a failure is
// logged with a greppable "EGRESS SELF-CHECK FAILED" prefix so the host can
// identify a broken-network boot from the serial log, but the agent continues
// serving vsock traffic regardless.
//
// Decision: log-only (not fatal) so existing working sandboxes are never
// regressed. A failed check is diagnostic, not a hard blocker.
//
// TODO(builder-role): the builder-role path should surface a failed egress
// check as the build error (instead of letting buildkit time out on DNS).
// That integration belongs in the builder-role init, not here.
func checkEgress(iface string, con *os.File) {
	checkEgressWith(iface, "/sys", probeGatewayDNS, con)
}

// checkEgressWith is the testable implementation of checkEgress.
//
//   - sysRoot: sysfs root ("/sys" in production; a tmpdir in tests)
//   - probe: called with the DNS server address and a timeout; nil = reachable
//
// Returns true if all checks pass, false on any failure.
func checkEgressWith(iface, sysRoot string, probe func(addr string, timeout time.Duration) error, con *os.File) bool {
	const probeTimeout = 3 * time.Second

	// 1. Assert the interface is hardware-backed.
	//    Virtual interfaces (dummy0, veth*, bridge) lack a sysfs "device" entry.
	//    This is the same heuristic used by firstNonLoIfaceAt to select the
	//    interface; checking it here gives defence-in-depth against the class of
	//    bug where the IP is assigned to a black-hole device.
	hwBacked := false
	if _, err := os.Stat(sysRoot + "/class/net/" + iface + "/device"); err == nil {
		hwBacked = true
	}

	// 2. Probe gateway/DNS reachability.
	//    Send a minimal DNS query to 192.168.127.1:53 (the gvproxy netstack
	//    that serves both routing and DNS).  Any valid response proves the
	//    perimeter is up and traffic is flowing.
	dnsStatus := "ok"
	dnsOK := true
	if err := probe(guestNetworkDNS+":53", probeTimeout); err != nil {
		dnsOK = false
		dnsStatus = fmt.Sprintf("timeout(%v)", err)
	}

	if !hwBacked || !dnsOK {
		consoleLog(con,
			"nexus3-agent: EGRESS SELF-CHECK FAILED: iface=%s hwbacked=%v gateway=%s dns=%s"+
				" — network egress is broken; in-guest builds/pulls will fail\n",
			iface, hwBacked, guestNetworkGateway, dnsStatus)
		return false
	}

	consoleLog(con, "nexus3-agent: egress self-check ok (iface=%s)\n", iface)
	return true
}

// probeGatewayDNS sends a minimal DNS wire query (root "." NS IN) to addr over
// UDP and waits for any response. A valid response proves the perimeter
// (gvproxy) is reachable and serving DNS. This is the production probe used by
// checkEgress — never called in unit tests (a fake is injected instead).
func probeGatewayDNS(addr string, timeout time.Duration) error {
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	// Minimal valid DNS query: ". NS IN" (17 bytes).
	// Header: ID=0x0001, Flags=QR=0/OPCODE=0/RD=1 → 0x01 0x00,
	// QDCOUNT=1, ANCOUNT=0, NSCOUNT=0, ARCOUNT=0.
	// Question: QNAME=root (single 0x00 label), QTYPE=NS(2), QCLASS=IN(1).
	query := []byte{
		0x00, 0x01, // ID
		0x01, 0x00, // Flags: RD=1
		0x00, 0x01, // QDCOUNT
		0x00, 0x00, // ANCOUNT
		0x00, 0x00, // NSCOUNT
		0x00, 0x00, // ARCOUNT
		0x00,       // QNAME: root (empty label)
		0x00, 0x02, // QTYPE: NS
		0x00, 0x01, // QCLASS: IN
	}
	if _, err := conn.Write(query); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if n < 4 {
		return fmt.Errorf("response too short: %d bytes", n)
	}
	return nil
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
