package cred_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// makeTestCreds writes a .credentials.json to dir and returns the path.
func makeTestCreds(t *testing.T, dir string, expiresAt time.Time, accessToken, refreshToken string) string {
	t.Helper()
	type oauthCreds struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	}
	type credsFile struct {
		ClaudeAiOauth oauthCreds `json:"claudeAiOauth"`
	}
	f := credsFile{ClaudeAiOauth: oauthCreds{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt.UnixMilli(),
	}}
	data, _ := json.Marshal(f)
	path := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	return path
}

// fakeTokenServer returns an httptest.Server that responds to refresh requests.
// Each call atomically increments callCount. It returns newToken and
// expiresIn seconds.
func fakeTokenServer(t *testing.T, callCount *atomic.Int64, newToken string, expiresIn int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"new-refresh","expires_in":%d}`,
			newToken, expiresIn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCredGuardian_NoRefreshWhenFresh(t *testing.T) {
	dir := t.TempDir()
	var callCount atomic.Int64
	srv := fakeTokenServer(t, &callCount, "new-token", 3600)
	path := makeTestCreds(t, dir, time.Now().Add(2*time.Hour), "valid-token", "refresh-tok")
	g := cred.NewCredGuardianWithEndpoint(path, srv.URL)
	if err := g.GuardOnce(context.Background()); err != nil {
		t.Fatalf("GuardOnce: %v", err)
	}
	if callCount.Load() != 0 {
		t.Errorf("expected 0 HTTP calls for a fresh token, got %d", callCount.Load())
	}
}

func TestCredGuardian_RefreshesWhenExpiring(t *testing.T) {
	dir := t.TempDir()
	var callCount atomic.Int64
	srv := fakeTokenServer(t, &callCount, "new-access-token", 3600)
	// Token expiring in 10 minutes (within the 30-min threshold).
	path := makeTestCreds(t, dir, time.Now().Add(10*time.Minute), "old-token", "refresh-tok")
	g := cred.NewCredGuardianWithEndpoint(path, srv.URL)
	if err := g.GuardOnce(context.Background()); err != nil {
		t.Fatalf("GuardOnce: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("expected 1 HTTP call, got %d", callCount.Load())
	}
	// Verify the file was updated.
	data, _ := os.ReadFile(path)
	if string(data) == "" {
		t.Fatal("creds file empty after refresh")
	}
	var updated struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatalf("parse updated creds: %v", err)
	}
	if updated.ClaudeAiOauth.AccessToken != "new-access-token" {
		t.Errorf("access token not updated: got %q", updated.ClaudeAiOauth.AccessToken)
	}
}

// TestCredGuardian_TwoRacingGuardians is the mutation guard for the flock re-read:
//
//	Remove the re-read after flock acquisition → this test fails because both
//	guardians make an HTTP refresh call instead of exactly one.
//
// Two guardians both see an expiring token, both acquire the flock sequentially,
// but only ONE should make an HTTP call (the second re-reads after acquiring the
// flock and sees the token is now fresh).
func TestCredGuardian_TwoRacingGuardians(t *testing.T) {
	dir := t.TempDir()
	var callCount atomic.Int64

	// The fake server returns a FRESH expiresAt so the second guardian's
	// re-read after flock sees fresh credentials and skips the refresh.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		expiresIn := int(2 * time.Hour / time.Second)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"raced-token","refresh_token":"new-refresh","expires_in":%d}`, expiresIn)
	}))
	t.Cleanup(srv.Close)

	// Token expiring in 5 minutes so both guardians initially see it as needing refresh.
	path := makeTestCreds(t, dir, time.Now().Add(5*time.Minute), "stale-token", "refresh-tok")

	g1 := cred.NewCredGuardianWithEndpoint(path, srv.URL)
	g2 := cred.NewCredGuardianWithEndpoint(path, srv.URL)

	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() { defer wg.Done(); err1 = g1.GuardOnce(context.Background()) }()
	go func() { defer wg.Done(); err2 = g2.GuardOnce(context.Background()) }()
	wg.Wait()

	if err1 != nil {
		t.Errorf("guardian 1 error: %v", err1)
	}
	if err2 != nil {
		t.Errorf("guardian 2 error: %v", err2)
	}

	if n := callCount.Load(); n != 1 {
		t.Errorf("expected exactly 1 HTTP refresh call, got %d (flock re-read missing?)", n)
	}
}
