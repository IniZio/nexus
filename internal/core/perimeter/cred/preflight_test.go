package cred

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// JWT fixtures — signatures are syntactically valid base64url but cryptographically
// meaningless; ParseCursorJWTExpiry only decodes the payload, never verifies the
// signature.
//
// expiredJWT has exp=1000000000 (2001-09-09T01:46:40Z) — a genuinely past timestamp.
// validJWT   has exp=9999999999 (2286-11-20T17:46:39Z) — effectively never expires.
const (
	expiredJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjEwMDAwMDAwMDB9.ZmFrZQ"
	validJWT   = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjk5OTk5OTk5OTl9.ZmFrZQ"

	// sentinelToken is a recognisable non-empty string used by AC-4 to prove
	// that no token value leaks into a rendered Sentence().
	sentinelToken = "tok_SENTINEL_DO_NOT_LEAK"
)

// cursorProfileInDir returns a CursorAgentProfile whose credential directory
// is redirected to dir via the profile's CredDirEnvVar.  The env var is
// restored by t.Cleanup via t.Setenv.
func cursorProfileInDir(t *testing.T, dir string) AgentProfile {
	t.Helper()
	p := CursorAgentProfile
	t.Setenv(p.CredDirEnvVar, dir)
	return p
}

// writeAuthJSON writes {accessToken, refreshToken} to <dir>/cursor/auth.json.
func writeAuthJSON(t *testing.T, dir, accessToken string) {
	t.Helper()
	credDir := filepath.Join(dir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("mkdir cursor: %v", err)
	}
	payload := `{"accessToken":"` + accessToken + `","refreshToken":""}`
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), []byte(payload), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
}

// writeRawAuthJSON writes arbitrary bytes to <dir>/cursor/auth.json.
func writeRawAuthJSON(t *testing.T, dir string, content []byte) {
	t.Helper()
	credDir := filepath.Join(dir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("mkdir cursor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), content, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
}

// ── AC-1: each reason is reachable and distinguishable ───────────────────────

// TestPreflight_OK proves a valid, unexpired credential yields PreflightOK.
func TestPreflight_OK(t *testing.T) {
	dir := t.TempDir()
	p := cursorProfileInDir(t, dir)
	writeAuthJSON(t, dir, validJWT)

	got := CheckCred(p)

	if got.Reason != PreflightOK {
		t.Fatalf("want PreflightOK, got %v (sentence: %q)", got.Reason, got.Sentence())
	}
	if !got.OK() {
		t.Fatal("OK() should return true for PreflightOK")
	}
	if got.Sentence() != "" {
		t.Fatalf("Sentence() should be empty for OK, got %q", got.Sentence())
	}
}

// TestPreflight_Absent proves a missing credential file yields PreflightAbsent.
// AC-1.
func TestPreflight_Absent(t *testing.T) {
	dir := t.TempDir()
	p := cursorProfileInDir(t, dir)
	// No auth.json written.

	got := CheckCred(p)

	if got.Reason != PreflightAbsent {
		t.Fatalf("want PreflightAbsent, got %v", got.Reason)
	}
	if got.AgentName != p.Name {
		t.Fatalf("AgentName: want %q, got %q", p.Name, got.AgentName)
	}
	if s := got.Sentence(); !strings.Contains(s, p.Name) {
		t.Fatalf("Sentence() does not name the agent: %q", s)
	}
}

// TestPreflight_Unreadable proves a corrupt credential file yields
// PreflightUnreadable.  AC-1.
func TestPreflight_Unreadable(t *testing.T) {
	dir := t.TempDir()
	p := cursorProfileInDir(t, dir)
	writeRawAuthJSON(t, dir, []byte("not valid json{{{{"))

	got := CheckCred(p)

	if got.Reason != PreflightUnreadable {
		t.Fatalf("want PreflightUnreadable, got %v", got.Reason)
	}
	if s := got.Sentence(); !strings.Contains(s, p.Name) {
		t.Fatalf("Sentence() does not name the agent: %q", s)
	}
}

// TestPreflight_EmptyToken proves a credential file with an empty accessToken
// yields PreflightUnreadable (not Absent — the file is present but unusable).
// AC-1.
func TestPreflight_EmptyToken(t *testing.T) {
	dir := t.TempDir()
	p := cursorProfileInDir(t, dir)
	writeRawAuthJSON(t, dir, []byte(`{"accessToken":"","refreshToken":""}`))

	got := CheckCred(p)

	if got.Reason != PreflightUnreadable {
		t.Fatalf("want PreflightUnreadable for empty token, got %v", got.Reason)
	}
}

// ── AC-2: expired case with real JWT fixture + clock injection proof ──────────

// TestPreflight_Expired_RealJWT proves that a credential file containing
// expiredJWT (a genuine past timestamp, no clock injection) yields
// PreflightExpired.  AC-2 (genuine expired JWT fixture).
func TestPreflight_Expired_RealJWT(t *testing.T) {
	dir := t.TempDir()
	p := cursorProfileInDir(t, dir)
	writeAuthJSON(t, dir, expiredJWT)

	// No clock injection: time.Now() is after 2001-09-09.
	got := CheckCred(p)

	if got.Reason != PreflightExpired {
		t.Fatalf("want PreflightExpired for genuinely expired JWT, got %v", got.Reason)
	}
	if got.ExpiredAt.IsZero() {
		t.Fatal("ExpiredAt must be non-zero for PreflightExpired")
	}
	// Expiry should be 2001-09-09T01:46:40Z (Unix 1000000000).
	wantUnix := int64(1000000000)
	if got.ExpiredAt.Unix() != wantUnix {
		t.Fatalf("ExpiredAt.Unix(): want %d, got %d", wantUnix, got.ExpiredAt.Unix())
	}
	if s := got.Sentence(); !strings.Contains(s, p.Name) {
		t.Fatalf("Sentence() does not name the agent: %q", s)
	}
}

// TestPreflight_Expired_ClockInjection proves that checkCredAt with an
// explicit future "now" can expire an otherwise-valid token.  This double-
// confirms the expiry comparison path independently of real wall time.  AC-2.
func TestPreflight_Expired_ClockInjection(t *testing.T) {
	dir := t.TempDir()
	p := cursorProfileInDir(t, dir)
	writeAuthJSON(t, dir, validJWT) // exp = 9999999999 (far future)

	// Advance now past the far-future expiry.
	futureNow := time.Unix(9999999999+1, 0)
	got := checkCredAt(p, futureNow)

	if got.Reason != PreflightExpired {
		t.Fatalf("want PreflightExpired with injected clock past expiry, got %v", got.Reason)
	}
}

// TestPreflight_ParseCursorJWTExpiry_RoundTrip proves that ParseCursorJWTExpiry
// correctly decodes the exp claim from both fixtures, independently of
// checkCredAt.  AC-2 (proves the real expiry parse works).
func TestPreflight_ParseCursorJWTExpiry_RoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		wantUnix  int64
	}{
		{"expired", expiredJWT, 1000000000},
		{"valid", validJWT, 9999999999},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exp, err := ParseCursorJWTExpiry(tc.token)
			if err != nil {
				t.Fatalf("ParseCursorJWTExpiry: %v", err)
			}
			if exp.Unix() != tc.wantUnix {
				t.Fatalf("want Unix %d, got %d", tc.wantUnix, exp.Unix())
			}
		})
	}
}

// ── AC-3: CredentialFormatNone agent (Claude Code) always yields OK ───────────

// TestPreflight_ClaudeCode_AlwaysOK proves that a profile with
// CredentialFormatNone (Claude Code) returns PreflightOK regardless of
// whether a credential file exists.  AC-3.
func TestPreflight_ClaudeCode_AlwaysOK(t *testing.T) {
	// Use an empty dir so no claude credential file is present.
	dir := t.TempDir()
	t.Setenv(ClaudeCodeProfile.CredDirEnvVar, dir)
	p := ClaudeCodeProfile

	got := CheckCred(p)

	if got.Reason != PreflightOK {
		t.Fatalf(
			"ClaudeCodeProfile (CredentialFormatNone) must yield PreflightOK, got %v (sentence: %q)",
			got.Reason, got.Sentence(),
		)
	}
	if !got.OK() {
		t.Fatal("OK() must return true for CredentialFormatNone profile")
	}
}

// ── AC-4: no token value in any returned reason or message ────────────────────

// TestPreflight_NoTokenLeak proves that sentinelToken does not appear in
// Sentence() for any of the four result kinds.  AC-4.
func TestPreflight_NoTokenLeak(t *testing.T) {
	assertNoLeak := func(t *testing.T, r PreflightResult) {
		t.Helper()
		s := r.Sentence()
		if strings.Contains(s, sentinelToken) {
			t.Errorf("Sentence() leaks token value: %q", s)
		}
	}

	t.Run("absent", func(t *testing.T) {
		dir := t.TempDir()
		p := cursorProfileInDir(t, dir)
		// Write no file; name contains sentinel but the token is absent.
		r := CheckCred(p)
		assertNoLeak(t, r)
	})

	t.Run("unreadable", func(t *testing.T) {
		dir := t.TempDir()
		p := cursorProfileInDir(t, dir)
		writeRawAuthJSON(t, dir, []byte(`{"accessToken":"`+sentinelToken+`_BAD_JSON`))
		r := CheckCred(p)
		assertNoLeak(t, r)
	})

	t.Run("expired", func(t *testing.T) {
		dir := t.TempDir()
		p := cursorProfileInDir(t, dir)
		writeAuthJSON(t, dir, expiredJWT) // token value is the JWT string
		r := CheckCred(p)
		// The JWT string itself must not appear; the sentence only contains the
		// expiry timestamp and the agent name.
		if strings.Contains(r.Sentence(), expiredJWT) {
			t.Errorf("Sentence() contains JWT token string: %q", r.Sentence())
		}
		assertNoLeak(t, r)
	})

	t.Run("ok", func(t *testing.T) {
		dir := t.TempDir()
		p := cursorProfileInDir(t, dir)
		writeAuthJSON(t, dir, sentinelToken) // deliberately not a valid JWT
		// sentinelToken is not a valid JWT so ExpiresAt will be zero → OK.
		r := CheckCred(p)
		// Result is either OK (zero expiry → not expired) or Unreadable;
		// either way the sentence must not contain the token.
		assertNoLeak(t, r)
	})
}

// ── AC-5: mutation proof (documented here; the RED/GREEN pair is in the report)
//
// The expiry predicate in checkCredAt is:
//
//   if !store.ExpiresAt.IsZero() && store.ExpiresAt.Before(now) {
//
// Mutating the predicate to always-false (e.g. changing Before to After) makes
// TestPreflight_Expired_RealJWT go RED: the test expects PreflightExpired but
// gets PreflightOK.  Restoring the predicate makes it GREEN again.  This is
// verified manually and recorded in the slice report — no automated mutation
// runner is required.

// TestPreflight_ReasonDistinctness asserts that all four PreflightReason
// constants are distinct values, i.e. no two share the same iota.
func TestPreflight_ReasonDistinctness(t *testing.T) {
	reasons := []struct {
		name string
		val  PreflightReason
	}{
		{"OK", PreflightOK},
		{"Absent", PreflightAbsent},
		{"Unreadable", PreflightUnreadable},
		{"Expired", PreflightExpired},
	}
	seen := map[PreflightReason]string{}
	for _, r := range reasons {
		if prev, dup := seen[r.val]; dup {
			t.Errorf("PreflightReason %v conflicts with %v (both = %d)", r.name, prev, r.val)
		}
		seen[r.val] = r.name
	}
}
