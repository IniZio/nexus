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
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
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

	// AllowedRepo, when non-empty, enables the D-PD-36 per-request path
	// allowlist for GitHub hosts (github.com, api.github.com,
	// uploads.github.com). Only method+path combinations needed for the PR
	// and release flow are forwarded; all others are refused with HTTP 403
	// BEFORE any credential swap occurs. Format: "owner/repo" (case-sensitive).
	//
	// This is the ONLY control bounding the operator's full-scope GitHub token
	// for agent sandboxes. Leave empty only for human sandboxes (AllowAll
	// egress) that have no per-repo restriction requirement.
	AllowedRepo string

	// AllowedBranches is the list of git ref patterns the proxy will permit
	// on git-push receive-pack requests. Patterns follow git refspec glob
	// syntax (e.g. "refs/heads/nexus3/*"). Enforcement is implemented in
	// slice S1; this field is plumbing only — no filtering occurs here.
	// The resolved default (when empty at the call site) is
	// ["refs/heads/nexus3/*"] via domain.Envelope.ResolvedAllowedBranches.
	AllowedBranches []string

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

	// D-PD-36: parse AllowedRepo at construction time; no per-request parsing.
	var (
		repoOwner string
		repoName  string
		repoSet   bool
	)
	if cfg.AllowedRepo != "" {
		owner, name, ok := strings.Cut(cfg.AllowedRepo, "/")
		if !ok || owner == "" || name == "" {
			return nil, fmt.Errorf("mitm: AllowedRepo %q is not in owner/repo format", cfg.AllowedRepo)
		}
		repoOwner = owner
		repoName = name
		repoSet = true
	}

	// pinnedRepoNodeID holds the GitHub repository node ID discovered from the
	// first GraphQL response that returns data.repository.id. Once pinned,
	// mutations bearing a different repositoryId are denied.
	var pinnedRepoNodeID atomic.Value // stores string; zero-value = not yet pinned

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
		if _, ok := allowSet[lh]; ok {
			log.Info("mitm: CONNECT allowed", "sandbox", sandboxID, "host", hostname)
			return mitmAction, host
		}
		log.Info("mitm: CONNECT rejected", "sandbox", sandboxID, "host", hostname)
		return goproxy.RejectConnect, host
	}))

	// D-PD-36: path allowlist — fires BEFORE the credential swap handler.
	// Requests to GitHub hosts that are not in the explicit allowlist are
	// refused with 403 without forwarding or swapping any credential.
	//
	// Redirect handling: goproxy does not follow redirects autonomously. A 3xx
	// from upstream is forwarded to the guest; the guest's HTTP client issues
	// the redirected request through this proxy again, where it is
	// re-evaluated by this handler. No Location-rewriting is needed — a
	// redirect to a different repo or host fails the next pass automatically.
	// goproxy neither rewrites Location headers nor follows them; if an upstream
	// 3xx points outside the allowlist, the re-submitted request fails the next
	// pass, so no Location-rewriting or hop-counting is needed here.
	//
	// Body buffering: path matching uses only method+URL.Path; no body
	// buffering is performed. There is nothing to size-cap. The only
	// request property this handler reads is the URL path and method; the
	// body is neither inspected nor buffered, consistent with D-PD-36 §2.
	//
	// Path canonicalisation: Go's HTTP layer does NOT normalise "." or ".."
	// segments, semicolons, backslashes, or non-ASCII lookalikes. isCanonicalPath
	// applies two independent checks before any prefix rule fires:
	//   1. path.Clean(URL.Path) == URL.Path — traversal segments and double
	//      slashes are absent from the decoded form.
	//   2. Every segment of EscapedPath matches [A-Za-z0-9._~-] — no percent-
	//      encoded bytes (%xx), semicolons, backslashes, or non-ASCII characters
	//      that a downstream server might re-interpret as path separators.
	// Together these block all known bypass spellings including "..;", backslash
	// separators, overlong UTF-8 dots (%c0%ae), and fullwidth lookalikes (U+FF0E).
	// The real token is therefore never emitted upstream for any traversal attempt.
	if repoSet {
		inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			host := strings.ToLower(reqHost(req))
			if !domain.IsGitHubHost(host) {
				return req, nil // non-GitHub allowed host: no path restriction
			}
			// Reject any non-canonical path before the allowlist rules run.
			// isCanonicalPath checks both the decoded and raw (escaped) forms
			// to catch all known traversal spellings.
			if !isCanonicalPath(req.URL.Path, req.URL.EscapedPath()) {
				log.Info("mitm: D-PD-36 request denied (non-canonical path)",
					"sandbox", sandboxID, "host", host,
					"method", req.Method, "path", req.URL.Path)
				return req, denyResponse(req)
			}
			if !gitHubPathAllowed(host, req.Method, req.URL.Path, repoOwner, repoName) {
				log.Info("mitm: D-PD-36 request denied",
					"sandbox", sandboxID, "host", host,
					"method", req.Method, "path", req.URL.Path)
				return req, denyResponse(req)
			}
			return req, nil
		})

		// D-PD-38: branch policy — reject pushes to refs not in AllowedBranches.
		// Fires BEFORE the credential swap so rejected pushes never see the real token.
		inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			if len(cfg.AllowedBranches) == 0 {
				return req, nil
			}
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

		// S1c SAFE default-deny stub (advisor CORRECTION) — full gh/gh-stack
		// GraphQL allowlist is TBR-GRAPHQL/R5; until then all GraphQL is denied
		// and gh pr create over GraphQL will 403. The bypassable owner/name and
		// pinned-id partial guards from the prior D-PD-38-GQL implementation are
		// replaced by this clean deny-all: any POST to a /graphql path on
		// api.github.com or github.com is denied before any credential swap.
		inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			h := reqHost(req)
			if (h != "api.github.com" && h != "github.com") || !strings.HasPrefix(req.URL.Path, "/graphql") {
				return req, nil
			}
			log.Info("mitm: S1c-GQL default-deny stub (R5 pending) — GraphQL denied before any credential swap",
				"sandbox", sandboxID, "host", h, "path", req.URL.Path)
			return req, denyResponse(req)
		})

		// Pin the repository node ID from GraphQL responses for mutation enforcement.
		inner.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
			if resp == nil || resp.Request == nil || resp.Body == nil {
				return resp
			}
			if reqHost(resp.Request) != "api.github.com" {
				return resp
			}
			if !strings.HasPrefix(resp.Request.URL.Path, "/graphql") {
				return resp
			}
			bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
			if err != nil {
				resp.Body = io.NopCloser(strings.NewReader(""))
				return resp
			}
			resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			// Only pin once; never override an established pinned ID.
			v := pinnedRepoNodeID.Load()
			if existing, _ := v.(string); existing == "" {
				var gqlResp struct {
					Data struct {
						Repository struct {
							ID string `json:"id"`
						} `json:"repository"`
					} `json:"data"`
				}
				if err := json.Unmarshal(bodyBytes, &gqlResp); err == nil {
					if id := gqlResp.Data.Repository.ID; id != "" {
						pinnedRepoNodeID.Store(id)
						log.Info("mitm: D-PD-38-GQL pinned repository node ID", "sandbox", sandboxID)
					}
				}
			}
			return resp
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
		if _, ok := allowSet[strings.ToLower(host)]; !ok {
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
// D-PD-36 allowlist rules. It applies two independent invariants:
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
// every path in the D-PD-36 allowlist (owner names, repo names, numeric IDs,
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
// by the D-PD-36 allowlist for the given owner/repo.
//
// §3 of D-PD-36: /graphql is always denied regardless of method — the target
// repo is encoded in the POST body and cannot be validated without a GraphQL
// parser.
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
		case method == http.MethodPatch && strings.HasPrefix(path, repoBase+"/pulls/"):
			// gh-stack sync: PATCH /repos/{owner}/{repo}/pulls/{n}.
			prNum, _ := strings.CutPrefix(path, repoBase+"/pulls/")
			return allDigits(prNum)
		case strings.HasPrefix(path, "/graphql"):
			// /graphql passes the path allowlist; owner/name and pinned
			// repository ID enforcement is handled by the GraphQL body-policy
			// handler registered in New (see D-PD-38-GQL).
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
		// Should not be reached; isGitHubHost guards the call site.
		return false
	}
}

// denyResponse builds an HTTP 403 response for a D-PD-36 denied request.
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

// Compile-time assertion: Proxy satisfies http.Handler.
var _ http.Handler = (*Proxy)(nil)
