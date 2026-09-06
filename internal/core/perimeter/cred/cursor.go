package cred

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cursorAuthFile is the on-disk shape of $XDG_CONFIG_HOME/cursor/auth.json.
// Both fields mirror the documented shape: {accessToken, refreshToken}.
type cursorAuthFile struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// StaticCredFilePath returns the absolute path to the credential file described
// by profile, using profile.CredDirEnvVar as the base directory env var and
// profile.CredentialFile as the relative path within that base.
//
// Resolution order:
//  1. The env var named by profile.CredDirEnvVar.
//  2. The XDG default: $HOME/.config.
//
// This is the generic path resolver for file-based agents.  It is profile-driven
// and never branches on agent name or credential format.
func StaticCredFilePath(profile AgentProfile) (string, error) {
	base := os.Getenv(profile.CredDirEnvVar)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cred: StaticCredFilePath: cannot determine home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, profile.CredentialFile), nil
}

// CursorCredPath returns the absolute path to the cursor auth.json credential
// file, resolved from the profile descriptor.  It delegates to [StaticCredFilePath].
//
// Kept for compatibility; prefer [StaticCredFilePath] in new call sites.
func CursorCredPath(profile AgentProfile) (string, error) {
	return StaticCredFilePath(profile)
}

// ImportCursorCredentials reads the credential file described by profile and
// returns a [DedicatedCredStore] populated with the cursor accessToken.
//
// The credential file is resolved via [CursorCredPath].  A missing file returns
// an error wrapping [os.ErrNotExist].  An empty accessToken field returns a
// descriptive error rather than a nil-error sentinel — callers must not silently
// treat an absent credential as "no credential present" (see [LoadStore]).
//
// Cursor uses a static JWT credential (D-MAC-09); no [Refresher] is wired.
// The store's ExpiresAt is set from the JWT's exp claim via [ParseCursorJWTExpiry].
// If the claim cannot be decoded (malformed token), ExpiresAt is the zero
// [time.Time] and the import still succeeds; the caller can test ExpiresAt.IsZero()
// and warn the operator that the expiry is unknown.
func ImportCursorCredentials(profile AgentProfile) (*DedicatedCredStore, error) {
	path, err := CursorCredPath(profile)
	if err != nil {
		return nil, err
	}
	return importCursorCredentialsAt(profile, path)
}

// importCursorCredentialsAt is the path-explicit internal entry point, used by
// tests to inject a synthetic credential directory without mutating the process
// environment.
func importCursorCredentialsAt(profile AgentProfile, path string) (*DedicatedCredStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cred: ImportCursorCredentials %s: %w", path, os.ErrNotExist)
		}
		return nil, fmt.Errorf("cred: ImportCursorCredentials %s: reading file: %w", path, err)
	}

	var f cursorAuthFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("cred: ImportCursorCredentials %s: parsing JSON: %w", path, err)
	}

	if f.AccessToken == "" {
		return nil, fmt.Errorf("cred: ImportCursorCredentials %s: %s is empty; cannot import unusable credential",
			path, profile.CredentialFileKey)
	}

	// Decode the session JWT expiry locally — best-effort; zero on failure.
	exp, _ := ParseCursorJWTExpiry(f.AccessToken)

	return &DedicatedCredStore{
		AccessToken:  f.AccessToken,
		RefreshToken: f.RefreshToken,
		ExpiresAt:    exp,
		TokenType:    "Bearer",
		// No ClientID/ClientSecret/TokenEndpoint: cursor credentials are static
		// JWTs (D-MAC-09). Refresh is manual re-login; no host-side refresher.
	}, nil
}

// NewCursorCredentialSource imports the cursor credential described by profile
// and wraps it in a [StaticCredentialSource].
//
// Cursor credentials are static JWTs (D-MAC-09); no Refresher is constructed.
// The returned source yields the token unchanged until the operator re-runs
// the cursor-agent login flow.
func NewCursorCredentialSource(profile AgentProfile) (*StaticCredentialSource, error) {
	store, err := ImportCursorCredentials(profile)
	if err != nil {
		return nil, err
	}
	return NewStaticCredentialSource(store), nil
}

// ParseCursorJWTExpiry decodes the exp claim from the JWT payload without
// validating the signature or any other claims.
//
// The token is split on '.' and the middle (payload) segment is
// base64url-decoded (no padding); the exp field (Unix seconds integer) is
// returned as a [time.Time].
//
// An error is returned for any malformed input: fewer than three segments,
// an undecodable payload, non-JSON payload, or an absent or non-numeric exp
// claim.  The function never panics.  Call sites that use the expiry for
// informational display should treat an error as "expiry unknown" and tell
// the operator to re-login rather than failing.
func ParseCursorJWTExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 3 {
		return time.Time{}, fmt.Errorf("cred: ParseCursorJWTExpiry: token has %d segment(s); want 3", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("cred: ParseCursorJWTExpiry: base64url-decode payload: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("cred: ParseCursorJWTExpiry: JSON-decode payload: %w", err)
	}

	raw, ok := claims["exp"]
	if !ok {
		return time.Time{}, fmt.Errorf("cred: ParseCursorJWTExpiry: payload has no exp claim")
	}

	// JSON numbers unmarshal to float64 in a map[string]any.
	expFloat, ok := raw.(float64)
	if !ok {
		return time.Time{}, fmt.Errorf("cred: ParseCursorJWTExpiry: exp claim is not a number (got %T)", raw)
	}

	return time.Unix(int64(expFloat), 0), nil
}
