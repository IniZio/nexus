package cred

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"golang.org/x/oauth2"
)

// refreshExpiryDelta is the margin before token expiry at which the Refresher
// proactively fetches a new access token. 30 s avoids clock-skew races on
// short-lived tokens.
const refreshExpiryDelta = 30 * time.Second

// realTokenSetter is the subset of [*Broker] the Refresher needs. Using an
// interface keeps the seam testable without importing a concrete Broker.
// [*Broker] already satisfies this interface via [Broker.SetRealToken].
type realTokenSetter interface {
	SetRealToken(sandboxID domain.SandboxID, host, newRealToken string) error
}

// oauthRefreshBase is a non-caching [oauth2.TokenSource] that forces an HTTP
// token-endpoint call every time Token() is invoked. It is used as the inner
// source for [oauth2.ReuseTokenSourceWithExpiry]; the outer wrapper provides
// caching so the HTTP call is only made when the access token is near expiry.
//
// Concurrency: safe (guarded by mu).
type oauthRefreshBase struct {
	mu  sync.Mutex
	cfg *oauth2.Config
	ctx context.Context
	rt  string // latest refresh_token; updated on each successful refresh
}

// Token implements [oauth2.TokenSource]. It always performs an HTTP refresh by
// presenting a token with no access_token, which the oauth2 package treats as
// invalid and immediately exchanges via the refresh_token grant.
func (b *oauthRefreshBase) Token() (*oauth2.Token, error) {
	b.mu.Lock()
	rt := b.rt
	b.mu.Unlock()

	// Seed a token with only refresh_token and no access_token. oauth2 treats
	// an empty/zero-expiry access_token as invalid and uses refresh_token to
	// obtain a fresh one from the token endpoint.
	seed := &oauth2.Token{RefreshToken: rt}
	t, err := b.cfg.TokenSource(b.ctx, seed).Token()
	if err != nil {
		return nil, err
	}

	// Persist a rotated refresh_token so subsequent calls stay current.
	if t.RefreshToken != "" && t.RefreshToken != rt {
		b.mu.Lock()
		b.rt = t.RefreshToken
		b.mu.Unlock()
	}
	return t, nil
}

// Refresher is a [CredentialSource] backed by x/oauth2's
// [oauth2.ReuseTokenSourceWithExpiry]. It is the SOLE refresher for nexus3's
// dedicated OAuth credential: when the access token rotates, it pushes the new
// real token into the broker for every registered sandbox so the guest-side
// agent never self-refreshes.
//
// Sandboxes are registered with [Refresher.Register] before (or shortly after)
// sandbox creation and deregistered with [Refresher.Deregister] on removal.
//
// A zero-value Refresher is not usable; construct one via [NewRefresher].
type Refresher struct {
	host   string
	broker realTokenSetter
	// ts is the caching oauth2.TokenSource (ReuseTokenSourceWithExpiry in
	// production; a fake injected source in tests). Note: oauth2.TokenSource.Token()
	// takes no context, so ctx cannot be forwarded from Refresher.Token(ctx) —
	// the context is captured at construction time for the HTTP refresh client.
	ts oauth2.TokenSource

	mu           sync.Mutex
	sandboxes    map[domain.SandboxID]struct{}
	lastToken    string  // most recently vended access token; "" before first call
	lastPushErrs []error // broker push errors from the last rotation; informational
}

// NewRefresher loads a [DedicatedCredStore] from storePath, validates it, and
// constructs a refreshing [Refresher]. It wraps an [oauth2.ReuseTokenSourceWithExpiry]
// over a non-caching base that calls the token endpoint whenever the cached
// access token is within [refreshExpiryDelta] of expiry.
//
// host is the credentialed hostname (e.g. "api.anthropic.com") from the
// [AgentProfile]; it scopes all [Broker.SetRealToken] calls.
//
// Errors:
//   - Returns a wrapped [ErrStoreAbsent] when storePath does not exist.
//   - Returns a descriptive error when the store lacks a refresh_token or
//     token_endpoint (both are required for unattended refresh).
func NewRefresher(storePath, host string, broker realTokenSetter) (*Refresher, error) {
	if host == "" {
		return nil, fmt.Errorf("cred: refresher: host must not be empty")
	}
	if broker == nil {
		return nil, fmt.Errorf("cred: refresher: broker must not be nil")
	}

	store, err := LoadStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("cred: refresher: loading store: %w", err)
	}
	if store.RefreshToken == "" {
		return nil, fmt.Errorf("cred: refresher: store %s has empty refresh_token; unattended refresh requires one", storePath)
	}
	if store.TokenEndpoint == "" {
		return nil, fmt.Errorf("cred: refresher: store %s has empty token_endpoint; cannot refresh without one", storePath)
	}

	cfg := &oauth2.Config{
		ClientID:     store.ClientID,
		ClientSecret: store.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: store.TokenEndpoint,
			// InParams is standard for most OAuth servers; AuthStyleAutoDetect
			// would probe with two requests, making error cases non-deterministic.
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	initialTok := &oauth2.Token{
		AccessToken:  store.AccessToken,
		RefreshToken: store.RefreshToken,
		Expiry:       store.ExpiresAt,
		TokenType:    store.TokenType,
	}

	base := &oauthRefreshBase{
		cfg: cfg,
		ctx: context.Background(),
		rt:  store.RefreshToken,
	}
	ts := oauth2.ReuseTokenSourceWithExpiry(initialTok, base, refreshExpiryDelta)

	return newRefresherWithSource(ts, host, broker), nil
}

// newRefresherWithSource is the internal constructor used by NewRefresher and
// by tests that inject a fake [oauth2.TokenSource] to avoid network calls.
func newRefresherWithSource(ts oauth2.TokenSource, host string, broker realTokenSetter) *Refresher {
	return &Refresher{
		host:      host,
		broker:    broker,
		ts:        ts,
		sandboxes: make(map[domain.SandboxID]struct{}),
	}
}

// Register adds sandboxID to the set of sandboxes that receive token-rotation
// updates via [Broker.SetRealToken]. It is idempotent.
//
// W2 wiring order: call [Broker.RegisterPlaceholder] first, then Register.
// This ensures the broker scope exists before the next rotation push.
func (r *Refresher) Register(sandboxID domain.SandboxID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sandboxes[sandboxID] = struct{}{}
}

// Deregister removes sandboxID from the rotation-push set. It is idempotent
// and safe to call after [Broker.Revoke].
//
// W2 wiring order: call Deregister before or alongside [Broker.Revoke].
func (r *Refresher) Deregister(sandboxID domain.SandboxID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sandboxes, sandboxID)
}

// PushErrors returns any errors encountered when pushing the most recent token
// rotation to registered sandboxes. A nil or empty slice means all pushes
// succeeded (or no rotation occurred since the last call).
//
// Token vending succeeds regardless of push errors — the caller's request is
// never blocked by a stale sandbox registration. Use PushErrors for monitoring
// or to detect sandboxes that were not deregistered before removal.
func (r *Refresher) PushErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lastPushErrs) == 0 {
		return nil
	}
	out := make([]error, len(r.lastPushErrs))
	copy(out, r.lastPushErrs)
	return out
}

// Token implements [CredentialSource]. It returns the current real access token
// and its expiry, refreshing transparently when the token is within
// [refreshExpiryDelta] of expiry. On each rotation (access token value changes)
// it calls [Broker.SetRealToken] for every registered sandbox; push errors are
// stored and retrievable via [Refresher.PushErrors] but do not fail vending.
//
// Note: the underlying [oauth2.TokenSource.Token] takes no context; ctx is
// accepted to satisfy the [CredentialSource] interface but cannot be threaded
// to the HTTP refresh call (the context is captured at construction time).
func (r *Refresher) Token(_ context.Context) (string, time.Time, error) {
	t, err := r.ts.Token()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("cred: refresher: obtaining token: %w", err)
	}
	if t.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("cred: refresher: token source returned empty access token")
	}

	r.mu.Lock()
	rotated := t.AccessToken != r.lastToken
	if rotated {
		r.lastToken = t.AccessToken
		r.lastPushErrs = nil // reset before new push round
	}
	sandboxes := make([]domain.SandboxID, 0, len(r.sandboxes))
	for id := range r.sandboxes {
		sandboxes = append(sandboxes, id)
	}
	r.mu.Unlock()

	if rotated && len(sandboxes) > 0 {
		var pushErrs []error
		for _, id := range sandboxes {
			if sErr := r.broker.SetRealToken(id, r.host, t.AccessToken); sErr != nil {
				pushErrs = append(pushErrs, fmt.Errorf("sandbox %v: %w", id, sErr))
			}
		}
		if len(pushErrs) > 0 {
			r.mu.Lock()
			r.lastPushErrs = pushErrs
			r.mu.Unlock()
		}
	}

	return t.AccessToken, t.Expiry, nil
}
