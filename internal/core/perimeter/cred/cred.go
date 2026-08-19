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
type entry struct {
	realToken   string
	placeholder string
	sc          scope
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

	sc := scope{sandboxID: sandboxID, host: host}
	e := &entry{
		realToken:   realToken,
		placeholder: ph,
		sc:          sc,
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Revoke any existing placeholder for this scope.
	if old, ok := b.byScope[sc]; ok {
		delete(b.byPlaceholder, old.placeholder)
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
// placeholder belongs to sandboxID. It returns ("", false) if the placeholder
// is unknown OR belongs to a different sandbox.
//
// P1-S4 SHOULD use this form when the calling context has the sandbox ID
// readily available, to prevent cross-sandbox token theft in the unlikely
// event two sandboxes collude.
func (b *Broker) ResolveScoped(placeholder string, sandboxID domain.SandboxID) (realToken string, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	e, found := b.byPlaceholder[placeholder]
	if !found || e.sc.sandboxID != sandboxID {
		return "", false
	}
	return e.realToken, true
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
	delete(b.byPlaceholder, e.placeholder)
	delete(b.byScope, sc)
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
