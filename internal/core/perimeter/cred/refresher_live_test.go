//go:build integration

package cred

// TestRefresherLiveRefreshGrant — TBD-P5-3 live-proof acceptance test.
//
// This test exercises the PRODUCTION refresher code path (NewRefresher →
// Token) against the real Anthropic OAuth token endpoint to confirm:
//
//  1. A live refresh grant succeeds (HTTP 200 from the real endpoint).
//  2. The returned access token is non-empty and has a future expiry.
//  3. The refresher's persist-on-rotation code (commit 9cf4977) writes the
//     rotated refresh_token to the store file on disk.
//
// IMPORTANT: This test CONSUMES one refresh token per run. Each call rotates
// the refresh_token chain. It is tagged `integration` and skip-guarded so
// `go test ./...` never executes it accidentally and burns the credential chain.
//
// After a successful run the rotated credentials are copied back over the real
// creds.json so the live chain stays usable.
//
// HAZARD: the copy-back is not crash-safe. If this test is interrupted after the
// grant succeeds but before the rotated token is copied back to creds.json, the
// live refresh_token is consumed but persisted only in a t.TempDir() that Go then
// deletes — the credential chain becomes unrecoverable (creds.json holds a
// consumed token; the dedicated session's .credentials.json is already stale).
// Recovery: re-bootstrap with a fresh
// `CLAUDE_CONFIG_DIR=~/.config/nexus3/claude-dedicated claude login` followed by
// `nexus3 auth login --force`.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// liveCredStorePath replicates the logic of service.DefaultDedicatedCredStorePath
// without importing the service package (which imports cred, causing a cycle).
func liveCredStorePath() string {
	if p := os.Getenv("NEXUS3_DEDICATED_CRED_STORE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "nexus3", "creds.json")
}

func TestRefresherLiveRefreshGrant(t *testing.T) {
	// ── Skip guard ────────────────────────────────────────────────────────────
	realPath := liveCredStorePath()
	if realPath == "" {
		t.Skip("cannot determine cred store path (no HOME); skipping live test")
	}

	realStore, err := LoadStore(realPath)
	if err != nil {
		if errors.Is(err, ErrStoreAbsent) {
			t.Skipf("no cred store at %s; run nexus3 auth login (TBD-P5-3 live proof)", realPath)
		}
		t.Fatalf("LoadStore(%s): %v", realPath, err)
	}

	preRefreshRT := realStore.RefreshToken
	preRefreshAT := realStore.AccessToken

	t.Logf("live test starting; RT prefix=%q AT prefix=%q",
		masked(preRefreshRT), masked(preRefreshAT))

	// ── Build the expired-seed temp store ─────────────────────────────────────
	// Copy all fields from the real store but set ExpiresAt in the past.
	// This causes lockedToken to call through to oauthRefreshBase
	// (which hits the real token endpoint) instead of returning the cached token.
	// The refresh_token is the SAME live token — using it will rotate it.
	//
	// We point NewRefresher at the TEMP path so persistence writes go there, not
	// directly to the real file. On success we copy the rotated temp store back
	// to realPath to keep the live chain current.
	tempDir := t.TempDir()
	tempPath := filepath.Join(tempDir, "creds-live.json")

	expiredStore := &DedicatedCredStore{
		AccessToken:   realStore.AccessToken,   // non-empty (required by SaveStore)
		RefreshToken:  realStore.RefreshToken,  // live RT — will be consumed by grant
		ExpiresAt:     time.Now().Add(-1 * time.Hour), // past → forces endpoint call
		TokenType:     realStore.TokenType,
		ClientID:      realStore.ClientID,
		ClientSecret:  realStore.ClientSecret,
		TokenEndpoint: realStore.TokenEndpoint,
	}
	if err := SaveStore(tempPath, expiredStore); err != nil {
		t.Fatalf("SaveStore temp: %v", err)
	}

	// ── Construct the refresher ───────────────────────────────────────────────
	broker := &fakeRealTokenSetter{}
	r, err := NewRefresher(tempPath, "api.anthropic.com", broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	// ── Execute the live refresh grant ───────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tok, expiry, err := r.Token(ctx)
	if err != nil {
		t.Fatalf("Token(): live refresh grant failed: %v", err)
	}

	// ── Token assertions ──────────────────────────────────────────────────────
	if tok == "" {
		t.Error("Token(): returned empty access token")
	}
	if !expiry.After(time.Now()) {
		t.Errorf("Token(): expiry %v is not in the future", expiry)
	}
	t.Logf("live refresh grant succeeded; new AT prefix=%q expiry=%v", masked(tok), expiry)

	// ── Persist assertions ────────────────────────────────────────────────────
	if pErr := r.PersistError(); pErr != nil {
		t.Errorf("PersistError() must be nil after successful grant, got: %v", pErr)
	}

	afterStore, err := LoadStore(tempPath)
	if err != nil {
		t.Fatalf("LoadStore(tempPath) after Token(): %v", err)
	}

	// Anthropic rotates the refresh_token on every grant. Assert it changed.
	// If the endpoint does not rotate (unexpected), fall back to asserting the
	// access_token or expiry advanced so the test still provides useful signal.
	if afterStore.RefreshToken != preRefreshRT {
		t.Logf("refresh_token rotated (expected): old prefix=%q new prefix=%q",
			masked(preRefreshRT), masked(afterStore.RefreshToken))
	} else {
		// RT did not rotate — assert the AT at least advanced.
		if afterStore.AccessToken == preRefreshAT {
			t.Error("neither refresh_token nor access_token changed after live grant; endpoint may have returned a cached response")
		} else {
			t.Logf("refresh_token unchanged (endpoint did not rotate); access_token did change: new prefix=%q", masked(afterStore.AccessToken))
		}
	}

	// Assert the persisted AT is non-empty and expiry is future.
	if afterStore.AccessToken == "" {
		t.Error("persisted access_token is empty")
	}
	if !afterStore.ExpiresAt.After(time.Now()) {
		t.Errorf("persisted ExpiresAt %v is not in the future", afterStore.ExpiresAt)
	}

	// ── Copy rotated creds back to the real path ──────────────────────────────
	// The refresh grant consumed the original RT; the rotated one is now in
	// tempPath. Copy it back to creds.json so the live chain stays usable.
	if err := SaveStore(realPath, afterStore); err != nil {
		t.Errorf("copy-back to real creds.json FAILED — live chain may be broken: %v", err)
	} else {
		t.Logf("copy-back to %s succeeded; live chain preserved", realPath)
	}

	// ── Verify creds.json is still loadable ───────────────────────────────────
	verify, err := LoadStore(realPath)
	if err != nil {
		t.Errorf("LoadStore(realPath) after copy-back: %v — creds.json may be broken", err)
	} else {
		t.Logf("creds.json still valid after run; RT prefix=%q", masked(verify.RefreshToken))
	}
}

// masked returns the first 8 chars of s followed by "…" to avoid logging
// full credential values while still providing enough entropy for debugging.
func masked(s string) string {
	if len(s) <= 8 {
		return fmt.Sprintf("%s…", s)
	}
	return fmt.Sprintf("%s…", s[:8])
}
