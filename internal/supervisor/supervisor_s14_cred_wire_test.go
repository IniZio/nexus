package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// TestBuildSeedEgressOpts_ConstructionSite is the mutation guard for the
// construction site introduced by S14:
//
//	resolveSeedProfile(sb) → sbProfile
//	cred.NewCredentialSourceForProfile(sbProfile) → src
//	service.WireAgentEgress(&opts, sbProfile, broker, nil, src) → opts.AgentCredSource
//
// Two mutations must be RED:
//  1. resolveSeedProfile's result replaced with cred.ClaudeCodeProfile (wrong
//     profile): NewCredentialSourceForProfile(ClaudeCodeProfile) returns nil →
//     AgentCredSource = nil → first assertion fails → RED.
//  2. WireAgentEgress call dropped (AgentCredSource not assigned): opts stays
//     zero → AgentCredSource = nil → first assertion fails → RED.
//
// The test then feeds the constructed AgentCredSource directly into runSeedRoute
// via a hand-built seedRouteInputs literal to confirm runSeedRoute pushes the
// real token when StaticCredSrc is non-nil (second assertion). This covers the
// runSeedRoute behaviour, NOT the assignment site in buildSeedRouteInputs —
// that site is covered by TestBuildSeedRouteInputs_WiresStaticCredSrc.
func TestBuildSeedEgressOpts_ConstructionSite(t *testing.T) {
	const realToken = "tok-s14-construction-test"

	// ── Write synthetic cursor auth.json ──────────────────────────────────────
	tmpDir := t.TempDir()
	credDir := filepath.Join(tmpDir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("mkdir cursor dir: %v", err)
	}
	authJSON, err := json.Marshal(map[string]string{
		"accessToken":  realToken,
		"refreshToken": "refresh-unused",
	})
	if err != nil {
		t.Fatalf("marshal auth.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), authJSON, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	// XDG_CONFIG_HOME → tmpDir so CursorCredPath resolves to tmpDir/cursor/auth.json.
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// ── Construction site under test ──────────────────────────────────────────
	broker := cred.NewBroker()
	var id domain.SandboxID
	id[0] = 0xC3

	sb := domain.Sandbox{
		ID:        id,
		AgentName: cred.CursorAgentProfile.Name,
	}

	egressWire, err := buildSeedEgressOpts(sb, broker)
	if err != nil {
		t.Fatalf("buildSeedEgressOpts: %v", err)
	}
	// Mutation 1 (wrong profile) and mutation 2 (WireAgentEgress dropped) both
	// leave AgentCredSource nil — this assertion catches both.
	if egressWire.AgentCredSource == nil {
		t.Fatal("buildSeedEgressOpts: AgentCredSource is nil for cursor-agent; " +
			"resolveSeedProfile returned wrong profile or WireAgentEgress was not called")
	}

	// ── Assignment site: feed into seedRouteInputs and verify end-to-end ──────
	if _, err := broker.RegisterPlaceholder(id, cred.CursorAgentProfile.CredentialedHost, ""); err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}

	cert := fakeCert()
	noopSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	// Mutation 3 (StaticCredSrc assignment dropped): remove
	// StaticCredSrc: egressWire.AgentCredSource and the broker push does not
	// happen → broker.Resolve returns "" ≠ realToken → RED.
	ok, _ := runSeedRoute(context.Background(), routeAgent, seedRouteInputs{
		SB:            sb,
		Cert:          cert,
		CASeeder:      noopSeeder,
		AgentSeeder:   noopSeeder,
		Broker:        broker,
		Refreshers:    nil,
		StaticCredSrc: egressWire.AgentCredSource,
	})
	if !ok {
		t.Fatal("runSeedRoute returned ok=false; seed failed")
	}

	// Real token must be resolved via the credential source wired at construction.
	ph, hasPh := broker.Placeholder(id, cred.CursorAgentProfile.CredentialedHost)
	if !hasPh {
		t.Fatalf("broker has no placeholder for %s after runSeedRoute",
			cred.CursorAgentProfile.CredentialedHost)
	}
	got, ok2 := broker.Resolve(ph)
	if !ok2 {
		t.Fatalf("broker.Resolve(%q) = false after runSeedRoute", ph)
	}
	if got != realToken {
		t.Errorf("broker.Resolve(placeholder) = %q, want %q\n"+
			"(construction site did not wire real token via egressWire.AgentCredSource;\n"+
			" check resolveSeedProfile, NewCredentialSourceForProfile, or StaticCredSrc assignment)",
			got, realToken)
	}
}

// TestBuildSeedRouteInputs_WiresStaticCredSrc is the mutation guard for the
// assignment site in buildSeedRouteInputs:
//
//	StaticCredSrc: egressWire.AgentCredSource
//
// Dropping or zero-initialising that assignment leaves inputs.StaticCredSrc nil
// → assertion below fails → RED.
func TestBuildSeedRouteInputs_WiresStaticCredSrc(t *testing.T) {
	// ── Write synthetic cursor auth.json ──────────────────────────────────────
	tmpDir := t.TempDir()
	credDir := filepath.Join(tmpDir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("mkdir cursor dir: %v", err)
	}
	authJSON, err := json.Marshal(map[string]string{
		"accessToken":  "tok-s14-assignment-site",
		"refreshToken": "refresh-unused",
	})
	if err != nil {
		t.Fatalf("marshal auth.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), authJSON, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// ── Build egressWire with a real AgentCredSource ──────────────────────────
	broker := cred.NewBroker()
	var id domain.SandboxID
	id[0] = 0xA5
	sb := domain.Sandbox{
		ID:        id,
		AgentName: cred.CursorAgentProfile.Name,
	}

	egressWire, err := buildSeedEgressOpts(sb, broker)
	if err != nil {
		t.Fatalf("buildSeedEgressOpts: %v", err)
	}
	// Precondition: egressWire must carry a non-nil AgentCredSource so that the
	// test is not vacuously green because the source was never set.
	if egressWire.AgentCredSource == nil {
		t.Fatal("precondition: egressWire.AgentCredSource is nil — test is vacuous; " +
			"check buildSeedEgressOpts and NewCredentialSourceForProfile")
	}

	// ── Assignment site under test ────────────────────────────────────────────
	// nil values for fields not under test (cert, seeders, agentClient, svc).
	inputs := buildSeedRouteInputs(sb, nil, nil, nil, nil, broker, nil, egressWire, nil)
	if inputs.StaticCredSrc == nil {
		t.Fatal("buildSeedRouteInputs: StaticCredSrc is nil — " +
			"egressWire.AgentCredSource was not assigned to StaticCredSrc in buildSeedRouteInputs")
	}
	if inputs.StaticCredSrc != egressWire.AgentCredSource {
		t.Fatal("buildSeedRouteInputs: StaticCredSrc is not the same object as egressWire.AgentCredSource")
	}
}
