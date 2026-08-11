package cred

import (
	"context"
	"fmt"
	"log/slog"
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

// credStoreMeta holds the non-rotating fields of a [DedicatedCredStore] that
// are captured at [NewRefresher] construction time. These are read at persist
// time without re-reading the on-disk file (torn-read risk against concurrent
// writers).
type credStoreMeta struct {
	tokenType     string
	clientID      string
	clientSecret  string
	tokenEndpoint string
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

	// storePath and meta are set by NewRefresher; empty in test constructors that
	// inject a fake TokenSource (persistence is skipped when storePath is "").
	storePath string
	meta      credStoreMeta

	mu                   sync.Mutex
	sandboxes            map[domain.SandboxID]struct{}
	lastToken            string  // most recently vended access token; "" before first call
	lastPushErrs         []error // broker push errors from the last rotation; informational
	lastPersistedRefresh string  // last refresh_token successfully written to storePath
	lastPersistErr       error   // most recent SaveStore failure; nil on success
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

	meta := credStoreMeta{
		tokenType:     store.TokenType,
		clientID:      store.ClientID,
		clientSecret:  store.ClientSecret,
		tokenEndpoint: store.TokenEndpoint,
	}
	r := newRefresherWithSource(ts, host, broker, storePath, meta)
	// Seed lastPersistedRefresh so the first cache-hit vend (access token still
	// valid) does not redundantly re-persist the already-stored refresh_token.
	// Without this, a valid cached token triggers needPersist==true on first call
	// and overwrites the file with identical content, creating a torn-write window.
	r.lastPersistedRefresh = store.RefreshToken
	return r, nil
}

// newRefresherWithSource is the internal constructor used by NewRefresher and
// by tests that inject a fake [oauth2.TokenSource] to avoid network calls.
// storePath and meta are used for refresh-token persistence; pass storePath=""
// to disable persistence (test paths that do not exercise store I/O).
func newRefresherWithSource(ts oauth2.TokenSource, host string, broker realTokenSetter, storePath string, meta credStoreMeta) *Refresher {
	return &Refresher{
		host:      host,
		broker:    broker,
		ts:        ts,
		storePath: storePath,
		meta:      meta,
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

// Host returns the hostname this Refresher manages credentials for. Read-only;
// safe for concurrent use without locks.
func (r *Refresher) Host() string { return r.host }

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

// PersistError returns the most recent error from persisting a rotated
// refresh_token back to the on-disk store, or nil if the last persist
// succeeded or no persist has been attempted.
//
// Persist failures do not fail token vending. Use PersistError for monitoring:
// a persistent non-nil value means the on-disk store is stale and a process
// restart would load a consumed refresh_token, causing invalid_grant.
//
// This is DISTINCT from [Refresher.PushErrors] (broker delivery errors).
func (r *Refresher) PersistError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPersistErr
}

// Token implements [CredentialSource]. It returns the current real access token
// and its expiry, refreshing transparently when the token is within
// [refreshExpiryDelta] of expiry. On each rotation (access token value changes)
// it calls [Broker.SetRealToken] for every registered sandbox; push errors are
// stored and retrievable via [Refresher.PushErrors] but do not fail vending.
//
// When the refresh_token rotates (Anthropic rotates it on every grant), Token
// atomically persists the new refresh_token to the on-disk store so that a
// process restart loads a live credential. Persist failures are surfaced via
// [Refresher.PersistError] and logged at slog warn; they never fail vending.
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

	// Persist the refresh_token when it rotates. Triggered by refresh_token
	// change, not access_token change; Anthropic rotates refresh_token on every
	// grant. Skip if storePath is empty (test paths) or RefreshToken is empty
	// (would brick the store: LoadStore rejects empty refresh_token).
	currentRT := t.RefreshToken
	if r.storePath != "" && currentRT != "" {
		r.mu.Lock()
		needPersist := currentRT != r.lastPersistedRefresh
		r.mu.Unlock()

		if needPersist {
			store := &DedicatedCredStore{
				AccessToken:   t.AccessToken,
				RefreshToken:  currentRT,
				ExpiresAt:     t.Expiry,
				TokenType:     r.meta.tokenType,
				ClientID:      r.meta.clientID,
				ClientSecret:  r.meta.clientSecret,
				TokenEndpoint: r.meta.tokenEndpoint,
			}
			if pErr := SaveStore(r.storePath, store); pErr != nil {
				slog.Warn("cred: refresher: failed to persist rotated refresh_token; process restart may hit invalid_grant",
					"path", r.storePath, "err", pErr)
				r.mu.Lock()
				r.lastPersistErr = pErr
				r.mu.Unlock()
			} else {
				r.mu.Lock()
				r.lastPersistedRefresh = currentRT
				r.lastPersistErr = nil
				r.mu.Unlock()
			}
		}
	}

	return t.AccessToken, t.Expiry, nil
}
