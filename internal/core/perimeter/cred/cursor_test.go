package cred

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeCursorDir writes a cursor/auth.json fixture under a fresh temp dir and
// returns the temp dir root (the value to set as XDG_CONFIG_HOME) and the full
// path to auth.json.
func makeCursorDir(t *testing.T, content string) (xdgBase string, authPath string) {
	t.Helper()
	dir := t.TempDir()
	credDir := filepath.Join(dir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("makeCursorDir mkdir: %v", err)
	}
	p := filepath.Join(credDir, "auth.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("makeCursorDir write: %v", err)
	}
	return dir, p
}

// makeSyntheticJWT builds a minimal JWT string with the given payload claims.
// The header and signature are dummy values; only the payload is inspected by
// ParseCursorJWTExpiry.  Never logs or returns the token value.
func makeSyntheticJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("makeSyntheticJWT marshal: %v", err)
	}
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString(payload)
	return hdr + "." + body + ".fakesig"
}

// cursorTestProfile returns a copy of CursorAgentProfile for use in tests.
func cursorTestProfile() AgentProfile { return CursorAgentProfile }

// ── empty-accessToken rejection ───────────────────────────────────────────────

// TestImportCursorCredentials_EmptyToken verifies that an empty accessToken
// field returns a descriptive error rather than a nil error with an unusable
// store.  (Mutation-proofed: see TestMutation_EmptyTokenRejection.)
func TestImportCursorCredentials_EmptyToken(t *testing.T) {
	xdgBase, _ := makeCursorDir(t, `{"accessToken":"","refreshToken":"r"}`)
	t.Setenv("XDG_CONFIG_HOME", xdgBase)

	store, err := ImportCursorCredentials(cursorTestProfile())
	if err == nil {
		t.Fatalf("expected error for empty accessToken; got nil (store=%+v)", store)
	}
	if store != nil {
		t.Errorf("expected nil store on error; got non-nil")
	}
	if !containsStr(err.Error(), "empty") {
		t.Errorf("error %q does not mention 'empty'", err)
	}
	if !containsStr(err.Error(), "unusable") {
		t.Errorf("error %q does not mention 'unusable'", err)
	}
}

// ── missing file ──────────────────────────────────────────────────────────────

// TestImportCursorCredentials_MissingFile verifies that a missing auth.json
// returns an error wrapping os.ErrNotExist.
func TestImportCursorCredentials_MissingFile(t *testing.T) {
	dir := t.TempDir() // no cursor/auth.json inside
	t.Setenv("XDG_CONFIG_HOME", dir)

	_, err := ImportCursorCredentials(cursorTestProfile())
	if err == nil {
		t.Fatal("expected error for missing file; got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error does not wrap os.ErrNotExist: %v", err)
	}
}

// ── valid round-trip ──────────────────────────────────────────────────────────

// TestImportCursorCredentials_Valid verifies that a well-formed auth.json
// produces a DedicatedCredStore with the correct fields.
func TestImportCursorCredentials_Valid(t *testing.T) {
	const wantExp = int64(1893456000) // 2030-01-01 00:00:00 UTC
	tok := makeSyntheticJWT(t, map[string]any{
		"iss":  "authentication.cursor.sh",
		"aud":  "cursor.com",
		"type": "session",
		"exp":  float64(wantExp),
	})
	content, _ := json.Marshal(map[string]string{
		"accessToken":  tok,
		"refreshToken": "rt",
	})
	xdgBase, _ := makeCursorDir(t, string(content))
	t.Setenv("XDG_CONFIG_HOME", xdgBase)

	store, err := ImportCursorCredentials(cursorTestProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.AccessToken != tok {
		t.Error("AccessToken mismatch (value hidden)")
	}
	if store.RefreshToken != "rt" {
		t.Errorf("RefreshToken = %q; want %q", store.RefreshToken, "rt")
	}
	if store.TokenType != "Bearer" {
		t.Errorf("TokenType = %q; want Bearer", store.TokenType)
	}
	wantTime := time.Unix(wantExp, 0)
	if !store.ExpiresAt.Equal(wantTime) {
		t.Errorf("ExpiresAt = %v; want %v", store.ExpiresAt, wantTime)
	}
	// Static credential: no OAuth client plumbing.
	if store.ClientID != "" {
		t.Errorf("ClientID should be empty for static cursor credential; got %q", store.ClientID)
	}
	if store.TokenEndpoint != "" {
		t.Errorf("TokenEndpoint should be empty for static cursor credential")
	}
}

// ── JWT expiry decode ─────────────────────────────────────────────────────────

// TestParseCursorJWTExpiry_Valid verifies the normal path: exp claim decoded to
// the correct time.Time.  (Mutation-proofed: see TestMutation_ExpiryDecode.)
func TestParseCursorJWTExpiry_Valid(t *testing.T) {
	const wantExp = int64(1893456000)
	tok := makeSyntheticJWT(t, map[string]any{"exp": float64(wantExp)})

	got, err := ParseCursorJWTExpiry(tok)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Unix(wantExp, 0)
	if !got.Equal(want) {
		t.Errorf("expiry = %v; want %v", got, want)
	}
}

// TestParseCursorJWTExpiry_Malformed verifies that non-JWT and truncated tokens
// return a descriptive error without panicking.
func TestParseCursorJWTExpiry_Malformed(t *testing.T) {
	// Pre-build a token whose payload has no exp claim.
	noExpPayload, _ := json.Marshal(map[string]any{"sub": "x"})
	noExpTok := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(noExpPayload) + ".sig"

	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no_dots", "notajwt"},
		{"one_dot", "a.b"},
		{"bad_base64", "header.!!!.sig"},
		{"non_json_payload", "header." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".sig"},
		{"missing_exp", noExpTok},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCursorJWTExpiry(tc.token)
			if err == nil {
				t.Errorf("expected error for %q; got nil", tc.name)
			}
		})
	}
}

// ── NewCursorCredentialSource ─────────────────────────────────────────────────

// TestNewCursorCredentialSource verifies end-to-end: import → StaticSource → Token().
func TestNewCursorCredentialSource(t *testing.T) {
	const wantExp = int64(1893456000)
	tok := makeSyntheticJWT(t, map[string]any{"exp": float64(wantExp)})
	content, _ := json.Marshal(map[string]string{
		"accessToken":  tok,
		"refreshToken": "rt",
	})
	xdgBase, _ := makeCursorDir(t, string(content))
	t.Setenv("XDG_CONFIG_HOME", xdgBase)

	src, err := NewCursorCredentialSource(cursorTestProfile())
	if err != nil {
		t.Fatalf("NewCursorCredentialSource: %v", err)
	}

	gotToken, gotExp, err := src.Token(nil)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if gotToken != tok {
		t.Error("Token mismatch (value hidden)")
	}
	wantTime := time.Unix(wantExp, 0)
	if !gotExp.Equal(wantTime) {
		t.Errorf("ExpiresAt = %v; want %v", gotExp, wantTime)
	}
}

// ── CursorCredPath ────────────────────────────────────────────────────────────

// TestCursorCredPath_EnvVar verifies that a set XDG_CONFIG_HOME is preferred.
func TestCursorCredPath_EnvVar(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	p, err := CursorCredPath(cursorTestProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/custom/xdg/cursor/auth.json"
	if p != want {
		t.Errorf("path = %q; want %q", p, want)
	}
}

// TestCursorCredPath_Default verifies fallback to ~/.config when XDG_CONFIG_HOME
// is unset.
func TestCursorCredPath_Default(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	p, err := CursorCredPath(cursorTestProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must end with /cursor/auth.json (absolute path, home-rooted).
	if !containsStr(p, "cursor/auth.json") {
		t.Errorf("path %q does not contain cursor/auth.json", p)
	}
	if len(p) == 0 || p[0] != '/' {
		t.Errorf("path %q is not absolute", p)
	}
}
