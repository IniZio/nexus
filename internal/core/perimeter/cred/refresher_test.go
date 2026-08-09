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

	r := newRefresherWithSource(fts, host, broker)
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

	r := newRefresherWithSource(fts, host, broker)
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
	r := newRefresherWithSource(fts, host, broker)
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
	r := newRefresherWithSource(fts, "api.anthropic.com", broker)
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

	r := newRefresherWithSource(fts, "api.anthropic.com", broker)
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

	r := newRefresherWithSource(fts, "api.anthropic.com", broker)
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
