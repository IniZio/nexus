// Package netstack implements perimeter.Perimeter using gvproxy's gvisor-based
// VirtualNetwork. It wires the netfilter AllowList into the TCP/UDP/ICMP
// forwarder callbacks and feeds DNS A answers into ObserveDNSAnswer so the
// allow list can resolve names to IPs before the first outbound SYN arrives.
//
// Each Run call creates its own isolated VirtualNetwork (one per sandbox).
// All sandboxes may share the same private CGNAT address range because the
// gvisor stacks are fully independent — the isolation is at the socketpair
// level, not the network level.
//
// Dependency direction: netstack imports virtualnetwork (gvproxy); it MUST NOT
// import internal/core/driver. The raw TAP fd arrives as io.ReadWriteCloser;
// netstack asserts its dynamic type to net.Conn (the driver always provides a
// net.Conn backed by an AF_UNIX SOCK_DGRAM socketpair).
package netstack

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
)

// Virtual network address constants for the per-sandbox gvisor stack.
// All sandboxes share these addresses because each Run call creates a
// fully isolated VirtualNetwork — there is no cross-sandbox routing.
const (
	netstackSubnet     = "192.168.127.0/24"
	netstackGatewayIP  = "192.168.127.1"
	netstackGatewayMAC = "5a:94:ef:e4:0c:dd"
	netstackDeviceIP   = "192.168.127.2"
	netstackMTU        = 1500
)

// Stack implements perimeter.Perimeter using gvproxy's VirtualNetwork.
// For every Run call it creates a new VirtualNetwork that:
//   - routes all guest TCP through AllowTCP (default-deny)
//   - routes all guest UDP through AllowUDP (default-deny)
//   - routes all guest ICMP through AllowICMP (default-deny)
//   - feeds every DNS A answer into ObserveDNSAnswer so the allow list can
//     attribute subsequent TCP SYNs to the hostname the guest resolved
//
// The onAudit callback (if non-nil) is called without any lock held for every
// egress decision — both allow and deny — from the goroutines that gvproxy
// spawns internally. Callers must not block inside onAudit.
//
// A Stack serves exactly one supervisor. SetDialer is last-writer-wins and
// must be called before Run; it is not safe to call concurrently with Run.
type Stack struct {
	al      *netfilter.AllowList
	onAudit func(perimeter.AuditEvent)
	dialer  func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Option configures a Stack at construction time.
type Option func(*Stack)

// WithDialer supplies a custom outbound dialer for the VirtualNetwork's TCP
// forwarder. When dialer is nil the original net.Dial("tcp", addr) behaviour
// is preserved exactly. The filter callback (if any) still runs before the
// dial.
func WithDialer(d func(ctx context.Context, network, addr string) (net.Conn, error)) Option {
	return func(s *Stack) { s.dialer = d }
}

// New creates a Stack that enforces policy via al.
// onAudit receives every egress decision (allow and deny). Pass nil to discard
// audit events. Optional Option values configure additional behaviour.
func New(al *netfilter.AllowList, onAudit func(perimeter.AuditEvent), opts ...Option) *Stack {
	s := &Stack{al: al, onAudit: onAudit}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SetDialer configures the outbound dialer for this Stack. It must be called
// before Run. Satisfies the unexported perimeter.dialerSetter interface so
// PerimeterSupervisor.Start can inject the per-supervisor dialer after the
// MITM listener address is known.
func (s *Stack) SetDialer(d func(ctx context.Context, network, addr string) (net.Conn, error)) {
	s.dialer = d
}

// Run implements perimeter.Perimeter.
//
// rw must satisfy net.Conn — the driver's GuestNetworkFD always returns a
// net.Conn backed by an AF_UNIX SOCK_DGRAM socketpair. Each Read on rw returns
// exactly one raw Ethernet frame (VfkitProtocol / IFF_NO_PI framing).
//
// Run blocks until ctx is cancelled or rw returns an error on read. It does
// not close rw on return; the caller owns that responsibility.
func (s *Stack) Run(ctx context.Context, id domain.SandboxID, rw io.ReadWriteCloser) error {
	conn, ok := rw.(net.Conn)
	if !ok {
		return fmt.Errorf("netstack: Run requires a net.Conn; got %T", rw)
	}

	conf := &types.Configuration{
		MTU:               netstackMTU,
		Subnet:            netstackSubnet,
		GatewayIP:         netstackGatewayIP,
		GatewayMacAddress: netstackGatewayMAC,
		DeviceIP:          netstackDeviceIP,
	}

	vnOpts := []virtualnetwork.Option{
		virtualnetwork.WithTCPFilter(s.makeFilter(id)),
		virtualnetwork.WithUDPFilter(s.makeFilter(id)),
		virtualnetwork.WithICMPFilter(s.makeICMPFilter(id)),
		virtualnetwork.WithDNSObserver(s.al.ObserveDNSAnswer),
	}
	if s.dialer != nil {
		vnOpts = append(vnOpts, virtualnetwork.WithDialer(s.dialer))
	}

	vn, err := virtualnetwork.New(conf, vnOpts...)
	if err != nil {
		return fmt.Errorf("netstack: create virtual network: %w", err)
	}

	// AcceptVfkit's rx loop checks ctx.Done() between reads but cannot
	// interrupt a blocked conn.Read. Watch for context cancellation and
	// set a past read deadline to unblock the rx goroutine immediately.
	// done is closed when Run returns (for any reason, not only ctx cancel),
	// so the watcher goroutine never leaks.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.SetReadDeadline(time.Now())
		case <-done:
		}
	}()

	return vn.AcceptVfkit(ctx, conn)
}

// makeFilter returns an AddressFilter for TCP and UDP flows.
// addr is "host:port" for TCP/UDP. The filter calls AllowList.Allow and emits
// an AuditEvent for both allow and deny outcomes.
func (s *Stack) makeFilter(id domain.SandboxID) func(addr string) error {
	return func(addr string) error {
		err := s.al.Allow(addr)
		s.emit(id, addr, err)
		return err
	}
}

// makeICMPFilter returns an AddressFilter for ICMP flows.
// addr is a bare destination IP (no port) for ICMP. The filter calls
// AllowList.AllowICMP and emits an AuditEvent.
func (s *Stack) makeICMPFilter(id domain.SandboxID) func(addr string) error {
	return func(addr string) error {
		err := s.al.AllowICMP(addr)
		s.emit(id, addr, err)
		return err
	}
}

// emit sends an AuditEvent to onAudit (if non-nil). err == nil → Allow;
// err != nil → Deny.
func (s *Stack) emit(id domain.SandboxID, dest string, err error) {
	if s.onAudit == nil {
		return
	}
	ev := perimeter.AuditEvent{
		Timestamp: time.Now(),
		SandboxID: id,
		DestHost:  dest,
	}
	if err == nil {
		ev.Decision = perimeter.Allow
		ev.Reason = "host allowed by policy"
	} else {
		ev.Decision = perimeter.Deny
		ev.Reason = err.Error()
	}
	s.onAudit(ev)
}

// Compile-time assertion: Stack satisfies perimeter.Perimeter.
var _ perimeter.Perimeter = (*Stack)(nil)
