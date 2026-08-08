//go:build integration

// Package netstack integration tests.
//
// These tests start a real gvisor VirtualNetwork over an AF_UNIX SOCK_DGRAM
// socketpair and inject crafted Ethernet/IPv4/TCP frames to drive allow and
// deny decisions through the full gvproxy+AllowList+AuditEvent call path.
//
// No CAP_NET_ADMIN or kernel TUN/TAP devices are required: gvproxy's
// VirtualNetwork is pure userspace. The test skips if the VirtualNetwork
// fails to initialize (should not happen in a standard Go test environment).
package netstack

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
)

// gatewayHWAddr is the VirtualNetwork gateway MAC (must match netstackGatewayMAC).
var gatewayHWAddr = [6]byte{0x5a, 0x94, 0xef, 0xe4, 0x0c, 0xdd}

// guestHWAddr is the fake guest MAC used when crafting frames.
var guestHWAddr = [6]byte{0x02, 0x00, 0xde, 0xad, 0xbe, 0xef}

// onesComplement computes the one's-complement checksum over b.
func onesComplement(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// ipv4Checksum computes the IPv4 header checksum.
func ipv4Checksum(hdr []byte) uint16 { return onesComplement(hdr) }

// tcpChecksum computes the TCP checksum using the IPv4 pseudo-header.
func tcpChecksum(srcIP, dstIP [4]byte, tcpSeg []byte) uint16 {
	pseudo := make([]byte, 12+len(tcpSeg))
	copy(pseudo[0:4], srcIP[:])
	copy(pseudo[4:8], dstIP[:])
	pseudo[8] = 0
	pseudo[9] = 6 // TCP
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(tcpSeg)))
	copy(pseudo[12:], tcpSeg)
	return onesComplement(pseudo)
}

// craftTCPSYN builds an Ethernet/IPv4/TCP SYN frame from guestIPv4:srcPort to
// dstIPv4:dstPort. The Ethernet destination is the VirtualNetwork gateway MAC
// so the gvisor switch delivers the frame to the stack.
func craftTCPSYN(guestIPv4, dstIPv4 [4]byte, srcPort, dstPort uint16) []byte {
	frame := make([]byte, 14+20+20)

	// Ethernet header (14 bytes).
	copy(frame[0:6], gatewayHWAddr[:]) // dst MAC = gateway
	copy(frame[6:12], guestHWAddr[:])  // src MAC = guest
	binary.BigEndian.PutUint16(frame[12:14], 0x0800) // EtherType IPv4

	// IPv4 header (20 bytes, no options).
	ip := frame[14 : 14+20]
	ip[0] = 0x45 // version=4, IHL=5
	ip[1] = 0x00
	binary.BigEndian.PutUint16(ip[2:4], 40) // total length (20+20)
	binary.BigEndian.PutUint16(ip[4:6], 1)  // identification
	binary.BigEndian.PutUint16(ip[6:8], 0)  // flags + fragment offset
	ip[8] = 64                              // TTL
	ip[9] = 6                               // protocol TCP
	// ip[10:12] = checksum (computed below)
	copy(ip[12:16], guestIPv4[:])
	copy(ip[16:20], dstIPv4[:])
	binary.BigEndian.PutUint16(ip[10:12], ipv4Checksum(ip))

	// TCP header (20 bytes, no options).
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:2], srcPort)
	binary.BigEndian.PutUint16(tcp[2:4], dstPort)
	binary.BigEndian.PutUint32(tcp[4:8], 0)      // seq
	binary.BigEndian.PutUint32(tcp[8:12], 0)     // ack
	tcp[12] = 0x50                               // data offset = 5 (20 bytes)
	tcp[13] = 0x02                               // flags: SYN
	binary.BigEndian.PutUint16(tcp[14:16], 1024) // window
	// tcp[16:18] = checksum (computed below)
	binary.BigEndian.PutUint16(tcp[18:20], 0) // urgent pointer
	binary.BigEndian.PutUint16(tcp[16:18], tcpChecksum(guestIPv4, dstIPv4, tcp))

	copy(frame[14+20:], tcp)
	return frame
}

// integSocketpair creates an AF_UNIX SOCK_DGRAM socketpair wrapped as net.Conn.
func integSocketpair(t *testing.T) (stackSide, guestSide net.Conn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	wrap := func(fd int, name string) net.Conn {
		f := os.NewFile(uintptr(fd), name)
		conn, err := net.FileConn(f)
		f.Close() // net.FileConn dups the fd; original is no longer needed
		if err != nil {
			t.Fatalf("net.FileConn(%s): %v", name, err)
		}
		return conn
	}
	return wrap(fds[0], "stack-side"), wrap(fds[1], "guest-side")
}

// TestSandboxNet_AllowAndDenyThroughStack drives an allow and a deny through the
// full Stack → VirtualNetwork → AllowList → AuditEvent call path by injecting
// crafted Ethernet/IPv4/TCP SYN frames into the socketpair.
//
// No CAP_NET_ADMIN is required; gvproxy's VirtualNetwork is pure userspace.
func TestSandboxNet_AllowAndDenyThroughStack(t *testing.T) {
	// Addresses: 10.0.1.100 is allowed; 10.0.2.200 is denied.
	allowedIP := [4]byte{10, 0, 1, 100}
	deniedIP := [4]byte{10, 0, 2, 200}
	guestIPv4 := [4]byte{192, 168, 127, 2}

	al, err := netfilter.NewAllowList([]string{net.IP(allowedIP[:]).String()}, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}
	al.Start(30 * time.Second)
	defer al.Stop()

	auditCh := make(chan perimeter.AuditEvent, 8)
	s := New(al, func(ev perimeter.AuditEvent) { auditCh <- ev })
	id := domain.NewSandboxID()

	stackSide, guestSide := integSocketpair(t)
	defer stackSide.Close()
	defer guestSide.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the Stack in a goroutine — blocks reading frames from stackSide.
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(ctx, id, stackSide) }()

	// Give the VirtualNetwork a moment to initialize its goroutines.
	time.Sleep(100 * time.Millisecond)

	// Inject an ALLOWED TCP SYN (guest 192.168.127.2:12345 → 10.0.1.100:80).
	if _, err := guestSide.Write(craftTCPSYN(guestIPv4, allowedIP, 12345, 80)); err != nil {
		t.Fatalf("write allow frame: %v", err)
	}

	// Inject a DENIED TCP SYN (guest 192.168.127.2:12346 → 10.0.2.200:80).
	if _, err := guestSide.Write(craftTCPSYN(guestIPv4, deniedIP, 12346, 80)); err != nil {
		t.Fatalf("write deny frame: %v", err)
	}

	// Collect events with a timeout.
	var events []perimeter.AuditEvent
	deadline := time.After(5 * time.Second)
collect:
	for len(events) < 2 {
		select {
		case ev := <-auditCh:
			events = append(events, ev)
		case <-deadline:
			t.Logf("timed out; collected %d event(s) so far", len(events))
			break collect
		}
	}

	cancel() // stop Run

	var allowCount, denyCount int
	for _, ev := range events {
		t.Logf("AuditEvent: decision=%s dest=%s reason=%s", ev.Decision, ev.DestHost, ev.Reason)
		switch ev.Decision {
		case perimeter.Allow:
			allowCount++
		case perimeter.Deny:
			denyCount++
		}
	}

	if allowCount == 0 {
		t.Error("no Allow AuditEvent received for allowed IP (10.0.1.100)")
	}
	if denyCount == 0 {
		t.Error("no Deny AuditEvent received for denied IP (10.0.2.200)")
	}

	select {
	case err := <-runErr:
		t.Logf("Run exited: %v", err)
	case <-time.After(3 * time.Second):
		t.Error("Run did not exit after context cancel within 3s")
	}
}
