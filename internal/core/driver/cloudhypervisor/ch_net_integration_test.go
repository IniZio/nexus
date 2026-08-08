//go:build integration

package cloudhypervisor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestSandboxNet_NoLeakV4V6 verifies the LEAK-TIGHT invariant for the
// two-TAP/L2-bridge topology: IPv6 disabled, per-interface forwarding=0,
// and no IPv4 address on any of the three sandbox interfaces.
//
// Skip conditions (checked explicitly to give a clear skip message):
//   - /dev/net/tun not accessible (kernel TUN/TAP support absent)
//   - TUNSETIFF fails (CAP_NET_ADMIN absent; typical in unprivileged CI)
func TestSandboxNet_NoLeakV4V6(t *testing.T) {
	// Probe for CAP_NET_ADMIN by attempting a real TUNSETIFF call.
	probefd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping: /dev/net/tun not accessible: %v", err)
	}
	probereq, err := unix.NewIfreq("nx3-probe")
	if err != nil {
		_ = unix.Close(probefd)
		t.Skipf("skipping: NewIfreq: %v", err)
	}
	probereq.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(probefd, unix.TUNSETIFF, probereq); err != nil {
		_ = unix.Close(probefd)
		t.Skipf("skipping: TUNSETIFF requires CAP_NET_ADMIN: %v", err)
	}
	_ = unix.Close(probefd)

	// Use a deterministic sandbox ID based on the test PID to avoid collisions.
	var id domain.SandboxID
	pid := os.Getpid()
	id[0] = byte(pid >> 24)
	id[1] = byte(pid >> 16)
	id[2] = byte(pid >> 8)
	id[3] = byte(pid)
	id[4] = 0xEE // integration test marker

	guestTap, hostTap, bridge := tapIfNames(id)
	t.Logf("interfaces: guestTap=%s hostTap=%s bridge=%s", guestTap, hostTap, bridge)

	if err := createTapBridge(guestTap, hostTap, bridge); err != nil {
		t.Fatalf("createTapBridge: %v", err)
	}
	t.Cleanup(func() { deleteTapBridge(guestTap, hostTap, bridge) })

	// LEAK-TIGHT check 1: IPv6 must be disabled on all three interfaces.
	// The sysctl must have been written BEFORE the interface was brought up.
	for _, iface := range []string{guestTap, hostTap, bridge} {
		v6path := fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/disable_ipv6", iface)
		data, err := os.ReadFile(v6path)
		if err != nil {
			t.Errorf("read disable_ipv6 for %s: %v", iface, err)
			continue
		}
		if string(bytes.TrimSpace(data)) != "1" {
			t.Errorf("disable_ipv6 for %s: got %q want \"1\"", iface, string(data))
		}
	}

	// LEAK-TIGHT check 2: per-interface forwarding must be 0.
	// (We never write the global /proc/sys/net/ipv4/ip_forward.)
	for _, iface := range []string{guestTap, hostTap, bridge} {
		fwdpath := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/forwarding", iface)
		data, err := os.ReadFile(fwdpath)
		if err != nil {
			t.Errorf("read forwarding for %s: %v", iface, err)
			continue
		}
		if string(bytes.TrimSpace(data)) != "0" {
			t.Errorf("forwarding for %s: got %q want \"0\"", iface, string(data))
		}
	}

	// LEAK-TIGHT check 3: no IPv4 address on any interface.
	// createTapBridge never calls "ip addr add" — verified here via ip addr show.
	for _, iface := range []string{guestTap, hostTap, bridge} {
		out, err := exec.Command("ip", "addr", "show", iface).Output()
		if err != nil {
			t.Logf("ip addr show %s: %v (interface may have already been deleted)", iface, err)
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "inet ") {
				t.Errorf("interface %s has an IPv4 address: %s", iface, trimmed)
			}
		}
	}

	t.Log("LEAK-TIGHT invariant verified: IPv6 disabled, forwarding=0, no IPv4 addr")

	// TODO(P1-S3): When gvproxy VirtualNetwork is wired up, add a positive
	// traffic test here: boot a guest and verify zero packets egress through
	// the host-side TAP for both IPv4 and IPv6 destinations.
}
