package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func seedTestID(n int) domain.SandboxID {
	var id domain.SandboxID
	id[0] = byte(n)
	return id
}

type captureSeeder struct {
	mu      sync.Mutex
	payload []byte
	calls   int
}

func (c *captureSeeder) fn() GuestSeeder {
	return func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.payload = append([]byte(nil), payload...) // copy
		c.calls++
		return nil
	}
}

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
	profile := cred.MustProfileByName(cred.ClaudeCodeProfileName)

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

	if !strings.Contains(oauthPayload, "CLAUDE_CODE_OAUTH_TOKEN=") {
		t.Errorf("kindOAuth payload must contain CLAUDE_CODE_OAUTH_TOKEN= (broker placeholder path); got:\n%s", oauthPayload)
	}
	if strings.Contains(oauthPayload, "ANTHROPIC_AUTH_TOKEN=") {
		t.Errorf("kindOAuth payload must NOT contain ANTHROPIC_AUTH_TOKEN; got:\n%s", oauthPayload)
	}
	// MUTATION-PIN: NODE_EXTRA_CA_CERTS always written (CACertEnvVars path).
	if !strings.Contains(oauthPayload, "NODE_EXTRA_CA_CERTS=") {
		t.Errorf("kindOAuth payload missing NODE_EXTRA_CA_CERTS; got:\n%s", oauthPayload)
	}

	// kindAuthToken: ANTHROPIC_AUTH_TOKEN present.
	if !strings.Contains(authPayload, "ANTHROPIC_AUTH_TOKEN="+placeholder) {
		t.Errorf("kindAuthToken payload missing ANTHROPIC_AUTH_TOKEN=<placeholder>; got:\n%s", authPayload)
	}
	if strings.Contains(authPayload, "CLAUDE_CODE_OAUTH_TOKEN=") {
		t.Errorf("kindAuthToken payload must NOT contain CLAUDE_CODE_OAUTH_TOKEN; got:\n%s", authPayload)
	}

	// Payloads must differ.
	if oauthPayload == authPayload {
		t.Error("kindOAuth and kindAuthToken payloads are identical; per-sandbox differentiation is broken")
	}
}

func TestResolveAgentCredKindDefaultIsOAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	got := resolveAgentCredKind(cred.MustProfileByName(cred.ClaudeCodeProfileName))
	if got != kindOAuth {
		t.Errorf("expected kindOAuth (%d) with empty ANTHROPIC_AUTH_TOKEN, got %d", kindOAuth, got)
	}
}

func TestResolveAgentCredKindAuthTokenEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-ant-test")
	got := resolveAgentCredKind(cred.MustProfileByName(cred.ClaudeCodeProfileName))
	if got != kindAuthToken {
		t.Errorf("expected kindAuthToken (%d) with ANTHROPIC_AUTH_TOKEN set, got %d", kindAuthToken, got)
	}
}

func TestBuildGuestShellProfileScriptContainsHostUID(t *testing.T) {
	t.Parallel()
	script := buildGuestShellProfileScript(1234, 5678)
	if !strings.Contains(script, "NEXUS_HOST_UID=1234") {
		t.Errorf("script missing NEXUS_HOST_UID=1234:\n%s", script)
	}
	if !strings.Contains(script, "NEXUS_HOST_GID=5678") {
		t.Errorf("script missing NEXUS_HOST_GID=5678:\n%s", script)
	}
}

func TestSeedGuestHostUIDContent(t *testing.T) {
	t.Parallel()
	var cap captureSeeder
	if err := SeedGuestHostUID(context.Background(), seedTestID(1), 1001, 1002, cap.fn()); err != nil {
		t.Fatalf("SeedGuestHostUID: %v", err)
	}
	got := string(cap.payload)
	if !strings.Contains(got, "NEXUS_HOST_UID=1001") {
		t.Errorf("payload missing NEXUS_HOST_UID=1001:\n%s", got)
	}
	if !strings.Contains(got, "NEXUS_HOST_GID=1002") {
		t.Errorf("payload missing NEXUS_HOST_GID=1002:\n%s", got)
	}
}
