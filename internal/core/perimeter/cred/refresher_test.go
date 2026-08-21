package cred

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"golang.org/x/oauth2"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

// fakeTokenSource is an injectable [oauth2.TokenSource] that returns tokens
// from a pre-loaded slice. When the slice is exhausted it returns errNoMore.
// Set the err field to force every call to fail (refresh-failure test path).
type fakeTokenSource struct {
	mu     sync.Mutex
	tokens []*oauth2.Token
	idx    int
	err    error // if non-nil, every Token() call returns this error
}

var errNoMoreTokens = errors.New("fake: no more tokens")

func (f *fakeTokenSource) Token() (*oauth2.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.idx >= len(f.tokens) {
		return nil, errNoMoreTokens
	}
	t := f.tokens[f.idx]
	f.idx++
	return t, nil
}

// fakeRealTokenSetter records every [realTokenSetter.SetRealToken] call.
type fakeRealTokenSetter struct {
	mu    sync.Mutex
	calls []setCall
	err   error // if non-nil, every call returns this error
}

type setCall struct {
	sandboxID domain.SandboxID
	host      string
	token     string
}

func (f *fakeRealTokenSetter) SetRealToken(id domain.SandboxID, host, tok string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, setCall{id, host, tok})
	return f.err
}

func (f *fakeRealTokenSetter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// makeTok returns an oauth2.Token with the given access token value and expiry.
func makeTok(val string, expiry time.Time) *oauth2.Token {
	return &oauth2.Token{AccessToken: val, TokenType: "Bearer", Expiry: expiry}
}

// writeStoreJSON writes a DedicatedCredStore JSON fixture to dir and returns
// the path. ExpiresAt is set in the past so that ReuseTokenSourceWithExpiry
// immediately treats the initial token as expired and calls the base source.
func writeStoreJSON(t *testing.T, dir, tokenEndpoint string) string {
	t.Helper()
	s := map[string]interface{}{
		"access_token":   "initial-access-token",
		"refresh_token":  "valid-refresh-token",
		"expires_at":     "2000-01-01T00:00:00Z", // in the past → forces refresh
		"token_type":     "Bearer",
		"client_id":      "test-client",
		"client_secret":  "",
		"token_endpoint": tokenEndpoint,
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("writeStoreJSON: marshal: %v", err)
	}
	p := filepath.Join(dir, "store.json")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatalf("writeStoreJSON: write: %v", err)
	}
	return p
}

// ── Registry behaviour (injected fake TokenSource) ────────────────────────────

// TestRefresher_RotationPushesToAllSandboxes verifies that when the vended
// access token changes, SetRealToken is called for every registered sandbox.
func TestRefresher_RotationPushesToAllSandboxes(t *testing.T) {
	sid1 := domain.SandboxID{1}
	sid2 := domain.SandboxID{2}
	const host = "api.anthropic.com"

	future := time.Now().Add(time.Hour)
	fts := &fakeTokenSource{
		tokens: []*oauth2.Token{
			makeTok("token-alpha", future),
			makeTok("token-beta", future),
		},
	}
	broker := &fakeRealTokenSetter{}

	r := newRefresherWithSource(fts, host, broker, "", credStoreMeta{})
	r.Register(sid1)
	r.Register(sid2)

	// First call: lastToken="" → rotation → push token-alpha to both sandboxes.
	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token(): %v", err)
	}
	if tok != "token-alpha" {
		t.Errorf("got %q want token-alpha", tok)
	}
	if got := broker.callCount(); got != 2 {
		t.Errorf("after first rotation: want 2 SetRealToken calls, got %d", got)
	}
	broker.mu.Lock()
	for _, c := range broker.calls {
		if c.token != "token-alpha" {
			t.Errorf("SetRealToken: want token-alpha, got %q", c.token)
		}
		if c.host != host {
			t.Errorf("SetRealToken: want host %q, got %q", host, c.host)
		}
	}
	broker.mu.Unlock()

	// Second call: fts returns token-beta → rotation again → push token-beta.
	tok2, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token(): %v", err)
	}
	if tok2 != "token-beta" {
		t.Errorf("got %q want token-beta", tok2)
	}
	if got := broker.callCount(); got != 4 {
		t.Errorf("after second rotation: want 4 SetRealToken calls, got %d", got)
	}
}

// TestRefresher_NoRotation_NoBrokerCall verifies that when the same token is
// vended twice (no rotation), SetRealToken is NOT called a second time.
func TestRefresher_NoRotation_NoBrokerCall(t *testing.T) {
	sid := domain.SandboxID{5}
	const host = "api.anthropic.com"

	future := time.Now().Add(time.Hour)
	tok := makeTok("stable-token", future)
	// Return the same token object on both calls.
	fts := &fakeTokenSource{tokens: []*oauth2.Token{tok, tok}}
	broker := &fakeRealTokenSetter{}

	r := newRefresherWithSource(fts, host, broker, "", credStoreMeta{})
	r.Register(sid)

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("first Token(): %v", err)
	}
	before := broker.callCount()

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("second Token(): %v", err)
	}
	after := broker.callCount()

	if after != before {
		t.Errorf("SetRealToken called on non-rotation: before=%d after=%d", before, after)
	}
}

// TestRefresher_DeregisterStopsUpdates verifies that after Deregister, a
// subsequent rotation does NOT push to the removed sandbox.
func TestRefresher_DeregisterStopsUpdates(t *testing.T) {
	sid := domain.SandboxID{3}
	const host = "api.anthropic.com"

	future := time.Now().Add(time.Hour)
	fts := &fakeTokenSource{
		tokens: []*oauth2.Token{
			makeTok("tok-1", future),
			makeTok("tok-2", future),
		},
	}
	broker := &fakeRealTokenSetter{}
	r := newRefresherWithSource(fts, host, broker, "", credStoreMeta{})
	r.Register(sid)

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("first Token(): %v", err)
	}
	before := broker.callCount()

	r.Deregister(sid)

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("second Token(): %v", err)
	}
	after := broker.callCount()
	if after != before {
		t.Errorf("SetRealToken called after Deregister: before=%d after=%d", before, after)
	}
}

// TestRefresher_NoSandboxes_NoBrokerCalls verifies that Token() never calls the
// broker when no sandboxes are registered.
func TestRefresher_NoSandboxes_NoBrokerCalls(t *testing.T) {
	future := time.Now().Add(time.Hour)
	fts := &fakeTokenSource{tokens: []*oauth2.Token{makeTok("tok-x", future)}}
	broker := &fakeRealTokenSetter{}
	r := newRefresherWithSource(fts, "api.anthropic.com", broker, "", credStoreMeta{})
	// No Register calls.

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token(): %v", err)
	}
	if n := broker.callCount(); n != 0 {
		t.Errorf("expected 0 broker calls with no sandboxes, got %d", n)
	}
}

// TestRefresher_PushErrors_SurfacedButDoesNotFailToken verifies that a broker
// error does not cause Token() to fail; errors are retrievable via PushErrors.
func TestRefresher_PushErrors_SurfacedButDoesNotFailToken(t *testing.T) {
	sid := domain.SandboxID{7}
	future := time.Now().Add(time.Hour)
	fts := &fakeTokenSource{tokens: []*oauth2.Token{makeTok("tok-push-err", future)}}
	broker := &fakeRealTokenSetter{err: errors.New("no placeholder registered")}

	r := newRefresherWithSource(fts, "api.anthropic.com", broker, "", credStoreMeta{})
	r.Register(sid)

	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() must not fail on broker push error, got: %v", err)
	}
	if tok != "tok-push-err" {
		t.Errorf("got %q want tok-push-err", tok)
	}
	errs := r.PushErrors()
	if len(errs) == 0 {
		t.Error("PushErrors() must return the broker error, got empty")
	}
}

// TestRefresher_RefreshFailure verifies that a base-source error surfaces from
// Token() as a non-nil, descriptive error — no stale or blank token is returned.
func TestRefresher_RefreshFailure(t *testing.T) {
	refreshErr := errors.New("transport: connection refused")
	fts := &fakeTokenSource{err: refreshErr}
	broker := &fakeRealTokenSetter{}

	r := newRefresherWithSource(fts, "api.anthropic.com", broker, "", credStoreMeta{})
	r.Register(domain.SandboxID{4})

	tok, _, err := r.Token(context.Background())
	if err == nil {
		t.Fatal("expected error from refresh failure, got nil")
	}
	if tok != "" {
		t.Errorf("on error, token must be empty, got %q", tok)
	}
}

// ── Constructor / store validation ────────────────────────────────────────────

// TestRefresher_StoreAbsent verifies that NewRefresher wraps ErrStoreAbsent when
// the store file does not exist.
func TestRefresher_StoreAbsent(t *testing.T) {
	broker := &fakeRealTokenSetter{}
	_, err := NewRefresher(
		filepath.Join(t.TempDir(), "nonexistent.json"),
		"api.anthropic.com",
		broker,
	)
	if err == nil {
		t.Fatal("expected error for absent store, got nil")
	}
	if !errors.Is(err, ErrStoreAbsent) {
		t.Errorf("want ErrStoreAbsent in error chain, got: %v", err)
	}
}

// TestRefresher_StoreValidation_EmptyRefreshToken checks that an absent
// refresh_token is caught eagerly at construction (not silently at refresh time).
func TestRefresher_StoreValidation_EmptyRefreshToken(t *testing.T) {
	s := map[string]interface{}{
		"access_token":   "tok",
		"refresh_token":  "", // missing
		"expires_at":     "2099-01-01T00:00:00Z",
		"token_type":     "Bearer",
		"client_id":      "cid",
		"client_secret":  "",
		"token_endpoint": "https://example.com/token",
	}
	data, _ := json.Marshal(s)
	p := filepath.Join(t.TempDir(), "store.json")
	_ = os.WriteFile(p, data, 0600)

	_, err := NewRefresher(p, "api.anthropic.com", &fakeRealTokenSetter{})
	if err == nil {
		t.Fatal("expected error for empty refresh_token, got nil")
	}
}

// TestRefresher_StoreValidation_EmptyTokenEndpoint checks that an absent
// token_endpoint is caught eagerly at construction.
func TestRefresher_StoreValidation_EmptyTokenEndpoint(t *testing.T) {
	s := map[string]interface{}{
		"access_token":   "tok",
		"refresh_token":  "rt",
		"expires_at":     "2099-01-01T00:00:00Z",
		"token_type":     "Bearer",
		"client_id":      "cid",
		"client_secret":  "",
		"token_endpoint": "", // missing
	}
	data, _ := json.Marshal(s)
	p := filepath.Join(t.TempDir(), "store.json")
	_ = os.WriteFile(p, data, 0600)

	_, err := NewRefresher(p, "api.anthropic.com", &fakeRealTokenSetter{})
	if err == nil {
		t.Fatal("expected error for empty token_endpoint, got nil")
	}
}

// ── Real oauth2 stack (httptest — loopback only, no external network) ────────

// TestRefresher_NewRefresher_RealStack exercises the full NewRefresher path:
// an expired store triggers lockedToken → oauthRefreshBase →
// HTTP to the fake token endpoint → new token returned and pushed to broker.
func TestRefresher_NewRefresher_RealStack(t *testing.T) {
	const freshToken = "server-issued-access-token"
	const host = "api.anthropic.com"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  freshToken,
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "refresh-rotated",
		}); err != nil {
			t.Errorf("httptest encode: %v", err)
		}
	}))
	defer srv.Close()

	storePath := writeStoreJSON(t, t.TempDir(), srv.URL)
	sid := domain.SandboxID{10}
	broker := &fakeRealTokenSetter{}

	r, err := NewRefresher(storePath, host, broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}
	r.Register(sid)

	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("Token(): %v", err)
	}
	if tok != freshToken {
		t.Errorf("got %q want %q", tok, freshToken)
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.calls) != 1 {
		t.Fatalf("want 1 SetRealToken call, got %d", len(broker.calls))
	}
	if broker.calls[0].token != freshToken {
		t.Errorf("SetRealToken got %q want %q", broker.calls[0].token, freshToken)
	}
	if broker.calls[0].host != host {
		t.Errorf("SetRealToken host got %q want %q", broker.calls[0].host, host)
	}
	if broker.calls[0].sandboxID != sid {
		t.Errorf("SetRealToken sandboxID got %v want %v", broker.calls[0].sandboxID, sid)
	}

	// Verify NewRefresher's storePath+meta wiring persisted the rotated
	// refresh_token and the static metadata (token_endpoint, client_id).
	// The httptest server returns refresh_token="refresh-rotated".
	if pErr := r.PersistError(); pErr != nil {
		t.Errorf("PersistError() must be nil, got: %v", pErr)
	}
	persisted, err := LoadStore(storePath)
	if err != nil {
		t.Fatalf("LoadStore after Token(): %v", err)
	}
	if persisted.RefreshToken != "refresh-rotated" {
		t.Errorf("persisted refresh_token: got %q, want refresh-rotated", persisted.RefreshToken)
	}
	if persisted.TokenEndpoint != srv.URL {
		t.Errorf("persisted token_endpoint: got %q, want %q", persisted.TokenEndpoint, srv.URL)
	}
	if persisted.ClientID != "test-client" {
		t.Errorf("persisted client_id: got %q, want test-client", persisted.ClientID)
	}
}

// ── P5-S5b: refresh_token persistence tests ──────────────────────────────────

// makeStoreMeta returns a credStoreMeta populated with test values.
func makeStoreMeta(tokenEndpoint string) credStoreMeta {
	return credStoreMeta{
		tokenType:     "Bearer",
		clientID:      "test-client",
		clientSecret:  "",
		tokenEndpoint: tokenEndpoint,
	}
}

// makeTokRT returns an oauth2.Token with access token, refresh token, and expiry.
func makeTokRT(at, rt string, expiry time.Time) *oauth2.Token {
	return &oauth2.Token{AccessToken: at, RefreshToken: rt, TokenType: "Bearer", Expiry: expiry}
}

// TestRefresherPersistsRotatedRefreshTokenAcrossRestart is the core acceptance
// test for TBD-P5-4. It exercises the exact failure mode: after a refresh_token
// rotation, a second NewRefresher loading the same path must see the rotated
// value (not the initial one). A process restart that sees the initial (consumed)
// refresh_token would fail with invalid_grant.
func TestRefresherPersistsRotatedRefreshTokenAcrossRestart(t *testing.T) {
	const initialRT = "rt-initial"
	const rotatedRT = "rt-rotated"
	const ep = "https://token.example.com/token"
	future := time.Now().Add(time.Hour)

	// Write the initial store to a temp file.
	dir := t.TempDir()
	p := filepath.Join(dir, "creds.json")
	if err := SaveStore(p, &DedicatedCredStore{
		AccessToken:   "at-initial",
		RefreshToken:  initialRT,
		ExpiresAt:     future,
		TokenType:     "Bearer",
		ClientID:      "test-client",
		ClientSecret:  "",
		TokenEndpoint: ep,
	}); err != nil {
		t.Fatalf("SaveStore initial: %v", err)
	}

	// Fake token source returns a token with a rotated refresh_token.
	fts := &fakeTokenSource{
		tokens: []*oauth2.Token{makeTokRT("at-new", rotatedRT, future)},
	}
	broker := &fakeRealTokenSetter{}
	r := newRefresherWithSource(fts, "api.anthropic.com", broker, p, makeStoreMeta(ep))

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token(): %v", err)
	}

	// Verify no persist error.
	if pErr := r.PersistError(); pErr != nil {
		t.Fatalf("PersistError() must be nil after successful persist, got: %v", pErr)
	}

	// Simulate a process restart: load the store from the SAME path via LoadStore
	// for a quick RT check, then via NewRefresher to assert the credential is
	// usable (non-empty endpoint passes validation — no dial needed here).
	loaded, err := LoadStore(p)
	if err != nil {
		t.Fatalf("LoadStore after rotation: %v", err)
	}
	if loaded.RefreshToken != rotatedRT {
		t.Errorf("after restart: got refresh_token %q, want %q (rotated value)", loaded.RefreshToken, rotatedRT)
	}

	// NewRefresher validates non-empty refresh_token and token_endpoint eagerly.
	// A zeroed meta (missing endpoint) would cause this to fail — proving meta is wired.
	_, err = NewRefresher(p, "api.anthropic.com", &fakeRealTokenSetter{})
	if err != nil {
		t.Errorf("second NewRefresher (restart) must succeed on persisted store, got: %v", err)
	}
}

// TestRefresherEmptyRotatedRefreshTokenDoesNotClobberStore verifies that when
// the token source returns a token with an empty RefreshToken, the on-disk store
// is NOT overwritten (we keep the prior stored refresh_token).
func TestRefresherEmptyRotatedRefreshTokenDoesNotClobberStore(t *testing.T) {
	const existingRT = "rt-existing"
	const ep = "https://token.example.com/token"
	future := time.Now().Add(time.Hour)

	dir := t.TempDir()
	p := filepath.Join(dir, "creds.json")
	if err := SaveStore(p, &DedicatedCredStore{
		AccessToken:   "at-initial",
		RefreshToken:  existingRT,
		ExpiresAt:     future,
		TokenType:     "Bearer",
		ClientID:      "test-client",
		ClientSecret:  "",
		TokenEndpoint: ep,
	}); err != nil {
		t.Fatalf("SaveStore initial: %v", err)
	}

	// Token source returns a token with NO refresh_token (empty string).
	fts := &fakeTokenSource{
		tokens: []*oauth2.Token{makeTok("at-new", future)}, // makeTok has no RefreshToken
	}
	broker := &fakeRealTokenSetter{}
	r := newRefresherWithSource(fts, "api.anthropic.com", broker, p, makeStoreMeta(ep))

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token(): %v", err)
	}

	// The on-disk store must still have the original refresh_token.
	loaded, err := LoadStore(p)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if loaded.RefreshToken != existingRT {
		t.Errorf("store was clobbered: got refresh_token %q, want %q", loaded.RefreshToken, existingRT)
	}
	// PersistError must be nil — we skipped persistence, which is correct.
	if pErr := r.PersistError(); pErr != nil {
		t.Errorf("PersistError() should be nil when persist was skipped, got: %v", pErr)
	}
}

// TestRefresherPersistFailureSurfacedViaPersistError verifies that a SaveStore
// failure (bad path) is surfaced via PersistError() and does NOT fail Token().
func TestRefresherPersistFailureSurfacedViaPersistError(t *testing.T) {
	const ep = "https://token.example.com/token"
	future := time.Now().Add(time.Hour)

	// Use a path in a non-existent directory — SaveStore will fail.
	badPath := filepath.Join(t.TempDir(), "nonexistent-subdir", "creds.json")

	fts := &fakeTokenSource{
		tokens: []*oauth2.Token{makeTokRT("at-ok", "rt-rotated", future)},
	}
	broker := &fakeRealTokenSetter{}
	r := newRefresherWithSource(fts, "api.anthropic.com", broker, badPath, makeStoreMeta(ep))

	// Token() must succeed even though SaveStore fails.
	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() must not fail on persist error, got: %v", err)
	}
	if tok != "at-ok" {
		t.Errorf("got token %q want at-ok", tok)
	}

	// PersistError must carry the SaveStore failure.
	pErr := r.PersistError()
	if pErr == nil {
		t.Fatal("PersistError() must return the SaveStore error, got nil")
	}
	t.Logf("surfaced persist error (expected): %v", pErr)
}

// TestRefresherPersistErrorDistinctFromPushErrors verifies that persist errors
// and push errors are separate: a push error does not set PersistError, and
// a persist error does not set PushErrors.
func TestRefresherPersistErrorDistinctFromPushErrors(t *testing.T) {
	const ep = "https://token.example.com/token"
	future := time.Now().Add(time.Hour)
	sid := domain.SandboxID{99}

	// Persist will fail (bad path); push will also fail (broker error).
	badPath := filepath.Join(t.TempDir(), "no-such-dir", "creds.json")
	broker := &fakeRealTokenSetter{err: errors.New("broker: no placeholder")}

	fts := &fakeTokenSource{
		tokens: []*oauth2.Token{makeTokRT("at-x", "rt-x", future)},
	}
	r := newRefresherWithSource(fts, "api.anthropic.com", broker, badPath, makeStoreMeta(ep))
	r.Register(sid)

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token() must not fail: %v", err)
	}
	if r.PersistError() == nil {
		t.Error("PersistError() should be non-nil (bad path)")
	}
	if len(r.PushErrors()) == 0 {
		t.Error("PushErrors() should be non-nil (broker error)")
	}
	// Cross-check: PushErrors does not contain the persist error message.
	for _, pe := range r.PushErrors() {
		if pe == r.PersistError() {
			t.Error("PushErrors() must not contain the persist error object")
		}
	}
}

// TestRefresherNoPersistWhenStorePathEmpty verifies that tests using
// newRefresherWithSource with storePath="" never attempt persistence.
func TestRefresherNoPersistWhenStorePathEmpty(t *testing.T) {
	future := time.Now().Add(time.Hour)
	fts := &fakeTokenSource{
		tokens: []*oauth2.Token{makeTokRT("at-z", "rt-z", future)},
	}
	r := newRefresherWithSource(fts, "api.anthropic.com", &fakeRealTokenSetter{}, "", credStoreMeta{})

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token(): %v", err)
	}
	if pErr := r.PersistError(); pErr != nil {
		t.Errorf("PersistError() must be nil when storePath is empty, got: %v", pErr)
	}
}

// TestRefresherSameRefreshTokenNotRepersisted verifies that calling Token()
// twice with the same refresh_token only persists once (idempotent).
func TestRefresherSameRefreshTokenNotRepersisted(t *testing.T) {
	const ep = "https://token.example.com/token"
	future := time.Now().Add(time.Hour)
	const rt = "rt-stable"

	dir := t.TempDir()
	p := filepath.Join(dir, "creds.json")
	if err := SaveStore(p, &DedicatedCredStore{
		AccessToken:   "at-old",
		RefreshToken:  "rt-old", // different from rt so first call persists
		ExpiresAt:     future,
		TokenType:     "Bearer",
		ClientID:      "test-client",
		ClientSecret:  "",
		TokenEndpoint: ep,
	}); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	// Both calls return the same refresh token.
	tok := makeTokRT("at-stable", rt, future)
	fts := &fakeTokenSource{tokens: []*oauth2.Token{tok, tok}}
	r := newRefresherWithSource(fts, "api.anthropic.com", &fakeRealTokenSetter{}, p, makeStoreMeta(ep))

	// First call persists rt.
	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("first Token(): %v", err)
	}
	// Second call: same rt → no re-persist; lastPersistedRefresh is already rt.
	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("second Token(): %v", err)
	}

	// On-disk value must be rt (not the old value).
	loaded, err := LoadStore(p)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if loaded.RefreshToken != rt {
		t.Errorf("got refresh_token %q want %q", loaded.RefreshToken, rt)
	}
	if r.PersistError() != nil {
		t.Errorf("unexpected persist error: %v", r.PersistError())
	}
}

// TestRefresher_NewRefresher_InvalidGrant verifies that an `invalid_grant`
// response (expired or revoked refresh token) surfaces as a clear error from
// Token() — not a stale or blank token.
func TestRefresher_NewRefresher_InvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"error":             "invalid_grant",
			"error_description": "refresh token expired",
		}); err != nil {
			t.Errorf("httptest encode: %v", err)
		}
	}))
	defer srv.Close()

	storePath := writeStoreJSON(t, t.TempDir(), srv.URL)
	broker := &fakeRealTokenSetter{}

	r, err := NewRefresher(storePath, "api.anthropic.com", broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	tok, _, err := r.Token(context.Background())
	if err == nil {
		t.Fatalf("expected error from invalid_grant, got nil (tok=%q)", tok)
	}
	if tok != "" {
		t.Errorf("on error, token must be empty, got %q", tok)
	}
	t.Logf("surfaced expected error: %v", err)
}

// ── DEFECT 3 / wiring proof ───────────────────────────────────────────────────

// TestNewRefresher_LockedPathWired is the wiring proof for the cross-process
// lock. It asserts a BEHAVIOURAL consequence of the locked path being taken:
// WithStoreLock creates the lock file at storePath+".lock". If Token() bypasses
// the locked path (e.g. because r.base was not wired), the lock file does not
// exist and the test fails.
//
// The test uses an expired store (forces the slow path — fast path skips the
// lock) and an httptest server so no real network is involved.
//
// # Mutation proof
//
// Comment out `r.base = base` in NewRefresher → r.base == nil → Token() falls
// to the unlocked test path → lock file is never created → test goes RED:
//
//	--- FAIL: TestNewRefresher_LockedPathWired (0.00s)
//	    refresher_test.go:NNN: lock file does not exist after Token(): storePath+".lock"
//	    want: lock file created by WithStoreLock (proof the locked path ran)
func TestNewRefresher_LockedPathWired(t *testing.T) {
	const freshToken = "locked-path-token"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  freshToken,
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "rt-locked-fresh",
		}); err != nil {
			t.Errorf("httptest encode: %v", err)
		}
	}))
	defer srv.Close()

	// writeStoreJSON sets expires_at in the past → forces the slow path so
	// WithStoreLock is always invoked (fast path skips the lock).
	storePath := writeStoreJSON(t, t.TempDir(), srv.URL)
	lockPath := lockFilePath(storePath)

	r, err := NewRefresher(storePath, "api.anthropic.com", &fakeRealTokenSetter{})
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("Token(): %v", err)
	}
	if tok != freshToken {
		t.Errorf("got token %q, want %q", tok, freshToken)
	}

	// Lock file existence is the behavioural proof: WithStoreLock creates it via
	// OpenLock (O_CREATE). If the unlocked path ran instead, this file does not
	// exist and the wiring is broken.
	if _, statErr := os.Stat(lockPath); os.IsNotExist(statErr) {
		t.Errorf("lock file does not exist after Token(): %s\nwant: lock file created by WithStoreLock (proof the locked path ran)", lockPath)
	}
}

// ── DEFECT 1 mutation proof ───────────────────────────────────────────────────

// TestLockedToken_FastPathPushesToBroker asserts that even when the in-process
// cached token is still fresh (fast path taken, no HTTP call), the broker push
// happens on the first Token() call of a freshly-constructed Refresher.
//
// The regression: lockedToken()'s original fast path did an early return before
// vend(), so lastToken was "" and SetRealToken was never called. The broker had
// no real token behind the sandbox placeholder, and the MITM proxied nothing.
//
// # Mutation proof
//
// Change the fast-path call from `return r.vend(ct)` back to the early return
// `return ct.AccessToken, ct.Expiry, nil`. The broker push is skipped. With a
// registered sandbox the test goes RED:
//
//	--- FAIL: TestLockedToken_FastPathPushesToBroker (0.00s)
//	    refresher_test.go:NNN: SetRealToken call count = 0, want 1
//	    (broker never received the real token; fast path skipped vend)
func TestLockedToken_FastPathPushesToBroker(t *testing.T) {
	const freshToken = "fresh-cached-token"
	sid := domain.SandboxID{42}
	broker := &fakeRealTokenSetter{}

	// Build a store with a FUTURE expiry so the fast path is taken.
	dir := t.TempDir()
	storePath := filepath.Join(dir, "creds.json")
	futureExpiry := time.Now().Add(time.Hour)
	store := &DedicatedCredStore{
		AccessToken:   freshToken,
		RefreshToken:  "rt-still-valid",
		ExpiresAt:     futureExpiry,
		TokenType:     "Bearer",
		ClientID:      "test-client",
		ClientSecret:  "",
		TokenEndpoint: "http://unused.example.com/token",
	}
	if err := SaveStore(storePath, store); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	r, err := NewRefresher(storePath, "api.anthropic.com", broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}
	r.Register(sid)

	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("Token(): %v", err)
	}
	if tok != freshToken {
		t.Errorf("got token %q, want %q", tok, freshToken)
	}

	// The fast path must still push to the broker. On first call lastToken is ""
	// so rotated==true regardless of which path is taken — vend() must run.
	broker.mu.Lock()
	n := len(broker.calls)
	broker.mu.Unlock()
	if n != 1 {
		t.Errorf("SetRealToken call count = %d, want 1\n(broker never received the real token; fast path skipped vend)", n)
	}
}

// ── DEFECT 2 mutation proof ───────────────────────────────────────────────────

// TestLockedToken_SlowPathUsesDiskRefreshToken asserts that when the slow path
// is taken, the HTTP refresh grant uses the refresh_token from the on-disk store
// (not the potentially-stale in-memory base.rt).
//
// Scenario: after construction, base.rt is set to "initial-rt". A sibling process
// then refreshes and writes "rotated-rt" to disk. The next Token() call finds the
// access token expired, enters the slow path, and MUST send "rotated-rt" in the
// HTTP grant — not the stale "initial-rt".
//
// # Mutation proof
//
// Remove the unconditional `r.base.rt = diskStore.RefreshToken` sync from
// lockedToken (leave only the sync on the "disk token fresh" branch that was
// present in the original broken code). The HTTP server records which refresh_token
// it received. Without the fix it gets "stale-rt"; with the fix it gets "disk-rt".
// The test goes RED:
//
//	--- FAIL: TestLockedToken_SlowPathUsesDiskRefreshToken (0.00s)
//	    refresher_test.go:NNN: HTTP grant used refresh_token "stale-rt", want "disk-rt"
//	    (base.rt was not synced from disk before the HTTP call)
func TestLockedToken_SlowPathUsesDiskRefreshToken(t *testing.T) {
	const freshToken = "server-fresh-token"
	const diskRT = "disk-rt"
	const staleRT = "stale-rt"

	var gotRefreshToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Errorf("httptest ParseForm: %v", err)
		}
		gotRefreshToken = req.FormValue("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  freshToken,
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "server-new-rt",
		}); err != nil {
			t.Errorf("httptest encode: %v", err)
		}
	}))
	defer srv.Close()

	// Create store with an expired access token (force slow path) and diskRT.
	dir := t.TempDir()
	storePath := filepath.Join(dir, "creds.json")
	expiredStore := &DedicatedCredStore{
		AccessToken:   "expired-token",
		RefreshToken:  diskRT,
		ExpiresAt:     time.Now().Add(-time.Hour), // expired → slow path
		TokenType:     "Bearer",
		ClientID:      "test-client",
		ClientSecret:  "",
		TokenEndpoint: srv.URL,
	}
	if err := SaveStore(storePath, expiredStore); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	r, err := NewRefresher(storePath, "api.anthropic.com", &fakeRealTokenSetter{})
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	// Simulate stale in-memory base.rt (as if a sibling process rotated it since
	// construction). White-box access is intentional: this is package cred.
	r.base.mu.Lock()
	r.base.rt = staleRT
	r.base.mu.Unlock()

	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("Token(): %v", err)
	}
	if tok != freshToken {
		t.Errorf("got token %q, want %q", tok, freshToken)
	}

	// The HTTP grant must have used the disk RT, not the stale in-memory one.
	if gotRefreshToken != diskRT {
		t.Errorf("HTTP grant used refresh_token %q, want %q\n(base.rt was not synced from disk before the HTTP call)", gotRefreshToken, diskRT)
	}
}

// ── Finding A: push retry on previous failure ─────────────────────────────────

// TestVend_RetryPushOnPreviousFailure is the regression test for the supervisor
// push-retry fix. On the supervisor/restart path the first 60s ticker fires
// before SeedGuestAgent has registered the broker scope, so SetRealToken returns
// an error. The token has not rotated, so the ORIGINAL code skipped the push on
// the next tick, leaving the guest permanently on a placeholder.
//
// The fix: vend() retries the push whenever lastPushErrs > 0, regardless of
// rotation, clearing lastPushErrs before the attempt and repopulating it only
// if the retry also fails.
//
// # Mutation proof
//
// Revert vend()'s push condition from `rotated || hadPushErrs` back to
// `rotated`. The second Token() call does not push (rotated == false),
// broker.callCount() stays 1, and the test goes RED:
//
//	--- FAIL: TestVend_RetryPushOnPreviousFailure (0.00s)
//	    refresher_test.go:NNN: broker call count after retry tick = 1, want 2
//	    (push not retried after previous failure; supervisor guest stuck on placeholder)
func TestVend_RetryPushOnPreviousFailure(t *testing.T) {
	const stableToken = "stable-supervisor-token"
	sid := domain.SandboxID{88}

	// Loopback HTTP server returns a stable access token on every call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  stableToken,
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "rt-stable",
		}); err != nil {
			t.Errorf("httptest encode: %v", err)
		}
	}))
	defer srv.Close()

	// Expired store forces the slow path on the first Token() call.
	storePath := writeStoreJSON(t, t.TempDir(), srv.URL)

	broker := &fakeRealTokenSetter{err: errors.New("scope not registered: RegisterPlaceholder not yet called")}
	r, err := NewRefresher(storePath, "api.anthropic.com", broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}
	r.Register(sid)

	// First Token() call: slow path (expired store), real token fetched,
	// push FAILS because broker scope is not yet registered.
	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() (first call): %v", err)
	}
	if tok != stableToken {
		t.Errorf("first call: got token %q, want %q", tok, stableToken)
	}
	if broker.callCount() != 1 {
		t.Fatalf("after first Token(): broker call count = %d, want 1", broker.callCount())
	}
	if len(r.PushErrors()) == 0 {
		t.Fatal("after first Token(): PushErrors() empty; want the broker error recorded")
	}

	// Simulate scope becoming available: clear the broker error.
	broker.mu.Lock()
	broker.err = nil
	broker.mu.Unlock()

	// Second Token() call: fast path (token is now fresh in cache).
	// rotated == false (same token), but hadPushErrs == true → push must retry.
	tok2, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() (retry tick): %v", err)
	}
	if tok2 != stableToken {
		t.Errorf("retry tick: got token %q, want %q", tok2, stableToken)
	}
	if broker.callCount() != 2 {
		t.Errorf("broker call count after retry tick = %d, want 2\n(push not retried after previous failure; supervisor guest stuck on placeholder)", broker.callCount())
	}
	if len(r.PushErrors()) != 0 {
		t.Errorf("PushErrors() non-empty after successful retry: %v", r.PushErrors())
	}

	// Confirm the correct token was delivered.
	broker.mu.Lock()
	lastTok := broker.calls[len(broker.calls)-1].token
	broker.mu.Unlock()
	if lastTok != stableToken {
		t.Errorf("broker received token %q on retry, want %q", lastTok, stableToken)
	}
}

// ── C1 regression: post-seed ForcePush after re-mint ─────────────────────────

// TestRefresher_ForcePush_PostSeedRemint reproduces the C1 defect sequence
// against the REAL cred.Broker:
//
//  1. RegisterPlaceholder mints scope with empty realToken (seed attempt 1, guest not reachable).
//  2. r.Register wires the refresher to the sandbox.
//  3. r.Token (ticker) fires: lastToken was "", access token is different → rotated=true → SetRealToken pushes.
//  4. RegisterPlaceholder re-mints (seed attempt 2 succeeds): new entry has realToken="", old placeholder revoked.
//  5. Post-seed call: with the OLD Token() path, rotated=false, hadPushErrs=false → no push → guest gets 401.
//     With ForcePush: SetRealToken is called unconditionally → broker holds real token.
//  6. Assert broker.Resolve(newPlaceholder) == realToken.
//
// Mutation proof (apply to PRODUCTION code, not this file):
//   In refresher.go ForcePush, remove or no-op the r.broker.SetRealToken call.
//   ForcePush returns nil, but broker.Resolve(rec2.Placeholder) == "" → RED:
//   "after post-seed push: broker resolves new placeholder to ..."
// Do NOT mutate the r.ForcePush call on line ~1086 of this file — that mutates
// the test itself and only proves the test checks its own assertion.
func TestRefresher_ForcePush_PostSeedRemint(t *testing.T) {
	ctx := context.Background()
	const host = "api.anthropic.com"
	sbID := domain.SandboxID{42}
	const realToken = "tok-real-c1"

	// Use the REAL Broker — not a fake — as the reviewer specified.
	broker := NewBroker()

	fts := &fakeTokenSource{
		tokens: []*oauth2.Token{
			makeTok(realToken, time.Now().Add(time.Hour)),
			makeTok(realToken, time.Now().Add(time.Hour)), // second call (ForcePush path)
		},
	}
	r := newRefresherWithSource(fts, host, broker, "", credStoreMeta{})

	// Step 1: seed attempt 1 — RegisterPlaceholder mints scope with empty realToken.
	rec1, err := broker.RegisterPlaceholder(sbID, host, "")
	if err != nil {
		t.Fatalf("RegisterPlaceholder (seed 1): %v", err)
	}

	// Step 2: wire refresher.
	r.Register(sbID)

	// Step 3: ticker fires → Token → vend → lastToken changes "" → realToken → push.
	if _, _, tokErr := r.Token(ctx); tokErr != nil {
		t.Fatalf("Token (ticker): %v", tokErr)
	}
	// Verify ticker pushed correctly.
	if got, ok := broker.Resolve(rec1.Placeholder); !ok || got != realToken {
		t.Fatalf("after ticker push: placeholder1 resolves to %q (ok=%v), want %q", got, ok, realToken)
	}

	// Step 4: seed retry → RegisterPlaceholder re-mints scope; realToken wiped in new entry.
	rec2, err := broker.RegisterPlaceholder(sbID, host, "")
	if err != nil {
		t.Fatalf("RegisterPlaceholder (seed retry): %v", err)
	}
	// Old placeholder must be revoked.
	if _, ok := broker.Resolve(rec1.Placeholder); ok {
		t.Error("old placeholder must be revoked after re-mint")
	}
	// New placeholder resolves to "" — the bug state before post-seed push.
	if got, _ := broker.Resolve(rec2.Placeholder); got != "" {
		t.Fatalf("new placeholder pre-push: got %q, want \"\" (precondition)", got)
	}

	// Step 5: post-seed push.
	if pushErr := r.ForcePush(ctx, sbID); pushErr != nil {
		t.Fatalf("ForcePush: %v", pushErr)
	}

	// Step 6: broker must hold the real token on the new placeholder.
	got, ok := broker.Resolve(rec2.Placeholder)
	if !ok {
		t.Fatal("after post-seed push: broker does not know new placeholder (registered=false)")
	}
	if got != realToken {
		t.Errorf("after post-seed push: broker resolves new placeholder to %q, want %q", got, realToken)
	}
}

// ── Finding 2: ForcePush failure must be recorded for ticker retry ────────────

// TestForcePush_FailureIsRetried is the regression test for the F2 fix:
// before the fix, a ForcePush failure was returned to the caller but NOT
// recorded in lastPushErrs, so the next vend tick (rotated=false,
// hadPushErrs=false) silently skipped the retry, leaving the guest on an
// unresolvable placeholder until the next genuine token rotation.
//
// # Mutation proof
//
// Remove the `r.lastPushErrs = append(...)` block inside ForcePush's error
// branch in refresher.go. PushErrors() returns nil after the failed push, so
// the test goes RED:
//
//	--- FAIL: TestForcePush_FailureIsRetried (0.00s)
//	    refresher_test.go:NNN: after failed ForcePush: PushErrors() empty;
//	        want error recorded (needed for ticker retry)
//
// Also: remove the `hadPushErrs` clause from vend's push condition
// (`rotated || hadPushErrs` → `rotated`). The retry tick skips the push,
// broker stays stale, and the test goes RED:
//
//	--- FAIL: TestForcePush_FailureIsRetried (0.00s)
//	    refresher_test.go:NNN: PushErrors() non-empty after retry tick — push not retried
func TestForcePush_FailureIsRetried(t *testing.T) {
	// Use NewRefresher (production locked path → lockedToken → vend) so that
	// vend's hadPushErrs retry logic is exercised. The store has a fresh token
	// so no HTTP call is made.
	const realToken = "tok-fp-retry-test"
	sid := domain.SandboxID{77}

	// Write a fresh store: token expires 1 hour from now, far beyond the
	// refreshExpiryDelta (30 s) so lockedToken's fast path returns the cached token.
	dir := t.TempDir()
	storeData := map[string]interface{}{
		"access_token":   realToken,
		"refresh_token":  "rt-dummy-fp-retry",
		"expires_at":     time.Now().Add(time.Hour).Format(time.RFC3339),
		"token_type":     "Bearer",
		"client_id":      "test-client",
		"client_secret":  "",
		"token_endpoint": "http://localhost:0/no-calls",
	}
	storeBytes, err := json.Marshal(storeData)
	if err != nil {
		t.Fatalf("marshal store: %v", err)
	}
	storePath := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(storePath, storeBytes, 0600); err != nil {
		t.Fatalf("write store: %v", err)
	}

	broker := &fakeRealTokenSetter{}
	r, err := NewRefresher(storePath, "api.anthropic.com", broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}
	r.Register(sid)

	// Step 1: first Token() establishes lastToken = realToken; push succeeds.
	if _, _, tokErr := r.Token(context.Background()); tokErr != nil {
		t.Fatalf("initial Token(): %v", tokErr)
	}
	if broker.callCount() != 1 {
		t.Fatalf("after initial Token(): call count = %d, want 1", broker.callCount())
	}

	// Step 2: broker starts failing (simulates a re-minted scope that is not yet
	// registered, i.e. the post-seed window where ForcePush may fail transiently).
	broker.mu.Lock()
	broker.err = errors.New("broker: scope not found after re-mint")
	broker.mu.Unlock()

	// Step 3: ForcePush fails. lockedToken fast path returns cached token
	// (no rotation detected in vend → no bulk push). Only ForcePush's own
	// SetRealToken is attempted and fails.
	pushErr := r.ForcePush(context.Background(), sid)
	if pushErr == nil {
		t.Fatal("ForcePush: expected error, got nil")
	}
	// F2 fix: the error must be recorded so the ticker can retry.
	if len(r.PushErrors()) == 0 {
		t.Fatal("after failed ForcePush: PushErrors() empty; want error recorded (needed for ticker retry)")
	}

	// Step 4: broker recovers (transient error resolved).
	broker.mu.Lock()
	broker.err = nil
	broker.mu.Unlock()

	// Step 5: next Token() tick — rotated=false, but hadPushErrs=true → retry.
	if _, _, tokErr := r.Token(context.Background()); tokErr != nil {
		t.Fatalf("retry tick Token(): %v", tokErr)
	}
	if len(r.PushErrors()) != 0 {
		t.Errorf("PushErrors() non-empty after retry tick — push not retried: %v", r.PushErrors())
	}

	// Confirm the real token was delivered on the retry.
	broker.mu.Lock()
	lastCall := broker.calls[len(broker.calls)-1]
	broker.mu.Unlock()
	if lastCall.token != realToken {
		t.Errorf("retry push delivered %q, want %q", lastCall.token, realToken)
	}
}
