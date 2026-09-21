package cred_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// TestRaceAuditA4_LostWrite: guardian writes rt-1 under flock; guest renames
// rt-0 over it without the host flock. On-disk token is the consumed rt-0.
func TestRaceAuditA4_LostWrite(t *testing.T) {
	dir := t.TempDir()
	credsPath := makeTestCreds(t, dir, time.Now().Add(10*time.Minute), "at-0", "rt-0")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)

	g := cred.NewCredGuardianWithEndpoint(credsPath, srv.URL)
	if err := g.GuardOnce(context.Background()); err != nil {
		t.Fatalf("guardian: %v", err)
	}

	rtAfterGuardian := readCredsRT(t, credsPath)
	if rtAfterGuardian == "rt-0" {
		t.Fatal("guardian did not update the refresh token; test precondition broken")
	}

	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	guestData, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "at-guest",
			"refreshToken": "rt-0",
			"expiresAt":    expiresAt,
		},
	})
	tmp := credsPath + ".guest.tmp"
	if err := os.WriteFile(tmp, guestData, 0o600); err != nil {
		t.Fatalf("guest tmp: %v", err)
	}
	if err := os.Rename(tmp, credsPath); err != nil {
		t.Fatalf("guest rename: %v", err)
	}

	onDisk := readCredsRT(t, credsPath)
	if onDisk != "rt-0" {
		t.Fatalf("expected rt-0 (guest stale overwrite) on disk, got %q", onDisk)
	}
	t.Logf("LOST-WRITE EXHIBITED: guardian wrote %q; guest renamed rt-0 over it (consumed under D-9 premise)", rtAfterGuardian)
}

// TestRaceAuditA4_DoubleHTTP: host guardian and guest writer both call the
// fake server with rt-0 concurrently; server rejects call 2 with 401 (D-9).
func TestRaceAuditA4_DoubleHTTP(t *testing.T) {
	dir := t.TempDir()

	var (
		callCount   atomic.Int64
		mu          sync.Mutex
		seenRT      []string
		second      = make(chan struct{})
		unblock     = make(chan struct{})
		unblockOnce sync.Once
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rt := r.FormValue("refresh_token")
		mu.Lock()
		seenRT = append(seenRT, rt)
		mu.Unlock()

		if n == 2 {
			unblockOnce.Do(func() { close(second) })
		}
		select {
		case <-unblock:
		case <-time.After(5 * time.Second):
		}

		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600}`)
		} else {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)

	go func() {
		select {
		case <-second:
		case <-time.After(5 * time.Second):
		}
		close(unblock)
	}()

	credsPath := makeTestCreds(t, dir, time.Now().Add(10*time.Minute), "at-0", "rt-0")
	g := cred.NewCredGuardianWithEndpoint(credsPath, srv.URL)

	guestRefresh := func() error {
		data, err := os.ReadFile(credsPath)
		if err != nil {
			return err
		}
		var c struct {
			ClaudeAiOauth struct {
				RefreshToken string `json:"refreshToken"`
			} `json:"claudeAiOauth"`
		}
		if err := json.Unmarshal(data, &c); err != nil {
			return err
		}
		body := strings.NewReader(
			"grant_type=refresh_token&client_id=test&refresh_token=" + c.ClaudeAiOauth.RefreshToken,
		)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("guest: server %d (D-9: second caller with rt-0 receives invalid_grant)", resp.StatusCode)
		}
		return nil
	}

	var hostErr, guestErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); hostErr = g.GuardOnce(context.Background()) }()
	go func() { defer wg.Done(); guestErr = guestRefresh() }()
	wg.Wait()

	total := callCount.Load()
	if total != 2 {
		t.Errorf("want 2 HTTP calls (host+guest), got %d; concurrent window may not have fired", total)
	}
	_ = seenRT
	t.Logf("DOUBLE-HTTP EXHIBITED: %d calls with rt-0; host err=%v guest err=%v", total, hostErr, guestErr)
}

func readCredsRT(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readCredsRT: %v", err)
	}
	var c struct {
		ClaudeAiOauth struct {
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("readCredsRT parse: %v", err)
	}
	return c.ClaudeAiOauth.RefreshToken
}
