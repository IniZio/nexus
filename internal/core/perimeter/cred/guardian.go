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

const guardianRefreshAhead = 40 * time.Minute

/**
40 minutes is strictly ahead of claude-code's own 30-minute self-refresh so the guardian
wins the race by construction (claude-code refreshing at the same moment would invalidate
the guardian's refresh token with zero overlap — see memory: claude-oauth-refresh-revokes-prior-token).
*/

const guardianCheckInterval = time.Minute

type CredGuardian struct {
	credsPath     string
	lockPath      string
	tokenEndpoint string
	client        *http.Client
}

/**
Concurrent refreshes are serialised by an advisory flock(2) on a sidecar lock file
(<credsPath>.nexus3.lock), with a re-read after acquiring the lock so the loser of
the race skips the refresh if the winner already did it. Never writes expiresAt:0;
never caps the token response body (memory: an unparsed 2xx costs the credential).
*/

func NewCredGuardian(credsPath string) *CredGuardian {
	return &CredGuardian{
		credsPath:     credsPath,
		lockPath:      credsPath + ".nexus3.lock",
		tokenEndpoint: ClaudeCodeTokenEndpoint,
		client:        &http.Client{Timeout: 30 * time.Second},
	}
}

func NewCredGuardianWithEndpoint(credsPath, tokenEndpoint string) *CredGuardian {
	g := NewCredGuardian(credsPath)
	g.tokenEndpoint = tokenEndpoint
	return g
}

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

func (g *CredGuardian) GuardOnce(ctx context.Context) error {
	creds, err := g.readCreds()
	if err != nil {
		return nil
	}
	if !g.needsRefresh(creds, time.Now()) {
		return nil
	}

	lockF, lockErr := os.OpenFile(g.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if lockErr != nil {
		return fmt.Errorf("guardian: open lock %s: %w", g.lockPath, lockErr)
	}
	defer lockF.Close()

	if err := syscall.Flock(int(lockF.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("guardian: flock %s: %w", g.lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(lockF.Fd()), syscall.LOCK_UN) }()

	creds, err = g.readCreds()
	if err != nil {
		return nil // file disappeared between check and lock — skip
	}
	if !g.needsRefresh(creds, time.Now()) {
		slog.Debug("cred.guardian.skipped_after_lock", "reason", "another guardian already refreshed")
		return nil
	}

	patch, refreshErr := g.refresh(ctx, creds)
	if refreshErr != nil {
		return fmt.Errorf("guardian: refresh: %w", refreshErr)
	}

	if patch.ExpiresAt == 0 {
		return fmt.Errorf("guardian: refresh returned expiresAt=0; refusing to write")
	}

	rawDoc, err := os.ReadFile(g.credsPath)
	if err != nil {
		return fmt.Errorf("guardian: re-read for patch: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rawDoc, &doc); err != nil {
		return fmt.Errorf("guardian: parse doc for patch: %w", err)
	}
	var oauthFields map[string]json.RawMessage
	if err := json.Unmarshal(doc["claudeAiOauth"], &oauthFields); err != nil {
		return fmt.Errorf("guardian: parse claudeAiOauth for patch: %w", err)
	}
	rawAccessToken, _ := json.Marshal(patch.AccessToken)
	oauthFields["accessToken"] = json.RawMessage(rawAccessToken)
	if patch.RefreshToken != "" {
		rawRefreshToken, _ := json.Marshal(patch.RefreshToken)
		oauthFields["refreshToken"] = json.RawMessage(rawRefreshToken)
	}
	rawExpiresAt, _ := json.Marshal(patch.ExpiresAt)
	oauthFields["expiresAt"] = json.RawMessage(rawExpiresAt)
	if patch.RefreshTokenExpiresAt != 0 {
		rawRTEA, _ := json.Marshal(patch.RefreshTokenExpiresAt)
		oauthFields["refreshTokenExpiresAt"] = json.RawMessage(rawRTEA)
	}
	patchedOauth, err := json.Marshal(oauthFields)
	if err != nil {
		return fmt.Errorf("guardian: marshal patched oauth: %w", err)
	}
	doc["claudeAiOauth"] = json.RawMessage(patchedOauth)
	data, err := json.Marshal(doc)
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

	newExpiry := time.UnixMilli(patch.ExpiresAt)
	slog.Info("cred.guardian.refreshed", "path", g.credsPath, "expires_at", newExpiry)
	return nil
}

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

type guardianTokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int64  `json:"expires_in"`               // seconds
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"` // seconds; 0 if absent
}

// credPatch carries fields from a successful token refresh to patch on-disk.
type credPatch struct {
	AccessToken           string
	RefreshToken          string // empty → keep existing
	ExpiresAt             int64  // epoch ms; must be non-zero
	RefreshTokenExpiresAt int64  // epoch ms; 0 → leave untouched
}

/*
*
Never cap the token response body — an unparsed 2xx costs the credential
(memory: an-unparsed-2xx-costs-the-credential).
*/
func (g *CredGuardian) refresh(ctx context.Context, creds claudeCredentials) (credPatch, error) {
	body := strings.NewReader(
		"grant_type=refresh_token" +
			"&client_id=" + ClaudeCodeClientID +
			"&refresh_token=" + creds.ClaudeAiOauth.RefreshToken,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenEndpoint, body)
	if err != nil {
		return credPatch{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.client.Do(req)
	if err != nil {
		return credPatch{}, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	var tokResp guardianTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return credPatch{}, fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return credPatch{}, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	if tokResp.AccessToken == "" {
		return credPatch{}, fmt.Errorf("token endpoint returned empty access_token")
	}

	var p credPatch
	p.AccessToken = tokResp.AccessToken
	// Keep the old refresh token if the endpoint didn't rotate it.
	if tokResp.RefreshToken != "" {
		p.RefreshToken = tokResp.RefreshToken
	}
	if tokResp.ExpiresIn > 0 {
		p.ExpiresAt = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second).UnixMilli()
	} else {
		p.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	}
	if tokResp.RefreshTokenExpiresIn > 0 {
		p.RefreshTokenExpiresAt = time.Now().Add(time.Duration(tokResp.RefreshTokenExpiresIn) * time.Second).UnixMilli()
	}
	return p, nil
}
