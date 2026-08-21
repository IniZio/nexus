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
// token-endpoint call every time Token() is invoked.
//
// In production, [lockedToken] calls this directly; caching is handled by
// [Refresher.cachedTok] and the on-disk store, not by any wrapping
// TokenSource. A [oauth2.ReuseTokenSourceWithExpiry] wrapper is constructed
// in [NewRefresher] and stored in [Refresher.ts], but that wrapper is only
// reached via the test path (base == nil); it is dead on the production path.
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

// Refresher is a [CredentialSource] that maintains a live OAuth access token
// for nexus3's dedicated credential store. It is the SOLE refresher for
// nexus3's dedicated OAuth credential: when the access token rotates, it
// pushes the new real token into the broker for every registered sandbox so
// the guest-side agent never self-refreshes.
//
// Production token vending uses [lockedToken], which drives [oauthRefreshBase]
// directly under a cross-process flock ([WithStoreLock] in store.go), with
// [Refresher.cachedTok] providing in-process caching. The
// [oauth2.ReuseTokenSourceWithExpiry] wrapper stored in [Refresher.ts] is
// constructed by [NewRefresher] but is only reachable on the test path
// (base == nil, via [newRefresherWithSource]).
//
// Sandboxes are registered with [Refresher.Register] before (or shortly after)
// sandbox creation and deregistered with [Refresher.Deregister] on removal.
//
// A zero-value Refresher is not usable; construct one via [NewRefresher].
type Refresher struct {
	host   string
	broker realTokenSetter
	// ts is the oauth2.TokenSource used on the TEST path only (base == nil).
	// In tests constructed via [newRefresherWithSource], ts holds a fake injected
	// source that avoids network calls. In [NewRefresher], ts is set to a
	// [oauth2.ReuseTokenSourceWithExpiry] wrapper, but that wrapper is never
	// reached on the production path because Token() dispatches to [lockedToken]
	// whenever base != nil. Note: oauth2.TokenSource.Token() takes no context, so
	// ctx cannot be forwarded from Refresher.Token(ctx) — the context is captured
	// at construction time for the HTTP refresh client.
	ts oauth2.TokenSource

	// base is the non-caching HTTP refresh source used by the production locked
	// path (lockedToken). Set only by NewRefresher; nil in test constructors that
	// inject a fake TokenSource via newRefresherWithSource.
	base *oauthRefreshBase

	// storePath and meta are set by NewRefresher; empty in test constructors that
	// inject a fake TokenSource (persistence is skipped when storePath is "").
	storePath string
	meta      credStoreMeta

	mu                   sync.Mutex
	sandboxes            map[domain.SandboxID]struct{}
	lastToken            string        // most recently vended access token; "" before first call
	cachedTok            *oauth2.Token // in-process token cache for the production locked path
	lastPushErrs         []error       // broker push errors from the last rotation; informational
	lastPersistedRefresh string        // last refresh_token successfully written to storePath
	lastPersistErr       error         // most recent SaveStore failure; nil on success
}

// NewRefresher loads a [DedicatedCredStore] from storePath, validates it, and
// constructs a refreshing [Refresher]. It builds an [oauthRefreshBase] that
// performs HTTP token-endpoint calls and wires it as the production locked
// path: [Refresher.Token] calls [lockedToken], which drives the base directly
// under a cross-process flock. An [oauth2.ReuseTokenSourceWithExpiry] wrapper
// is also constructed (and stored in [Refresher.ts]) for use by the test path,
// but it is dead on the production call path.
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
	// Wire the production locked path: base drives the HTTP refresh call;
	// cachedTok seeds the in-process cache from the initial on-disk state.
	r.base = base
	r.cachedTok = initialTok
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
// Production path (NewRefresher, base != nil): uses [lockedToken], which holds
// a process-global file lock across the load → HTTP refresh → save cycle to
// prevent concurrent supervisors from racing on the same refresh_token.
//
// Test path (newRefresherWithSource, base == nil): uses the injected
// oauth2.TokenSource directly without any file locking.
//
// Note: the underlying [oauth2.TokenSource.Token] takes no context; ctx is
// accepted to satisfy the [CredentialSource] interface but cannot be threaded
// to the HTTP refresh call (the context is captured at construction time).
func (r *Refresher) Token(_ context.Context) (string, time.Time, error) {
	// Production path: cross-process serialisation via file lock.
	if r.base != nil {
		return r.lockedToken()
	}

	// Test / fake-source path: use the injected oauth2.TokenSource as before.
	t, err := r.ts.Token()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("cred: refresher: obtaining token: %w", err)
	}
	if t.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("cred: refresher: token source returned empty access token")
	}

	r.mu.Lock()
	rotated := t.AccessToken != r.lastToken
	hadPushErrs := len(r.lastPushErrs) > 0
	if rotated {
		r.lastToken = t.AccessToken
		r.lastPushErrs = nil // reset before new push round
	} else if hadPushErrs {
		r.lastPushErrs = nil // clear before retry; repopulated below if retry fails
	}
	sandboxes := make([]domain.SandboxID, 0, len(r.sandboxes))
	for id := range r.sandboxes {
		sandboxes = append(sandboxes, id)
	}
	r.mu.Unlock()

	if (rotated || hadPushErrs) && len(sandboxes) > 0 {
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

// ForcePush obtains the current token (via the same path as Token) and calls
// [Broker.SetRealToken] for sandboxID unconditionally, bypassing the rotation-
// detect guard in vend.
//
// Use this after [Broker.RegisterPlaceholder] re-mints a scope for a sandbox
// that is already registered with this Refresher. The re-mint replaces the
// broker entry (wiping the previously pushed real token), and a normal Token
// call would skip re-pushing because lastToken has not changed.
func (r *Refresher) ForcePush(ctx context.Context, sandboxID domain.SandboxID) error {
	tok, _, err := r.Token(ctx)
	if err != nil {
		return fmt.Errorf("cred: ForcePush: obtain token: %w", err)
	}
	if sErr := r.broker.SetRealToken(sandboxID, r.host, tok); sErr != nil {
		// Record the failure so the ticker's vend retry path (hadPushErrs) can
		// repair it. Without this, a ForcePush failure leaves the guest on an
		// unresolvable placeholder until the next genuine token rotation (up to
		// an hour), with no automatic recovery.
		//
		// The window for a concurrent rotation between Token() and SetRealToken is
		// bounded by WithStoreLock (internal/core/perimeter/cred/store.go): the
		// production path (r.base != nil) holds a process-global file lock across
		// the entire HTTP refresh + disk save cycle. A second HTTP rotation is
		// therefore unreachable within this sub-millisecond window. No re-check is
		// needed; the ticker's vend retry path handles any push errors.
		r.mu.Lock()
		r.lastPushErrs = append(r.lastPushErrs, fmt.Errorf("sandbox %v: %w", sandboxID, sErr))
		r.mu.Unlock()
		return fmt.Errorf("cred: ForcePush: set real token: %w", sErr)
	}
	return nil
}

// vend records a token vend: it detects rotation, updates the in-process cache,
// pushes the real token to all registered sandboxes if the value changed OR if
// the previous push attempt failed, and returns the access token and its expiry.
//
// vend is the single place where rotation-detect + broker-push + cache-update
// happen for the production (lockedToken) path. Both the fast path (in-process
// cache hit) and the slow path (post HTTP refresh) call vend, ensuring broker
// push is never skipped.
//
// Push retry: if a previous push failed (e.g., ForcePush's SetRealToken error
// recorded in lastPushErrs), the next vend call retries the push regardless of
// rotation, clearing lastPushErrs before the attempt and repopulating it only
// if the retry also fails.
func (r *Refresher) vend(tok *oauth2.Token) (string, time.Time, error) {
	r.mu.Lock()
	rotated := tok.AccessToken != r.lastToken
	hadPushErrs := len(r.lastPushErrs) > 0
	if rotated {
		r.lastToken = tok.AccessToken
		r.lastPushErrs = nil
	} else if hadPushErrs {
		// Clear before the retry round; re-populated below if the retry also fails.
		r.lastPushErrs = nil
	}
	r.cachedTok = tok
	sandboxes := make([]domain.SandboxID, 0, len(r.sandboxes))
	for id := range r.sandboxes {
		sandboxes = append(sandboxes, id)
	}
	r.mu.Unlock()

	if (rotated || hadPushErrs) && len(sandboxes) > 0 {
		var pushErrs []error
		for _, id := range sandboxes {
			if sErr := r.broker.SetRealToken(id, r.host, tok.AccessToken); sErr != nil {
				pushErrs = append(pushErrs, fmt.Errorf("sandbox %v: %w", id, sErr))
			}
		}
		if len(pushErrs) > 0 {
			r.mu.Lock()
			r.lastPushErrs = pushErrs
			r.mu.Unlock()
		}
	}

	return tok.AccessToken, tok.Expiry, nil
}

// lockedToken is the production token-vend path. It holds a process-global file
// lock (via [WithStoreLock]) across the entire in-process cache check → disk
// re-read → HTTP refresh (if needed) → disk save cycle.
//
// Fast path: if the in-process cached token is still fresh (>refreshExpiryDelta
// until expiry), call vend immediately without any I/O or lock acquisition.
//
// Slow path (token stale): acquire the file lock, re-read the store. If another
// process refreshed while we waited for the lock the disk token is now fresh —
// use it and skip the HTTP call. Otherwise make the HTTP refresh call while
// holding the lock and save the result.
func (r *Refresher) lockedToken() (string, time.Time, error) {
	// Fast path: in-process cache hit — no lock, no I/O.
	// vend is called even on cache hit so the hadPushErrs retry fires each tick.
	r.mu.Lock()
	ct := r.cachedTok
	r.mu.Unlock()
	if ct != nil && ct.AccessToken != "" && time.Until(ct.Expiry) > refreshExpiryDelta {
		return r.vend(ct)
	}

	// Slow path: acquire file lock, re-read disk, refresh if still needed.
	var (
		result  *oauth2.Token // token to vend; set inside the closure
		savedRT string        // non-empty iff an HTTP refresh was made and a save was attempted
	)
	lockErr := WithStoreLock(context.Background(), r.storePath, func(diskStore *DedicatedCredStore) (*DedicatedCredStore, error) {
		// --- Begin cross-process critical section ---

		// Sync base.rt from disk before any HTTP call so a sibling-process
		// rotation is used rather than a potentially stale in-memory value.
		if diskStore != nil && diskStore.RefreshToken != "" {
			r.base.mu.Lock()
			r.base.rt = diskStore.RefreshToken
			r.base.mu.Unlock()
		}

		// Re-check: another process may have refreshed while we waited for the lock.
		if diskStore != nil && diskStore.AccessToken != "" && time.Until(diskStore.ExpiresAt) > refreshExpiryDelta {
			tok := &oauth2.Token{
				AccessToken:  diskStore.AccessToken,
				RefreshToken: diskStore.RefreshToken,
				Expiry:       diskStore.ExpiresAt,
				TokenType:    diskStore.TokenType,
			}
			result = tok
			return nil, nil // no save needed
		}

		// On-disk token is also stale. Make the HTTP refresh call WHILE HOLDING
		// THE FILE LOCK so no other process can concurrently consume the same
		// refresh_token.
		newTok, err := r.base.Token() // HTTP round-trip (≈200 ms)
		if err != nil {
			return nil, fmt.Errorf("cred: refresher: HTTP refresh: %w", err)
		}
		if newTok.AccessToken == "" {
			return nil, fmt.Errorf("cred: refresher: HTTP refresh returned empty access token")
		}

		rt := newTok.RefreshToken
		if rt == "" {
			r.base.mu.Lock()
			rt = r.base.rt
			r.base.mu.Unlock()
		}
		tt := newTok.TokenType
		if tt == "" {
			tt = r.meta.tokenType
		}
		result = newTok

		if rt == "" {
			return nil, nil // no refresh_token to persist
		}
		savedRT = rt
		return &DedicatedCredStore{
			AccessToken:   newTok.AccessToken,
			RefreshToken:  rt,
			ExpiresAt:     newTok.Expiry,
			TokenType:     tt,
			ClientID:      r.meta.clientID,
			ClientSecret:  r.meta.clientSecret,
			TokenEndpoint: r.meta.tokenEndpoint,
		}, nil
		// --- End cross-process critical section ---
	})

	if lockErr != nil {
		if savedRT != "" {
			// HTTP refresh succeeded but SaveStore failed. Surface via PersistError.
			r.mu.Lock()
			r.lastPersistErr = lockErr
			r.mu.Unlock()
			slog.Warn("cred: refresher: failed to persist rotated refresh_token; process restart may hit invalid_grant",
				"path", r.storePath, "err", lockErr)
			// Fall through: result is set, we can still vend.
		} else {
			return "", time.Time{}, lockErr
		}
	}

	if result == nil || result.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("cred: refresher: lockedToken: no valid token after lock cycle")
	}

	if savedRT != "" && lockErr == nil {
		r.mu.Lock()
		r.lastPersistedRefresh = savedRT
		r.lastPersistErr = nil
		r.mu.Unlock()
	}

	return r.vend(result)
}
