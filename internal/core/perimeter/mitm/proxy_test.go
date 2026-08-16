package mitm_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
)

// newSandboxID returns a deterministic SandboxID for tests.
func newSandboxID(b byte) domain.SandboxID {
	var id domain.SandboxID
	id[0] = b
	return id
}

// newTestProxy creates a Proxy with a custom transport that redirects ALL
// outbound TCP connections to upstreamAddr. This lets plain-HTTP integration
// tests use arbitrary host names without DNS.
func newTestProxy(t *testing.T, cfg mitm.Config, upstreamAddr string) *httptest.Server {
	t.Helper()
	cfg.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upstreamAddr)
		},
	}
	p, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	return httptest.NewServer(p)
}

// proxyClient returns an *http.Client that routes all requests through
// the given proxy URL.
func proxyClient(proxyURL string) *http.Client {
	u, _ := url.Parse(proxyURL)
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(u),
		},
		Timeout: 5 * time.Second,
	}
}

// captureAuthUpstream returns a test HTTP server that writes the
// Authorization header it received to the returned channel.
func captureAuthUpstream(t *testing.T) (*httptest.Server, <-chan string) {
	t.Helper()
	ch := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

// receiveWithTimeout reads from ch or fails the test after 3 s.
func receiveWithTimeout(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for upstream to receive request")
		return ""
	}
}

// TestProxy_SwapOnAllow verifies that a Bearer token containing a registered
// placeholder is replaced with the real token when the request targets an
// allowlisted host.
func TestProxy_SwapOnAllow(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(1)
	const allowedHost = "api.allowed.example"
	const realToken = "real-secret-token"

	rec, err := broker.RegisterPlaceholder(sid, allowedHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{allowedHost},
		Broker:       broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)

	req, _ := http.NewRequest(http.MethodGet, "http://"+allowedHost+"/api", nil)
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	if want := "Bearer " + realToken; got != want {
		t.Errorf("upstream Authorization = %q, want %q", got, want)
	}
}

// TestProxy_NoSwapOnDeny verifies that a Bearer token is NOT replaced when
// the request targets a host that is NOT in the allowlist. The placeholder
// reaches the upstream unchanged, which is effectively useless — the real
// token stays on the host side.
func TestProxy_NoSwapOnDeny(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(2)
	const allowedHost = "api.allowed.example"
	const deniedHost = "evil.example"
	const realToken = "real-secret-token"

	// Register a placeholder for the allowed host (not used in this request).
	_, err := broker.RegisterPlaceholder(sid, allowedHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// Also register a placeholder for the denied host so we have a valid
	// placeholder string; the proxy must still refuse to swap it.
	recDenied, err := broker.RegisterPlaceholder(sid, deniedHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder (denied): %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{allowedHost}, // deniedHost is intentionally absent
		Broker:       broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)

	req, _ := http.NewRequest(http.MethodGet, "http://"+deniedHost+"/api", nil)
	req.Header.Set("Authorization", "Bearer "+recDenied.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	// The upstream must see the original placeholder — not the real token.
	if got == "Bearer "+realToken {
		t.Error("upstream received the real token for a non-allowlisted host; credential leak detected")
	}
	if want := "Bearer " + recDenied.Placeholder; got != want {
		t.Errorf("upstream Authorization = %q, want placeholder %q", got, want)
	}
}

// TestProxy_CACertIsNotNil verifies that the per-sandbox CA is generated and
// accessible for guest trust-store seeding.
func TestProxy_CACertIsNotNil(t *testing.T) {
	t.Parallel()

	broker := cred.NewBroker()
	p, err := mitm.New(mitm.Config{
		SandboxID:    newSandboxID(3),
		AllowedHosts: []string{"api.example.com"},
		Broker:       broker,
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	if p.CACert() == nil {
		t.Fatal("CACert() returned nil; guest has no trust anchor to import")
	}
	if !p.CACert().IsCA {
		t.Error("CACert() is not a CA certificate")
	}
}

// TestProxy_SwapAfterSetRealToken_HTTPS is the D-P4-02 empirical confirmation.
// It exercises the full HTTPS CONNECT-MITM path with the empty-then-SetRealToken
// broker pattern — the same sequence the agent sandbox uses at runtime.
//
// Specifically it verifies:
//  1. RegisterPlaceholder(sid, host, "") mints a placeholder with no real token.
//  2. SetRealToken(sid, host, realToken) fills the real token without changing
//     the placeholder.
//  3. A client sending HTTPS CONNECT through the proxy with Bearer <placeholder>
//     has its Authorization header rewritten to Bearer <realToken> before the
//     request reaches the upstream server.
//
// The upstream is a real TLS server; the client trusts the proxy's per-sandbox
// CA cert (not the upstream's self-signed cert). The proxy transport redirects
// all outbound TCP to the TLS upstream and skips hostname verification so the
// test works without a matching certificate for the allowlisted hostname.
func TestProxy_SwapAfterSetRealToken_HTTPS(t *testing.T) {
	t.Parallel()

	// TLS upstream: captures the Authorization header the proxy forwarded.
	authCh := make(chan string, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCh <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	broker := cred.NewBroker()
	sid := newSandboxID(20)
	const allowedHost = "api.anthropic.com"
	const realToken = "real-claude-oauth-token"

	// Build the MITM proxy.  The transport redirects all outbound TCP to the TLS
	// upstream and skips hostname verification (upstream cert is for 127.0.0.1,
	// not allowedHost).
	cfg := mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{allowedHost},
		Broker:       broker,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only redirect
		},
	}
	p, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	proxyServer := httptest.NewServer(p)
	t.Cleanup(proxyServer.Close)

	// D-P4-02 pattern: register with empty real token, then fill it.
	rec, err := broker.RegisterPlaceholder(sid, allowedHost, "")
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	if err := broker.SetRealToken(sid, allowedHost, realToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	// Client: trusts the proxy CA for MITM leaf certs; routes through the proxy.
	proxyURL, _ := url.Parse(proxyServer.URL)
	pool := x509.NewCertPool()
	pool.AddCert(p.CACert())
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 10 * time.Second,
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://"+allowedHost+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do (HTTPS through MITM proxy): %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	if want := "Bearer " + realToken; got != want {
		t.Errorf("upstream Authorization = %q, want %q (placeholder swap did not fire over HTTPS CONNECT)", got, want)
	}
}

// TestProxy_AllowAll verifies the AllowAll policy:
//   - AllowAll: true tunnels a CONNECT to a non-listed host (real cert, no MITM).
//   - AllowAll: false rejects that CONNECT.
func TestProxy_AllowAll(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	const host = "notlisted.example"

	makeCfg := func(allowAll bool) mitm.Config {
		return mitm.Config{
			SandboxID:    newSandboxID(30),
			AllowedHosts: []string{},
			Broker:       nil,
			AllowAll:     allowAll,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
				},
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only redirect
			},
		}
	}

	t.Run("allow-all tunnels non-listed host", func(t *testing.T) {
		t.Parallel()
		p, err := mitm.New(makeCfg(true))
		if err != nil {
			t.Fatalf("mitm.New: %v", err)
		}
		proxyServer := httptest.NewServer(p)
		t.Cleanup(proxyServer.Close)

		proxyURL, _ := url.Parse(proxyServer.URL)
		client := &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // tunneled real cert
			},
			Timeout: 5 * time.Second,
		}

		resp, err := client.Get("https://" + host + "/ping")
		if err != nil {
			t.Fatalf("AllowAll=true: CONNECT to non-listed host should tunnel: %v", err)
		}
		resp.Body.Close()
	})

	t.Run("default rejects non-listed host", func(t *testing.T) {
		t.Parallel()
		p, err := mitm.New(makeCfg(false))
		if err != nil {
			t.Fatalf("mitm.New: %v", err)
		}
		proxyServer := httptest.NewServer(p)
		t.Cleanup(proxyServer.Close)

		proxyURL, _ := url.Parse(proxyServer.URL)
		pool := x509.NewCertPool()
		pool.AddCert(p.CACert())
		client := &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{RootCAs: pool},
			},
			Timeout: 5 * time.Second,
		}

		_, err = client.Get("https://" + host + "/ping")
		if err == nil {
			t.Fatal("AllowAll=false: expected CONNECT to non-listed host to be rejected, but it succeeded")
		}
	})
}

// TestProxy_CrossSandboxPlaceholderNotSwapped verifies that a placeholder
// belonging to sandbox A is not swapped when the proxy is serving sandbox B,
// even if the host is allowlisted. This exercises the ResolveScoped defence.
func TestProxy_CrossSandboxPlaceholderNotSwapped(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sidA := newSandboxID(10)
	sidB := newSandboxID(11)
	const host = "api.allowed.example"
	const realToken = "real-secret-for-A"

	// Register a placeholder for sandbox A.
	recA, err := broker.RegisterPlaceholder(sidA, host, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// Create proxy serving sandbox B with the same allowlist.
	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sidB, // NOT sidA
		AllowedHosts: []string{host},
		Broker:       broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)

	// Send sandbox A's placeholder through sandbox B's proxy.
	req, _ := http.NewRequest(http.MethodGet, "http://"+host+"/api", nil)
	req.Header.Set("Authorization", "Bearer "+recA.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	if got == "Bearer "+realToken {
		t.Error("cross-sandbox placeholder was swapped; token leak across sandbox boundary detected")
	}
}

// TestProxy_SwapBasicOnAllow verifies git-over-HTTPS Authorization: Basic
// (D-PD-23). Git sends base64(user:password); the password field is the
// placeholder and must be replaced, then the header re-encoded.
func TestProxy_SwapBasicOnAllow(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(3)
	const allowedHost = "github.com"
	const realToken = "ghp_real_token"
	rec, err := broker.RegisterPlaceholder(sid, allowedHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{allowedHost},
		Broker:       broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)
	req, _ := http.NewRequest(http.MethodGet, "http://"+allowedHost+"/git-upload-pack", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+rec.Placeholder)))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+realToken))
	if got != want {
		t.Errorf("upstream Authorization = %q, want %q", got, want)
	}
}

// TestProxy_NoSwapBasicOnDeny verifies Basic is not rewritten off-allowlist.
func TestProxy_NoSwapBasicOnDeny(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(4)
	const allowedHost = "api.allowed.example"
	const deniedHost = "evil.example"
	const realToken = "should-not-leak"
	rec, err := broker.RegisterPlaceholder(sid, allowedHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{allowedHost},
		Broker:       broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	placeholderBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+rec.Placeholder))
	client := proxyClient(proxyServer.URL)
	req, _ := http.NewRequest(http.MethodGet, "http://"+deniedHost+"/git-upload-pack", nil)
	req.Header.Set("Authorization", placeholderBasic)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	if got != placeholderBasic {
		t.Errorf("denied-host Authorization = %q, want placeholder unchanged %q", got, placeholderBasic)
	}
}

// TestProxy_AllowAll_SecretHostSwapsGitHub proves D-PD-25: AllowAll + SecretHosts
// MITMs github.com (swap fires) without putting it on AllowedHosts.
func TestProxy_AllowAll_SecretHostSwapsGitHub(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(25)
	const secretHost = "github.com"
	const realToken = "ghs_allowall_swap"
	rec, err := broker.RegisterPlaceholder(sid, secretHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:   sid,
		SecretHosts: []string{secretHost},
		Broker:      broker,
		AllowAll:    true,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)
	req, _ := http.NewRequest(http.MethodGet, "http://"+secretHost+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	if want := "Bearer " + realToken; got != want {
		t.Errorf("upstream Authorization = %q, want %q", got, want)
	}
}
