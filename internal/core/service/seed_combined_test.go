package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// TestSeedGuestAgentAndSecrets_ContainsBothCredSets is the primary mutation
// guard for the combined seeding path. It verifies that the payload delivered
// to the guest contains BOTH the agent credential vars (CLAUDE_CODE_OAUTH_TOKEN,
// NODE_EXTRA_CA_CERTS) AND the human secret var (GH_TOKEN).
//
// Mutation guards:
//
//   - Drop the agent payload half from seedGuestAgentAndSecrets → this test fails RED.
//   - Drop the secret payload half from seedGuestAgentAndSecrets → this test fails RED.
func TestSeedGuestAgentAndSecrets_ContainsBothCredSets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Stub out GitHub token resolution so the test does not require `gh`.
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

	// Agent half: CLAUDE_CODE_OAUTH_TOKEN must be present.
	if !bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("combined payload missing CLAUDE_CODE_OAUTH_TOKEN (agent half absent)\npayload:\n%s", payload)
	}
	// Agent half: NODE_EXTRA_CA_CERTS must be present.
	if !bytes.Contains(payload, []byte("NODE_EXTRA_CA_CERTS=")) {
		t.Errorf("combined payload missing NODE_EXTRA_CA_CERTS (agent half absent)\npayload:\n%s", payload)
	}
	// Secret half: GH_TOKEN must be present.
	if !bytes.Contains(payload, []byte("GH_TOKEN=")) {
		t.Errorf("combined payload missing GH_TOKEN (secret half absent)\npayload:\n%s", payload)
	}
}

// TestSeedGuestAgentAndSecrets_OneWrite asserts that the combined path calls the
// seeder exactly once. Two writes would mean the second silently overwrites the
// first credential set.
func TestSeedGuestAgentAndSecrets_OneWrite(t *testing.T) {
	t.Parallel()
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

// TestSeedGuestAgentAndSecrets_NoRealToken asserts the security invariant:
// the combined payload must not contain any real token value.
//
// Mutation guard: remove the placeholder-only constraint from buildSeedPayload
// or buildAgentSeedPayload → this test fails RED.
func TestSeedGuestAgentAndSecrets_NoRealToken(t *testing.T) {
	t.Parallel()
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

// TestSeedGuestAgentAndSecrets_AgentOnlyPath verifies that when specs is nil
// (no secret binds), the function still delivers agent credentials — the
// combined path must not suppress agent seeding when there are no secrets.
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
		t.Errorf("agent-only combined path missing CLAUDE_CODE_OAUTH_TOKEN\npayload:\n%s", cap.payload)
	}
	if cap.calls != 1 {
		t.Errorf("seeder called %d times, want 1", cap.calls)
	}
}
