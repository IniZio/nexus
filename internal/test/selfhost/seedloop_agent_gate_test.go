package selfhost_test

// TBD-PD-32: SeedLoop must write the agent's credential placeholder only into
// sandboxes that actually run an agent.
//
// It previously seeded on every non-human-secret sandbox, so a plain sandbox
// received an agent credential env file it never reads — and, once a second
// agent exists, would have received the wrong agent's variables. The CA cert is
// seeded either way: any sandbox behind the proxy needs it to speak HTTPS.
//
// Unit test: no KVM, no live VM.

import (
	"context"
	"crypto/x509"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func TestSeedLoop_AgentCredsOnlyForAgentSandboxes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		seedAgentCreds bool
		wantAgentSeed  bool
	}{
		{name: "agent sandbox", seedAgentCreds: true, wantAgentSeed: true},
		{name: "plain sandbox", seedAgentCreds: false, wantAgentSeed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var caSeeded, agentSeeded bool
			caSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
				caSeeded = true
				return nil
			})
			agentSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
				agentSeeded = true
				return nil
			})

			cert := &x509.Certificate{}
			// nil svc is safe: cert is non-nil, so GetPerimeterCACert is never called.
			done := supervisor.SeedLoop(context.Background(), domain.NewSandboxID(), &cert,
				caSeeder, agentSeeder, cred.NewBroker(), nil, 3, 0, nil, tc.seedAgentCreds)

			if !done {
				t.Fatal("SeedLoop returned false although every seeder succeeded")
			}
			if !caSeeded {
				t.Error("CA cert was not seeded; every sandbox behind the proxy needs it")
			}
			if agentSeeded != tc.wantAgentSeed {
				t.Errorf("agent credential seeded = %v, want %v", agentSeeded, tc.wantAgentSeed)
			}
		})
	}
}
