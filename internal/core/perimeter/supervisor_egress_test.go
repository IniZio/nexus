package perimeter

// T5-AC1 / T5-AC2: AllowEgress mutates both MITM and netfilter layers; empty
// host or nil-proxy modes are handled correctly.

import (
	"bufio"
	"net"
	"net/http"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
)

// newTestProxy creates a minimal mitm.Proxy with no initially allowed hosts.
func newTestProxy(t *testing.T) *mitm.Proxy {
	t.Helper()
	p, err := mitm.New(mitm.Config{AllowedHosts: nil})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	return p
}

// newTestAllowList creates a netfilter.AllowList with no initially allowed domains.
func newTestAllowList(t *testing.T) *netfilter.AllowList {
	t.Helper()
	al, err := netfilter.NewAllowList(nil, nil, nil)
	if err != nil {
		t.Fatalf("netfilter.NewAllowList: %v", err)
	}
	return al
}

// connectStatus sends a CONNECT request to proxyAddr for host:443 and returns
// the HTTP status code from the proxy's response line.
func connectStatus(t *testing.T, proxyAddr, host string) int {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy %s: %v", proxyAddr, err)
	}
	defer conn.Close()

	req, _ := http.NewRequest(http.MethodConnect, "https://"+host+":443", nil)
	req.Host = host + ":443"
	_ = req.Write(conn)

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		// A 403 response may close the connection abruptly; treat read error as 403.
		return http.StatusForbidden
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestAllowEgress_MutatesBothLayers verifies that AllowEgress with a real proxy
// and a real AllowList updates both layers.
//
//   - MITM layer: a previously-denied CONNECT is accepted after AllowEgress.
//   - Netfilter layer: Allow returns nil for the host's IP after AddDomain +
//     ObserveDNS.
//
// T5-AC1
func TestAllowEgress_MutatesBothLayers(t *testing.T) {
	const testHost = "api.example.com"
	const testIP = "203.0.113.1" // RFC 5737 TEST-NET-3, non-routable

	proxy := newTestProxy(t)
	al := newTestAllowList(t)
	// Do NOT call al.Start — background refresh is not needed for this test and
	// time.NewTicker(0) panics. DNS resolution is simulated via ObserveDNS.

	sup := &PerimeterSupervisor{proxy: proxy, al: al}

	// Serve the proxy on a local listener so we can send CONNECT requests.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: proxy}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	proxyAddr := ln.Addr().String()

	// Before AllowEgress: CONNECT to testHost should be denied (403).
	if got := connectStatus(t, proxyAddr, testHost); got != http.StatusForbidden {
		t.Errorf("before AllowEgress: CONNECT status = %d, want %d (forbidden)", got, http.StatusForbidden)
	}

	// T5-AC1: AllowEgress must succeed.
	if err := sup.AllowEgress(testHost); err != nil {
		t.Fatalf("AllowEgress: %v", err)
	}

	// MITM layer: CONNECT should now be accepted (200 Connection established).
	if got := connectStatus(t, proxyAddr, testHost); got != http.StatusOK {
		t.Errorf("after AllowEgress: CONNECT status = %d, want %d (OK)", got, http.StatusOK)
	}

	// Netfilter layer: simulate the guest observing a DNS answer for testHost,
	// then verify Allow passes. Without AddDomain, DecideName(testHost) would
	// deny and Allow("203.0.113.1:443") would return a non-nil error.
	al.ObserveDNS(testHost, net.ParseIP(testIP))
	if err := al.Allow(testIP + ":443"); err != nil {
		t.Errorf("netfilter layer: expected Allow to pass after AddDomain+ObserveDNS, got: %v", err)
	}
}

// TestAllowEgress_EmptyHost is rejected before mutating either layer (T5-AC2).
func TestAllowEgress_EmptyHost(t *testing.T) {
	proxy := newTestProxy(t)
	al := newTestAllowList(t)
	sup := &PerimeterSupervisor{proxy: proxy, al: al}

	if err := sup.AllowEgress(""); err == nil {
		t.Fatal("expected error for empty host, got nil")
	}

	// Verify neither layer was mutated: AllowList still denies any observed host.
	const anyHost = "untouched.example.com"
	al.ObserveDNS(anyHost, net.ParseIP("203.0.113.2"))
	if err := al.Allow("203.0.113.2:443"); err == nil {
		t.Error("netfilter layer: was mutated despite empty host — should not be")
	}
}

// TestAllowEgress_NilProxy_AllowAllMode verifies AllowEgress skips the MITM
// layer when proxy is nil (AllowAll mode) and still updates the netfilter layer.
// T5-AC1 (AllowAll branch)
func TestAllowEgress_NilProxy_AllowAllMode(t *testing.T) {
	const testHost = "packages.example.com"
	al := newTestAllowList(t)
	sup := &PerimeterSupervisor{proxy: nil, al: al}

	if err := sup.AllowEgress(testHost); err != nil {
		t.Fatalf("AllowEgress (AllowAll mode): unexpected error: %v", err)
	}

	// Netfilter layer should accept the host.
	al.ObserveDNS(testHost, net.ParseIP("198.51.100.5"))
	if err := al.Allow("198.51.100.5:443"); err != nil {
		t.Errorf("netfilter layer (AllowAll): expected Allow to pass after AddDomain, got: %v", err)
	}
}

// TestAllowEgress_NilBothLayers returns a clear error when no perimeter is
// configured (neither proxy nor AllowList present).
func TestAllowEgress_NilBothLayers(t *testing.T) {
	sup := &PerimeterSupervisor{proxy: nil, al: nil}
	if err := sup.AllowEgress("api.example.com"); err == nil {
		t.Fatal("expected error when both proxy and al are nil, got nil")
	}
}
