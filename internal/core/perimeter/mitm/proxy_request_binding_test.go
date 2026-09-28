package mitm_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
)

// newCONNECTSpoofProxy builds a proxy that allows github.com, api.github.com,
// and spoofTarget. All TCP is routed to a TLS upstream with InsecureSkipVerify.
// Returns the proxy server, the exposed *mitm.Proxy (for CACert), and the
// api.github.com placeholder record.
func newCONNECTSpoofProxy(
	t *testing.T,
	tlsUpstreamAddr string,
	spoofTarget string,
	sandboxByte byte,
) (*httptest.Server, *mitm.Proxy, cred.PlaceholderRecord) {
	t.Helper()
	const realToken = "ghp_real_connect_spoof"
	broker := cred.NewBroker()
	sid := newSandboxID(sandboxByte)
	if _, err := broker.RegisterPlaceholder(sid, "github.com", realToken); err != nil {
		t.Fatalf("RegisterPlaceholder github.com: %v", err)
	}
	recAPI, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder api.github.com: %v", err)
	}
	allowed := []string{"github.com", "api.github.com"}
	if spoofTarget != "" {
		allowed = append(allowed, spoofTarget)
	}
	cfg := mitm.Config{
		SandboxID:    sid,
		AllowedHosts: allowed,
		Broker:       broker,
		AllowedRepo:  "IniZio/nexus",
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, tlsUpstreamAddr)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only redirect
		},
	}
	p, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv, p, recAPI
}

// connectSpoofClient builds an http.Client that routes HTTPS through the proxy
// and trusts the proxy's CA cert (to accept MITM leaf certificates).
func connectSpoofClient(proxyURL string, caCert *x509.Certificate) *http.Client {
	u, _ := url.Parse(proxyURL)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(u),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 5 * time.Second,
	}
}

// TestHostSpoof_CONNECT_DeniedNoToken demonstrates the host-spoof attack:
// the guest CONNECTs to the MITM'd allowed host "allowedA.test:443",
// but sends an inner request with Host: api.github.com (spoofed).
// Without the host-bind guard, reqHost() returns "api.github.com", the swap
// fires and the real GitHub token reaches the attacker-controlled host A.
//
// RED (before fix): upstream A receives "Bearer ghp_real_connect_spoof" —
// the test fails with want 403, got 200 AND upstream received real token.
// GREEN (after fix): connectTarget guard fires (hdrHost "api.github.com"
// != ct.host "alloweda.test") → 403, upstream receives nothing.
func TestHostSpoof_CONNECT_DeniedNoToken(t *testing.T) {
	t.Parallel()

	authCh := make(chan string, 4)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case authCh <- r.Header.Get("Authorization"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"data":{"viewer":{"login":"x"}}}`) //nolint:errcheck
	}))
	t.Cleanup(upstream.Close)

	proxy, p, recAPI := newCONNECTSpoofProxy(t, upstream.Listener.Addr().String(), "alloweda.test", 108)
	client := connectSpoofClient(proxy.URL, p.CACert())

	req, err := http.NewRequest(http.MethodPost,
		"https://alloweda.test/graphql",
		strings.NewReader(`{"query":"query UserCurrent{viewer{login}}"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Host = "api.github.com"
	req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("CONNECT host-spoof: want 403, got %d", resp.StatusCode)
		}
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("CONNECT host-spoof: upstream received Authorization=%q (real token must NOT reach attacker host)", got)
	}
}

// TestHostSpoof_CONNECT_LegitSameHost_Allowed verifies that a legitimate
// CONNECT where the inner request Host matches the CONNECT target (api.github.com)
// passes the host-bind guard and the swap fires normally.
func TestHostSpoof_CONNECT_LegitSameHost_Allowed(t *testing.T) {
	t.Parallel()

	authCh := make(chan string, 4)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case authCh <- r.Header.Get("Authorization"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"data":{"viewer":{"login":"IniZio"}}}`) //nolint:errcheck
	}))
	t.Cleanup(upstream.Close)

	proxy, p, recAPI := newCONNECTSpoofProxy(t, upstream.Listener.Addr().String(), "", 109)
	client := connectSpoofClient(proxy.URL, p.CACert())

	req, err := http.NewRequest(http.MethodPost,
		"https://api.github.com/graphql",
		strings.NewReader(`{"query":"query UserCurrent{viewer{login}}"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do (HTTPS CONNECT legit): %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("CONNECT legit same-host: want 200, got %d", resp.StatusCode)
	}
	got, ok := receiveOrTimeout(authCh)
	if !ok {
		t.Errorf("CONNECT legit same-host: upstream never received request")
	} else if want := "Bearer ghp_real_connect_spoof"; got != want {
		t.Errorf("CONNECT legit same-host: upstream Authorization=%q, want %q", got, want)
	}
}

func newGQLProxyForCT(t *testing.T, upstreamAddr string, sandboxByte byte) (*httptest.Server, cred.PlaceholderRecord) {
	return newNexusProxy(t, upstreamAddr, "IniZio/nexus", sandboxByte)
}

func postGraphQL(t *testing.T, client *http.Client, proxyURL, placeholder, body, contentType string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+placeholder)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	return resp.StatusCode
}

const gqlViewerBody = `{"query":"query UserCurrent{viewer{login}}"}`

// TestGQL_QueryStringDenied proves the RawQuery/ForceQuery guard on
// api.github.com/graphql. It sends a body the allowlist ADMITS
// ({__typename}) with a valid Content-Type, so only the URL query string can
// cause the 403. If the guard is disabled, the proxy returns 200 and upstream
// receives the real token.
func TestGQL_QueryStringDenied(t *testing.T) {
	t.Parallel()
	const allowedBody = `{"query":"{__typename}"}`
	cases := []struct {
		name string
		url  string
		seed byte
	}{
		{"raw query smuggling node(id)", "http://api.github.com/graphql?query=%7Bnode(id:%22R_x%22)%7Bid%7D%7D", 102},
		{"force query (trailing ?)", "http://api.github.com/graphql?", 110},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			upstream, authCh := newGQLUpstream(t, `{"data":{"__typename":"Query"}}`)
			proxy, recAPI := newGQLProxyForCT(t, upstream.Listener.Addr().String(), tc.seed)
			client := proxyClient(proxy.URL)

			req, err := http.NewRequest(http.MethodPost, tc.url, strings.NewReader(allowedBody))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)
			req.Header.Set("Content-Type", "application/json")
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
				t.Errorf("%s: upstream received request (Authorization=%q); real token must not be emitted", tc.name, got)
			}
		})
	}
}

func TestGQL_FormURLEncodedDenied(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, `{}`)
	proxy, recAPI := newGQLProxyForCT(t, upstream.Listener.Addr().String(), 103)
	client := proxyClient(proxy.URL)

	if status := postGraphQL(t, client, proxy.URL, recAPI.Placeholder, "query=%7Bviewer%7Blogin%7D%7D", "application/x-www-form-urlencoded"); status != http.StatusForbidden {
		t.Errorf("form-urlencoded: want 403, got %d", status)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("form-urlencoded: upstream received request (Authorization=%q)", got)
	}
}

func TestGQL_TextPlainDenied(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, `{}`)
	proxy, recAPI := newGQLProxyForCT(t, upstream.Listener.Addr().String(), 104)
	client := proxyClient(proxy.URL)

	if status := postGraphQL(t, client, proxy.URL, recAPI.Placeholder, gqlViewerBody, "text/plain"); status != http.StatusForbidden {
		t.Errorf("text/plain: want 403, got %d", status)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("text/plain: upstream received request (Authorization=%q)", got)
	}
}

func TestGQL_MissingContentTypeDenied(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, `{}`)
	proxy, recAPI := newGQLProxyForCT(t, upstream.Listener.Addr().String(), 105)
	client := proxyClient(proxy.URL)

	if status := postGraphQL(t, client, proxy.URL, recAPI.Placeholder, gqlViewerBody, ""); status != http.StatusForbidden {
		t.Errorf("missing CT: want 403, got %d", status)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("missing CT: upstream received request (Authorization=%q)", got)
	}
}

func TestGQL_ApplicationJSONAllowed(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, `{"data":{"viewer":{"login":"IniZio"}}}`)
	proxy, recAPI := newGQLProxyForCT(t, upstream.Listener.Addr().String(), 106)
	client := proxyClient(proxy.URL)

	if status := postGraphQL(t, client, proxy.URL, recAPI.Placeholder, gqlViewerBody, "application/json"); status != http.StatusOK {
		t.Errorf("application/json: want 200, got %d", status)
	}
	got, ok := receiveOrTimeout(authCh)
	if !ok {
		t.Errorf("application/json: upstream never received request")
	} else if want := "Bearer ghp_real_r30_test"; got != want {
		t.Errorf("application/json: upstream Authorization=%q, want %q", got, want)
	}
}

func TestGQL_ApplicationJSONCharsetUTF8Allowed(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, `{"data":{"viewer":{"login":"IniZio"}}}`)
	proxy, recAPI := newGQLProxyForCT(t, upstream.Listener.Addr().String(), 107)
	client := proxyClient(proxy.URL)

	if status := postGraphQL(t, client, proxy.URL, recAPI.Placeholder, gqlViewerBody, "application/json; charset=utf-8"); status != http.StatusOK {
		t.Errorf("application/json; charset=utf-8: want 200, got %d", status)
	}
	got, ok := receiveOrTimeout(authCh)
	if !ok {
		t.Errorf("application/json; charset=utf-8: upstream never received request")
	} else if want := "Bearer ghp_real_r30_test"; got != want {
		t.Errorf("application/json; charset=utf-8: upstream Authorization=%q, want %q", got, want)
	}
}
