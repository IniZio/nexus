// Package mitm implements the per-sandbox L7 TLS-MITM proxy.
//
// Each sandbox gets its own certificate authority (CA). The CA is a trust
// anchor that the sandbox bootstrap path MUST inject into the guest trust
// store before any HTTPS traffic flows (e.g. by appending the DER-encoded
// cert from [Proxy.CACert] to /etc/ssl/certs/ca-certificates.crt or
// equivalent inside the guest). Without this, the guest's HTTPS clients will
// reject the leaf certificates minted by this proxy.
//
// # Architecture
//
//	guest HTTP client
//	  → proxy (this package): HandleConnect decides allow/reject by hostname
//	      → allowed: MITM (goproxy signs leaf cert with per-sandbox CA)
//	          → OnRequest swaps placeholder Bearer/Basic with real token (broker)
//	          → forwarded to real upstream
//	      → non-allowed: reject (CONNECT receives 403)
//
// The real token is NEVER written to any log. Audit entries record only the
// allow/deny decision and the target hostname.
package mitm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/elazarl/goproxy"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// Proxy is the per-sandbox L7 TLS-MITM proxy. It wraps a goproxy.ProxyHttpServer
// with:
//   - A per-sandbox CA whose trust anchor ([Proxy.CACert]) the guest must import.
//   - A HandleConnect handler that enforces the hostname allowlist.
//   - An OnRequest handler that swaps placeholder tokens in Authorization
//     Bearer (gh) and Basic (git HTTPS) headers with the real broker token.
// Proxy implements [http.Handler]; run it behind an httptest.Server in tests
// or a standard net/http server in production.
//
// A zero-value Proxy is not usable; construct one via [New].
type Proxy struct {
	// ca is the per-sandbox CA certificate. Its Leaf field is always populated.
	// Exported via CACert for the sandbox bootstrap path to seed into the guest.
	ca    tls.Certificate
	inner *goproxy.ProxyHttpServer
}

// Config configures a new [Proxy].
type Config struct {
	// SandboxID identifies the sandbox this proxy serves. Used by the scoped
	// credential resolver to prevent cross-sandbox token theft.
	SandboxID domain.SandboxID

	// AllowedHosts is the set of hostnames the proxy will MITM and for which
	// it will perform bearer-token swap when AllowAll is false. Comparison is
	// case-insensitive. Non-listed hosts receive a CONNECT rejection unless
	// AllowAll is set.
	AllowedHosts []string

	// SecretHosts are MITM'd even when AllowAll is true, so a placeholder
	// token can be swapped without putting the host on AllowedHosts (which
	// would 403 every other destination). Non-secret hosts under AllowAll
	// are CONNECT-tunneled without TLS interception.
	SecretHosts []string

	// Broker is the host-side credential store. ResolveScoped is called for
	// each intercepted request bearing a placeholder Authorization header.
	Broker *cred.Broker

	// AllowAll makes the proxy permit EVERY CONNECT regardless of
	// AllowedHosts. SecretHosts are MITM'd; other hosts are tunneled
	// (real server cert, no swap). Temporary stance until interactive
	// per-connection approval exists; do not enable for credential-bearing
	// agent sandboxes where a curated allowlist is the safeguard.
	AllowAll bool

	// Logger is used for audit events (allow/deny decisions). If nil,
	// slog.Default() is used. The real token is NEVER passed to the logger.
	Logger *slog.Logger

	// Transport is the outbound http.Transport used to reach upstream servers.
	// If nil, goproxy's default transport is used. Tests may supply a
	// custom Transport with a DialContext that redirects to a stub server.
	Transport *http.Transport
}

// New creates a Proxy for a single sandbox. It generates a fresh per-sandbox
// CA; call [Proxy.CACert] to obtain the trust anchor and seed it into the guest.
func New(cfg Config) (*Proxy, error) {
	ca, err := generateCA()
	if err != nil {
		return nil, fmt.Errorf("mitm: generate per-sandbox CA: %w", err)
	}

	allowSet := make(map[string]struct{}, len(cfg.AllowedHosts)+len(cfg.SecretHosts))
	for _, h := range cfg.AllowedHosts {
		allowSet[strings.ToLower(h)] = struct{}{}
	}
	secretSet := make(map[string]struct{}, len(cfg.SecretHosts))
	for _, h := range cfg.SecretHosts {
		lh := strings.ToLower(h)
		secretSet[lh] = struct{}{}
		allowSet[lh] = struct{}{}
	}

	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	// mitmAction is the per-sandbox MITM action. It uses the sandbox's own CA
	// (not goproxy's built-in GoproxyCa) so each sandbox has an isolated trust
	// domain — a compromised guest cannot use another sandbox's CA.
	mitmAction := &goproxy.ConnectAction{
		Action:    goproxy.ConnectMitm,
		TLSConfig: goproxy.TLSConfigFromCA(&ca),
	}

	inner := goproxy.NewProxyHttpServer()
	inner.Verbose = false // goproxy verbose output is suppressed; we use slog

	if cfg.Transport != nil {
		inner.Tr = cfg.Transport
	}

	sandboxID := cfg.SandboxID
	broker := cfg.Broker
	allowAll := cfg.AllowAll

	// HandleConnect:
	//   secret host    → MITM (swap possible)
	//   allow-all else → CONNECT tunnel (real cert, no swap)
	//   allowed host   → MITM
	//   other host     → reject
	inner.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		hostname := stripHost(host)
		lh := strings.ToLower(hostname)
		if _, ok := secretSet[lh]; ok {
			log.Info("mitm: CONNECT allowed (secret host)", "sandbox", sandboxID, "host", hostname)
			return mitmAction, host
		}
		if allowAll {
			log.Info("mitm: CONNECT tunneled (allow-all)", "sandbox", sandboxID, "host", hostname)
			return goproxy.OkConnect, host
		}
		if _, ok := allowSet[lh]; ok {
			log.Info("mitm: CONNECT allowed", "sandbox", sandboxID, "host", hostname)
			return mitmAction, host
		}
		log.Info("mitm: CONNECT rejected", "sandbox", sandboxID, "host", hostname)
		return goproxy.RejectConnect, host
	}))

	// OnRequest swaps placeholder Authorization tokens with real tokens.
	// Bearer (gh CLI) and Basic (git HTTPS) are both handled (D-PD-23).
	// Swap only for allowlisted / secret hosts.
	inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		host := reqHost(req)
		if _, ok := allowSet[strings.ToLower(host)]; !ok {
			return req, nil
		}
		swapped, ok := swapAuthorization(req.Header.Get("Authorization"), sandboxID, broker)
		if !ok {
			return req, nil
		}
		req2 := req.Clone(req.Context())
		req2.Header.Set("Authorization", swapped)
		log.Info("mitm: credential swapped", "sandbox", sandboxID, "host", host)
		return req2, nil
	})

	return &Proxy{ca: ca, inner: inner}, nil
}

// CACert returns the parsed CA certificate that the guest trust store must
// import before HTTPS traffic flows through this proxy. The caller is
// responsible for delivering it into the guest (e.g. appending its PEM
// encoding to the guest's system CA bundle).
//
// The returned pointer is the pre-parsed Leaf field of the per-sandbox
// tls.Certificate; it is safe to read concurrently but must not be modified.
func (p *Proxy) CACert() *x509.Certificate {
	return p.ca.Leaf
}

// ServeHTTP implements [http.Handler] by delegating to the goproxy server.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.inner.ServeHTTP(w, r)
}

// generateCA mints a fresh ECDSA P-256 CA certificate suitable for use as a
// goproxy MITM trust anchor. The CA is self-signed and valid for 10 years.
// The Leaf field of the returned tls.Certificate is always populated.
func generateCA() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate CA serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"nexus3"},
			CommonName:   "nexus3 per-sandbox MITM CA",
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create CA cert: %w", err)
	}

	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse CA cert: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// swapAuthorization rewrites an Authorization header whose token/password
// field is a broker placeholder. Returns ("", false) when there is nothing
// to swap. The real token is never logged.
func swapAuthorization(authHeader string, sandboxID domain.SandboxID, broker *cred.Broker) (string, bool) {
	if broker == nil || authHeader == "" {
		return "", false
	}
	switch {
	case strings.HasPrefix(authHeader, "Bearer "):
		placeholder := strings.TrimPrefix(authHeader, "Bearer ")
		realToken, ok := broker.ResolveScoped(placeholder, sandboxID)
		if !ok {
			return "", false
		}
		return "Bearer " + realToken, true
	case strings.HasPrefix(authHeader, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
		if err != nil {
			return "", false
		}
		user, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return "", false
		}
		realToken, ok := broker.ResolveScoped(pass, sandboxID)
		if !ok {
			return "", false
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+realToken)), true
	default:
		return "", false
	}
}

// stripHost extracts the hostname without port from a "host:port" string.
// If addr has no port (e.g. it is already a bare hostname), addr is returned
// unchanged.
func stripHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// reqHost returns the lowercase target hostname for an HTTP request, stripping
// any port. For MITM'd HTTPS the Host header is authoritative; for plain
// proxy-forwarded HTTP the URL host is the fallback.
func reqHost(req *http.Request) string {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	return stripHost(host)
}

// Compile-time assertion: Proxy satisfies http.Handler.
var _ http.Handler = (*Proxy)(nil)
