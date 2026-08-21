package supervisor

import (
	"bytes"
	"context"
	"crypto/x509"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

// captureGuestSeeder is a service.GuestSeeder stub that accumulates payloads
// and counts calls.
type captureGuestSeeder struct {
	payloads [][]byte
	calls    int
}

func (c *captureGuestSeeder) fn() service.GuestSeeder {
	return func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		c.payloads = append(c.payloads, append([]byte(nil), payload...))
		c.calls++
		return nil
	}
}

func (c *captureGuestSeeder) combined() []byte {
	var out []byte
	for _, p := range c.payloads {
		out = append(out, p...)
	}
	return out
}

// fakeCert returns a *x509.Certificate with a non-nil Raw so that SeedCA
// proceeds past its nil-cert guard and calls the caSeeder. SeedCA calls
// pem.EncodeToMemory(cert.Raw); the Raw bytes' validity doesn't matter
// because the caSeeder used in these tests is a no-op.
func fakeCert() *x509.Certificate {
	return &x509.Certificate{Raw: []byte("fake-cert-der-for-test")}
}

// combinedSandboxWithEnvSecret returns a domain.Sandbox configured with both
// an agent name and a non-GitHub secret spec resolved from a process env var.
// This avoids calling `gh auth token` in supervisor-layer tests.
func combinedSandboxWithEnvSecret(id domain.SandboxID, envKey string) domain.Sandbox {
	// Use "example.com" — not in the GitHub host list, so ResolveEnvelopeSecrets
	// reads the token from os.Getenv(envKey) rather than `gh auth token`.
	spec := envKey + "@example.com"
	return domain.Sandbox{
		ID:        id,
		AgentName: "claude",
		Envelope: domain.Envelope{
			SecretHosts: []string{"example.com"},
			SecretSpecs: []string{spec},
		},
	}
}

// TestSeedAgentAndHumanSecrets_ContainsAgentVars is the mutation guard for the
// agent half inside seedAgentAndHumanSecrets:
//
//	Drop the SeedGuestAgentAndSecrets call → CLAUDE_CODE_OAUTH_TOKEN disappears → RED.
func TestSeedAgentAndHumanSecrets_ContainsAgentVars(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "") // ensure kindOAuth path
	t.Setenv("NEXUS3_TEST_SECRET_A1", "supervisor-secret-for-a1")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA1
	sb := combinedSandboxWithEnvSecret(id, "NEXUS3_TEST_SECRET_A1")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	// caSeeder: no-op; SeedCA writes PEM to it but it's discarded.
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false; combined seeding failed")
	}

	payload := credCap.combined()

	// Agent half must be present.
	if !bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("combined supervisor payload missing CLAUDE_CODE_OAUTH_TOKEN (agent half absent)\npayload:\n%s", payload)
	}
}

// TestSeedAgentAndHumanSecrets_ContainsSecretVars is the mutation guard for the
// secret half inside seedAgentAndHumanSecrets:
//
//	Drop SecretSpecs from the SeedGuestAgentAndSecrets call →
//	NEXUS3_CRED_EXAMPLE_COM_TOKEN disappears → RED.
func TestSeedAgentAndHumanSecrets_ContainsSecretVars(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS3_TEST_SECRET_A2", "supervisor-secret-for-a2")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA2
	sb := combinedSandboxWithEnvSecret(id, "NEXUS3_TEST_SECRET_A2")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false")
	}

	payload := credCap.combined()

	// Secret half must be present: applySecrets emits the bind's Env name
	// (NEXUS3_TEST_SECRET_A2) as the var, not a NEXUS3_CRED_* key.
	if !bytes.Contains(payload, []byte("NEXUS3_TEST_SECRET_A2=")) {
		t.Errorf("combined supervisor payload missing NEXUS3_TEST_SECRET_A2= (secret half absent)\npayload:\n%s", payload)
	}
}

// TestSeedAgentAndHumanSecrets_OneWrite asserts the credSeeder is called
// exactly once. Two writes would mean the second silently overwrites the first.
func TestSeedAgentAndHumanSecrets_OneWrite(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS3_TEST_SECRET_A3", "supervisor-secret-for-a3")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA3
	sb := combinedSandboxWithEnvSecret(id, "NEXUS3_TEST_SECRET_A3")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false")
	}

	if credCap.calls != 1 {
		t.Errorf("credSeeder called %d times, want exactly 1 (overwrite prevented)", credCap.calls)
	}
}
