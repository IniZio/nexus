package mitm_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

// TestProxy_CrossHostPlaceholderNotSwapped verifies the host-boundary check:
// a placeholder registered for hostA is NOT swapped when the request targets
// hostB, even though both hosts are in the allowlist and the sandbox matches.
// This is the regression test for the cross-credential exfiltration gap closed
// by the host parameter added to ResolveScoped.
//
// Mutation evidence: removing the e.sc.host != host check from ResolveScoped
// causes this test to fail (the swap fires when it must not).
func TestProxy_CrossHostPlaceholderNotSwapped(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(12)
	const hostA = "mcp.linear.app"
	const hostB = "app.glitchtip.com"
	const realTokenA = "linear-real-token"

	// Register a placeholder scoped to hostA only.
	recA, err := broker.RegisterPlaceholder(sid, hostA, realTokenA)
	if err != nil {
		t.Fatalf("RegisterPlaceholder hostA: %v", err)
	}

	// Both hosts are allowlisted so the swap handler is reached for hostB.
	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{hostA, hostB},
		Broker:       broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)

	// Send hostA's placeholder in a request directed at hostB.
	req, _ := http.NewRequest(http.MethodGet, "http://"+hostB+"/api", nil)
	req.Header.Set("Authorization", "Bearer "+recA.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	// The real token must NOT appear in the forwarded header.
	if got == "Bearer "+realTokenA {
		t.Error("cross-host placeholder was swapped; token exfiltration gap detected — host-boundary check not enforced")
	}
	// The placeholder itself should pass through unchanged (no swap = forward as-is).
	if want := "Bearer " + recA.Placeholder; got != want {
		t.Logf("note: upstream received %q (not the real token, gap is closed)", got)
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

// ============================================================
// "token " Authorization scheme tests
//
// The `gh` CLI and some git helpers send `Authorization: token <TOKEN>` rather
// than `Bearer` or `Basic`. swapAuthorization handles this scheme at the same
// priority as Bearer. The tests below prove that the swap fires (or does not
// fire) correctly — the scheme is the real injection path for `gh auth token`
// credentials.
//
// Mutation evidence for each swap point is documented inline.
// ============================================================

// TestProxy_SwapTokenSchemeOnAllow verifies that `Authorization: token
// <placeholder>` is rewritten to `token <realToken>` when the request
// targets an allowlisted host.
//
// Mutation evidence: remove the `case strings.HasPrefix(authHeader, "token "):`
// block from swapAuthorization → upstream receives the placeholder unchanged
// and this test fails. Restore → passes.
func TestProxy_SwapTokenSchemeOnAllow(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(50)
	const allowedHost = "api.github.com"
	const realToken = "ghp_real_token_for_token_scheme"
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
	req, _ := http.NewRequest(http.MethodGet, "http://"+allowedHost+"/user", nil)
	req.Header.Set("Authorization", "token "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	if want := "token " + realToken; got != want {
		t.Errorf("upstream Authorization = %q, want %q", got, want)
	}
}

// TestProxy_NoSwapTokenSchemeOnDeny verifies that `Authorization: token
// <placeholder>` is NOT rewritten when the request targets a host that is not
// in the allowlist. The placeholder value reaches upstream unchanged; the real
// token never leaves the host.
//
// Mutation evidence: broaden AllowedHosts to include deniedHost → the swap
// fires and the real token reaches upstream, causing the "want placeholder
// unchanged" assertion to fail. Restore → passes.
func TestProxy_NoSwapTokenSchemeOnDeny(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(51)
	const allowedHost = "api.github.com"
	const deniedHost = "evil.example"
	const realToken = "ghp_must_not_leak"
	rec, err := broker.RegisterPlaceholder(sid, allowedHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{allowedHost}, // deniedHost intentionally absent
		Broker:       broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	wantHeader := "token " + rec.Placeholder
	client := proxyClient(proxyServer.URL)
	req, _ := http.NewRequest(http.MethodGet, "http://"+deniedHost+"/steal", nil)
	req.Header.Set("Authorization", wantHeader)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	if got != wantHeader {
		t.Errorf("denied-host Authorization = %q, want placeholder unchanged %q", got, wantHeader)
	}
}

// TestProxy_TokenSchemeNoSwapNonPlaceholder verifies that a `token ` header
// whose value is NOT a registered placeholder is forwarded verbatim. This
// confirms that the swap does not corrupt arbitrary token-scheme headers.
func TestProxy_TokenSchemeNoSwapNonPlaceholder(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(52)
	const allowedHost = "api.github.com"

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{allowedHost},
		Broker:       broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	const arbitraryToken = "some-non-placeholder-value"
	wantHeader := "token " + arbitraryToken
	client := proxyClient(proxyServer.URL)
	req, _ := http.NewRequest(http.MethodGet, "http://"+allowedHost+"/user", nil)
	req.Header.Set("Authorization", wantHeader)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	got := receiveWithTimeout(t, authCh)
	if got != wantHeader {
		t.Errorf("non-placeholder token: upstream Authorization = %q, want unchanged %q", got, wantHeader)
	}
}

// ============================================================
// D-PD-36 tests: per-request path allowlist
//
// Every test below calls mitm.New (the real proxy constructor) and routes
// requests through the real proxy handler via httptest.Server. No test
// reconstructs the allowlist logic locally — all assertions are on observable
// proxy behaviour (HTTP status codes, upstream receipt, absence of token leaks).
//
// Mutation evidence for each enforcement point is documented inline.
// ============================================================

// newGitHubAllowedRepoProxy creates a proxy with AllowedRepo="acme/myrepo" and
// the three GitHub hosts in AllowedHosts. Returns the proxy server and one
// registered record per host (github.com, api.github.com, uploads.github.com).
// The real token for all three is "ghp_real_secret_token".
func newGitHubAllowedRepoProxy(t *testing.T, upstreamAddr string) (srv *httptest.Server, recGH, recAPI, recUploads cred.PlaceholderRecord) {
	t.Helper()
	const realToken = "ghp_real_secret_token"
	broker := cred.NewBroker()
	sid := newSandboxID(36)

	var err error
	recGH, err = broker.RegisterPlaceholder(sid, "github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder github.com: %v", err)
	}
	recAPI, err = broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder api.github.com: %v", err)
	}
	recUploads, err = broker.RegisterPlaceholder(sid, "uploads.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder uploads.github.com: %v", err)
	}

	cfg := mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"github.com", "api.github.com", "uploads.github.com"},
		Broker:       broker,
		AllowedRepo:  "acme/myrepo",
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstreamAddr)
			},
		},
	}
	p, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	srv = httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv, recGH, recAPI, recUploads
}

// receiveOrTimeout reads from ch with a 2 s deadline. Returns ("", false) on
// timeout — meaning the upstream never received the request.
func receiveOrTimeout(ch <-chan string) (string, bool) {
	select {
	case v := <-ch:
		return v, true
	case <-time.After(2 * time.Second):
		return "", false
	}
}

// TestD36_AllowedGitPaths verifies that the three git-over-HTTPS paths for the
// configured repo pass through the proxy (upstream receives the request) and
// that the real token is emitted (swap fires, confirming no early denial).
//
// Mutation evidence: revert gitHubPathAllowed to always return false for
// github.com → all sub-tests fail: proxy returns 403, upstream never receives
// the request. Restore → tests pass.
func TestD36_AllowedGitPaths(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, recGH, _, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/acme/myrepo.git/info/refs"},
		{http.MethodPost, "/acme/myrepo.git/git-upload-pack"},
		{http.MethodPost, "/acme/myrepo.git/git-receive-pack"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, "http://github.com"+tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+recGH.Placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("path %q: want 200 (allowed), got %d", tc.path, resp.StatusCode)
			}
			got, ok := receiveOrTimeout(authCh)
			if !ok {
				t.Fatalf("upstream never received request for allowed path %q", tc.path)
			}
			if want := "Bearer ghp_real_secret_token"; got != want {
				t.Errorf("upstream Authorization = %q, want %q (real token swap must fire)", got, want)
			}
		})
	}
}

// TestD36_AllowedAPIPaths verifies that the REST API endpoints needed for the
// PR and release flow are permitted.
//
// Mutation evidence: revert gitHubPathAllowed to always return false for
// api.github.com → all sub-tests fail with 403 and upstream receives nothing.
func TestD36_AllowedAPIPaths(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, _, recAPI, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/user"},
		{http.MethodGet, "/repos/acme/myrepo"},
		{http.MethodGet, "/repos/acme/myrepo/pulls"},
		{http.MethodPost, "/repos/acme/myrepo/pulls"},
		{http.MethodGet, "/repos/acme/myrepo/releases"},
		{http.MethodPost, "/repos/acme/myrepo/releases"},
		{http.MethodGet, "/repos/acme/myrepo/releases/12345"},
		{http.MethodPatch, "/repos/acme/myrepo/releases/12345"},
		{http.MethodDelete, "/repos/acme/myrepo/releases/12345"},
		// Babysit-loop PR reads (D-BABYSIT-1).
		{http.MethodGet, "/repos/acme/myrepo/pulls/1"},
		{http.MethodGet, "/repos/acme/myrepo/pulls/1/reviews"},
		{http.MethodGet, "/repos/acme/myrepo/pulls/1/comments"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, "http://api.github.com"+tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("path %q: want 200 (allowed), got %d", tc.path, resp.StatusCode)
			}
			if _, ok := receiveOrTimeout(authCh); !ok {
				t.Fatalf("upstream never received request for allowed path %q", tc.path)
			}
		})
	}
}

// TestD36_DeniedPaths verifies that paths outside the allowlist are refused
// with 403 and that the upstream never receives the request.
//
// Covered cases: different repo under same owner, different owner, /graphql,
// /user/repos, /search/repositories, and paths for a sibling repo on each host.
//
// Mutation evidence: revert gitHubPathAllowed to always return true → all
// sub-tests fail because the proxy returns 200 and upstream receives the request.
func TestD36_DeniedPaths(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, recGH, recAPI, recUploads := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	type dcase struct {
		host      string
		method    string
		path      string
		placeholder string
	}
	cases := []dcase{
		{"github.com", http.MethodGet, "/acme/otherrepo.git/info/refs", recGH.Placeholder},
		{"github.com", http.MethodGet, "/evil/myrepo.git/info/refs", recGH.Placeholder},
		{"api.github.com", http.MethodPost, "/graphql", recAPI.Placeholder},
		{"api.github.com", http.MethodGet, "/user/repos", recAPI.Placeholder},
		{"api.github.com", http.MethodGet, "/search/repositories", recAPI.Placeholder},
		{"api.github.com", http.MethodGet, "/repos/acme/otherrepo/pulls", recAPI.Placeholder},
		{"api.github.com", http.MethodPost, "/repos/acme/otherrepo/pulls", recAPI.Placeholder},
		{"api.github.com", http.MethodPost, "/repos/evil/myrepo/releases", recAPI.Placeholder},
		{"uploads.github.com", http.MethodPost, "/repos/acme/otherrepo/releases/1/assets", recUploads.Placeholder},
		// Babysit-loop PR reads: denials (D-BABYSIT-1).
		// Non-numeric PR slug must be denied (proves allDigits guard is load-bearing).
		{"api.github.com", http.MethodGet, "/repos/acme/myrepo/pulls/abc", recAPI.Placeholder},
		// Unknown sub-path must be denied (proves sub-path scoping is load-bearing).
		{"api.github.com", http.MethodGet, "/repos/acme/myrepo/pulls/1/files", recAPI.Placeholder},
		{"api.github.com", http.MethodGet, "/repos/acme/myrepo/pulls/1/merge", recAPI.Placeholder},
		// Deeper sub-path (reviews/{id}) must be denied (proves depth scoping is load-bearing).
		{"api.github.com", http.MethodGet, "/repos/acme/myrepo/pulls/1/reviews/9", recAPI.Placeholder},
		// Wrong method on allowed path must be denied (proves GET-only scoping is load-bearing).
		{"api.github.com", http.MethodPost, "/repos/acme/myrepo/pulls/1", recAPI.Placeholder},
		// Wrong repo must be denied (proves repo-pinning is load-bearing).
		{"api.github.com", http.MethodGet, "/repos/otherowner/otherrepo/pulls/1", recAPI.Placeholder},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.host+tc.path, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, "http://"+tc.host+tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tc.placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s%s: want 403 (denied), got %d", tc.method, tc.host, tc.path, resp.StatusCode)
			}
			if got, ok := receiveOrTimeout(authCh); ok {
				t.Errorf("upstream received request for denied path %q: Authorization = %q (must be rejected before forwarding)", tc.path, got)
			}
		})
	}
}

// TestD36_NoTokenEmittedOnDenied is the critical security assertion: a real
// credential must not reach upstream on any denied request.
//
// Mutation evidence: remove the `return req, denyResponse(req)` return in the
// deny handler (replace with `return req, nil`) → the test fails because the
// proxy forwards the request and upstream receives the real token.
func TestD36_NoTokenEmittedOnDenied(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, _, recAPI, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	// /user/repos is not in the allowlist. The real token must never reach upstream.
	req, _ := http.NewRequest(http.MethodGet, "http://api.github.com/user/repos", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("want 403 on denied path, got %d", resp.StatusCode)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("upstream received request on denied path; Authorization = %q (real token must not be emitted)", got)
	}
}

// TestS1c_GraphQLTokenValidationQuery is the regression guard for the
// `gh auth status` false-negative: gh validates GH_TOKEN with the read-only
// GraphQL POST `query UserCurrent{viewer{login}}`. Before the carve-out this
// was default-denied (403) → gh reported "The token in GH_TOKEN is invalid"
// even though the token was valid (REST GET /user succeeded and swapped).
//
// The allowed sub-test bites: revert the isGitHubTokenValidationQuery carve-out
// in New's S1c handler → the request is 403'd, upstream never receives it, and
// the real-token assertion fails. The denied sub-tests bite in the opposite
// direction: widen the carve-out to any /graphql body → they start returning
// 200 and leaking the real token upstream.
func TestS1c_GraphQLTokenValidationQuery(t *testing.T) {
	t.Parallel()

	t.Run("allowed viewer.login validates and swaps", func(t *testing.T) {
		t.Parallel()
		upstream, authCh := captureAuthUpstream(t)
		proxy, _, recAPI, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
		client := proxyClient(proxy.URL)

		// Exact document gh 2.x sends, with gh's real formatting.
		body := `{"query":"query UserCurrent{viewer{login}}"}`
		req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("token-validation query: want 200 (allowed), got %d", resp.StatusCode)
		}
		got, ok := receiveOrTimeout(authCh)
		if !ok {
			t.Fatalf("upstream never received the token-validation query (denied before forwarding)")
		}
		if want := "Bearer ghp_real_secret_token"; got != want {
			t.Errorf("upstream Authorization = %q, want %q (real token swap must fire for the validation query)", got, want)
		}
	})

	// Every non-validation GraphQL document must stay default-denied and never
	// emit the real token — the GraphQL allowlist is otherwise TBR-GRAPHQL/R5.
	denied := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"mutation", `{"query":"mutation{deleteRepository(input:{repositoryId:\"x\"}){clientMutationId}}"}`},
		{"extra fields beyond login", `{"query":"query{viewer{login repositories(first:100){nodes{nameWithOwner}}}}"}`},
		{"viewer with variables", `{"query":"query UserCurrent{viewer{login}}","variables":{"x":1}}`},
		{"aliased viewer", `{"query":"query{me:viewer{login}}"}`},
		// Duplicate top-level key: Go's json.Unmarshal would see the second (allowlisted)
		// query, but a differently-behaving parser might execute the first (malicious) one.
		// The duplicate-key check must fire and deny before any value is trusted.
		{"duplicate query key", `{"query":"mutation{evil}","query":"query UserCurrent{viewer{login}}"}`},
	}
	for _, tc := range denied {
		t.Run("denied "+tc.name, func(t *testing.T) {
			t.Parallel()
			upstream, authCh := captureAuthUpstream(t)
			proxy, _, recAPI, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
			client := proxyClient(proxy.URL)

			req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: want 403 (denied), got %d", tc.name, resp.StatusCode)
			}
			if got, ok := receiveOrTimeout(authCh); ok {
				t.Errorf("%s: upstream received denied GraphQL request; Authorization = %q (real token must not be emitted)", tc.name, got)
			}
		})
	}
}

// TestD36_AllowedUploadsPath verifies that release-asset upload to the target
// repo is permitted through uploads.github.com.
//
// Mutation evidence: revert the uploads.github.com case in gitHubPathAllowed to
// always return false → test fails with 403, upstream receives nothing.
func TestD36_AllowedUploadsPath(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, _, _, recUploads := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	req, _ := http.NewRequest(http.MethodPost,
		"http://uploads.github.com/repos/acme/myrepo/releases/42/assets?name=binary.tar.gz",
		http.NoBody)
	req.Header.Set("Authorization", "Bearer "+recUploads.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("uploads path: want 200 (allowed), got %d", resp.StatusCode)
	}
	if _, ok := receiveOrTimeout(authCh); !ok {
		t.Fatal("upstream never received upload request for allowed path")
	}
}

// TestD36_GraphQLDeniedUnconditionally verifies D-PD-36 §3: /graphql is always
// denied — the target repo lives in the POST body and cannot be validated
// without a GraphQL AST parser.
//
// Mutation evidence: change the /graphql check in gitHubPathAllowed to return
// true → the test fails because the proxy returns 200 and upstream sees the
// request.
func TestD36_GraphQLDeniedUnconditionally(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, _, recAPI, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("graphql: want 403, got %d", resp.StatusCode)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("upstream received graphql request; Authorization = %q (must never reach upstream)", got)
	}
}

// TestD36_NoAllowedRepo_NoPathCheck verifies that when AllowedRepo is empty
// (human sandbox with AllowAll), no path restriction is applied. Requests to
// arbitrary GitHub API paths pass through when the host is allowlisted.
//
// Mutation evidence: remove the `if repoSet` guard (apply the deny handler
// even when AllowedRepo is empty) → /user/repos would be denied, failing this
// test.
func TestD36_NoAllowedRepo_NoPathCheck(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(37)
	const realToken = "ghp_human_token"
	rec, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// AllowedRepo intentionally empty — human sandbox, no path restriction.
	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"api.github.com"},
		Broker:       broker,
		AllowedRepo:  "",
	}, upstream.Listener.Addr().String())
	defer srv.Close()

	client := proxyClient(srv.URL)
	req, _ := http.NewRequest(http.MethodGet, "http://api.github.com/user/repos", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("no-AllowedRepo: want 200 (no path restriction), got %d", resp.StatusCode)
	}
	if _, ok := receiveOrTimeout(authCh); !ok {
		t.Fatal("upstream never received request; AllowedRepo='' should not restrict paths")
	}
}

// TestD36_AllowedRepoMalformed verifies that mitm.New returns an error for a
// malformed AllowedRepo, so misconfiguration fails loudly at construction time.
func TestD36_AllowedRepoMalformed(t *testing.T) {
	t.Parallel()

	broker := cred.NewBroker()
	_, err := mitm.New(mitm.Config{
		SandboxID:    newSandboxID(38),
		AllowedHosts: []string{"github.com"},
		Broker:       broker,
		AllowedRepo:  "notaslash", // missing owner/repo separator
	})
	if err == nil {
		t.Fatal("mitm.New: want error for malformed AllowedRepo, got nil")
	}
}

// ============================================================
// D-PD-36 dot-segment traversal tests
//
// These tests expose the path-traversal bypass: an attacker inside the guest
// constructs a request whose URL contains ".." or percent-encoded equivalents.
// strings.HasPrefix on URL.Path matches the allowed prefix, so the allowlist
// returns true — but the upstream receives a request for an arbitrary path.
//
// Fix: containsDotSegment is checked against BOTH URL.Path (decoded) and
// EscapedPath (raw) before any prefix rule fires.
// ============================================================

// newRawURLRequest builds an http.Request whose URL is constructed directly
// (bypassing url.Parse normalisation). rawPath is placed in URL.Path; when
// rawPath contains percent-encoded characters that differ from re-encoding the
// decoded form, set encodedPath as the URL.RawPath too (nil means not set).
func newRawURLRequest(method, host, rawPath, encodedPath string, authHeader string) *http.Request {
	u := &url.URL{
		Scheme: "http",
		Host:   host,
		Path:   rawPath,
	}
	if encodedPath != "" {
		u.RawPath = encodedPath
	}
	return &http.Request{
		Method:     method,
		URL:        u,
		Header:     http.Header{"Authorization": []string{authHeader}},
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Body:       http.NoBody,
	}
}

// TestD36_DotSegmentTraversalDenied is the adversarial coverage gap that allowed
// the bug to ship. Every sub-test encodes a traversal that reaches a path
// outside the configured repo (acme/myrepo) — most target /user/repos.
//
// Before the fix: the proxy allows these requests because HasPrefix on the
// decoded path matches the /releases/ prefix, then forwards them upstream
// (emitting the real token).
// After the fix: containsDotSegment rejects them with 403 before any prefix
// check runs. The upstream never receives the request.
//
// Mutation evidence: see TestD36_DotSegmentMutationEvidence below.
func TestD36_DotSegmentTraversalDenied(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, _, recAPI, recUploads := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	type tcase struct {
		name        string
		host        string
		method      string
		rawPath     string // URL.Path (decoded)
		encodedPath string // URL.RawPath (raw, optional)
		placeholder string
	}
	const relBase = "/repos/acme/myrepo/releases/"
	cases := []tcase{
		// 1. Raw dot-dot segments reaching /user/repos via api.github.com.
		{
			name:    "raw-dotdot-to-user-repos",
			host:    "api.github.com",
			method:  http.MethodGet,
			rawPath: relBase + "../../../user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 2. %2e%2e (lowercase percent-encoded) reaching /user/repos.
		{
			name:        "pct-2e2e-lower-to-user-repos",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "../../../user/repos",
			encodedPath: relBase + "%2e%2e/%2e%2e/%2e%2e/user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 3. %2E%2E (uppercase percent-encoded) — same traversal.
		{
			name:        "pct-2E2E-upper-to-user-repos",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "../../../user/repos",
			encodedPath: relBase + "%2E%2E/%2E%2E/%2E%2E/user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 4. Double-encoded (%252e%252e): URL.Path holds %2e%2e (one decode pass),
		//    EscapedPath holds %252e%252e. An upstream doing two decode passes would
		//    resolve this to "..". Rejected: in scope because GitHub decodes paths
		//    twice in some contexts.
		{
			name:        "double-encoded-pct252e-to-user-repos",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "%2e%2e/%2e%2e/%2e%2e/user/repos", // one-decoded in URL.Path
			encodedPath: relBase + "%252e%252e/%252e%252e/%252e%252e/user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 5. Traversal reaching a different repo under the same owner.
		{
			name:    "raw-dotdot-to-other-repo",
			host:    "api.github.com",
			method:  http.MethodGet,
			rawPath: relBase + "../../../repos/acme/otherrepo/pulls",
			placeholder: recAPI.Placeholder,
		},
		// 6. uploads.github.com — the second HasPrefix site.
		{
			name:    "uploads-raw-dotdot-to-user-repos",
			host:    "uploads.github.com",
			method:  http.MethodPost,
			rawPath: relBase + "42/assets/../../../../../user/repos",
			placeholder: recUploads.Placeholder,
		},
		// 7. %2e%2e on uploads.github.com.
		{
			name:        "uploads-pct-2e2e",
			host:        "uploads.github.com",
			method:      http.MethodPost,
			rawPath:     relBase + "42/assets/../../../../../user/repos",
			encodedPath: relBase + "42/assets/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/user/repos",
			placeholder: recUploads.Placeholder,
		},
		// --- Six additional bypass spellings found by independent attack review ---
		// 7. "..;" segments: semicolons act as path separators on some servers.
		//    isCanonicalPath catches ";" via the charset check ([A-Za-z0-9._~-]).
		{
			name:        "dotdotsemi-segments",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "..;/..;/user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 8. "....//....//user/repos" — quadruple-dot with double slashes.
		//    path.Clean normalises // to / so cleaned != raw → caught by check 1.
		{
			name:        "quaddot-doubleslash",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "....//....//" + "user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 9. Null-byte suffix: "..\x00" — null terminates path on some parsers.
		//    In EscapedPath the null becomes %00; "%" fails the charset check.
		{
			name:        "null-byte-suffix",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "..\x00/user/repos",
			encodedPath: relBase + "..%00/user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 10. Backslash path separators ("\..\..\user\repos") — used by Windows
		//     and some proxy implementations. "\" fails the charset check.
		{
			name:        "backslash-dotdot",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + `\..\..` + `\user\repos`,
			placeholder: recAPI.Placeholder,
		},
		// 11. Overlong UTF-8 encoding of ".": %c0%ae encodes 0x2E using a
		//     two-byte sequence that strict decoders reject as invalid UTF-8.
		//     In EscapedPath the raw bytes are percent-encoded; "%" fails the
		//     charset check.
		{
			name:        "overlong-utf8-dotdot",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "\xc0\xae\xc0\xae/user/repos",
			encodedPath: relBase + "%c0%ae%c0%ae/user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 12. Fullwidth dot lookalikes: U+FF0E FULLWIDTH FULL STOP. In EscapedPath
		//     these are percent-encoded as %ef%bc%ae; "%" fails the charset check.
		{
			name:        "fullwidth-dotdot",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "．．/user/repos",
			encodedPath: relBase + "%ef%bc%ae%ef%bc%ae/user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 14. Semicolon segments inside /tags/ prefix — charset-only enforcement.
		//     path.Clean does NOT traverse "..;" (it's not ".."). allDigits does
		//     not apply (tags/ is a HasPrefix rule). Only the charset check catches
		//     the ";" character. Without segmentOK, this reaches upstream and leaks
		//     the real token. Used as the mutation target for Mutation 2 (charset).
		{
			name:        "tags-dotdotsemi-charset-only",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "tags/..;/..;/user/repos",
			placeholder: recAPI.Placeholder,
		},
		// 13. Traversal via the /tags/ prefix match (SEVENTH bypass spelling).
		//     The /tags/ case in gitHubPathAllowed is a HasPrefix check, so a path
		//     of /releases/tags/v1.0.0/../../../user/repos satisfies the prefix.
		//     path.Clean resolves the ".." segments → cleaned ≠ original → caught
		//     by isCanonicalPath check 1 (path.Clean). This is the only attack
		//     form that bypasses allDigits but is caught solely by path.Clean.
		{
			name:        "tags-prefix-dotdot-traversal",
			host:        "api.github.com",
			method:      http.MethodGet,
			rawPath:     relBase + "tags/v1.0.0/../../../user/repos",
			placeholder: recAPI.Placeholder,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := newRawURLRequest(tc.method, tc.host, tc.rawPath, tc.encodedPath, "Bearer "+tc.placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()

			// Must be denied with 403.
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("traversal %q: want 403 (denied), got %d (allowlist bypass!)", tc.name, resp.StatusCode)
			}
			// The real token must never reach the upstream — this is the critical
			// security invariant: denial fires BEFORE the credential swap.
			if got, ok := receiveOrTimeout(authCh); ok {
				t.Errorf("traversal %q: upstream received request; Authorization=%q (real token leaked!)", tc.name, got)
			}
		})
	}
}

// TestD36_LegitimateDotsAllowed verifies that the canonicalization check is not
// over-broad: paths that contain literal dots WITHIN a segment (e.g. version
// strings, filenames) must still be allowed. A segment like "v1.0.0" or
// "binary.tar.gz" differs from "." or "..".
func TestD36_LegitimateDotsAllowed(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, recGH, recAPI, recUploads := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	type tcase struct {
		name        string
		host        string
		method      string
		path        string
		placeholder string
	}
	cases := []tcase{
		// GET /repos/acme/myrepo/releases/12345 — numeric release ID.
		{
			name:        "release-by-id",
			host:        "api.github.com",
			method:      http.MethodGet,
			path:        "/repos/acme/myrepo/releases/12345",
			placeholder: recAPI.Placeholder,
		},
		// PATCH /repos/acme/myrepo/releases/12345 — update release.
		{
			name:        "patch-release-by-id",
			host:        "api.github.com",
			method:      http.MethodPatch,
			path:        "/repos/acme/myrepo/releases/12345",
			placeholder: recAPI.Placeholder,
		},
		// uploads.github.com upload with version-string in asset name (query param,
		// not a path segment — path itself is clean).
		{
			name:        "uploads-clean-path-with-version-query",
			host:        "uploads.github.com",
			method:      http.MethodPost,
			path:        "/repos/acme/myrepo/releases/42/assets",
			placeholder: recUploads.Placeholder,
		},
		// GET /releases/latest — get the latest published release.
		{
			name:        "release-latest",
			host:        "api.github.com",
			method:      http.MethodGet,
			path:        "/repos/acme/myrepo/releases/latest",
			placeholder: recAPI.Placeholder,
		},
		// GET /releases/tags/v1.0.0 — get release by tag name.
		{
			name:        "release-by-tag",
			host:        "api.github.com",
			method:      http.MethodGet,
			path:        "/repos/acme/myrepo/releases/tags/v1.0.0",
			placeholder: recAPI.Placeholder,
		},
		// GET /releases/tags/preview/some-tag — tag name with "/" in it.
		{
			name:        "release-by-tag-with-slash",
			host:        "api.github.com",
			method:      http.MethodGet,
			path:        "/repos/acme/myrepo/releases/tags/preview/some-tag",
			placeholder: recAPI.Placeholder,
		},
		// GET /releases/12345/assets — list assets for a release.
		{
			name:        "release-assets-list",
			host:        "api.github.com",
			method:      http.MethodGet,
			path:        "/repos/acme/myrepo/releases/12345/assets",
			placeholder: recAPI.Placeholder,
		},
		// GET /releases/assets/516754282 — download a specific release asset by ID.
		// This is the path used in the endpr-2026-08-16 live proof.
		{
			name:        "release-asset-by-id",
			host:        "api.github.com",
			method:      http.MethodGet,
			path:        "/repos/acme/myrepo/releases/assets/516754282",
			placeholder: recAPI.Placeholder,
		},
		// /o/r.git/git-upload-pack — git clone/fetch data transfer (hyphen in segment).
		{
			name:        "git-upload-pack",
			host:        "github.com",
			method:      http.MethodPost,
			path:        "/acme/myrepo.git/git-upload-pack",
			placeholder: recGH.Placeholder,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, "http://"+tc.host+tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tc.placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("legitimate path %q: want 200 (allowed), got %d", tc.path, resp.StatusCode)
			}
			if _, ok := receiveOrTimeout(authCh); !ok {
				t.Errorf("legitimate path %q: upstream never received request (false denial)", tc.path)
			}
		})
	}
}

// TestD36_BroadIsGitHubHostFilters verifies Problem 3 of the security fix: a
// *.github.com host in AllowedHosts (e.g. codeload.github.com) now gets path
// filtering applied rather than being forwarded with no restriction.
//
// Before the fix: isGitHubHost in the proxy only matched three specific hosts
// (github.com, api.github.com, uploads.github.com). A host like
// codeload.github.com returned false, so the D-PD-36 handler returned
// `req, nil` with NO path restriction — the real token was injected into any
// path because the credential-swap handler still ran for any host in allowSet.
//
// After the fix: domain.IsGitHubHost matches *.github.com broadly, so
// codeload.github.com is now path-filtered. gitHubPathAllowed has no case for
// codeload.github.com → all requests are denied with 403, and the real token
// is never emitted upstream.
//
// Mutation evidence: see TestD36_BroadIsGitHubHostMutation below.
func TestD36_BroadIsGitHubHostFilters(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(36)
	const realToken = "ghp_codeload_must_not_leak"
	const codeloadHost = "codeload.github.com"
	rec, err := broker.RegisterPlaceholder(sid, codeloadHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"github.com", "api.github.com", "uploads.github.com", codeloadHost},
		Broker:       broker,
		AllowedRepo:  "acme/myrepo",
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)

	// codeload.github.com is used for tarball/zipball downloads but is NOT in
	// the gitHubPathAllowed switch → every path must be denied with 403.
	req, _ := http.NewRequest(http.MethodGet,
		"http://codeload.github.com/acme/myrepo/tar.gz/refs/heads/main", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("codeload.github.com: want 403 (path filter applied), got %d — real token leak possible", resp.StatusCode)
	}
	// The critical invariant: real token must not reach upstream even when the
	// host is in allowSet and a placeholder credential is present.
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("codeload.github.com: real token emitted upstream (Authorization=%q) — allowlist bypass", got)
	}
}

// TestD36_BroadIsGitHubHostMutation is the mutation proof for
// TestD36_BroadIsGitHubHostFilters. It documents that reverting to the narrow
// isGitHubHost (checking only the three hardcoded hosts) causes the test to
// fail — confirming that the broad check is the actual enforcement gate.
//
// Mutation run (2026-08-16):
//
//	MUTANT: in proxy.go, replace `domain.IsGitHubHost(host)` with the old narrow
//	  switch: case "github.com", "api.github.com", "uploads.github.com": true
//	RESULT: TestD36_BroadIsGitHubHostFilters FAIL — got 200, want 403; upstream
//	  received Authorization header (real token emitted).
//	RESTORE: reverted to domain.IsGitHubHost; test PASS.
//
// (Mutation output is in the implementation report; not re-run here to avoid
// leaving the codebase in a broken state.)
func TestD36_BroadIsGitHubHostMutation(t *testing.T) {
	t.Skip("Mutation documentation test — run manually per the mutation protocol")
}

// TestD36_TrailingDotHostFiltered verifies that a request whose Host header
// carries a trailing dot (e.g. "api.github.com." — a valid FQDN form per
// RFC 1034) is treated identically to the canonical form by the D-PD-36 filter
// and the credential swap handler.
//
// The enforcement has two layers:
//  1. domain.IsGitHubHost strips the trailing dot internally, so the D-PD-36
//     filter fires even when the Host arrives with a dot.
//  2. reqHost also strips the trailing dot so the allowSet lookup used by the
//     credential swap handler agrees with the filter by construction.
//
// Mutation evidence (domain level): removing strings.TrimSuffix from
// domain.IsGitHubHost causes TestIsGitHubHost to fail for the trailing-dot
// table entries, and causes this test to receive 200 (filter skipped) instead
// of 403 when the Go HTTP stack does not normalise req.Host before forwarding
// to the proxy (observable in HTTPS/CONNECT paths where the host comes from
// the CONNECT target rather than the HTTP request line).
//
// Note: Go's HTTP client normalises trailing dots from plain-HTTP (non-CONNECT)
// Host headers before the proxy handler sees them, so end-to-end HTTP tests
// may not expose the CONNECT-path vulnerability. The domain-level unit test
// (TestIsGitHubHost) is the primary mutation proof for the trailing-dot fix.
func TestD36_TrailingDotHostFiltered(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(37)
	const realToken = "ghp_trailing_dot_must_not_leak"
	const canonicalHost = "api.github.com"
	rec, err := broker.RegisterPlaceholder(sid, canonicalHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"github.com", canonicalHost, "uploads.github.com"},
		Broker:       broker,
		AllowedRepo:  "acme/myrepo",
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)
	// Use a path that is NOT in the D-PD-36 allowlist so a bypassed filter
	// would yield 200 from upstream while a working filter yields 403.
	req, _ := http.NewRequest(http.MethodGet,
		"http://"+canonicalHost+"/admin/unpublished", http.NoBody)
	// Override Host to the trailing-dot FQDN form — this is the vector.
	req.Host = canonicalHost + "."
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("trailing-dot host: want 403 (filter applied), got %d — path filter bypassed", resp.StatusCode)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("trailing-dot host: real token emitted upstream (Authorization=%q)", got)
	}
}

// TestProxy_AllowAll_SecretHostSwapsGitHub proves D-PD-25: AllowAll + SecretHosts
// MITMs github.com (swap fires) without putting it on AllowedHosts.
// TestProxy_BroadAllowSelectiveMITM proves the "broad-allow + selective MITM"
// contract end-to-end:
//
//   - A host in SecretHosts is intercepted (MITM) even when AllowAll=true, and
//     its placeholder Bearer token is swapped to the real token before egress.
//     Mutation sensitivity: removing the swap step causes the upstream to receive
//     the placeholder ("PLACEHOLDER-xxxx"), not the real token ("REAL-yyyy") →
//     the assertion goes RED.
//
//   - A host NOT in SecretHosts (and not in AllowedHosts) is tunneled by the
//     proxy under AllowAll=true; its Authorization header is NOT rewritten, so
//     a placeholder sent to that host reaches upstream unchanged.
func TestProxy_BroadAllowSelectiveMITM(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(50)
	const secretHost = "api.credentialed.example"
	const otherHost = "registry.npmjs.org"
	const realToken = "REAL-yyyy-secret-token"

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

	t.Run("secret_host_swapped", func(t *testing.T) {
		// Mutation-sensitive: if the swap DoFunc is removed or bypassed, the
		// upstream receives the placeholder and the assertion fails.
		req, _ := http.NewRequest(http.MethodGet, "http://"+secretHost+"/v1/messages", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()

		got := receiveWithTimeout(t, authCh)
		if want := "Bearer " + realToken; got != want {
			t.Errorf("secret host: upstream Authorization = %q, want %q (placeholder leaked or swap bypassed)", got, want)
		}
	})

	t.Run("other_host_not_swapped", func(t *testing.T) {
		// Under AllowAll, a host not in SecretHosts is tunneled (CONNECT path) or
		// forwarded (plain HTTP) without interception. The OnRequest swap guard
		// (allowSet check) rejects it, so the Authorization header is NOT rewritten.
		const fakePlaceholder = "PLACEHOLDER-xxxx-not-registered"
		req, _ := http.NewRequest(http.MethodGet, "http://"+otherHost+"/package", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+fakePlaceholder)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()

		got := receiveWithTimeout(t, authCh)
		// The proxy must NOT have rewritten the header — the placeholder arrives unchanged.
		if got != "Bearer "+fakePlaceholder {
			t.Errorf("other host: upstream Authorization = %q, want placeholder %q unchanged (unexpected swap)", got, "Bearer "+fakePlaceholder)
		}
	})
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

// ============================================================
// D-PD-38 tests: branch policy, gh-stack REST allowlist, GraphQL body policy
// ============================================================

// buildPktLines constructs a valid git pkt-line sequence for a push (ref update
// commands) followed by a flush packet "0000". Each ref uses old-sha = 40 zeros,
// new-sha = 40 'a' chars, matching the format the proxy branch-policy parser expects.
func buildPktLines(refs []string) []byte {
	var buf bytes.Buffer
	oldSHA := strings.Repeat("0", 40)
	newSHA := strings.Repeat("a", 40)
	for _, ref := range refs {
		data := oldSHA + " " + newSHA + " " + ref + "\n"
		total := 4 + len(data)
		fmt.Fprintf(&buf, "%04x%s", total, data) //nolint:errcheck
	}
	buf.WriteString("0000") //nolint:errcheck
	return buf.Bytes()
}

// TestD38_BranchPolicy_DeniedBeforeCredSwap verifies that a push to a ref not
// matching AllowedBranches is rejected with 403 and that the real credential is
// never forwarded to the upstream — denial fires before the swap.
//
// Mutation evidence: remove the branch-policy deny path (return allowed instead
// of denyResponse) → upstream receives the request and the real token is emitted,
// causing the credential-leak assertion to fail.
func TestD38_BranchPolicy_DeniedBeforeCredSwap(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(60)
	const realToken = "ghp_branch_real_token"
	rec, err := broker.RegisterPlaceholder(sid, "github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:       sid,
		AllowedHosts:    []string{"github.com"},
		Broker:          broker,
		AllowedRepo:     "acme/myrepo",
		AllowedBranches: []string{"refs/heads/nexus3/*"},
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	body := buildPktLines([]string{"refs/heads/main"}) // denied ref
	req, _ := http.NewRequest(http.MethodPost,
		"http://github.com/acme/myrepo.git/git-receive-pack",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := proxyClient(proxyServer.URL).Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("denied branch push: want 403, got %d", resp.StatusCode)
	}
	// Upstream must not receive the real token — denial fires before forwarding.
	if got, ok := receiveOrTimeout(authCh); ok {
		if got == "Bearer "+realToken {
			t.Errorf("real token reached upstream for denied branch push (credential leaked before push deny): Authorization=%q", got)
		}
	}
}

// TestD38_BranchPolicy_AllowedStreamsThrough verifies that a push to a ref
// matching AllowedBranches is forwarded to upstream with the body intact.
//
// Mutation evidence: change the glob match to always return false → proxy returns
// 403 instead of 200 and upstream never receives the body, failing both assertions.
func TestD38_BranchPolicy_AllowedStreamsThrough(t *testing.T) {
	t.Parallel()

	bodyCh := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyCh <- b
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	broker := cred.NewBroker()
	sid := newSandboxID(61)
	const realToken = "ghp_allowed_branch_token"
	rec, err := broker.RegisterPlaceholder(sid, "github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:       sid,
		AllowedHosts:    []string{"github.com"},
		Broker:          broker,
		AllowedRepo:     "acme/myrepo",
		AllowedBranches: []string{"refs/heads/nexus3/*"},
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	pktBody := buildPktLines([]string{"refs/heads/nexus3/feat-1"}) // allowed ref
	req, _ := http.NewRequest(http.MethodPost,
		"http://github.com/acme/myrepo.git/git-receive-pack",
		bytes.NewReader(pktBody))
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := proxyClient(proxyServer.URL).Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("allowed branch push: want 200, got %d", resp.StatusCode)
	}
	select {
	case got := <-bodyCh:
		if !strings.Contains(string(got), "nexus3/feat-1") {
			t.Errorf("upstream body does not contain allowed ref name: %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for upstream to receive push body")
	}
}

// TestD38_BranchPolicy_EmptyAllowlistDeniesAll verifies that when AllowedBranches
// is nil/empty, the proxy DENIES all pushes (fail-closed). An empty allowlist
// signals misconfiguration — domain.Envelope.ResolvedAllowedBranches() always
// supplies the hardcoded default, so empty can only occur via a misconfigured caller.
//
// Mutation evidence: revert the fail-closed guard to the old fail-open
// `return req, nil` → the test fails because the proxy returns 200 instead of 403.
func TestD38_BranchPolicy_EmptyAllowlistDeniesAll(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    newSandboxID(62),
		AllowedHosts: []string{"github.com"},
		Broker:       cred.NewBroker(),
		AllowedRepo:  "acme/myrepo",
		// AllowedBranches intentionally nil — no enforcement.
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	body := buildPktLines([]string{"refs/heads/main"}) // denied because allowlist is empty (fail-closed)
	req, _ := http.NewRequest(http.MethodPost,
		"http://github.com/acme/myrepo.git/git-receive-pack",
		bytes.NewReader(body))

	resp, err := proxyClient(proxyServer.URL).Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("empty AllowedBranches: want 403 (fail-closed), got %d", resp.StatusCode)
	}
}

// TestD38_BranchPolicy_UnresolvedSentinelDeniesAll verifies that when
// AllowedBranches is exactly [domain.UnresolvedBranchSentinel] — the value
// service.resolveAllowedBranches stores when a bound workspace's branch
// could not be derived (TBD-1 fail-closed path) — every push is denied, even
// for a ref an operator might plausibly expect to be allowed (the sandbox's
// own worktree branch name).
//
// Mutation evidence: replace the sentinel's NUL byte with an empty string
// (making the pattern "refs/heads/unresolved", an ordinary matchable
// pattern) → the test fails because a push to that literal ref now succeeds
// with 200 instead of being denied for every ref including that one.
func TestD38_BranchPolicy_UnresolvedSentinelDeniesAll(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    newSandboxID(203),
		AllowedHosts: []string{"github.com"},
		Broker:       cred.NewBroker(),
		AllowedRepo:  "acme/myrepo",
		AllowedBranches: []string{domain.UnresolvedBranchSentinel},
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	for _, ref := range []string{"refs/heads/main", "refs/heads/newman/some-work", "refs/heads/nexus3/foo"} {
		t.Run(ref, func(t *testing.T) {
			body := buildPktLines([]string{ref})
			req, _ := http.NewRequest(http.MethodPost,
				"http://github.com/acme/myrepo.git/git-receive-pack",
				bytes.NewReader(body))
			resp, err := proxyClient(proxyServer.URL).Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("sentinel allowlist: ref %q: want 403 (fail-closed), got %d", ref, resp.StatusCode)
			}
		})
	}
}

// TestT2_AC2_UnconfiguredDefaultAllowsOnlyNexus3 verifies that when
// AllowedBranches is set to the resolved default refs/heads/nexus3/**, the proxy
// allows nexus3/ refs and denies others (e.g. refs/heads/main).
func TestT2_AC2_UnconfiguredDefaultAllowsOnlyNexus3(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	// Simulate the resolved default: ["refs/heads/nexus3/**"].
	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    newSandboxID(201),
		AllowedHosts: []string{"github.com"},
		Broker:       cred.NewBroker(),
		AllowedRepo:  "acme/myrepo",
		AllowedBranches: []string{"refs/heads/nexus3/**"},
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	t.Run("nexus3_ref_passes", func(t *testing.T) {
		body := buildPktLines([]string{"refs/heads/nexus3/my-feature"})
		req, _ := http.NewRequest(http.MethodPost,
			"http://github.com/acme/myrepo.git/git-receive-pack",
			bytes.NewReader(body))
		resp, err := proxyClient(proxyServer.URL).Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("nexus3 ref: want 200, got %d", resp.StatusCode)
		}
	})

	t.Run("main_ref_denied", func(t *testing.T) {
		body := buildPktLines([]string{"refs/heads/main"})
		req, _ := http.NewRequest(http.MethodPost,
			"http://github.com/acme/myrepo.git/git-receive-pack",
			bytes.NewReader(body))
		resp, err := proxyClient(proxyServer.URL).Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("non-nexus3 ref: want 403, got %d", resp.StatusCode)
		}
	})
}

// TestT2_AC3_OnEgressEmitsRecords verifies that denied pushes and denied hosts
// each emit an egress record with the expected shape to the OnEgress hook.
//
// Host-deny is exercised via HTTPS CONNECT (the HandleConnect path that fires
// goproxy.RejectConnect) — plain HTTP requests to unlisted hosts bypass that
// path, so the upstream must be an httptest.NewTLSServer.
//
// Mutation evidence: remove the cfg.OnEgress call from any deny path → the
// corresponding record is missing from the collected slice, failing the
// assertion.
func TestT2_AC3_OnEgressEmitsRecords(t *testing.T) {
	t.Parallel()

	type egressRecord struct {
		host    string
		verdict string
		reason  string
	}

	// Use a TLS server as the redirect target so the CONNECT host-deny path
	// is reachable (the client sends CONNECT for https:// targets).
	tlsUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(tlsUpstream.Close)

	plainUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(plainUpstream.Close)

	var mu sync.Mutex
	var records []egressRecord
	collect := func(host, verdict, reason string, _ time.Time) {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, egressRecord{host: host, verdict: verdict, reason: reason})
	}

	broker := cred.NewBroker()
	sid := newSandboxID(202)

	// Build the proxy directly (not via newTestProxy) so we can supply a
	// custom Transport that redirects to our stub server.
	p, err := mitm.New(mitm.Config{
		SandboxID:   sid,
		AllowedHosts: []string{"github.com"},
		Broker:       broker,
		AllowedRepo:  "acme/myrepo",
		AllowedBranches: []string{"refs/heads/nexus3/**"},
		OnEgress:     collect,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, plainUpstream.Listener.Addr().String())
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only redirect
		},
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	proxyServer := httptest.NewServer(p)
	t.Cleanup(proxyServer.Close)

	proxyURL, _ := url.Parse(proxyServer.URL)
	pool := x509.NewCertPool()
	pool.AddCert(p.CACert())
	httpsClient := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 5 * time.Second,
	}

	// Trigger a branch allowlist deny (refs/heads/main is not in refs/heads/nexus3/**).
	plainClient := proxyClient(proxyServer.URL)
	body := buildPktLines([]string{"refs/heads/main"})
	req, _ := http.NewRequest(http.MethodPost,
		"http://github.com/acme/myrepo.git/git-receive-pack",
		bytes.NewReader(body))
	resp, err := plainClient.Do(req)
	if err != nil {
		t.Fatalf("branch deny client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for branch not in AllowedBranches, got %d", resp.StatusCode)
	}

	// Trigger a host-not-in-allowlist deny via HTTPS CONNECT (HandleConnect path).
	_, _ = httpsClient.Get("https://notallowed.example.com/ping")

	mu.Lock()
	got := records
	mu.Unlock()

	var foundBranchDeny, foundHostDeny bool
	for _, r := range got {
		if r.verdict == "deny" && strings.Contains(r.reason, "AllowedBranches") {
			foundBranchDeny = true
		}
		if r.verdict == "deny" && strings.Contains(r.reason, "allowlist") {
			foundHostDeny = true
		}
	}
	if !foundBranchDeny {
		t.Errorf("OnEgress: no deny record emitted for branch-not-in-allowlist push; records=%v", got)
	}
	if !foundHostDeny {
		t.Errorf("OnEgress: no deny record emitted for blocked host; records=%v", got)
	}
}

// TestD38_GhStack_RESTAllowed verifies that the gh-stack REST paths for the
// configured repo are permitted through the proxy.
//
// Mutation evidence: remove any of the gh-stack cases from gitHubPathAllowed →
// the corresponding sub-test receives 403 instead of 200.
func TestD38_GhStack_RESTAllowed(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    newSandboxID(63),
		AllowedHosts: []string{"api.github.com"},
		Broker:       cred.NewBroker(),
		AllowedRepo:  "acme/myrepo",
	}, upstream.Listener.Addr().String())
	t.Cleanup(proxyServer.Close)

	client := proxyClient(proxyServer.URL)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/repos/acme/myrepo/stacks"},
		{http.MethodPost, "/repos/acme/myrepo/stacks"},
		{http.MethodPost, "/stacks/42/add"},
		{http.MethodPost, "/stacks/42/unstack"},
		{http.MethodPatch, "/repos/acme/myrepo/pulls/42"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, "http://api.github.com"+tc.path, http.NoBody)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("gh-stack %s %s: want 200, got %d", tc.method, tc.path, resp.StatusCode)
			}
		})
	}
}

// TestD38_GhStack_NonNumericPRRejected verifies that PATCH /repos/owner/repo/pulls/{non-numeric}
// is denied with 403 — only all-digit PR numbers are allowed by the path policy.
//
// Mutation evidence: remove the allDigits check for the PATCH pulls case → the
// proxy returns 200 instead of 403 for the non-numeric segment.
func TestD38_GhStack_NonNumericPRRejected(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    newSandboxID(64),
		AllowedHosts: []string{"api.github.com"},
		Broker:       cred.NewBroker(),
		AllowedRepo:  "acme/myrepo",
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	req, _ := http.NewRequest(http.MethodPatch,
		"http://api.github.com/repos/acme/myrepo/pulls/abc", // non-numeric PR number
		http.NoBody)

	resp, err := proxyClient(proxyServer.URL).Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-numeric PR number: want 403, got %d", resp.StatusCode)
	}
}

// TestD38_GraphQL_OwnerNameMismatchDenied verifies that a GraphQL request whose
// variables contain owner/name that don't match the configured repo is denied.
//
// Mutation evidence: remove the owner/name variable check from the GraphQL
// OnRequest handler → the proxy returns 200 instead of 403, allowing cross-repo
// data access.
func TestD38_GraphQL_OwnerNameMismatchDenied(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    newSandboxID(65),
		AllowedHosts: []string{"api.github.com"},
		Broker:       cred.NewBroker(),
		AllowedRepo:  "acme/myrepo",
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	body := strings.NewReader(`{"query":"query { repository(owner: \"other\", name: \"repo\") { id } }","variables":{"owner":"other","name":"repo"}}`)
	req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := proxyClient(proxyServer.URL).Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("graphql owner/name mismatch: want 403, got %d", resp.StatusCode)
	}
}

// TestD38_GraphQL_MutationWithWrongRepoIDDenied verifies that a GraphQL mutation
// specifying a repositoryId that doesn't match the allowed repo is denied with 403.
//
// Updated for the S1c default-deny stub (advisor CORRECTION, TBR-GRAPHQL/R5):
// ALL GraphQL requests are now denied — both the probe query and the mutation
// return 403. The old two-step pin-then-check logic is gone; the deny fires
// unconditionally at the OnRequest layer before any credential swap or upstream
// call occurs.
func TestD38_GraphQL_MutationWithWrongRepoIDDenied(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:    newSandboxID(66),
		AllowedHosts: []string{"api.github.com"},
		Broker:       cred.NewBroker(),
		AllowedRepo:  "acme/myrepo",
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)

	// S1c deny-all: even a query with correct owner/name is denied.
	pinBody := strings.NewReader(`{"query":"query { repository(owner: \"acme\", name: \"myrepo\") { id } }","variables":{"owner":"acme","name":"myrepo"}}`)
	pinReq, _ := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", pinBody)
	pinReq.Header.Set("Content-Type", "application/json")
	pinResp, err := client.Do(pinReq)
	if err != nil {
		t.Fatalf("probe query: client.Do: %v", err)
	}
	io.Copy(io.Discard, pinResp.Body) //nolint:errcheck
	pinResp.Body.Close()
	if pinResp.StatusCode != http.StatusForbidden {
		t.Fatalf("probe query: want 403 (S1c default-deny stub), got %d", pinResp.StatusCode)
	}

	// Mutation with wrong repositoryId is also denied by the same deny-all.
	mutBody := strings.NewReader(`{"query":"mutation CreatePR($input: CreatePullRequestInput!) { createPullRequest(input: $input) { pullRequest { id } } }","variables":{"input":{"repositoryId":"R_WRONG","title":"test"}}}`)
	mutReq, _ := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", mutBody)
	mutReq.Header.Set("Content-Type", "application/json")
	mutResp, err := client.Do(mutReq)
	if err != nil {
		t.Fatalf("mutation: client.Do: %v", err)
	}
	io.Copy(io.Discard, mutResp.Body) //nolint:errcheck
	mutResp.Body.Close()

	if mutResp.StatusCode != http.StatusForbidden {
		t.Errorf("mutation wrong repoID: want 403, got %d", mutResp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// R2-branch-glob-depth: refMatchesGlob unit tests
// ---------------------------------------------------------------------------

// refMatchesGlobForTest wraps the unexported refMatchesGlob via the exported
// surface: build a minimal proxy config with a single-entry AllowedBranches
// list and exercise the D-PD-38 branch-policy path through the full proxy
// handler.  Because refMatchesGlob is package-private, we test it indirectly
// through a git push request; a 403 response means the pattern denied the ref
// and a 200/non-403 means it was allowed (the upstream being absent causes the
// proxy to 502, which is not 403 — still "allowed by policy").
//
// To keep the test surface small and deterministic we test refMatchesGlob
// directly via a thin exported shim defined in export_test.go.  If no such
// shim exists yet we fall back to the black-box approach above — but for
// isolation the shim approach is preferred.  Since this file is in package
// mitm_test we call through the exported wrapper below.

// TestRefMatchesGlob_DoubleStarNamespace verifies that a "/**" pattern matches
// refs at any depth under the namespace prefix, and denies refs outside it.
// This is the D-PD-03 fix: refs/heads/nexus3/** must cover
// refs/heads/nexus3/<slug>/<id> (2 levels deep).
func TestRefMatchesGlob_DoubleStarNamespace(t *testing.T) {
	pattern := "refs/heads/nexus3/**"
	cases := []struct {
		ref  string
		want bool
		desc string
	}{
		{"refs/heads/nexus3/my-motive/abc123", true, "2-level nexus3 ref (D-PD-03 convention)"},
		{"refs/heads/nexus3/foo", true, "1-level nexus3 ref"},
		{"refs/heads/nexus3/a/b/c", true, "3-level nexus3 ref"},
		{"refs/heads/main", false, "non-namespace ref"},
		{"refs/heads/rogue", false, "rogue ref"},
		{"refs/heads/nexus3-evil/foo", false, "lookalike prefix, no slash boundary"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := mitm.RefMatchesGlobForTest(pattern, tc.ref)
			if got != tc.want {
				t.Errorf("refMatchesGlob(%q, %q) = %v; want %v", pattern, tc.ref, got, tc.want)
			}
		})
	}
}

// TestRefMatchesGlob_SingleStarKeptPathMatchSemantics verifies that an
// explicit single-segment "*" pattern retains path.Match behaviour (no
// cross-slash matching) so operator overrides like
// "refs/heads/nexus3/e2e/*" still work correctly.
func TestRefMatchesGlob_SingleStarKeptPathMatchSemantics(t *testing.T) {
	pattern := "refs/heads/nexus3/e2e/*"
	cases := []struct {
		ref  string
		want bool
		desc string
	}{
		{"refs/heads/nexus3/e2e/run1", true, "single-segment match"},
		{"refs/heads/nexus3/e2e/run1/sub", false, "two-segment: * must not cross /"},
		{"refs/heads/nexus3/other/run1", false, "wrong namespace segment"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := mitm.RefMatchesGlobForTest(pattern, tc.ref)
			if got != tc.want {
				t.Errorf("refMatchesGlob(%q, %q) = %v; want %v", pattern, tc.ref, got, tc.want)
			}
		})
	}
}

// TestProxy_GraphQL_DefaultDeny verifies the S1c safe default-deny stub
// (advisor CORRECTION): any POST to a /graphql path on api.github.com or
// github.com is denied with 403 and NO credential swap occurs — regardless of
// whether the body targets a cross-repo or same-repo operation.
//
// Mutation-proof rationale: if the return in the GraphQL OnRequest handler is
// changed back to (req, nil), the proxy forwards the request to the upstream
// and swaps the credential; authCh receives a value, and the "upstream must
// not be called" assertion fails. The test is therefore mutation-proven.
func TestProxy_GraphQL_DefaultDeny(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(99)
	const realToken = "real-github-token-gql"

	rec, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:       sid,
		SecretHosts:     []string{"api.github.com"},
		AllowedRepo:     "owner/repo",
		AllowedBranches: []string{"refs/heads/main"},
		Broker:          broker,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)

	cases := []struct {
		name string
		body string
	}{
		{
			// Cross-repo GraphQL mutation: repositoryId points at a foreign repo.
			// The old partial guards would only deny if repositoryId mismatched the
			// pinned ID, but the pin was empty (no prior query), so this passed the
			// old check and reached the credential swap. The new deny-all blocks it.
			name: "cross-repo mutation (createCommitOnBranch)",
			body: `{"query":"mutation($input:CreateCommitOnBranchInput!){createCommitOnBranch(input:$input){commit{oid}}}","variables":{"input":{"repositoryId":"MDEwOlJlcG9zaXRvcnk5OTk5OTk=","branch":{"repositoryNameWithOwner":"evil/evil","branchName":"main"},"expectedHeadOid":"abc123","fileChanges":{},"message":{"headline":"pwn"}}}}`,
		},
		{
			// Same-repo query with matching owner/name variables: the old owner/name
			// guard would ALLOW this through because owner=="owner" and name=="repo"
			// match the configured AllowedRepo. The new deny-all blocks it.
			name: "same-repo query with matching owner/name variables",
			body: `{"query":"query($owner:String!,$name:String!){repository(owner:$owner,name:$name){id}}","variables":{"owner":"owner","name":"repo"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/graphql",
				strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()

			// Assert 403 — the deny must be real, not advisory.
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("S1c-GQL %s: status = %d, want 403 (default-deny stub broken)", tc.name, resp.StatusCode)
			}

			// Assert no credential swap: upstream must never have been called.
			// If the OnRequest handler returned (req, nil) instead of (req, denyResponse),
			// goproxy forwards the request, the swap handler fires, and authCh receives
			// a value here — failing this assertion.
			select {
			case auth := <-authCh:
				if auth == "Bearer "+realToken {
					t.Errorf("S1c-GQL %s: real token leaked to upstream — credential swap occurred despite GraphQL deny", tc.name)
				} else {
					t.Errorf("S1c-GQL %s: upstream was called (auth=%q) — GraphQL deny did not short-circuit the proxy pipeline", tc.name, auth)
				}
			case <-time.After(200 * time.Millisecond):
				// Good: upstream was not called; no swap occurred.
			}
		})
	}
}

// ============================================================
// D-PDE-16 tests: per-(placeholder, host) PathPolicies API
//
// These tests exercise the new generic path policy framework directly via
// Config.PathPolicies (not the AllowedRepo compat shim). They are mutation-
// proof: negative controls verify that disabling or inverting the matcher
// breaks the expected outcome, and positive controls verify that real tokens
// are NOT emitted on denied requests.
// ============================================================

// TestD_PDE16_PerBindKeying verifies that PathPolicies entries are keyed on
// the exact placeholder: two GitHub binds for different repos are each denied
// on the other's repo path (no global AllowedRepo bottleneck).
//
// Mutation evidence:
//   - Change lookupPolicy to return the first matching host entry regardless
//     of placeholder → both placeholders would see the same policy → test
//     fails because each placeholder's denied case returns 200 instead of 403.
//   - Remove the exact-placeholder check in lookupPolicy and fall back only to
//     the wildcard → same failure.
func TestD_PDE16_PerBindKeying(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(90)

	const realTokenA = "ghp_bind_a_secret"
	const realTokenB = "ghp_bind_b_secret"

	recA, err := broker.RegisterPlaceholder(sid, "api.github.com", realTokenA)
	if err != nil {
		t.Fatalf("RegisterPlaceholder A: %v", err)
	}
	recB, err := broker.RegisterPlaceholder(sid, "api.github.com", realTokenB)
	if err != nil {
		t.Fatalf("RegisterPlaceholder B: %v", err)
	}

	// Two disjoint per-bind policies keyed on the exact placeholder.
	policies := mitm.PathPolicies{
		recA.Placeholder: {
			"api.github.com": {GitHub: &mitm.GitHubPolicy{Owner: "orgA", Name: "repoA"}},
		},
		recB.Placeholder: {
			"api.github.com": {GitHub: &mitm.GitHubPolicy{Owner: "orgB", Name: "repoB"}},
		},
	}

	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"api.github.com"},
		Broker:       broker,
		PathPolicies: policies,
	}, upstream.Listener.Addr().String())
	client := proxyClient(srv.URL)

	type tcase struct {
		name        string
		placeholder string
		path        string
		wantStatus  int
	}
	cases := []tcase{
		// A's placeholder on A's path → allowed.
		{"A-on-A-allowed", recA.Placeholder, "/repos/orgA/repoA/pulls", http.StatusOK},
		// A's placeholder on B's path → denied (per-bind default-deny).
		{"A-on-B-denied", recA.Placeholder, "/repos/orgB/repoB/pulls", http.StatusForbidden},
		// B's placeholder on B's path → allowed.
		{"B-on-B-allowed", recB.Placeholder, "/repos/orgB/repoB/pulls", http.StatusOK},
		// B's placeholder on A's path → denied.
		{"B-on-A-denied", recB.Placeholder, "/repos/orgA/repoA/pulls", http.StatusForbidden},
	}

	// Sequential — authCh buffer=1; parallel allowed cases would both write to it
	// and the second write would block the upstream handler, hanging client.Do.
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "http://api.github.com"+tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tc.placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("%s: want %d, got %d", tc.name, tc.wantStatus, resp.StatusCode)
			}
			if tc.wantStatus == http.StatusOK {
				receiveOrTimeout(authCh) // drain so next allowed case doesn't block
			}
		})
	}
	// Verify that the no-token invariant holds for the cross-repo denied case
	// using a dedicated sequential request (avoids parallel authCh race).
	t.Run("cross-bind-deny-no-token", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://api.github.com/repos/orgB/repoB/pulls", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+recA.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("cross-bind: want 403, got %d", resp.StatusCode)
		}
		if got, ok := receiveOrTimeout(authCh); ok {
			t.Errorf("cross-bind: real token reached upstream on denied path; Authorization=%q", got)
		}
	})
}

// TestD_PDE16_HostNoPolicyAllowed verifies that a host with NO PathPolicies
// entry is allowed through unrestricted and that the credential swap fires.
//
// Mutation evidence: change lookupPolicy to return (HostPolicy{}, true) for
// any host regardless of whether an entry exists → the default-deny handler
// fires for the unconstrained host and returns 403, failing the 200 assertion.
func TestD_PDE16_HostNoPolicyAllowed(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(91)
	const realToken = "ghp_unconstrained_host_token"

	// Register on api.github.com (has a policy) AND on other.example.com (no policy).
	recGH, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder github: %v", err)
	}
	recOther, err := broker.RegisterPlaceholder(sid, "other.example.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder other: %v", err)
	}

	// Only api.github.com has a path policy; other.example.com has none.
	policies := mitm.PathPolicies{
		recGH.Placeholder: {
			"api.github.com": {GitHub: &mitm.GitHubPolicy{Owner: "org", Name: "repo"}},
		},
	}

	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"api.github.com", "other.example.com"},
		Broker:       broker,
		PathPolicies: policies,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			},
		},
	}, upstream.Listener.Addr().String())
	client := proxyClient(srv.URL)

	// Request to other.example.com (no policy) with its placeholder → must pass through.
	req, _ := http.NewRequest(http.MethodGet, "http://other.example.com/arbitrary/path/anything", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+recOther.Placeholder)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("no-policy host: want 200 (unrestricted), got %d", resp.StatusCode)
	}
	// Swap must fire: upstream receives the real token.
	got, ok := receiveOrTimeout(authCh)
	if !ok {
		t.Fatal("upstream never received request; host with no policy must pass through")
	}
	if want := "Bearer " + realToken; got != want {
		t.Errorf("swap did not fire for no-policy host: upstream Authorization = %q, want %q", got, want)
	}
}

// TestD_PDE16_GenericGlobPolicy verifies the generic paths: [] HostPolicy
// on a non-GitHub host: allowed patterns pass, denied patterns 403, non-canonical
// paths 403, and method-specific patterns correctly gate on method.
//
// Mutation evidence:
//   - Replace matchGlobSegs with always-true → all denied cases return 200 (fail).
//   - Replace matchGlobSegs with always-false → all allowed cases return 403 (fail).
//   - Remove isCanonicalPath check → non-canonical case returns 200 (fail).
//   - Flip method comparison to EqualFold(method, gp.method) being inverted
//     → method-mismatch case returns 200 (fail).
func TestD_PDE16_GenericGlobPolicy(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(92)
	const realToken = "gl_personal_token"

	rec, err := broker.RegisterPlaceholder(sid, "gitlab.example.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	// Two method-specific patterns to test method enforcement and default-deny.
	// No ** wildcard so that method-denied cases stay denied.
	p1, err := mitm.CompileGlobPattern("GET /v4/projects/*/merge_requests")
	if err != nil {
		t.Fatalf("CompileGlobPattern p1: %v", err)
	}
	p2, err := mitm.CompileGlobPattern("POST /v4/projects/*/statuses")
	if err != nil {
		t.Fatalf("CompileGlobPattern p2: %v", err)
	}

	policies := mitm.PathPolicies{
		rec.Placeholder: {
			"gitlab.example.com": {Patterns: []mitm.GlobPattern{p1, p2}},
		},
	}

	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"gitlab.example.com"},
		Broker:       broker,
		PathPolicies: policies,
	}, upstream.Listener.Addr().String())
	client := proxyClient(srv.URL)

	// Allowed cases: run sequentially (separate requests, drain authCh after each).
	t.Run("merge-requests-GET-allowed", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://gitlab.example.com/v4/projects/123/merge_requests", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("merge-requests GET: want 200, got %d", resp.StatusCode)
		}
		if _, ok := receiveOrTimeout(authCh); !ok {
			t.Error("merge-requests GET: upstream never received request")
		}
	})

	t.Run("statuses-POST-allowed", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://gitlab.example.com/v4/projects/456/statuses", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("statuses POST: want 200, got %d", resp.StatusCode)
		}
		if _, ok := receiveOrTimeout(authCh); !ok {
			t.Error("statuses POST: upstream never received request")
		}
	})

	// Denied cases: sequential, so authCh is clean from prior allowed requests.
	deniedCases := []struct {
		name   string
		method string
		path   string
		raw    bool // bypass url.Parse for traversal tests
	}{
		// Wrong method for p1 (GET-only): POST denied.
		{"merge-requests-POST-denied", http.MethodPost, "/v4/projects/123/merge_requests", false},
		// Wrong method for p2 (POST-only): GET denied.
		{"statuses-GET-denied", http.MethodGet, "/v4/projects/123/statuses", false},
		// Path outside any pattern — default-deny.
		{"outside-deny", http.MethodGet, "/v4/users/1", false},
		// Non-canonical traversal — rejected by isCanonicalPath pre-check.
		{"traversal-denied", http.MethodGet, "/v4/projects/../users/1", true},
	}
	for _, tc := range deniedCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			if tc.raw {
				req = newRawURLRequest(tc.method, "gitlab.example.com", tc.path, "", "Bearer "+rec.Placeholder)
			} else {
				req, _ = http.NewRequest(tc.method, "http://gitlab.example.com"+tc.path, http.NoBody)
				req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: want 403, got %d", tc.name, resp.StatusCode)
			}
			if got, ok := receiveOrTimeout(authCh); ok {
				t.Errorf("%s: upstream received denied request; Authorization=%q", tc.name, got)
			}
		})
	}
}

// TestD_PDE16_DenyBeforeSwap is the security-critical ordering assertion:
// for a request denied by PathPolicies, the real token must NEVER be injected
// into the request — the deny handler fires BEFORE the credential swap handler.
//
// This test uses PathPolicies directly (not the AllowedRepo shim) to assert
// the ordering invariant on the new enforcement path.
//
// Mutation evidence: register the deny DoFunc AFTER the swap DoFunc in New()
// → the swap fires first, the denied path receives the real token, authCh
// receives a value, and both assertions below fail.
func TestD_PDE16_DenyBeforeSwap(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(93)
	const realToken = "ghp_must_never_reach_upstream"

	rec, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	policies := mitm.PathPolicies{
		rec.Placeholder: {
			"api.github.com": {GitHub: &mitm.GitHubPolicy{Owner: "secorg", Name: "secrepo"}},
		},
	}

	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"api.github.com"},
		Broker:       broker,
		PathPolicies: policies,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			},
		},
	}, upstream.Listener.Addr().String())
	client := proxyClient(srv.URL)

	// /user/repos is not in the GitHub allowlist for secorg/secrepo.
	// The real token must never be forwarded to upstream.
	req, _ := http.NewRequest(http.MethodGet, "http://api.github.com/user/repos", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("deny-before-swap: want 403 on denied path, got %d", resp.StatusCode)
	}
	// Critical: upstream must never receive the request — the deny intercepted it.
	if got, ok := receiveOrTimeout(authCh); ok {
		if got == "Bearer "+realToken {
			t.Errorf("deny-before-swap: REAL TOKEN reached upstream on denied path — swap fired before deny (ordering invariant violated)")
		} else {
			t.Errorf("deny-before-swap: upstream received request on denied path (auth=%q) — deny did not fire", got)
		}
	}
}

// TestD_PDE16_CompileGlobPatternValidation verifies that CompileGlobPattern
// rejects patterns with invalid segment characters (enforcing segmentOK rigor).
//
// Mutation evidence: remove the segmentOK check from CompileGlobPattern →
// the invalid-segment case returns nil error (fail).
func TestD_PDE16_CompileGlobPatternValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern string
		wantErr bool
	}{
		{"/v4/projects/*/issues", false},    // valid
		{"GET /v4/repos/**", false},         // valid with method prefix
		{"/v4/**", false},                   // valid double-star
		{"noslash", true},                   // must start with /
		{"/v4/pro%jects/issues", true},      // % not in segmentOK
		{"/v4/projects/issues;extra", true}, // ; not in segmentOK
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.pattern, func(t *testing.T) {
			t.Parallel()
			_, err := mitm.CompileGlobPattern(tc.pattern)
			if tc.wantErr && err == nil {
				t.Errorf("CompileGlobPattern(%q): want error, got nil", tc.pattern)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("CompileGlobPattern(%q): want nil error, got %v", tc.pattern, err)
			}
		})
	}
}

// TestD_PDE16_GitHubAllDigitsAndMethodPreserved verifies that the GitHub
// built-in policy (via PathPolicies) still enforces the allDigits guard on
// /stacks/{id} and method-scoping on /repos paths — proving gitHubPathAllowed
// is called verbatim, not replaced by a laxer glob.
//
// Mutation evidence: replace the gitHubPathAllowed call with a trivial
// strings.HasPrefix check → /stacks/notanumber and wrong-method cases return
// 200 instead of 403 (fail).
func TestD_PDE16_GitHubAllDigitsAndMethodPreserved(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(94)
	const realToken = "ghp_method_guard_token"

	rec, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	policies := mitm.PathPolicies{
		rec.Placeholder: {
			"api.github.com": {GitHub: &mitm.GitHubPolicy{Owner: "acme", Name: "myrepo"}},
		},
	}

	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"api.github.com"},
		Broker:       broker,
		PathPolicies: policies,
	}, upstream.Listener.Addr().String())
	client := proxyClient(srv.URL)

	// Run sequentially so authCh is clean between requests.
	// allDigits guard: non-numeric stack ID must be denied.
	t.Run("stacks-non-digit-denied", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/stacks/notanumber/add", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("stacks-non-digit: want 403, got %d", resp.StatusCode)
		}
		if got, ok := receiveOrTimeout(authCh); ok {
			t.Errorf("stacks-non-digit: upstream received denied request; Authorization=%q", got)
		}
	})

	// allDigits guard: numeric stack ID allowed.
	t.Run("stacks-digit-allowed", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/stacks/42/add", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("stacks-digit: want 200, got %d", resp.StatusCode)
		}
		receiveOrTimeout(authCh) // drain
	})

	// Method guard: DELETE on /repos/{owner}/{repo}/pulls is not allowed.
	t.Run("pulls-DELETE-denied", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, "http://api.github.com/repos/acme/myrepo/pulls", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("pulls-DELETE: want 403, got %d", resp.StatusCode)
		}
		if got, ok := receiveOrTimeout(authCh); ok {
			t.Errorf("pulls-DELETE: upstream received denied request; Authorization=%q", got)
		}
	})

	// Method guard: GET on /repos/{owner}/{repo}/releases is allowed.
	t.Run("releases-GET-allowed", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://api.github.com/repos/acme/myrepo/releases", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("releases-GET: want 200, got %d", resp.StatusCode)
		}
		receiveOrTimeout(authCh) // drain
	})

	// Method guard: DELETE on /repos/{owner}/{repo}/releases (list) is not allowed.
	t.Run("releases-list-DELETE-denied", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, "http://api.github.com/repos/acme/myrepo/releases", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("releases-DELETE: want 403, got %d", resp.StatusCode)
		}
		if got, ok := receiveOrTimeout(authCh); ok {
			t.Errorf("releases-DELETE: upstream received denied request; Authorization=%q", got)
		}
	})
}

// TestD_PDE16_GlobStarStar verifies that ** as a whole segment matches zero or
// more segments — exercising the recursive matchGlobSegs path.
//
// Mutation evidence: replace ** handling in matchGlobSegs with single-segment
// match → deep-path case returns 403 instead of 200 (fail).
func TestD_PDE16_GlobStarStar(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	broker := cred.NewBroker()
	sid := newSandboxID(96)
	const realToken = "gl_star_star_token"

	rec, err := broker.RegisterPlaceholder(sid, "gitlab.example.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	pStar, err := mitm.CompileGlobPattern("/v4/projects/**")
	if err != nil {
		t.Fatalf("CompileGlobPattern: %v", err)
	}

	policies := mitm.PathPolicies{
		rec.Placeholder: {"gitlab.example.com": {Patterns: []mitm.GlobPattern{pStar}}},
	}
	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"gitlab.example.com"},
		Broker:       broker,
		PathPolicies: policies,
	}, upstream.Listener.Addr().String())
	client := proxyClient(srv.URL)

	// ** matches deep paths.
	for _, p := range []string{
		"/v4/projects/123",
		"/v4/projects/123/issues",
		"/v4/projects/123/issues/1/notes",
	} {
		p := p
		t.Run(p, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "http://gitlab.example.com"+p, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: want 200 (**-matched), got %d", p, resp.StatusCode)
			}
			receiveOrTimeout(authCh) // drain
		})
	}

	// Outside /v4/projects/ — default-deny.
	t.Run("outside-deny", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://gitlab.example.com/v4/users/1", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("outside /v4/projects/: want 403, got %d", resp.StatusCode)
		}
		if got, ok := receiveOrTimeout(authCh); ok {
			t.Errorf("outside: upstream received denied request; Authorization=%q", got)
		}
	})
}

// TestD_PDE16_GraphQLClosedViaPathPolicies verifies that /graphql is denied
// when using the PathPolicies GitHub policy (belt-and-suspenders closure):
// gitHubPathAllowed returns true for /graphql, but the subsequent GraphQL
// deny-all handler catches it before the credential swap.
//
// Mutation evidence: remove the hasAnyGitHubPolicy guard that registers the
// GraphQL deny-all handler → /graphql returns 200 and upstream receives the
// request (fail).
func TestD_PDE16_GraphQLClosedViaPathPolicies(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(95)
	const realToken = "ghp_graphql_via_policies_token"

	rec, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	policies := mitm.PathPolicies{
		rec.Placeholder: {
			"api.github.com": {GitHub: &mitm.GitHubPolicy{Owner: "org", Name: "repo"}},
		},
	}

	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"api.github.com"},
		Broker:       broker,
		PathPolicies: policies,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			},
		},
	}, upstream.Listener.Addr().String())
	client := proxyClient(srv.URL)

	req, _ := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+rec.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("graphql via PathPolicies: want 403, got %d", resp.StatusCode)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("graphql via PathPolicies: upstream received request; Authorization=%q (must be denied before swap)", got)
	}
}

// TestD_PDE16_AllowedRepoShimCompatibility confirms the AllowedRepo shim
// still works (wildcard placeholder key "") so all existing callers compile
// and behave identically to before D-PDE-16. This is regression-coverage for
// the compat bridge.
//
// Mutation evidence: remove the wildcard key "" fallback from lookupPolicy →
// the shim path never finds a policy, AllowedRepo is effectively disabled,
// /user/repos returns 200 instead of 403 (fail).
func TestD_PDE16_AllowedRepoShimCompatibility(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	// newGitHubAllowedRepoProxy uses AllowedRepo (the compat shim path).
	proxy, _, recAPI, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	// Denied path — must still 403 via the shim.
	req, _ := http.NewRequest(http.MethodGet, "http://api.github.com/user/repos", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("shim compat: want 403 on /user/repos (AllowedRepo still restricts), got %d", resp.StatusCode)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("shim compat: upstream received request on denied path; Authorization=%q", got)
	}
}

// TestLookupPolicy_BogusPlaceholderKeyNotFound verifies that a PathPolicies
// map whose ONLY entry is stored under a non-"" placeholder key (e.g. "x")
// does NOT match a request carrying a DIFFERENT placeholder.
//
// This is the MITM-level proof that the guard==enforcement equivalence holds:
// if the guard were still an all-keys loop it would accept "x"-keyed configs,
// but lookupPolicy would still return not-found for them — confirming that the
// all-keys guard was the lie, not the enforcement.
//
// Mutation evidence: if the MITM lookupPolicy were changed to search all keys
// (not just pp[placeholder] then pp[""]), this test would return found=true,
// which would be a new MITM-level bypass — so the test also guards lookupPolicy
// against that direction of regression.
func TestLookupPolicy_BogusPlaceholderKeyNotFound(t *testing.T) {
	// A policy stored under "x" — as an attacker would supply.
	pp := mitm.PathPolicies{
		"x": {"api.github.com": mitm.HostPolicy{
			Patterns: nil, // just needs to be present
		}},
	}
	// A request arrives with placeholder "deadbeef1234" (a different key).
	_, found := mitm.LookupPolicyForTest(pp, "deadbeef1234", "api.github.com")
	if found {
		t.Fatal("lookupPolicy: bogus-key policy matched a different placeholder — enforcement gap detected")
	}

	// Also verify that "" key (wildcard) IS found when present.
	pp2 := mitm.PathPolicies{
		"": {"api.github.com": mitm.HostPolicy{}},
	}
	_, found2 := mitm.LookupPolicyForTest(pp2, "deadbeef1234", "api.github.com")
	if !found2 {
		t.Fatal("lookupPolicy: wildcard key \"\" should match any placeholder but did not")
	}
}

// ---------------------------------------------------------------------------
// T7: runtime AllowHost — MutableAllowSet plumbing
// ---------------------------------------------------------------------------

// TestT7_AC1_AllowHostAdmitsDynamicHost verifies that a host added via
// Proxy.AllowHost after New() transitions from denied to allowed without
// rebuilding the proxy or dropping any in-flight connections.
//
// Mutation evidence: comment out the allowSet.Has(lh) branch in HandleConnect
// so it always falls through to RejectConnect → the "before Add" assertion
// no longer sees a 403 and the test fails.
func TestT7_AC1_AllowHostAdmitsDynamicHost(t *testing.T) {
	t.Parallel()

	// TLS upstream — the MITM proxy mints a leaf cert and the outbound
	// connection uses InsecureSkipVerify (same pattern as TestT2_AC3).
	tlsUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(tlsUpstream.Close)

	var mu sync.Mutex
	var egressDenials []string
	collect := func(host, verdict, _ string, _ time.Time) {
		if verdict == "deny" {
			mu.Lock()
			egressDenials = append(egressDenials, host)
			mu.Unlock()
		}
	}

	p, err := mitm.New(mitm.Config{
		SandboxID:    newSandboxID(207),
		AllowedHosts: []string{"static.example.com"}, // dynamic host intentionally absent
		Broker:       cred.NewBroker(),
		OnEgress:     collect,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, tlsUpstream.Listener.Addr().String())
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only
		},
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	proxyServer := httptest.NewServer(p)
	t.Cleanup(proxyServer.Close)

	proxyURL, _ := url.Parse(proxyServer.URL)
	pool := x509.NewCertPool()
	pool.AddCert(p.CACert())
	httpsClient := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 5 * time.Second,
	}

	const dynamicHost = "dynamic.example.com"

	// Before AllowHost: CONNECT to dynamic host must be rejected; the client
	// sees an error (goproxy closes the connection after 403, which Go's
	// http.Client surfaces as a non-nil error).
	_, err = httpsClient.Get("https://" + dynamicHost + "/ping")
	if err == nil {
		t.Fatal("T7-AC1: expected CONNECT rejection before AllowHost, got nil error")
	}

	// Verify OnEgress recorded the denial.
	mu.Lock()
	denialsBefore := len(egressDenials)
	mu.Unlock()
	if denialsBefore == 0 {
		t.Error("T7-AC1: OnEgress did not record a deny before AllowHost")
	}

	// Admit the host at runtime — no proxy rebuild.
	p.AllowHost(dynamicHost)

	// After AllowHost: CONNECT must be accepted. The TLS handshake may fail
	// (stub upstream is not serving the right SNI) but the proxy itself must
	// not emit a new deny record for dynamic.example.com.
	_, _ = httpsClient.Get("https://" + dynamicHost + "/ping") // error expected (TLS), not CONNECT 403

	mu.Lock()
	newDenials := egressDenials[denialsBefore:]
	mu.Unlock()
	for _, h := range newDenials {
		if h == dynamicHost {
			t.Errorf("T7-AC1: proxy emitted a deny for %q after AllowHost — host not admitted", dynamicHost)
		}
	}
}

// TestT7_AC2_AllowHostDoesNotRebuildProxy verifies that AllowHost is a pure
// set mutation: the CA certificate pointer is identical before and after the
// call, and pre-existing allowed hosts continue to work across the Add.
//
// Mutation evidence: replace p.allowSet.Add with mitm.New reconstruction →
// CACert() returns a different pointer and the equality assertion fails.
func TestT7_AC2_AllowHostDoesNotRebuildProxy(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	p, err := mitm.New(mitm.Config{
		SandboxID:    newSandboxID(208),
		AllowedHosts: []string{"pre-existing.example.com"},
		Broker:       cred.NewBroker(),
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			},
		},
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}

	// Capture CA pointer before Add.
	caBefore := p.CACert()

	// Add a new host.
	p.AllowHost("newly-added.example.com")

	// CA pointer must be unchanged — no proxy rebuild.
	caAfter := p.CACert()
	if caBefore != caAfter {
		t.Error("T7-AC2: CACert() pointer changed after AllowHost — proxy was rebuilt unexpectedly")
	}

	// Pre-existing allowed host must still work (plain HTTP through proxy).
	proxyServer := httptest.NewServer(p)
	t.Cleanup(proxyServer.Close)

	req, _ := http.NewRequest(http.MethodGet, "http://pre-existing.example.com/check", nil)
	resp, err := proxyClient(proxyServer.URL).Do(req)
	if err != nil {
		t.Fatalf("T7-AC2: pre-existing host request failed after AllowHost: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("T7-AC2: pre-existing host: want 200, got %d", resp.StatusCode)
	}
}

// ============================================================
// SecretHostSuffixes tests: suffix-matched secret hosts
// ============================================================

// TestMatchesDotSuffix_DotBoundarySafety verifies the dot-boundary invariant of
// the suffix matching helper. A naive strings.HasSuffix without the leading dot
// would match "evilcursor.sh" against suffix "cursor.sh" — with the dot the
// match is correctly rejected. The operator's ruling: use ".cursor.sh" so only
// proper sub-domains match.
func TestMatchesDotSuffix_DotBoundarySafety(t *testing.T) {
	t.Parallel()
	suffixes := []string{".cursor.sh"}
	cases := []struct {
		host string
		want bool
	}{
		{"api2.cursor.sh", true},
		{"agentn.global.api5.cursor.sh", true},
		{"api3.cursor.sh", true},
		// Negative: no dot boundary
		{"evilcursor.sh", false},
		// Negative: suffix as subdomain of attacker domain
		{"cursor.sh.attacker.com", false},
		// Negative: apex domain itself (no sub-domain)
		{"cursor.sh", false},
		// Unrelated domain
		{"api.anthropic.com", false},
	}
	for _, tc := range cases {
		got := mitm.MatchesDotSuffixForTest(tc.host, suffixes)
		if got != tc.want {
			t.Errorf("MatchesDotSuffix(%q, %v) = %v, want %v", tc.host, suffixes, got, tc.want)
		}
	}
}

// TestProxy_SecretHostSuffix_SwapsInferenceHost proves that a host matched by
// SecretHostSuffixes (not in SecretHosts exactly) is MITM'd and the bearer
// placeholder is swapped for the real token. This is the S11 cursor inference
// fix: api2.cursor.sh is in SecretHosts; agentn.global.api5.cursor.sh is only
// covered by the ".cursor.sh" suffix and must also be swapped.
//
// The broker is registered for the primary host only (api2.cursor.sh); the
// suffix-matched host uses broker.Resolve (unscoped) which resolves by
// placeholder value alone — safe because the proxy is per-sandbox and the
// placeholder is 256-bit random.
func TestProxy_SecretHostSuffix_SwapsInferenceHost(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)

	broker := cred.NewBroker()
	sid := newSandboxID(77)
	const primaryHost = "api2.cursor.sh"
	const inferenceHost = "agentn.global.api5.cursor.sh"
	const realToken = "real-cursor-jwt-INFERENCE"

	// Register only the primary host — inference host is NOT registered.
	rec, err := broker.RegisterPlaceholder(sid, primaryHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID:          sid,
		SecretHosts:        []string{primaryHost},
		SecretHostSuffixes: []string{".cursor.sh"},
		Broker:             broker,
		AllowAll:           true,
	}, upstream.Listener.Addr().String())
	defer proxyServer.Close()

	client := proxyClient(proxyServer.URL)

	t.Run("suffix_host_swapped", func(t *testing.T) {
		// The inference host is covered by the suffix. The MITM must intercept
		// it and swap the placeholder for the real token.
		req, _ := http.NewRequest(http.MethodGet, "http://"+inferenceHost+"/v1/chat", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do (inference): %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		got := receiveWithTimeout(t, authCh)
		if want := "Bearer " + realToken; got != want {
			t.Errorf("suffix host: upstream Authorization = %q, want %q (swap did not fire for suffix-matched host)", got, want)
		}
	})

	t.Run("primary_host_still_swapped", func(t *testing.T) {
		// The exact SecretHosts entry must still work.
		req, _ := http.NewRequest(http.MethodGet, "http://"+primaryHost+"/v1/control", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+rec.Placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do (primary): %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		got := receiveWithTimeout(t, authCh)
		if want := "Bearer " + realToken; got != want {
			t.Errorf("primary host: upstream Authorization = %q, want %q", got, want)
		}
	})

	t.Run("negative_evilcursor_sh_not_matched", func(t *testing.T) {
		// "evilcursor.sh" must NOT match the ".cursor.sh" suffix — no dot boundary.
		// Under AllowAll it is tunneled, so the Authorization header must NOT
		// be rewritten.
		const evilHost = "evilcursor.sh"
		const fakePlaceholder = "PLACEHOLDER-evil-not-registered"
		req, _ := http.NewRequest(http.MethodGet, "http://"+evilHost+"/steal", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+fakePlaceholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do (evil): %v", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		got := receiveWithTimeout(t, authCh)
		if want := "Bearer " + fakePlaceholder; got != want {
			t.Errorf("evil host: upstream Authorization = %q, want %q (suffix matched incorrectly)", got, want)
		}
	})
}
