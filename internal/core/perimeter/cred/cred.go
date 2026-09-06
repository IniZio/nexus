// Package cred implements the host-side credential broker for nexus3's
// perimeter subsystem.
//
// # Security model
//
// The guest sandbox holds ONLY a high-entropy placeholder string and a
// synthetic far-future expiry (year 2099). Real bearer tokens are stored
// exclusively on the host side and never appear in the guest-facing record.
// All credential refresh is host-side: when the real token rotates, the
// broker's [Broker.SetRealToken] is called; the placeholder stays identical,
// so the guest perceives no change.
//
// # Usage flow
//
//  1. Before sandbox creation, call [Broker.RegisterPlaceholder] for each
//     (sandboxID, allowedHost) pair that requires a credential. The returned
//     [PlaceholderRecord] is seeded into the guest (P1-S6).
//
//  2. At runtime the L7 MITM proxy (P1-S4) calls [Broker.Resolve] with the
//     placeholder found in the guest's outbound Authorization header. The
//     returned real token is swapped into the header before forwarding.
//
//  3. When the upstream credential rotates, call [Broker.SetRealToken] with
//     the same scope. Resolve will immediately return the new token; the guest
//     is unaware.
package cred

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// syntheticExpiry is the far-future expiry seeded into every PlaceholderRecord.
// It is chosen so that a guest-side HTTP client will never attempt to refresh
// the credential on its own.
var syntheticExpiry = time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)

// placeholderEntropy is the number of random bytes used to generate each
// placeholder string (32 bytes → 64 hex chars → ~192 bits of entropy).
const placeholderEntropy = 32

// scope uniquely identifies the (sandbox, host) pair that a credential entry
// covers.
type scope struct {
	sandboxID domain.SandboxID
	host      string
}

// entry is the host-side record for one registered placeholder.
// A single entry may cover multiple hosts (e.g. github.com and api.github.com
// sharing one GH_TOKEN placeholder). All hosts in the set are stored in hosts;
// byScope points each (sandboxID, host) key to the same *entry so that
// SetRealToken, Placeholder, and Revoke still work per-host.
type entry struct {
	realToken   string
	placeholder string
	sandboxID   domain.SandboxID
	hosts       map[string]bool // every host this placeholder resolves for
}

// PlaceholderRecord is the guest-facing credential record. It carries only
// what the guest is permitted to know: the placeholder token value and a
// synthetic expiry. The real token is NEVER included.
type PlaceholderRecord struct {
	// Placeholder is the high-entropy token the guest places in its
	// Authorization headers. The L7 MITM proxy (P1-S4) swaps this for the
	// real token before forwarding the request.
	Placeholder string

	// ExpiresAt is a synthetic far-future timestamp. It is set to year 2099
	// so that guest-side HTTP clients never attempt self-refresh.
	ExpiresAt time.Time

	// SandboxID is the sandbox this record was issued for.
	SandboxID domain.SandboxID

	// Host is the allowlisted host this credential covers.
	Host string
}

// Broker is a thread-safe host-side credential store. It maps high-entropy
// placeholder strings to real bearer tokens for specific (sandbox, host)
// scopes.
//
// A zero-value Broker is not usable; construct one via [NewBroker].
type Broker struct {
	mu      sync.RWMutex
	byPlaceholder map[string]*entry // placeholder → entry
	byScope       map[scope]*entry  // (sandboxID, host) → entry
}

// NewBroker constructs an empty, ready-to-use Broker.
func NewBroker() *Broker {
	return &Broker{
		byPlaceholder: make(map[string]*entry),
		byScope:       make(map[scope]*entry),
	}
}

// RegisterPlaceholder mints a new high-entropy placeholder for the given
// (sandboxID, host) scope, stores the provided real token host-side, and
// returns a [PlaceholderRecord] suitable for delivery to the guest.
//
// If a placeholder is already registered for the same scope it is replaced:
// the old placeholder is revoked and a fresh one is issued. This ensures a
// clean mapping after a guest restart.
//
// host MUST be non-empty. realToken MAY be empty (e.g. when the real token
// will be provided later via [Broker.SetRealToken]).
func (b *Broker) RegisterPlaceholder(sandboxID domain.SandboxID, host, realToken string) (PlaceholderRecord, error) {
	if host == "" {
		return PlaceholderRecord{}, fmt.Errorf("cred: host must not be empty")
	}

	ph, err := mintPlaceholder()
	if err != nil {
		return PlaceholderRecord{}, fmt.Errorf("cred: generating placeholder: %w", err)
	}

	e := &entry{
		realToken:   realToken,
		placeholder: ph,
		sandboxID:   sandboxID,
		hosts:       map[string]bool{host: true},
	}

	sc := scope{sandboxID: sandboxID, host: host}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Revoke any existing placeholder for this scope.
	// If the old entry still covers other hosts, keep it in byPlaceholder.
	if old, ok := b.byScope[sc]; ok {
		delete(old.hosts, host)
		if len(old.hosts) == 0 {
			delete(b.byPlaceholder, old.placeholder)
		}
	}

	b.byScope[sc] = e
	b.byPlaceholder[ph] = e

	return PlaceholderRecord{
		Placeholder: ph,
		ExpiresAt:   syntheticExpiry,
		SandboxID:   sandboxID,
		Host:        host,
	}, nil
}

// Resolve looks up the real bearer token for a registered placeholder.
//
// This is the hot path called by the L7 MITM proxy (P1-S4) on every
// intercepted HTTPS request. It returns (realToken, true) for a known
// placeholder, or ("", false) if the placeholder is not registered.
//
// Placeholders are per-sandbox: a placeholder registered for sandbox A
// does not resolve in the context of sandbox B. However, because the
// placeholder is high-entropy and cryptographically unique, a rogue guest
// cannot guess another sandbox's placeholder. Callers that want scope
// enforcement at the resolution site may use [Broker.ResolveScoped].
func (b *Broker) Resolve(placeholder string) (realToken string, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	e, found := b.byPlaceholder[placeholder]
	if !found {
		return "", false
	}
	return e.realToken, true
}

// ResolveScoped is like [Broker.Resolve] but additionally checks that the
// placeholder belongs to sandboxID AND was registered for host. It returns
// ("", false) if the placeholder is unknown, belongs to a different sandbox,
// or was not registered for host.
//
// The host check closes the cross-credential exfiltration gap: without it, a
// placeholder registered for (sandbox S, hostA) could be resolved for a
// request targeting hostB — allowing a prompt-injected agent to present MCP-A's
// real token to MCP-B's endpoint.
//
// A single placeholder may cover multiple hosts (e.g. github.com and
// api.github.com sharing one GH_TOKEN) when the caller registered them via
// [Broker.RegisterPlaceholderForHost]. ResolveScoped returns true for any host
// in the registered set, and false for any host outside it — the host-boundary
// invariant is preserved; only explicitly registered hosts resolve.
//
// host comparison is performed against the stored host set exactly as
// registered; callers are responsible for normalising both sides consistently
// (e.g. lowercase, no port) before calling.
//
// P1-S4 MUST use this form so that both the sandbox-boundary and the
// host-boundary checks are enforced at the resolution site.
func (b *Broker) ResolveScoped(placeholder string, sandboxID domain.SandboxID, host string) (realToken string, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	e, found := b.byPlaceholder[placeholder]
	if !found || e.sandboxID != sandboxID || !e.hosts[host] {
		return "", false
	}
	return e.realToken, true
}

// RegisterPlaceholderForHost extends an existing placeholder to cover an
// additional host within the same sandbox. After this call,
// ResolveScoped(placeholder, sandboxID, host) returns the real token that was
// supplied at [Broker.RegisterPlaceholder] time.
//
// Use this when one credential (e.g. GH_TOKEN) must be swapped for requests to
// multiple hostnames — for example github.com and api.github.com both belong to
// the same bind and share one placeholder emitted as GH_TOKEN. Without this,
// the caller would mint a separate placeholder per host, but only one can be
// emitted as the env var, leaving the others unreachable via ResolveScoped.
//
// Security invariants preserved:
//   - Only the sandbox that originally registered the placeholder may extend it
//     (the sandboxID equality check prevents cross-sandbox aliasing).
//   - ResolveScoped still checks both sandboxID and the exact host set: a host
//     not added via RegisterPlaceholder or this method resolves to ("", false).
//   - The host-boundary exfiltration guard (see ResolveScoped) is not weakened:
//     the placeholder only resolves for hosts the caller explicitly registers.
func (b *Broker) RegisterPlaceholderForHost(sandboxID domain.SandboxID, placeholder, host string) error {
	if host == "" {
		return fmt.Errorf("cred: host must not be empty")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	e, found := b.byPlaceholder[placeholder]
	if !found {
		return fmt.Errorf("cred: RegisterPlaceholderForHost: unknown placeholder")
	}
	if e.sandboxID != sandboxID {
		return fmt.Errorf("cred: RegisterPlaceholderForHost: placeholder belongs to a different sandbox")
	}

	sc := scope{sandboxID: sandboxID, host: host}
	// If another placeholder already covers this (sandboxID, host) scope, evict it.
	if old, ok := b.byScope[sc]; ok && old != e {
		delete(old.hosts, host)
		if len(old.hosts) == 0 {
			delete(b.byPlaceholder, old.placeholder)
		}
	}
	e.hosts[host] = true
	b.byScope[sc] = e
	return nil
}

// Placeholder returns the placeholder currently registered for the
// (sandboxID, host) scope, and whether one exists.
//
// It returns ONLY the placeholder — never the real token. This is deliberate:
// callers use it to build the guest's environment, and the guest must never
// receive a real credential. The placeholder→real swap happens host-side in
// the L7 MITM proxy. Adding a real-token accessor here would defeat the
// zero-credential-in-guest invariant this package exists to enforce.
func (b *Broker) Placeholder(sandboxID domain.SandboxID, host string) (string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	e, ok := b.byScope[scope{sandboxID: sandboxID, host: host}]
	if !ok {
		return "", false
	}
	return e.placeholder, true
}

// SetRealToken updates the host-side real token for an existing (sandboxID,
// host) scope WITHOUT changing the placeholder. The guest is unaware of the
// rotation; on the next Resolve call the new token is returned.
//
// Returns an error if no placeholder has been registered for the scope.
func (b *Broker) SetRealToken(sandboxID domain.SandboxID, host, newRealToken string) error {
	if host == "" {
		return fmt.Errorf("cred: host must not be empty")
	}

	sc := scope{sandboxID: sandboxID, host: host}

	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.byScope[sc]
	if !ok {
		return fmt.Errorf("cred: no placeholder registered for sandbox %v host %q", sandboxID, host)
	}
	e.realToken = newRealToken
	return nil
}

// Revoke removes the placeholder for the given (sandboxID, host) scope.
// After revocation, Resolve will return false for that placeholder.
// Revoke is a no-op if the scope was not registered.
func (b *Broker) Revoke(sandboxID domain.SandboxID, host string) {
	sc := scope{sandboxID: sandboxID, host: host}

	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.byScope[sc]
	if !ok {
		return
	}
	delete(e.hosts, host)
	delete(b.byScope, sc)
	// Only remove from byPlaceholder when no host remains — the placeholder may
	// still be active for other hosts in the same bind (e.g. api.github.com after
	// revoking github.com).
	if len(e.hosts) == 0 {
		delete(b.byPlaceholder, e.placeholder)
	}
}

// mintPlaceholder generates a cryptographically random, hex-encoded placeholder
// string of length 2*placeholderEntropy.
func mintPlaceholder() (string, error) {
	buf := make([]byte, placeholderEntropy)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// mintJWTPlaceholder generates a synthetic JWT-shaped placeholder token.
// The token has valid JWT structure (header.payload.signature) with:
//   - alg: "HS256" in the header
//   - exp: year 2099 in the payload, so guest-side JWT clients never attempt
//     self-refresh (a refresh attempt would send the refresh_token in a POST
//     body, which the MITM proxy does not intercept)
//   - a random high-entropy signature section ([placeholderEntropy] random bytes,
//     base64url-encoded) that makes the placeholder cryptographically unique
//
// The token is NOT cryptographically signed — it is a broker placeholder that
// the L7 MITM proxy swaps for a real token before it reaches any server.
// Callers that need the guest client to treat its credential as a valid JWT
// (e.g. cursor-agent, which JWT-parses its access token to check expiry) should
// use this instead of [mintPlaceholder].
func mintJWTPlaceholder() (string, error) {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	// exp: 2099-12-31T23:59:59Z in Unix epoch seconds.
	exp := syntheticExpiry.Unix()
	pay := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"sub":"nexus3-placeholder"}`, exp)))
	sig := make([]byte, placeholderEntropy)
	if _, err := rand.Read(sig); err != nil {
		return "", err
	}
	return hdr + "." + pay + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// RegisterJWTPlaceholder is [RegisterPlaceholder] for agents that require a
// JWT-shaped placeholder (e.g. cursor-agent, which JWT-parses its access token
// to check expiry and will attempt an OAuth refresh grant if the token cannot
// be parsed as a JWT). The placeholder value is generated by [mintJWTPlaceholder]
// instead of [mintPlaceholder]; in all other respects the behaviour is identical.
func (b *Broker) RegisterJWTPlaceholder(sandboxID domain.SandboxID, host, realToken string) (PlaceholderRecord, error) {
	if host == "" {
		return PlaceholderRecord{}, fmt.Errorf("cred: host must not be empty")
	}

	ph, err := mintJWTPlaceholder()
	if err != nil {
		return PlaceholderRecord{}, fmt.Errorf("cred: generating JWT placeholder: %w", err)
	}

	e := &entry{
		realToken:   realToken,
		placeholder: ph,
		sandboxID:   sandboxID,
		hosts:       map[string]bool{host: true},
	}

	sc := scope{sandboxID: sandboxID, host: host}

	b.mu.Lock()
	defer b.mu.Unlock()

	if old, ok := b.byScope[sc]; ok {
		delete(old.hosts, host)
		if len(old.hosts) == 0 {
			delete(b.byPlaceholder, old.placeholder)
		}
	}

	b.byScope[sc] = e
	b.byPlaceholder[ph] = e

	return PlaceholderRecord{
		Placeholder: ph,
		ExpiresAt:   syntheticExpiry,
		SandboxID:   sandboxID,
		Host:        host,
	}, nil
}
