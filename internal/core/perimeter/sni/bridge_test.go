package sni

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// connectProxy starts a stub HTTP CONNECT proxy on a random port. It accepts
// one connection, reads the CONNECT request, validates that the request URI and
// Host header equal wantTarget (e.g. "example.com:443"), sends an HTTP
// response with wantCode, and—if wantCode == 200 and splice is non-nil—calls
// splice with the accepted conn to handle the tunnel.
func connectProxy(t *testing.T, wantCode int, wantTarget string, splice func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("connectProxy Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed; test already done
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			t.Errorf("stub proxy: read CONNECT request: %v", err)
			return
		}

		if req.Method != "CONNECT" {
			t.Errorf("stub proxy: method = %q; want CONNECT", req.Method)
		}
		if req.RequestURI != wantTarget {
			t.Errorf("stub proxy: request URI = %q; want %q", req.RequestURI, wantTarget)
		}
		if req.Host != wantTarget {
			t.Errorf("stub proxy: Host header = %q; want %q", req.Host, wantTarget)
		}

		// Send CONNECT response with no buffering beyond the header.
		line := fmt.Sprintf("HTTP/1.1 %d %s\r\n\r\n", wantCode, http.StatusText(wantCode))
		if _, err := io.WriteString(conn, line); err != nil {
			return
		}

		if wantCode == 200 && splice != nil {
			splice(conn)
		}
	}()

	return ln.Addr().String()
}

// ── Acceptance 2 ────────────────────────────────────────────────────────────

// TestBridge_CONNECTLine verifies that Bridge sends a correctly formed CONNECT
// request: the target is host:443 and the Host header matches.
func TestBridge_CONNECTLine(t *testing.T) {
	const host = "example.com"
	const wantTarget = "example.com:443"

	// Echo-proxy: after the 200 OK, read anything sent and ignore it.
	proxyAddr := connectProxy(t, 200, wantTarget, func(conn net.Conn) {
		io.Copy(conn, conn) //nolint:errcheck // best-effort echo; test drives close
	})

	rawServer, rawClient := net.Pipe()

	done := make(chan error, 1)
	go func() {
		done <- Bridge(rawServer, host, proxyAddr)
	}()

	// Close the raw client; Bridge should return nil.
	rawClient.Close()
	if err := <-done; err != nil {
		t.Fatalf("Bridge: %v", err)
	}
}

// TestBridge_SpliceBidirectional verifies that data flows in both directions
// through the tunnel after a 200 CONNECT response.
func TestBridge_SpliceBidirectional(t *testing.T) {
	const host = "data.example.com"
	const wantTarget = "data.example.com:443"
	const toProxy = "hello from raw"
	const fromProxy = "hello from proxy"

	proxyAddr := connectProxy(t, 200, wantTarget, func(conn net.Conn) {
		// Read exactly what the raw side sends, then reply.
		buf := make([]byte, len(toProxy))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Errorf("proxy ReadFull: %v", err)
			return
		}
		if string(buf) != toProxy {
			t.Errorf("proxy received %q; want %q", buf, toProxy)
		}
		if _, err := io.WriteString(conn, fromProxy); err != nil {
			t.Errorf("proxy WriteString: %v", err)
		}
		// Closing conn here unblocks Bridge's splice.
	})

	rawServer, rawClient := net.Pipe()

	done := make(chan error, 1)
	go func() {
		done <- Bridge(rawServer, host, proxyAddr)
	}()

	// Send data as the guest would.
	if _, err := io.WriteString(rawClient, toProxy); err != nil {
		t.Fatalf("rawClient Write: %v", err)
	}

	// Receive the proxy's reply through Bridge.
	got := make([]byte, len(fromProxy))
	if _, err := io.ReadFull(rawClient, got); err != nil {
		t.Fatalf("rawClient ReadFull: %v", err)
	}
	if string(got) != fromProxy {
		t.Errorf("rawClient received %q; want %q", got, fromProxy)
	}

	rawClient.Close()
	if err := <-done; err != nil {
		t.Fatalf("Bridge: %v", err)
	}
}

// TestBridge_NonOK verifies that a non-200 CONNECT response causes Bridge to
// return an error that includes the numeric status code.
func TestBridge_NonOK(t *testing.T) {
	for _, code := range []int{403, 502, 503} {
		code := code
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			proxyAddr := connectProxy(t, code, "reject.example.com:443", nil)

			rawServer, rawClient := net.Pipe()
			t.Cleanup(func() { rawClient.Close() })

			err := Bridge(rawServer, "reject.example.com", proxyAddr)
			if err == nil {
				t.Fatalf("Bridge: expected error for %d response, got nil", code)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%d", code)) {
				t.Errorf("Bridge error %q does not mention status code %d", err, code)
			}
		})
	}
}

// TestBridge_DialFailure verifies that Bridge returns an error when proxyAddr
// is unreachable, without panicking.
func TestBridge_DialFailure(t *testing.T) {
	rawServer, rawClient := net.Pipe()
	t.Cleanup(func() { rawClient.Close() })

	// Port 1 is reserved/privileged and essentially never listening.
	err := Bridge(rawServer, "example.com", "127.0.0.1:1")
	if err == nil {
		t.Fatal("Bridge: expected dial error, got nil")
	}
}
