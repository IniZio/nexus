package cred

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Claude Code OAuth constants extracted from the @anthropic-ai/claude-code CLI.
//
// These are an OPERATIONAL DEPENDENCY: nexus3's login flow and token refresh
// rely on them matching what the CLI uses. If Anthropic rotates the client_id
// or token endpoint, nexus3 login will break and these constants must be
// updated accordingly.
const (
	// ClaudeCodeClientID is the public PKCE OAuth client registered by the
	// Claude Code CLI. There is no client_secret; it is a public client.
	ClaudeCodeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// ClaudeCodeTokenEndpoint is the OAuth 2.0 token endpoint used by Claude
	// Code. Note: this is platform.claude.com, NOT api.anthropic.com.
	ClaudeCodeTokenEndpoint = "https://platform.claude.com/v1/oauth/token"

	// ClaudeCodeAuthorizeURL is the OAuth 2.0 authorization endpoint.
	ClaudeCodeAuthorizeURL = "https://platform.claude.com/oauth/authorize"

	// ClaudeCodeScopes is the space-separated list of OAuth scopes requested
	// by the Claude Code CLI, including offline_access for refresh token
	// grants.
	ClaudeCodeScopes = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload offline_access"
)

// claudeCodeDefaultFromPath returns the default --from path for the CLI
// `auth login` import route for claude-code: the dedicated session's
// .credentials.json written by `claude auth login` when
// CLAUDE_CONFIG_DIR=~/.config/nexus3/claude-dedicated is set.
//
// This matches the historical hard-coded path so that `nexus3 auth login`
// with no flags is byte-identical in behaviour to the pre-profile
// implementation.  The profile parameter is accepted to satisfy
// [AgentRegistration.DefaultFromPathFn] but is not currently used (the path
// is the same for all claude-code profiles).
func claudeCodeDefaultFromPath(_ AgentProfile) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "nexus3", "claude-dedicated", ".credentials.json")
}

// claudeCredentialsFile is the on-disk shape of Claude Code's
// ~/.config/claude/.credentials.json (or equivalent path).
type claudeCredentialsFile struct {
	ClaudeAiOauth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		// ExpiresAt is epoch MILLISECONDS (not seconds).
		ExpiresAt int64 `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

// ImportClaudeCredentials reads a Claude Code .credentials.json file at path
// and returns a [DedicatedCredStore] populated from it.
//
// The file must exist and contain valid JSON with a non-empty accessToken and
// refreshToken in the claudeAiOauth object. Both tokens are required: the
// access token is needed immediately and the refresh token is needed by the
// host-side refresher ([NewRefresher]).
//
// A missing file returns an error wrapping [os.ErrNotExist] so callers can
// use errors.Is(err, os.ErrNotExist) to detect it.
func ImportClaudeCredentials(path string) (*DedicatedCredStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cred: ImportClaudeCredentials %s: %w", path, os.ErrNotExist)
		}
		return nil, fmt.Errorf("cred: ImportClaudeCredentials %s: reading file: %w", path, err)
	}

	var f claudeCredentialsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("cred: ImportClaudeCredentials %s: parsing JSON: %w", path, err)
	}

	if f.ClaudeAiOauth.AccessToken == "" {
		return nil, fmt.Errorf("cred: ImportClaudeCredentials %s: claudeAiOauth.accessToken is empty; cannot import unusable credential", path)
	}
	if f.ClaudeAiOauth.RefreshToken == "" {
		return nil, fmt.Errorf("cred: ImportClaudeCredentials %s: claudeAiOauth.refreshToken is empty; refresh token required for host-side refresher", path)
	}

	return &DedicatedCredStore{
		AccessToken:   f.ClaudeAiOauth.AccessToken,
		RefreshToken:  f.ClaudeAiOauth.RefreshToken,
		ExpiresAt:     time.UnixMilli(f.ClaudeAiOauth.ExpiresAt),
		TokenType:     "Bearer",
		ClientID:      ClaudeCodeClientID,
		ClientSecret:  "",
		TokenEndpoint: ClaudeCodeTokenEndpoint,
	}, nil
}
