package cred

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// guardianRefreshAhead is how far before expiry the guardian proactively
// refreshes the credential. 30 minutes matches claude-code's own threshold.
const guardianRefreshAhead = 30 * time.Minute

// guardianCheckInterval is how often the guardian polls the credentials file.
const guardianCheckInterval = time.Minute

// CredGuardian watches the operator's ~/.claude/.credentials.json and
// proactively refreshes the claudeAiOauth token before it expires.
//
// Multiple supervisors running concurrently each arm their own guardian.
// Concurrent refreshes are serialised by an advisory flock(2) on a sidecar
// lock file (<credsPath>.nexus3.lock), with a re-read after acquiring the
// lock so the loser of the race skips the refresh if the winner already did it.
//
// The guardian never writes expiresAt:0. On refresh failure it logs and leaves
// the file untouched. It never caps the token response body (memory:
// an unparsed 2xx costs the credential).
type CredGuardian struct {
	credsPath     string
	lockPath      string
	tokenEndpoint string
	client        *http.Client
}

// NewCredGuardian returns a CredGuardian for the credentials file at credsPath.
// The sidecar lock file is <credsPath>.nexus3.lock.
func NewCredGuardian(credsPath string) *CredGuardian {
	return &CredGuardian{
		credsPath:     credsPath,
		lockPath:      credsPath + ".nexus3.lock",
		tokenEndpoint: ClaudeCodeTokenEndpoint,
		client:        &http.Client{Timeout: 30 * time.Second},
	}
}

// NewCredGuardianWithEndpoint is like NewCredGuardian but overrides the OAuth
// token endpoint URL. Intended for testing only.
func NewCredGuardianWithEndpoint(credsPath, tokenEndpoint string) *CredGuardian {
	g := NewCredGuardian(credsPath)
	g.tokenEndpoint = tokenEndpoint
	return g
}

// Guard runs the refresh loop until ctx is done. It is designed to be called
// as a goroutine. It checks the credentials file once per guardianCheckInterval
// and refreshes when expiresAt is within guardianRefreshAhead of now.
func (g *CredGuardian) Guard(ctx context.Context) {
	ticker := time.NewTicker(guardianCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.GuardOnce(ctx); err != nil {
				slog.Warn("cred.guardian.refresh_failed", "path", g.credsPath, "err", err)
			}
		}
	}
}

// GuardOnce checks whether the credential at g.credsPath needs refreshing
// and, if so, acquires the flock, re-checks, and refreshes. Returns nil
// when no refresh was needed or when a refresh succeeded. Returns a non-nil
// error only when a refresh was attempted and failed.
func (g *CredGuardian) GuardOnce(ctx context.Context) error {
	creds, err := g.readCreds()
	if err != nil {
		// Missing file or unreadable: not an error; sandbox may not have run
		// `claude login` yet.
		return nil
	}
	if !g.needsRefresh(creds, time.Now()) {
		return nil
	}

	// Token is expiring — acquire the advisory flock.
	lockF, lockErr := os.OpenFile(g.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if lockErr != nil {
		return fmt.Errorf("guardian: open lock %s: %w", g.lockPath, lockErr)
	}
	defer lockF.Close()

	if err := syscall.Flock(int(lockF.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("guardian: flock %s: %w", g.lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(lockF.Fd()), syscall.LOCK_UN) }()

	// Re-read after acquiring — another guardian may have refreshed.
	creds, err = g.readCreds()
	if err != nil {
		return nil // file disappeared between check and lock — skip
	}
	if !g.needsRefresh(creds, time.Now()) {
		slog.Debug("cred.guardian.skipped_after_lock", "reason", "another guardian already refreshed")
		return nil
	}

	// Refresh the token.
	newCreds, refreshErr := g.refresh(ctx, creds)
	if refreshErr != nil {
		return fmt.Errorf("guardian: refresh: %w", refreshErr)
	}

	// Never write expiresAt:0.
	if newCreds.ClaudeAiOauth.ExpiresAt == 0 {
		return fmt.Errorf("guardian: refresh returned expiresAt=0; refusing to write")
	}

	// Write atomically via temp+rename, preserving mode 0600.
	data, err := json.Marshal(newCreds)
	if err != nil {
		return fmt.Errorf("guardian: marshal: %w", err)
	}
	tmp := g.credsPath + ".nexus3guardian.tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("guardian: write tmp: %w", err)
	}
	if err := os.Rename(tmp, g.credsPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("guardian: rename: %w", err)
	}

	newExpiry := time.UnixMilli(newCreds.ClaudeAiOauth.ExpiresAt)
	slog.Info("cred.guardian.refreshed", "path", g.credsPath, "expires_at", newExpiry)
	return nil
}

// claudeCredentials is the on-disk shape of ~/.claude/.credentials.json.
type claudeCredentials struct {
	ClaudeAiOauth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"` // epoch milliseconds
	} `json:"claudeAiOauth"`
}

func (g *CredGuardian) readCreds() (claudeCredentials, error) {
	data, err := os.ReadFile(g.credsPath)
	if err != nil {
		return claudeCredentials{}, err
	}
	var c claudeCredentials
	if err := json.Unmarshal(data, &c); err != nil {
		return claudeCredentials{}, fmt.Errorf("guardian: parse %s: %w", g.credsPath, err)
	}
	return c, nil
}

func (g *CredGuardian) needsRefresh(creds claudeCredentials, now time.Time) bool {
	if creds.ClaudeAiOauth.RefreshToken == "" {
		return false
	}
	if creds.ClaudeAiOauth.ExpiresAt == 0 {
		return true // already expired/invalid
	}
	expiry := time.UnixMilli(creds.ClaudeAiOauth.ExpiresAt)
	return expiry.Sub(now) < guardianRefreshAhead
}

// guardianTokenResponse is the subset of the OAuth token response we need.
type guardianTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
}

func (g *CredGuardian) refresh(ctx context.Context, creds claudeCredentials) (claudeCredentials, error) {
	body := strings.NewReader(
		"grant_type=refresh_token" +
			"&client_id=" + ClaudeCodeClientID +
			"&refresh_token=" + creds.ClaudeAiOauth.RefreshToken,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenEndpoint, body)
	if err != nil {
		return claudeCredentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Never cap the response body — an unparsed 2xx costs the credential
	// (memory: an-unparsed-2xx-costs-the-credential).
	resp, err := g.client.Do(req)
	if err != nil {
		return claudeCredentials{}, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	var tokResp guardianTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return claudeCredentials{}, fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return claudeCredentials{}, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	if tokResp.AccessToken == "" {
		return claudeCredentials{}, fmt.Errorf("token endpoint returned empty access_token")
	}

	var updated claudeCredentials
	updated.ClaudeAiOauth.AccessToken = tokResp.AccessToken
	// Keep the old refresh token if the endpoint didn't rotate it.
	if tokResp.RefreshToken != "" {
		updated.ClaudeAiOauth.RefreshToken = tokResp.RefreshToken
	} else {
		updated.ClaudeAiOauth.RefreshToken = creds.ClaudeAiOauth.RefreshToken
	}
	if tokResp.ExpiresIn > 0 {
		updated.ClaudeAiOauth.ExpiresAt = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second).UnixMilli()
	} else {
		// Fallback: assume 1-hour lifetime.
		updated.ClaudeAiOauth.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	}
	return updated, nil
}
