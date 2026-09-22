package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func TestSeedGuestAgentAndSecrets_ContainsBothCredSets(t *testing.T) {
	// NO t.Parallel: races with other global-swapping tests on lookupGitHubToken.
	ctx := context.Background()

	orig := lookupGitHubToken
	t.Cleanup(func() { lookupGitHubToken = orig })
	const ghReal = "ghs_combined_test_real_token_never_in_guest"
	lookupGitHubToken = func(context.Context) (string, error) { return ghReal, nil }

	broker := cred.NewBroker()
	id := seedTestID(40)
	cap := &captureSeeder{}

	_, err := SeedGuestAgentAndSecrets(ctx, broker, id,
		[]string{"GH_TOKEN@github.com,api.github.com"},
		cap.fn(),
	)
	if err != nil {
		t.Fatalf("SeedGuestAgentAndSecrets: %v", err)
	}

	payload := cap.payload

	if !bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("combined payload must contain CLAUDE_CODE_OAUTH_TOKEN= (broker placeholder path)\npayload:\n%s", payload)
	}
	// MUTATION-PIN: NODE_EXTRA_CA_CERTS proves agent half ran.
	if !bytes.Contains(payload, []byte("NODE_EXTRA_CA_CERTS=")) {
		t.Errorf("combined payload missing NODE_EXTRA_CA_CERTS (agent half absent)\npayload:\n%s", payload)
	}
	if !bytes.Contains(payload, []byte("GH_TOKEN=")) {
		t.Errorf("combined payload missing GH_TOKEN (secret half absent)\npayload:\n%s", payload)
	}
}

func TestSeedGuestAgentAndSecrets_OneWrite(t *testing.T) {
	ctx := context.Background()

	orig := lookupGitHubToken
	t.Cleanup(func() { lookupGitHubToken = orig })
	lookupGitHubToken = func(context.Context) (string, error) { return "tok", nil }

	broker := cred.NewBroker()
	id := seedTestID(41)
	cap := &captureSeeder{}

	if _, err := SeedGuestAgentAndSecrets(ctx, broker, id,
		[]string{"GH_TOKEN@github.com,api.github.com"},
		cap.fn(),
	); err != nil {
		t.Fatalf("SeedGuestAgentAndSecrets: %v", err)
	}

	if cap.calls != 1 {
		t.Errorf("seeder called %d times, want exactly 1 (structural overwrite prevented)", cap.calls)
	}
}

func TestSeedGuestAgentAndSecrets_NoRealToken(t *testing.T) {
	ctx := context.Background()

	orig := lookupGitHubToken
	t.Cleanup(func() { lookupGitHubToken = orig })
	const ghReal = "ghs_combined_no_leak_secret"
	lookupGitHubToken = func(context.Context) (string, error) { return ghReal, nil }

	broker := cred.NewBroker()
	id := seedTestID(42)
	cap := &captureSeeder{}

	if _, err := SeedGuestAgentAndSecrets(ctx, broker, id,
		[]string{"GH_TOKEN@github.com,api.github.com"},
		cap.fn(),
	); err != nil {
		t.Fatalf("SeedGuestAgentAndSecrets: %v", err)
	}

	if bytes.Contains(cap.payload, []byte(ghReal)) {
		t.Errorf("combined payload must NOT contain the real GitHub token\npayload:\n%s", cap.payload)
	}
}

func TestSeedGuestAgentAndSecrets_AgentOnlyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	broker := cred.NewBroker()
	id := seedTestID(43)
	cap := &captureSeeder{}

	if _, err := SeedGuestAgentAndSecrets(ctx, broker, id, nil, cap.fn()); err != nil {
		t.Fatalf("SeedGuestAgentAndSecrets with no specs: %v", err)
	}

	if !bytes.Contains(cap.payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("agent-only combined path must contain CLAUDE_CODE_OAUTH_TOKEN= (broker placeholder path)\npayload:\n%s", cap.payload)
	}
	// MUTATION-PIN: NODE_EXTRA_CA_CERTS proves agent seeding path ran.
	if !bytes.Contains(cap.payload, []byte("NODE_EXTRA_CA_CERTS=")) {
		t.Errorf("agent-only combined path missing NODE_EXTRA_CA_CERTS\npayload:\n%s", cap.payload)
	}
	if cap.calls != 1 {
		t.Errorf("seeder called %d times, want 1", cap.calls)
	}
}
