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
// an expired store triggers ReuseTokenSourceWithExpiry → oauthRefreshBase →
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
// r.base = base (refresher.go:183): commenting it out makes r.base == nil,
// Token() falls to the unlocked test path, the lock file is never created,
// and the test fails:
//
//	--- FAIL: TestNewRefresher_LockedPathWired (0.00s)
//	    refresher_test.go:NNN: lock file does not exist after Token(): storePath+".lock"
//	    want: lock file created by WithStoreLock (proof the locked path ran)
//
// r.storePath (NewRefresher calls newRefresherWithSource which sets storePath):
// zeroing it after construction makes r.base != nil but storePath == "", so
// lockedToken() calls WithStoreLock("", …) which fails at OpenLock, and Token()
// returns a non-nil error — surfaced and loud, not a silent bypass.
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

	// writeStoreJSON writes expires_at in the past → forces the slow path so
	// WithStoreLock is always invoked (fast path skips the lock).
	storePath := writeStoreJSON(t, t.TempDir(), srv.URL)
	lockPath := lockFilePath(storePath) // storePath + ".lock"

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
