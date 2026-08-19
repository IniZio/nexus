package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// ── test helpers shared across seed_*_test.go ────────────────────────────────

// seedTestID returns a deterministic SandboxID for unit tests. n is encoded in
// byte 0 to ensure distinct IDs across concurrent tests (n must be 0–255).
func seedTestID(n int) domain.SandboxID {
	var id domain.SandboxID
	id[0] = byte(n)
	return id
}

// captureSeeder is a GuestSeeder stub that captures the last delivered payload
// and counts calls. It is safe for concurrent use.
type captureSeeder struct {
	mu      sync.Mutex
	payload []byte
	calls   int
}

// fn returns a GuestSeeder that records the delivered payload.
func (c *captureSeeder) fn() GuestSeeder {
	return func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.payload = append([]byte(nil), payload...) // copy
		c.calls++
		return nil
	}
}

// ── per-sandbox credential-kind unit tests ────────────────────────────────────

// TestBuildAgentSeedPayloadPerSandboxCredKind proves that two seed payloads
// built in the same process with different explicit credential kinds produce
// different placeholder env vars:
//
//   - kindOAuth      → CLAUDE_CODE_OAUTH_TOKEN=<placeholder>
//   - kindAuthToken  → ANTHROPIC_AUTH_TOKEN=<placeholder>
//
// No KVM, no network. Pure unit test over buildAgentSeedPayload.
func TestBuildAgentSeedPayloadPerSandboxCredKind(t *testing.T) {
	t.Parallel()
	placeholder := "deadbeef1234abcd5678ef90"
	expires := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
	records := []cred.PlaceholderRecord{
		{
			Host:        AnthropicAPIHost,
			Placeholder: placeholder,
			ExpiresAt:   expires,
			SandboxID:   seedTestID(99),
		},
	}
	profile := cred.ClaudeCodeProfile

	oauthBytes, err := buildAgentSeedPayload(records, kindOAuth, profile)
	if err != nil {
		t.Fatalf("buildAgentSeedPayload(kindOAuth): %v", err)
	}
	authBytes, err := buildAgentSeedPayload(records, kindAuthToken, profile)
	if err != nil {
		t.Fatalf("buildAgentSeedPayload(kindAuthToken): %v", err)
	}
	oauthPayload := string(oauthBytes)
	authPayload := string(authBytes)

	// --- kindOAuth assertions ---
	if !strings.Contains(oauthPayload, "CLAUDE_CODE_OAUTH_TOKEN="+placeholder) {
		t.Errorf("kindOAuth payload missing CLAUDE_CODE_OAUTH_TOKEN=<placeholder>; got:\n%s", oauthPayload)
	}
	if strings.Contains(oauthPayload, "ANTHROPIC_AUTH_TOKEN=") {
		t.Errorf("kindOAuth payload must NOT contain ANTHROPIC_AUTH_TOKEN; got:\n%s", oauthPayload)
	}

	// --- kindAuthToken assertions ---
	if !strings.Contains(authPayload, "ANTHROPIC_AUTH_TOKEN="+placeholder) {
		t.Errorf("kindAuthToken payload missing ANTHROPIC_AUTH_TOKEN=<placeholder>; got:\n%s", authPayload)
	}
	if strings.Contains(authPayload, "CLAUDE_CODE_OAUTH_TOKEN=") {
		t.Errorf("kindAuthToken payload must NOT contain CLAUDE_CODE_OAUTH_TOKEN; got:\n%s", authPayload)
	}

	// --- The two payloads must differ ---
	if oauthPayload == authPayload {
		t.Error("kindOAuth and kindAuthToken payloads are identical; per-sandbox differentiation is broken")
	}
}

// TestResolveAgentCredKindDefaultIsOAuth verifies that when ANTHROPIC_AUTH_TOKEN
// is absent from the environment the default resolver returns kindOAuth, so
// callers that set nothing still get the OAuth placeholder — no regression.
func TestResolveAgentCredKindDefaultIsOAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	got := resolveAgentCredKind(cred.ClaudeCodeProfile)
	if got != kindOAuth {
		t.Errorf("expected kindOAuth (%d) with empty ANTHROPIC_AUTH_TOKEN, got %d", kindOAuth, got)
	}
}

// TestResolveAgentCredKindAuthTokenEnv verifies that ANTHROPIC_AUTH_TOKEN in
// the host environment causes resolveAgentCredKind to return kindAuthToken.
func TestResolveAgentCredKindAuthTokenEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-ant-test")
	got := resolveAgentCredKind(cred.ClaudeCodeProfile)
	if got != kindAuthToken {
		t.Errorf("expected kindAuthToken (%d) with ANTHROPIC_AUTH_TOKEN set, got %d", kindAuthToken, got)
	}
}
