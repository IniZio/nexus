package netfilter

import (
	"net"
	"testing"
	"time"
)

// TestOnEgress_DenyEmitsReason verifies that a deny verdict calls OnEgress
// with a populated Reason field (T4-AC3). Production wires al.OnEgress
// directly in service.go; this test does the same to exercise the real path.
func TestOnEgress_DenyEmitsReason(t *testing.T) {
	al, err := NewAllowList(nil, nil, nil) // deny-all: nothing allowed
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}
	al.Start(time.Hour) // kick background goroutine
	defer al.Stop()

	type egressCall struct{ host, verdict, reason string }
	var calls []egressCall
	al.OnEgress = func(host, verdict, reason string, _ time.Time) {
		calls = append(calls, egressCall{host, verdict, reason})
	}

	// Simulate a guest DNS observation so we get a name in the event.
	al.ObserveDNSAnswer("blocked.example.com", net.ParseIP("1.2.3.4"))

	// Allow() always fails (deny-all policy); this exercises the deny path.
	_ = al.Allow("1.2.3.4:443")

	if len(calls) == 0 {
		t.Fatal("OnEgress not called for denied connection")
	}
	got := calls[0]
	if got.verdict != "deny" {
		t.Errorf("verdict = %q, want %q", got.verdict, "deny")
	}
	if got.reason == "" {
		t.Errorf("reason must not be empty for a deny event")
	}
	if got.host == "" {
		t.Errorf("host must not be empty")
	}
}

// TestOnEgress_AllowEmitsRecord verifies that an allow verdict also calls
// OnEgress (T4-AC2: the follow stream includes netfilter verdicts).
func TestOnEgress_AllowEmitsRecord(t *testing.T) {
	al, err := NewAllowList(nil, nil, []string{"allowed.example.com"})
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}
	al.Start(time.Hour)
	defer al.Stop()

	type egressCall struct{ host, verdict, reason string }
	var calls []egressCall
	al.OnEgress = func(host, verdict, reason string, _ time.Time) {
		calls = append(calls, egressCall{host, verdict, reason})
	}

	// Observe a DNS answer so the IP is associated with an allowed domain.
	al.ObserveDNSAnswer("allowed.example.com", net.ParseIP("5.6.7.8"))

	// Allow() should succeed (domain in policy, IP observed).
	if err := al.Allow("5.6.7.8:443"); err != nil {
		t.Fatalf("Allow unexpectedly denied: %v", err)
	}

	if len(calls) == 0 {
		t.Fatal("OnEgress not called for allowed connection")
	}
	got := calls[0]
	if got.verdict != "allow" {
		t.Errorf("verdict = %q, want %q", got.verdict, "allow")
	}
	if got.reason == "" {
		t.Errorf("reason must not be empty for an allow event")
	}
}
