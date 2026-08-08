package perimeter

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
)

// TestWire_EgressPath is the host-side integration test that verifies all three
// egress cases through the dialer wire without CAP_NET_ADMIN or a live guest VM.
// It exercises buildDialer (the unexported supervisor helper) directly.
//
// Case (a) allowlisted host on :443 — placeholder bearer is swapped; stub
//
//	upstream receives the real token, never the placeholder.
//
// Case (b) non-allowlisted host on :443 — CONNECT is rejected by mitm (403);
//
//	no traffic reaches the stub upstream.
//
// Note: in the real path the AllowList drops non-allowed destinations before
// the dialer runs. Case (b) tests the second gate (mitm HandleConnect → 403)
// and verifies that no credential leaks when that gate fires.
//
// Case (c) non-443 flow — bypasses the shim/mitm entirely; the returned
//
//	connection's network is "tcp", not "pipe".
func TestWire_EgressPath(t *testing.T) {
	const allowedHost = "api.example.com"
	const deniedHost = "evil.example.com"
	const realToken = "real-secret-token-xyz"

	id := domain.SandboxID{}
	id[0] = 0xAB // deterministic test sandbox ID

	broker := cred.NewBroker()
	rec, err := broker.RegisterPlaceholder(id, allowedHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// authCh receives the Authorization header captured by the stub upstream.
	authCh := make(chan string, 4)

	// stub is the upstream HTTPS server. The mitm proxy forwards decrypted
	// requests here after bearer-token swap. Must be TLS because goproxy issues
	// an HTTPS round-trip to the upstream after MITM TLS termination.
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case authCh <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(stub.Close)

	stubTLSConfig := stub.Client().Transport.(*http.Transport).TLSClientConfig

	// stubTransport redirects all mitm outbound connections to stub.
	stubTransport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, stub.Listener.Addr().String())
		},
		TLSClientConfig: stubTLSConfig,
	}

	proxy, err := mitm.New(mitm.Config{
		SandboxID:    id,
		AllowedHosts: []string{allowedHost},
		Broker:       broker,
		Transport:    stubTransport,
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}

	mitmSrv := httptest.NewServer(proxy)
	t.Cleanup(mitmSrv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dialer := buildDialer(ctx, mitmSrv.Listener.Addr().String())

	// ── Case (a): allowlisted host on :443 ────────────────────────────────────
	t.Run("(a)_allowlisted_443_bearer_swapped", func(t *testing.T) {
		conn, err := dialer(context.Background(), "tcp", "1.2.3.4:443")
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { conn.Close() })

		// tls.Client trusts the mitm's CA-minted leaf cert for allowedHost.
		pool := x509.NewCertPool()
		pool.AddCert(proxy.CACert())
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: allowedHost,
			RootCAs:    pool,
		})
		t.Cleanup(func() { tlsConn.Close() })
		tlsConn.SetDeadline(time.Now().Add(5 * time.Second))

		// Write HTTP/1.1 request with the placeholder bearer (triggers TLS handshake).
		reqStr := fmt.Sprintf(
			"GET / HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n",
			allowedHost, rec.Placeholder,
		)
		if _, err := io.WriteString(tlsConn, reqStr); err != nil {
			t.Fatalf("write request: %v", err)
		}

		// Drain the response so the proxy finishes forwarding.
		io.Copy(io.Discard, bufio.NewReader(tlsConn)) //nolint:errcheck

		// Stub upstream must have received the real token, not the placeholder.
		select {
		case got := <-authCh:
			want := "Bearer " + realToken
			if got != want {
				t.Errorf("upstream Authorization = %q, want %q", got, want)
			}
			if got == "Bearer "+rec.Placeholder {
				t.Error("placeholder token leaked to upstream — credential NOT swapped")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for stub upstream to receive request")
		}
	})

	// ── Case (b): non-allowlisted host on :443 ────────────────────────────────
	t.Run("(b)_denied_443_CONNECT_rejected", func(t *testing.T) {
		conn, err := dialer(context.Background(), "tcp", "2.3.4.5:443")
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { conn.Close() })

		// Send TLS ClientHello with SNI = deniedHost so ParseSNI can extract the
		// host. The handshake is expected to fail (CONNECT rejected → 403 → conn
		// closed before ServerHello).
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         deniedHost,
			InsecureSkipVerify: true, // we expect failure before cert verification
		})
		t.Cleanup(func() { tlsConn.Close() })
		tlsConn.SetDeadline(time.Now().Add(3 * time.Second))

		err = tlsConn.Handshake()
		if err == nil {
			t.Error("expected Handshake error (CONNECT rejected), got nil")
		}

		// No traffic should have reached the stub upstream.
		select {
		case got := <-authCh:
			t.Errorf("stub upstream received unexpected request with auth=%q (token leaked through rejected CONNECT)", got)
		default:
			// Correct: nothing reached upstream.
		}
	})

	// ── Case (c): non-443 flow bypasses shim/mitm ─────────────────────────────
	t.Run("(c)_non443_bypasses_shim", func(t *testing.T) {
		// Start a plain TCP echo target to serve as the direct destination.
		target, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { target.Close() })

		serverConnCh := make(chan net.Conn, 1)
		go func() {
			c, _ := target.Accept()
			serverConnCh <- c
		}()

		// Port 80 — not 443 — must bypass the shim entirely.
		conn, err := dialer(context.Background(), "tcp", target.Addr().String())
		if err != nil {
			t.Fatalf("dial non-443: %v", err)
		}
		t.Cleanup(func() { conn.Close() })

		// A net.Pipe end reports network "pipe"; a real TCP conn reports "tcp".
		if conn.RemoteAddr().Network() == "pipe" {
			t.Errorf("non-443 connection went through the shim pipe: RemoteAddr.Network()=%q", conn.RemoteAddr().Network())
		}

		// Verify the connection is genuinely end-to-end (not silently dropped).
		serverConn, ok := func() (net.Conn, bool) {
			select {
			case c := <-serverConnCh:
				return c, true
			case <-time.After(2 * time.Second):
				return nil, false
			}
		}()
		if !ok {
			t.Fatal("target server did not accept a connection within 2s")
		}
		t.Cleanup(func() { serverConn.Close() })

		const payload = "hello-wire"
		if _, err := io.WriteString(conn, payload); err != nil {
			t.Fatalf("write payload: %v", err)
		}
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			t.Fatalf("read payload on server side: %v", err)
		}
		if string(buf) != payload {
			t.Errorf("server received %q, want %q", buf, payload)
		}
	})
}
