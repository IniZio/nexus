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
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"path"
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
//
// Proxy implements [http.Handler]; run it behind an httptest.Server in tests
// or a standard net/http server in production.
//
// A zero-value Proxy is not usable; construct one via [New].
type Proxy struct {
	// ca is the per-sandbox CA certificate. Its Leaf field is always populated.
	// Exported via CACert for the sandbox bootstrap path to seed into the guest.
	ca    tls.Certificate
	inner *goproxy.ProxyHttpServer
	// allowSet is the mutable runtime allowlist of MITM-permitted hosts.
	// Seeded from Config.AllowedHosts ∪ Config.SecretHosts at construction.
	// T5/T6 callers use AllowHost to admit new hosts without rebuilding the proxy.
	allowSet *MutableAllowSet
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

	// PathPolicies is the per-(placeholder, host) path allowlist (D-PDE-16).
	// Key: placeholder token → host → HostPolicy. At enforcement time the
	// placeholder is extracted from the Authorization header (Bearer/token/
	// Basic). If no entry exists for (placeholder, host) the request is
	// allowed (host-only bind). PathPolicies entries take precedence over
	// AllowedRepo for any (placeholder, host) pair they cover.
	// Build HostPolicy values with CompileGlobPattern or GitHubPolicy.
	PathPolicies PathPolicies

	// AllowedRepo, when non-empty, enables the D-PD-36 per-request path
	// allowlist for GitHub hosts (github.com, api.github.com,
	// uploads.github.com). Only method+path combinations needed for the PR
	// and release flow are forwarded; all others are refused with HTTP 403
	// BEFORE any credential swap occurs. Format: "owner/repo" (case-sensitive).
	//
	// This is the ONLY control bounding the operator's full-scope GitHub token
	// for agent sandboxes. Leave empty only for human sandboxes (AllowAll
	// egress) that have no per-repo restriction requirement.
	//
	// Deprecated: prefer PathPolicies with GitHubPolicy for new callers.
	// New() folds AllowedRepo into PathPolicies under the wildcard placeholder
	// key "" so all existing call sites compile unchanged. TODO(T4/T6): migrate.
	AllowedRepo string

	// AllowedBranches is the list of git ref patterns the proxy will permit
	// on git-push receive-pack requests. Patterns follow git refspec glob
	// syntax (e.g. "refs/heads/nexus3/*"). Enforcement is implemented in
	// slice S1; this field is plumbing only — no filtering occurs here.
	// The resolved default (when empty at the call site) is
	// ["refs/heads/nexus3/*"] via domain.Envelope.ResolvedAllowedBranches.
	AllowedBranches []string

	// OnEgress, when non-nil, is called for every L7 egress verdict emitted
	// by this proxy. Wire it to the shared egress-decisions sink so
	// `nexus3 egress log` shows a unified MITM+netfilter stream. The hook is
	// called without any lock held; the caller is responsible for
	// concurrent-write safety (e.g. a sync.Mutex in the closure).
	OnEgress func(host, verdict, reason string, ts time.Time)

	// Logger is used for audit events (allow/deny decisions). If nil,
	// slog.Default() is used. The real token is NEVER passed to the logger.
	Logger *slog.Logger

	// Transport is the outbound http.Transport used to reach upstream servers.
	// If nil, goproxy's default transport is used. Tests may supply a
	// custom Transport with a DialContext that redirects to a stub server.
	Transport *http.Transport

	// SeedCACertPEM and SeedCAKeyPEM, when both non-empty, seed this Proxy's
	// CA from PEM-encoded material instead of minting a fresh one via
	// generateCA. This is the seam a hot-swap adopt path uses to continue
	// serving TLS interception with the SAME CA the guest already trusts —
	// generating a fresh CA here would invalidate every certificate the
	// guest has already pinned this boot (motive
	// nexus3-host-supervisor-hotswap, handoff.Payload.CA). Either both must
	// be set or both left empty; New returns an error for a partial pair.
	SeedCACertPEM []byte
	SeedCAKeyPEM  []byte
}

// GitHubPolicy pins all requests to one GitHub repository.
// Enforcement uses gitHubPathAllowed verbatim (method-aware, allDigits guard,
// canonical prefixes, /stacks numeric). NOT a glob rewrite — the full
// method+path intelligence is preserved.
type GitHubPolicy struct {
	Owner string // case-sensitive GitHub owner name
	Name  string // case-sensitive GitHub repository name
}

// GlobPattern is a compiled path glob for the generic HostPolicy.
// Pattern format: optional "METHOD " prefix (e.g. "GET "), then an absolute
// path where "*" as a whole segment matches exactly one path segment and "**"
// as a whole segment matches zero or more path segments. All other segments
// must be valid per segmentOK (ASCII letters, digits, dot, underscore, tilde,
// hyphen). Construct via CompileGlobPattern.
type GlobPattern struct {
	method string   // empty = any method; stored uppercase
	segs   []string // path split on "/", includes leading "" from "/x" → ["","x"]
}

// HostPolicy is the path restriction for one (placeholder, host) pair.
// Exactly one of GitHub or Patterns should be set.
//
// Enforcement order (always applied before the credential swap):
//  1. isCanonicalPath is checked first; non-canonical paths are denied.
//  2. Default-deny: the request must match the active policy; mismatches
//     receive HTTP 403 before any credential swap occurs.
type HostPolicy struct {
	GitHub   *GitHubPolicy // non-nil: GitHub built-in method-aware enforcement
	Patterns []GlobPattern // non-empty: generic default-deny glob matching
}

// PathPolicies maps a placeholder token → host → HostPolicy.
//
// At enforcement time the placeholder is extracted from the Authorization
// header (Bearer/token/Basic schemes). If no entry exists for (placeholder,
// host) the request is allowed (host-only bind; the swap handler may still fire).
//
// The wildcard key "" matches any placeholder and is used only by the
// AllowedRepo compatibility shim in New; production callers key on the exact
// placeholder string returned by the broker (one per secret bind).
type PathPolicies map[string]map[string]HostPolicy

// New creates a Proxy for a single sandbox. It generates a fresh per-sandbox
// CA; call [Proxy.CACert] to obtain the trust anchor and seed it into the guest.
func New(cfg Config) (*Proxy, error) {
	var ca tls.Certificate
	var err error
	switch {
	case len(cfg.SeedCACertPEM) == 0 && len(cfg.SeedCAKeyPEM) == 0:
		ca, err = generateCA()
		if err != nil {
			return nil, fmt.Errorf("mitm: generate per-sandbox CA: %w", err)
		}
	case len(cfg.SeedCACertPEM) == 0 || len(cfg.SeedCAKeyPEM) == 0:
		return nil, fmt.Errorf("mitm: SeedCACertPEM and SeedCAKeyPEM must both be set or both empty")
	default:
		ca, err = tls.X509KeyPair(cfg.SeedCACertPEM, cfg.SeedCAKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("mitm: parse seeded CA: %w", err)
		}
		if ca.Leaf, err = x509.ParseCertificate(ca.Certificate[0]); err != nil {
			return nil, fmt.Errorf("mitm: parse seeded CA leaf: %w", err)
		}
	}

	allowSet := NewMutableAllowSet(cfg.AllowedHosts...)
	secretSet := make(map[string]struct{}, len(cfg.SecretHosts))
	for _, h := range cfg.SecretHosts {
		lh := strings.ToLower(h)
		secretSet[lh] = struct{}{}
		allowSet.Add(lh)
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

	// D-PDE-16: build active path-policy map at construction time.
	// AllowedRepo compat shim is folded in under the wildcard placeholder key "".
	policies, err := buildPathPolicies(cfg.PathPolicies, cfg.AllowedRepo)
	if err != nil {
		return nil, err
	}

	// hasAnyGitHubPolicy is true when at least one HostPolicy.GitHub is set;
	// used to gate the belt-and-suspenders GraphQL deny-all handler.
	hasAnyGitHubPolicy := false
	for _, hostMap := range policies {
		for _, pol := range hostMap {
			if pol.GitHub != nil {
				hasAnyGitHubPolicy = true
				break
			}
		}
		if hasAnyGitHubPolicy {
			break
		}
	}

	// HandleConnect: broad-allow + selective MITM.
	//
	// Credentialed hosts MUST be in SecretHosts (secretSet), not AllowedHosts,
	// so they are intercepted BEFORE the allowAll tunnel path. Under open-egress
	// (AllowAll=true), AllowedHosts are shadowed by the tunnel and would NOT be
	// MITM'd — a host on AllowedHosts only receives the swap in closed-egress
	// mode. SecretHosts are checked first and always MITM'd regardless of
	// AllowAll, ensuring the placeholder is swapped for the real token before
	// any credential-bearing request leaves the host.
	//
	//   secret host    → MITM (swap fires; checked BEFORE the allowAll branch)
	//   allow-all else → CONNECT tunnel (real cert, no swap; placeholder reaches upstream unchanged)
	//   allowed host   → MITM (only reached in closed-egress mode)
	//   other host     → reject (closed-egress mode)
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
		if allowSet.Has(lh) {
			log.Info("mitm: CONNECT allowed", "sandbox", sandboxID, "host", hostname)
			return mitmAction, host
		}
		log.Info("mitm: CONNECT rejected", "sandbox", sandboxID, "host", hostname)
		if cfg.OnEgress != nil {
			cfg.OnEgress(hostname, "deny", "host not in MITM allowlist", time.Now())
		}
		return goproxy.RejectConnect, host
	}))

	// D-PDE-16: generic per-(placeholder, host) path policy enforcer.
	//
	// Fires BEFORE the credential swap handler — the 403 is emitted before any
	// real token is injected (ordering invariant: this DoFunc is registered
	// earlier than the swap DoFunc below).
	//
	// Redirect handling: goproxy does not follow redirects autonomously. A 3xx
	// from upstream is forwarded to the guest; the guest's HTTP client
	// re-submits the redirected request through this proxy, where it is
	// re-evaluated here. No Location-rewriting is needed — a redirect to a
	// different repo or host fails the next pass automatically.
	//
	// Body buffering: only method+URL.Path are inspected; no body buffering.
	//
	// Path canonicalisation: isCanonicalPath applies two independent checks
	// before any policy rule fires:
	//   1. path.Clean(URL.Path) == URL.Path — traversal segments and double
	//      slashes are absent from the decoded form.
	//   2. Every segment of EscapedPath matches [A-Za-z0-9._~-] — no percent-
	//      encoded bytes (%xx), semicolons, backslashes, or non-ASCII characters.
	// Together these block all known bypass spellings.
	// The real token is therefore never emitted upstream for any traversal attempt.
	if len(policies) > 0 {
		inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			host := strings.ToLower(reqHost(req))
			placeholder := extractPlaceholder(req.Header.Get("Authorization"))
			pol, ok := lookupPolicy(policies, placeholder, host)
			if !ok {
				return req, nil // no policy for (placeholder, host): allow
			}
			// isCanonicalPath pre-check — reused verbatim, never relaxed.
			// Checks decoded (path.Clean traversal) and escaped (charset) forms.
			if !isCanonicalPath(req.URL.Path, req.URL.EscapedPath()) {
				log.Info("mitm: D-PDE-16 request denied (non-canonical path)",
					"sandbox", sandboxID, "host", host,
					"method", req.Method, "path", req.URL.Path)
				return req, denyResponse(req)
			}
			// Default-deny: request must satisfy the policy.
			allowed := false
			switch {
			case pol.GitHub != nil:
				// GitHub built-in: method-aware, allDigits, canonical prefixes.
				// gitHubPathAllowed is called verbatim — NOT rewritten as a glob.
				allowed = gitHubPathAllowed(host, req.Method, req.URL.Path, pol.GitHub.Owner, pol.GitHub.Name)
			default:
				for _, gp := range pol.Patterns {
					if gp.matchPath(req.Method, req.URL.Path) {
						allowed = true
						break
					}
				}
			}
			if !allowed {
				log.Info("mitm: D-PDE-16 request denied",
					"sandbox", sandboxID, "host", host,
					"method", req.Method, "path", req.URL.Path)
				return req, denyResponse(req)
			}
			return req, nil
		})

	}

	// D-PD-38: branch allowlist enforcement. Fires BEFORE the credential swap so
	// rejected pushes never see the real token. Registered unconditionally.
	// INVARIANT: AllowedBranches must never be empty in production —
	// domain.Envelope.ResolvedAllowedBranches() always supplies the hardcoded default
	// ["refs/heads/nexus3/**"]. An empty list here means misconfiguration and is
	// therefore DENIED (fail-closed) to prevent silently allowing pushes to main.
	inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		if reqHost(req) != "github.com" {
			return req, nil
		}
		if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/git-receive-pack") {
			return req, nil
		}
		if req.Body == nil {
			return req, nil
		}
		// Parse pkt-line ref update commands from the push body (max 64KB).
		// Fail-closed note: if AllowedBranches is empty the inner match loop below
		// iterates 0 times → matched stays false → deniedRef is set → 403. In
		// production domain.Envelope.ResolvedAllowedBranches() always supplies the
		// hardcoded default, so empty here means misconfiguration, correctly denied.
		const maxPkt = 64 * 1024
		buf := &bytes.Buffer{}
		malformed := false
		var deniedRef string
		for buf.Len() <= maxPkt {
			var lenBuf [4]byte
			if _, err := io.ReadFull(req.Body, lenBuf[:]); err != nil {
				break // body ended cleanly
			}
			buf.Write(lenBuf[:])
			pktLen, ok := parseHex4(lenBuf[:])
			if !ok {
				malformed = true
				break
			}
			// Flush packet (0000) and delimiter packets (0001, 0002): stop.
			if pktLen == 0 || pktLen == 1 || pktLen == 2 {
				break
			}
			dataLen := pktLen - 4
			if dataLen <= 0 {
				continue // keep-alive: length field exactly 4, zero data bytes
			}
			if buf.Len()+dataLen > maxPkt {
				malformed = true
				break
			}
			data := make([]byte, dataLen)
			if _, err := io.ReadFull(req.Body, data); err != nil {
				buf.Write(data)
				break
			}
			buf.Write(data)
			// Parse ref update command: "<old-sha1> <new-sha1> <refname>\n"
			line := strings.TrimRight(string(data), "\n")
			parts := strings.SplitN(line, " ", 3)
			if len(parts) < 3 {
				continue // capability advertisement or keep-alive
			}
			ref := parts[2]
			// Strip NUL-separated capabilities (present on first pkt-line only).
			if idx := strings.IndexByte(ref, 0); idx >= 0 {
				ref = ref[:idx]
			}
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			// Allowlist check.
			matched := false
			for _, pattern := range cfg.AllowedBranches {
				if refMatchesGlob(pattern, ref) {
					matched = true
					break
				}
			}
			if !matched {
				deniedRef = ref
				break
			}
		}
		// Re-attach all bytes consumed from the original body.
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf.Bytes()), req.Body))
		if malformed {
			const body = "D-PD-38: push pkt-line header malformed or too large\n"
			log.Info("mitm: D-PD-38 push denied (malformed pkt-line)", "sandbox", sandboxID)
			if cfg.OnEgress != nil {
				cfg.OnEgress(reqHost(req), "deny", "D-PD-38: malformed pkt-line", time.Now())
			}
			return req, &http.Response{
				StatusCode: http.StatusForbidden, Status: "403 Forbidden",
				Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
				Header:        http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
				Body:          io.NopCloser(strings.NewReader(body)),
				ContentLength: int64(len(body)), Request: req,
			}
		}
		if deniedRef != "" {
			const body = "D-PD-38: push target ref not in AllowedBranches\n"
			log.Info("mitm: D-PD-38 push denied (branch not in AllowedBranches)",
				"sandbox", sandboxID, "ref", deniedRef)
			if cfg.OnEgress != nil {
				cfg.OnEgress(reqHost(req), "deny", "D-PD-38: ref not in AllowedBranches: "+deniedRef, time.Now())
			}
			return req, &http.Response{
				StatusCode: http.StatusForbidden, Status: "403 Forbidden",
				Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
				Header:        http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
				Body:          io.NopCloser(strings.NewReader(body)),
				ContentLength: int64(len(body)), Request: req,
			}
		}
		return req, nil
	})

	if hasAnyGitHubPolicy {
		// S1c SAFE default-deny stub (advisor CORRECTION, belt-and-suspenders).
		// gitHubPathAllowed returns true for /graphql to preserve its existing
		// call structure; this handler catches it afterwards and denies it.
		// Together they ensure /graphql is denied before any credential swap
		// even if the path policy handler passes it through.
		// Full gh/gh-stack GraphQL allowlist is TBR-GRAPHQL/R5.
		inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			h := reqHost(req)
			if (h != "api.github.com" && h != "github.com") || !strings.HasPrefix(req.URL.Path, "/graphql") {
				return req, nil
			}
			// Narrow carve-out: the read-only token-validation query that
			// `gh auth status` sends (query UserCurrent{viewer{login}}) is
			// permitted. Its selection set is exactly viewer.login — strictly a
			// subset of the already-allowed REST GET /user — so allowing it
			// grants NO capability beyond the existing D-PDE-16 allowlist and
			// does not widen the full-scope token's blast radius. The body is
			// read (capped) and restored so the swap handler and upstream still
			// see it. Every other GraphQL document remains default-denied; the
			// general gh/gh-stack GraphQL allowlist is still TBR-GRAPHQL/R5.
			if isGitHubTokenValidationQuery(req) {
				log.Info("mitm: S1c-GQL token-validation query allowed (viewer.login; ⊆ GET /user)",
					"sandbox", sandboxID, "host", h)
				return req, nil
			}
			log.Info("mitm: S1c-GQL default-deny stub (R5 pending) — GraphQL denied before any credential swap",
				"sandbox", sandboxID, "host", h, "path", req.URL.Path)
			return req, denyResponse(req)
		})
	}

	// OnRequest swaps placeholder Authorization tokens with real tokens.
	// Bearer (gh CLI) and Basic (git HTTPS) are both handled (D-PD-23).
	// Guard: only hosts in allowSet (= AllowedHosts ∪ SecretHosts) are swapped.
	// Under AllowAll, non-secret hosts are CONNECT-tunneled and therefore never
	// intercepted — they cannot reach this handler. SecretHosts are added to
	// allowSet at construction, so the swap fires for them under AllowAll too.
	inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		host := reqHost(req)
		if !allowSet.Has(host) {
			return req, nil
		}
		swapped, ok := swapAuthorization(req.Header.Get("Authorization"), sandboxID, host, broker)
		if !ok {
			return req, nil
		}
		req2 := req.Clone(req.Context())
		req2.Header.Set("Authorization", swapped)
		log.Info("mitm: credential swapped", "sandbox", sandboxID, "host", host)
		return req2, nil
	})

	return &Proxy{ca: ca, inner: inner, allowSet: allowSet}, nil
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

// CAKeyPair PEM-encodes this Proxy's CA certificate and private key. Used
// only by the hot-swap handoff path (motive nexus3-host-supervisor-hotswap)
// to carry the CA to a replacement supervisor so it can continue signing
// leaf certificates the guest already trusts, instead of the replacement
// minting a fresh CA that would invalidate every certificate the guest has
// already pinned this boot. The private key never leaves the host process
// tree except via this handoff's SCM_RIGHTS-free, host-only, direct
// process-to-process transport.
func (p *Proxy) CAKeyPair() (certPEM, keyPEM []byte, err error) {
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.ca.Certificate[0]})
	key, ok := p.ca.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("mitm: CA private key is %T, want *ecdsa.PrivateKey", p.ca.PrivateKey)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("mitm: marshal CA private key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// AllowHost admits host to this running proxy's MITM allowlist at runtime,
// without rebuilding the proxy or dropping in-flight connections. Safe for
// concurrent use. Returns nothing (idempotent add).
//
// This is the plumbing for the supervisor IPC egress-allow verb (T5) and the
// `nexus3 egress allow` CLI (T6).
func (p *Proxy) AllowHost(host string) {
	p.allowSet.Add(host)
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
// field is a broker placeholder. host is the request's target host (lowercase,
// no port) and must match the host under which the placeholder was registered;
// this enforces the host-boundary check that prevents cross-credential
// exfiltration between distinct MCP OAuth tokens sharing the same broker.
// Returns ("", false) when there is nothing to swap. The real token is never
// logged.
func swapAuthorization(authHeader string, sandboxID domain.SandboxID, host string, broker *cred.Broker) (string, bool) {
	if broker == nil || authHeader == "" {
		return "", false
	}
	switch {
	case strings.HasPrefix(authHeader, "Bearer "):
		placeholder := strings.TrimPrefix(authHeader, "Bearer ")
		realToken, ok := broker.ResolveScoped(placeholder, sandboxID, host)
		if !ok {
			return "", false
		}
		return "Bearer " + realToken, true
	case strings.HasPrefix(authHeader, "token "):
		// GitHub CLI uses "token <TOKEN>" (not Bearer) for classic PATs and GH_TOKEN.
		placeholder := strings.TrimPrefix(authHeader, "token ")
		realToken, ok := broker.ResolveScoped(placeholder, sandboxID, host)
		if !ok {
			return "", false
		}
		return "token " + realToken, true
	case strings.HasPrefix(authHeader, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
		if err != nil {
			return "", false
		}
		user, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return "", false
		}
		realToken, ok := broker.ResolveScoped(pass, sandboxID, host)
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
// any port and a single trailing dot (valid FQDN form). For MITM'd HTTPS the
// Host header is authoritative; for plain proxy-forwarded HTTP the URL host is
// the fallback.
//
// Trailing-dot normalisation is performed here — not separately in each handler
// — so the filter gate (domain.IsGitHubHost) and the swap handler (allowSet
// membership check) always operate on the same canonical form and cannot
// diverge.
func reqHost(req *http.Request) string {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	return strings.TrimSuffix(stripHost(host), ".")
}

// isCanonicalPath reports whether a request path is safe to forward to the
// D-PDE-16 policy rules. It applies two independent invariants:
//
//  1. path.Clean(decoded) == decoded: the decoded path is already in canonical
//     form — no ".." traversal, no "." self-references, no double slashes.
//     Any path that path.Clean normalises to a different value is refused.
//
//  2. Every slash-delimited segment of escaped (the wire form) matches the
//     positive charset [A-Za-z0-9._~-]: no percent-encoded bytes (%xx),
//     semicolons, backslashes, or non-ASCII characters that a downstream server
//     might re-interpret as path separators or traversal tokens.
//
// decoded is req.URL.Path (percent-decoded by Go's URL parser); escaped is
// req.URL.EscapedPath() (the raw wire form). Both are required because they
// catch orthogonal attack classes:
//   - decoded catches "..", ".%2f..", double-slash forms via path.Clean.
//   - escaped catches "..;", backslash separators, overlong UTF-8 dots
//     (%c0%ae), and fullwidth lookalikes (U+FF0E → %ef%bc%ae) via the charset.
//
// Implementation invariants — do not remove; their violation silently reopens
// the path-traversal vulnerability:
//
//   - segmentOK ACCEPTS ".." because dot (.) is in its allowed charset. The
//     `tags/` HasPrefix rule and every other allowlist prefix rule are safe ONLY
//     because invariant 1 (path.Clean equality) removes dot-segments before any
//     prefix match fires. Any future allowlist rule that skips path.Clean, or
//     that operates on the raw wire form without first applying path.Clean,
//     reopens traversal.
//
//   - Invariant 1 (path.Clean equality) is sufficient only because URL.Path
//     always carries a leading slash in requests that reach this handler. Go's
//     HTTP layer rejects relative request targets (targets with no leading slash)
//     via ParseRequestURI before they enter the proxy pipeline, so a relative
//     target such as "../../etc/passwd" never arrives here undecorated.
func isCanonicalPath(decoded, escaped string) bool {
	if path.Clean(decoded) != decoded {
		return false
	}
	for _, seg := range strings.Split(escaped, "/") {
		if seg == "" {
			continue // leading slash produces an empty first element; skip it
		}
		if !segmentOK(seg) {
			return false
		}
	}
	return true
}

// segmentOK reports whether a path segment consists entirely of characters
// that are safe in GitHub API paths: ASCII letters, digits, dot, underscore,
// tilde, and hyphen. Percent-encoded bytes (%xx), semicolons, backslashes, and
// any non-ASCII are rejected. This covers the full set of characters needed by
// every path in the D-PDE-16 allowlist (owner names, repo names, numeric IDs,
// tag names like "v1.0.0", and git path segments like "git-upload-pack").
func segmentOK(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '~' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// allDigits reports whether s is a non-empty string of ASCII decimal digits.
// GitHub release IDs and asset IDs are always positive integers in API paths.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// gitHubPathAllowed reports whether a request to a GitHub host is permitted
// by the D-PDE-16 GitHub built-in policy for the given owner/repo.
//
// §3 of D-PD-36: /graphql passes this function (returns true) and is caught
// by the belt-and-suspenders GraphQL deny-all handler registered in New.
//
// Permitted paths are the minimal set for the PR and release flow:
//
//	github.com        — git smart-HTTP (clone, fetch, push)
//	api.github.com    — read repo, list/create PRs, list/create/read/update releases, GET /user
//	uploads.github.com — release-asset upload (POST only)
//
// host must be lowercase. Returns false for any host not in the above set.
func gitHubPathAllowed(host, method, path, owner, repo string) bool {
	switch host {
	case "github.com":
		// Git smart-HTTP protocol endpoints for the target repo only.
		// Reference: https://git-scm.com/docs/http-backend
		prefix := "/" + owner + "/" + repo + ".git/"
		switch {
		case method == http.MethodGet && path == prefix+"info/refs":
			// Clone/fetch service discovery.
			return true
		case method == http.MethodPost && path == prefix+"git-upload-pack":
			// Clone/fetch data transfer.
			return true
		case method == http.MethodPost && path == prefix+"git-receive-pack":
			// Push data transfer.
			return true
		}
		return false

	case "api.github.com":
		// /graphql body validation is enforced by the graphql policy handler (see New).
		repoBase := "/repos/" + owner + "/" + repo
		relBase := repoBase + "/releases"
		switch {
		case method == http.MethodGet && path == "/user":
			// gh auth status. No per-repo context; safe to allow.
			return true
		case method == http.MethodGet && path == repoBase:
			// Read repository metadata (gh pr create reads it for base branch).
			return true
		case (method == http.MethodGet || method == http.MethodPost) && path == repoBase+"/pulls":
			// List or create pull requests.
			return true
		case method == http.MethodGet && path == relBase:
			// List releases.
			return true
		case method == http.MethodPost && path == relBase:
			// Create release.
			return true
		case method == http.MethodGet && path == relBase+"/latest":
			// Get the latest published release.
			return true
		case method == http.MethodGet && strings.HasPrefix(path, relBase+"/tags/"):
			// Get a release by tag name. GitHub allows "/" in tag names so a
			// prefix match is correct; isCanonicalPath above ensures no traversal
			// sequences are present before this point.
			return true
		case method == http.MethodGet && path == repoBase+"/stacks":
			// gh-stack list.
			return true
		case method == http.MethodPost && path == repoBase+"/stacks":
			// gh-stack create.
			return true
		case method == http.MethodPost && strings.HasPrefix(path, "/stacks/"):
			// gh-stack add/unstack: POST /stacks/{numeric_id}/{add|unstack}.
			rest, _ := strings.CutPrefix(path, "/stacks/")
			parts := strings.SplitN(rest, "/", 2)
			return len(parts) == 2 && allDigits(parts[0]) && (parts[1] == "add" || parts[1] == "unstack")
		case method == http.MethodGet && strings.HasPrefix(path, repoBase+"/pulls/"):
			// Babysit reads: GET /repos/{owner}/{repo}/pulls/{n}[/reviews|/comments].
			rest, _ := strings.CutPrefix(path, repoBase+"/pulls/")
			prNum, sub, hasSub := strings.Cut(rest, "/")
			if !allDigits(prNum) {
				return false
			}
			return !hasSub || sub == "reviews" || sub == "comments"
		case method == http.MethodPatch && strings.HasPrefix(path, repoBase+"/pulls/"):
			// gh-stack sync: PATCH /repos/{owner}/{repo}/pulls/{n}.
			prNum, _ := strings.CutPrefix(path, repoBase+"/pulls/")
			return allDigits(prNum)
		case strings.HasPrefix(path, "/graphql"):
			// /graphql passes the path allowlist; the belt-and-suspenders
			// GraphQL deny-all handler registered in New catches it afterwards.
			return true
		}
		// Remaining shapes require a suffix after /releases/.
		suf, ok := strings.CutPrefix(path, relBase+"/")
		if !ok {
			return false
		}
		// GET|PATCH|DELETE /releases/assets/{numeric_id} — per-asset operations.
		if rest, ok2 := strings.CutPrefix(suf, "assets/"); ok2 {
			return allDigits(rest) &&
				(method == http.MethodGet || method == http.MethodPatch || method == http.MethodDelete)
		}
		// /releases/{numeric_id} or /releases/{numeric_id}/assets.
		id, sub, hasSub := strings.Cut(suf, "/")
		if !allDigits(id) {
			return false
		}
		if !hasSub {
			// Read, update, or delete a specific release by numeric ID.
			return method == http.MethodGet || method == http.MethodPatch || method == http.MethodDelete
		}
		// GET /releases/{numeric_id}/assets — list assets for a release.
		return sub == "assets" && method == http.MethodGet

	case "uploads.github.com":
		// POST /repos/{owner}/{repo}/releases/{numeric_id}/assets — upload asset.
		repoBase := "/repos/" + owner + "/" + repo
		suf, ok := strings.CutPrefix(path, repoBase+"/releases/")
		if !ok || method != http.MethodPost {
			return false
		}
		id, sub, hasSub := strings.Cut(suf, "/")
		return hasSub && allDigits(id) && sub == "assets"

	default:
		// Should not be reached; policy map keys guard the call site.
		return false
	}
}

// githubTokenValidationQueries is the exact set of GraphQL documents permitted
// through the otherwise default-denied /graphql endpoint. Each selects only the
// authenticated viewer's login — a strict subset of the already-allowed REST
// GET /user — so admitting them grants no capability beyond the existing
// allowlist. Comparison is against the whitespace-stripped request document, so
// formatting variations of the same query match. `gh auth status` (gh ≥ 2) sends
// the first form to validate GH_TOKEN. The list is an explicit, auditable
// allowlist — NOT a general GraphQL parser (that is TBR-GRAPHQL/R5).
var githubTokenValidationQueries = map[string]struct{}{
	"queryUserCurrent{viewer{login}}": {}, // gh auth status
	"query{viewer{login}}":            {}, // anonymous named-op-free variant
	"{viewer{login}}":                 {}, // bare shorthand query
}

// stripASCIIWhitespace removes spaces, tabs, newlines, and carriage returns.
// GraphQL treats these (plus commas) as insignificant between tokens; removing
// them canonicalises formatting variants of the same document for exact-match
// comparison against githubTokenValidationQueries.
func stripASCIIWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', ',':
			// skip
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// isGitHubTokenValidationQuery reports whether req is the read-only GraphQL
// token-validation POST that `gh auth status` issues. It reads req.Body (capped
// at maxGraphQLValidationBody bytes) and ALWAYS restores it so the swap handler
// and upstream see the original body regardless of the verdict. Returns false —
// keeping the GraphQL default-deny intact — for any body that is not exactly one
// of githubTokenValidationQueries after whitespace stripping, is too large, is
// not valid JSON, or carries GraphQL variables.
func isGitHubTokenValidationQuery(req *http.Request) bool {
	if req.Method != http.MethodPost || req.Body == nil {
		return false
	}
	const maxGraphQLValidationBody = 4 << 10 // 4 KiB: the validation query is ~40 bytes.
	limited := io.LimitReader(req.Body, maxGraphQLValidationBody+1)
	buf, err := io.ReadAll(limited)
	// Restore the consumed bytes plus any unread remainder unconditionally.
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), req.Body))
	if err != nil || int64(len(buf)) > maxGraphQLValidationBody {
		return false
	}
	// Reject bodies with duplicate top-level JSON keys. Go's encoding/json is
	// last-wins on duplicates, so {"query":"mutation{evil}","query":"{viewer{login}}"}
	// would pass the gate (the second value wins) while a differently-behaving parser
	// could execute the first. Detect and deny before any key is trusted.
	if hasDuplicateTopLevelJSONKeys(buf) {
		return false
	}
	var body struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	if err := json.Unmarshal(buf, &body); err != nil {
		return false
	}
	// Reject any request that carries variables: the validation query has none,
	// and non-empty variables signal a different (unvetted) document.
	if v := strings.TrimSpace(string(body.Variables)); v != "" && v != "null" && v != "{}" {
		return false
	}
	_, ok := githubTokenValidationQueries[stripASCIIWhitespace(body.Query)]
	return ok
}

// hasDuplicateTopLevelJSONKeys reports whether data contains repeated keys at
// the top level of a JSON object. Go's encoding/json is last-wins on duplicates,
// so a body like {"query":"mutation{evil}","query":"{viewer{login}}"} would pass a
// gate that only inspects the decoded value. Scanning the token stream catches the
// duplication before any key is trusted. Returns false (no duplicates detected) for
// non-object JSON or parse errors — the caller's own Unmarshal handles those.
func hasDuplicateTopLevelJSONKeys(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return false
		}
		key, ok := tok.(string)
		if !ok {
			return false
		}
		if seen[key] {
			return true
		}
		seen[key] = true
		// Skip the value at any nesting depth.
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return false
		}
	}
	return false
}

// denyResponse builds an HTTP 403 response for a D-PDE-16 denied request.
// It is returned by the OnRequest deny handler to stop proxy processing and
// send the denial to the guest without forwarding the request or swapping
// any credential.
func denyResponse(req *http.Request) *http.Response {
	const body = "D-PD-36: request path not in allowlist\n"
	return &http.Response{
		StatusCode:    http.StatusForbidden,
		Status:        "403 Forbidden",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// parseHex4 parses a 4-character ASCII hexadecimal byte slice and returns the
// integer value. Returns (0, false) if any character is not a valid hex digit or
// if len(b) != 4.
func parseHex4(b []byte) (int, bool) {
	if len(b) != 4 {
		return 0, false
	}
	val := 0
	for _, c := range b {
		val <<= 4
		switch {
		case c >= '0' && c <= '9':
			val |= int(c - '0')
		case c >= 'a' && c <= 'f':
			val |= int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			val |= int(c-'A') + 10
		default:
			return 0, false
		}
	}
	return val, true
}

// refMatchesGlob reports whether ref matches the glob pattern.
//
// Two modes:
//   - Trailing "/**": namespace-prefix match. The pattern
//     "refs/heads/nexus3/**" matches "refs/heads/nexus3/foo",
//     "refs/heads/nexus3/foo/bar", and any deeper path — i.e. any ref
//     whose path starts with the prefix "refs/heads/nexus3/".  This is
//     the D-PD-03 convention where sandbox refs live under the nexus3/
//     namespace at arbitrary depth.
//   - All other patterns: path.Match semantics. A bare '*' never crosses
//     a '/'.  "refs/heads/nexus3/*" matches "refs/heads/nexus3/foo" but
//     NOT "refs/heads/nexus3/foo/bar".
func refMatchesGlob(pattern, ref string) bool {
	const doubleStarSuffix = "/**"
	if strings.HasSuffix(pattern, doubleStarSuffix) {
		prefix := strings.TrimSuffix(pattern, doubleStarSuffix)
		// Allow refs that equal the prefix itself or are strictly under it.
		return ref == prefix || strings.HasPrefix(ref, prefix+"/")
	}
	matched, err := path.Match(pattern, ref)
	return err == nil && matched
}

// githubWildcardHost is the sentinel host key stored by buildPathPolicies under
// the wildcard placeholder "" when AllowedRepo is set. lookupPolicy matches it
// via domain.IsGitHubHost so that ANY *.github.com host (including
// codeload.github.com) is subject to the path policy — preserving the broad
// coverage the old `domain.IsGitHubHost` check provided.
const githubWildcardHost = "*.github.com"

// buildPathPolicies returns the merged PathPolicies used at enforcement time.
// If allowedRepo is non-empty it is validated and folded in under wildcard key
// "" — the AllowedRepo compat shim. Entries from pp take precedence because
// lookupPolicy checks the exact placeholder before falling back to the wildcard.
func buildPathPolicies(pp PathPolicies, allowedRepo string) (PathPolicies, error) {
	if allowedRepo == "" {
		return pp, nil
	}
	owner, name, ok := strings.Cut(allowedRepo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("mitm: AllowedRepo %q is not in owner/repo format", allowedRepo)
	}
	merged := make(PathPolicies, len(pp)+1)
	for k, v := range pp {
		merged[k] = v
	}
	// githubWildcardHost sentinel covers all *.github.com hosts via IsGitHubHost
	// in lookupPolicy (e.g. codeload.github.com, api.github.com, github.com).
	merged[""] = map[string]HostPolicy{
		githubWildcardHost: {GitHub: &GitHubPolicy{Owner: owner, Name: name}},
	}
	return merged, nil
}

// lookupPolicy finds the HostPolicy for (placeholder, host) in pp.
// It checks the exact placeholder key first, then the wildcard "" key
// (AllowedRepo compat shim). Returns (HostPolicy{}, false) if neither matches.
func lookupPolicy(pp PathPolicies, placeholder, host string) (HostPolicy, bool) {
	// lookupInMap checks an exact host key then the *.github.com sentinel.
	lookupInMap := func(hostMap map[string]HostPolicy) (HostPolicy, bool) {
		if pol, ok := hostMap[host]; ok {
			return pol, true
		}
		if domain.IsGitHubHost(host) {
			if pol, ok := hostMap[githubWildcardHost]; ok {
				return pol, true
			}
		}
		return HostPolicy{}, false
	}

	if hostMap, ok := pp[placeholder]; ok {
		if pol, ok := lookupInMap(hostMap); ok {
			return pol, true
		}
	}
	// Wildcard key "" is the AllowedRepo compat shim.  Fall back to it only
	// when the request has a non-empty placeholder (i.e. it has auth) that
	// did not produce a direct hit — prevents unauthenticated requests from
	// matching per-placeholder entries stored under a real placeholder key.
	if placeholder != "" {
		if hostMap, ok := pp[""]; ok {
			if pol, ok := lookupInMap(hostMap); ok {
				return pol, true
			}
		}
	}
	return HostPolicy{}, false
}

// extractPlaceholder returns the raw placeholder token from an Authorization
// header. Handles Bearer, token (GitHub CLI classic PAT form), and Basic
// (git HTTPS) schemes — the same set as swapAuthorization. Returns "" when
// the header is absent, empty, or in an unrecognized scheme.
func extractPlaceholder(authHeader string) string {
	switch {
	case strings.HasPrefix(authHeader, "Bearer "):
		return strings.TrimPrefix(authHeader, "Bearer ")
	case strings.HasPrefix(authHeader, "token "):
		return strings.TrimPrefix(authHeader, "token ")
	case strings.HasPrefix(authHeader, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
		if err != nil {
			return ""
		}
		_, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return ""
		}
		return pass
	}
	return ""
}

// CompileGlobPattern parses a pattern string into a GlobPattern.
//
// Format: optional "METHOD " prefix (e.g. "GET ", "POST "), then an absolute
// path. Within a path:
//   - "*"  as a whole segment matches exactly one path segment.
//   - "**" as a whole segment matches zero or more path segments.
//   - All other segments must pass segmentOK (ASCII letters, digits, dot,
//     underscore, tilde, hyphen) — the same charset isCanonicalPath enforces
//     on incoming requests, so a pattern can only permit paths the pre-check
//     already passes.
func CompileGlobPattern(pattern string) (GlobPattern, error) {
	method := ""
	p := pattern
	if idx := strings.IndexByte(p, ' '); idx > 0 {
		method = strings.ToUpper(p[:idx])
		p = p[idx+1:]
	}
	if !strings.HasPrefix(p, "/") {
		return GlobPattern{}, fmt.Errorf("mitm: glob pattern %q must start with /", pattern)
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if i == 0 || s == "*" || s == "**" {
			continue // leading empty element; wildcards are always valid
		}
		if !segmentOK(s) {
			return GlobPattern{}, fmt.Errorf("mitm: glob pattern segment %q contains invalid characters in %q", s, pattern)
		}
	}
	return GlobPattern{method: method, segs: segs}, nil
}

// matchPath reports whether gp matches the given HTTP method and request path.
// Method comparison is case-insensitive; an empty method in the pattern matches any.
func (gp GlobPattern) matchPath(method, reqPath string) bool {
	if gp.method != "" && !strings.EqualFold(gp.method, method) {
		return false
	}
	return matchGlobSegs(gp.segs, strings.Split(reqPath, "/"))
}

// matchGlobSegs recursively matches compiled pattern segments against path
// segments. Both slices contain a leading "" element from splitting "/x" on "/".
// "*" matches exactly one segment; "**" matches zero or more segments.
func matchGlobSegs(pat, segs []string) bool {
	for {
		if len(pat) == 0 {
			return len(segs) == 0
		}
		p := pat[0]
		if p == "**" {
			// Try matching the remaining pattern against every suffix of segs.
			for k := 0; k <= len(segs); k++ {
				if matchGlobSegs(pat[1:], segs[k:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		s := segs[0]
		if p != "*" && p != s {
			return false
		}
		pat = pat[1:]
		segs = segs[1:]
	}
}

// Compile-time assertion: Proxy satisfies http.Handler.
var _ http.Handler = (*Proxy)(nil)
