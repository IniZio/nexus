// Package netstack — unit tests for the Stack and its filter/audit machinery.
//
// These tests require NO privilege, NO kernel TUN/TAP devices, and NO running
// VM. They exercise the filter functions and AuditEvent emission directly,
// without starting a gvisor VirtualNetwork.
package netstack

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
)

// TestMain skips the entire package when running inside the nexus3 guest VM.
//
// The perimeter/netstack package imports gvisor-tap-vsock → gvisor.dev/gvisor,
// whose pkg/sync uses //go:linkname assembly to hook into runtime.sched at a
// hardcoded byte offset.  Running the resulting test binary inside the
// constrained nexus3 microVM causes the guest agent to lose its vsock
// connection (silent EOF, empty stdout/stderr), which aborts the dogfood suite.
//
// Detection: /proc/1/comm == "nexus3-agent" → we are init inside the guest.
// On any normal host (bare-metal, CI, container) /proc/1/comm is something
// else (systemd, docker-init, …) and tests run normally.
func TestMain(m *testing.M) {
	if proc1comm, err := os.ReadFile("/proc/1/comm"); err == nil {
		if strings.TrimSpace(string(proc1comm)) == "nexus3-agent" {
			fmt.Fprintln(os.Stderr, "netstack: skipping tests — running inside nexus3 guest VM (host-side package)")
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// collectAudit returns a slice-accumulating callback and a drain function.
func collectAudit() (func(perimeter.AuditEvent), func() []perimeter.AuditEvent) {
	var events []perimeter.AuditEvent
	fn := func(ev perimeter.AuditEvent) { events = append(events, ev) }
	drain := func() []perimeter.AuditEvent {
		out := events
		events = nil
		return out
	}
	return fn, drain
}

// TestNew_compilesAndNilOnAudit verifies Stack can be created and that a nil
// onAudit does not panic when the filter is called.
func TestNew_compilesAndNilOnAudit(t *testing.T) {
	al, err := netfilter.NewAllowList([]string{"1.2.3.4"}, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}
	s := New(al, nil) // nil onAudit — must not panic
	if s == nil {
		t.Fatal("New returned nil")
	}

	id := domain.NewSandboxID()
	f := s.makeFilter(id)
	// allowed IP — must not panic
	if err := f("1.2.3.4:80"); err != nil {
		t.Logf("allow call returned error (may depend on AllowList state): %v", err)
	}
	// denied IP — must not panic
	_ = f("9.9.9.9:80")
}

// TestMakeFilter_AllowedIP verifies that an address matching the AllowList
// produces an Allow AuditEvent and nil error.
func TestMakeFilter_AllowedIP(t *testing.T) {
	al, err := netfilter.NewAllowList([]string{"1.2.3.4"}, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}

	onAudit, drain := collectAudit()
	s := New(al, onAudit)
	id := domain.NewSandboxID()
	f := s.makeFilter(id)

	if err := f("1.2.3.4:80"); err != nil {
		t.Fatalf("makeFilter allowed IP returned error: %v", err)
	}

	events := drain()
	if len(events) != 1 {
		t.Fatalf("expected 1 AuditEvent, got %d", len(events))
	}
	ev := events[0]
	if ev.Decision != perimeter.Allow {
		t.Errorf("decision: got %v, want Allow", ev.Decision)
	}
	if ev.SandboxID != id {
		t.Errorf("sandbox ID mismatch")
	}
	if ev.DestHost != "1.2.3.4:80" {
		t.Errorf("dest host: got %q, want %q", ev.DestHost, "1.2.3.4:80")
	}
	if ev.Timestamp.IsZero() {
		t.Error("timestamp is zero")
	}
}

// TestMakeFilter_DeniedIP verifies that an address NOT in the AllowList
// produces a Deny AuditEvent and non-nil error (default-deny).
func TestMakeFilter_DeniedIP(t *testing.T) {
	al, err := netfilter.NewAllowList([]string{"1.2.3.4"}, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}

	onAudit, drain := collectAudit()
	s := New(al, onAudit)
	id := domain.NewSandboxID()
	f := s.makeFilter(id)

	// 9.9.9.9 is NOT in the allowlist → deny.
	filterErr := f("9.9.9.9:80")
	if filterErr == nil {
		t.Fatal("makeFilter denied IP returned nil error (expected deny)")
	}

	events := drain()
	if len(events) != 1 {
		t.Fatalf("expected 1 AuditEvent, got %d", len(events))
	}
	ev := events[0]
	if ev.Decision != perimeter.Deny {
		t.Errorf("decision: got %v, want Deny", ev.Decision)
	}
	if ev.DestHost != "9.9.9.9:80" {
		t.Errorf("dest host: got %q, want %q", ev.DestHost, "9.9.9.9:80")
	}
	if ev.Reason == "" {
		t.Error("deny reason must not be empty")
	}
}

// TestMakeFilter_AllowAndDenyBothProduceAuditEvent is the primary criterion-4
// test: both an allow AND a deny through the same filter emit one AuditEvent
// each, with correct Decision fields.
func TestMakeFilter_AllowAndDenyBothProduceAuditEvent(t *testing.T) {
	al, err := netfilter.NewAllowList([]string{"10.0.0.1"}, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}

	var events []perimeter.AuditEvent
	s := New(al, func(ev perimeter.AuditEvent) { events = append(events, ev) })
	id := domain.NewSandboxID()
	f := s.makeFilter(id)

	// Allowed attempt.
	_ = f("10.0.0.1:443")
	// Denied attempt.
	_ = f("8.8.8.8:53")

	if len(events) != 2 {
		t.Fatalf("expected 2 AuditEvents (1 allow + 1 deny), got %d", len(events))
	}

	allow := events[0]
	deny := events[1]

	if allow.Decision != perimeter.Allow {
		t.Errorf("first event: got %v, want Allow", allow.Decision)
	}
	if deny.Decision != perimeter.Deny {
		t.Errorf("second event: got %v, want Deny", deny.Decision)
	}
}

// TestMakeICMPFilter_AllowAndDeny verifies the ICMP filter path (bare IP,
// no port) also produces AuditEvents for both allow and deny.
func TestMakeICMPFilter_AllowAndDeny(t *testing.T) {
	al, err := netfilter.NewAllowList([]string{"192.0.2.1"}, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}

	var events []perimeter.AuditEvent
	s := New(al, func(ev perimeter.AuditEvent) { events = append(events, ev) })
	id := domain.NewSandboxID()
	f := s.makeICMPFilter(id)

	// Allowed ICMP ping.
	_ = f("192.0.2.1")
	// Denied ICMP ping.
	_ = f("198.51.100.1")

	if len(events) != 2 {
		t.Fatalf("expected 2 AuditEvents, got %d", len(events))
	}
	if events[0].Decision != perimeter.Allow {
		t.Errorf("ICMP allow: got %v, want Allow", events[0].Decision)
	}
	if events[1].Decision != perimeter.Deny {
		t.Errorf("ICMP deny: got %v, want Deny", events[1].Decision)
	}
}

// nopRWC is a trivial io.ReadWriteCloser that does NOT satisfy net.Conn.
type nopRWC struct{}

func (n *nopRWC) Read(p []byte) (int, error)  { return 0, io.EOF }
func (n *nopRWC) Write(p []byte) (int, error) { return len(p), nil }
func (n *nopRWC) Close() error                { return nil }

// TestRun_RequiresNetConn verifies that Run returns a descriptive error when
// rw does not satisfy net.Conn.
func TestRun_RequiresNetConn(t *testing.T) {
	al, err := netfilter.NewAllowList(nil, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}
	s := New(al, nil)
	id := domain.NewSandboxID()

	err = s.Run(context.Background(), id, &nopRWC{})
	if err == nil {
		t.Fatal("Run with non-net.Conn rw: expected error, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestEmit_DecisionsAndReason verifies the emit helper's field population.
func TestEmit_DecisionsAndReason(t *testing.T) {
	al, _ := netfilter.NewAllowList(nil, nil, nil)
	var events []perimeter.AuditEvent
	s := New(al, func(ev perimeter.AuditEvent) { events = append(events, ev) })
	id := domain.NewSandboxID()

	// Allow (nil error).
	s.emit(id, "1.2.3.4:80", nil)
	// Deny (non-nil error).
	s.emit(id, "5.6.7.8:443", fmt.Errorf("not in allow list"))

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Decision != perimeter.Allow || events[0].Reason == "" {
		t.Errorf("allow event: decision=%v reason=%q", events[0].Decision, events[0].Reason)
	}
	if events[1].Decision != perimeter.Deny || events[1].Reason != "not in allow list" {
		t.Errorf("deny event: decision=%v reason=%q", events[1].Decision, events[1].Reason)
	}
}

// TestObserveDNSAnswer_WiredToAllowList verifies that the DNS observer is
// correctly wired: after ObserveDNSAnswer registers an IP for an allowed name,
// a subsequent filter call for that IP is permitted (observed set).
func TestObserveDNSAnswer_WiredToAllowList(t *testing.T) {
	al, err := netfilter.NewAllowList(nil, nil, []string{"example.com"})
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}
	// Start the AllowList background goroutine (needed for observed set).
	al.Start(30 * time.Second)
	defer al.Stop()

	// Register a DNS answer: example.com → 93.184.216.34
	al.ObserveDNSAnswer("example.com", net.ParseIP("93.184.216.34"))

	var events []perimeter.AuditEvent
	s := New(al, func(ev perimeter.AuditEvent) { events = append(events, ev) })
	id := domain.NewSandboxID()
	f := s.makeFilter(id)

	// 93.184.216.34 was observed as an A answer for allowed "example.com".
	if err := f("93.184.216.34:443"); err != nil {
		t.Errorf("filter rejected observed DNS IP: %v", err)
	}
	if len(events) == 0 || events[0].Decision != perimeter.Allow {
		t.Errorf("expected Allow AuditEvent for observed DNS IP, got %v", events)
	}
}

