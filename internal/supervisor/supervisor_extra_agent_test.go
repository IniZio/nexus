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

func TestSeedLoop_ExtraAgentPlaceholderRegistered(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	broker := cred.NewBroker()
	var id domain.SandboxID
	id[0] = 0xF2

	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	cap := &captureGuestSeeder{}
	cert := fakeCert()

	ok, _ := SeedLoop(
		context.Background(), id, &cert,
		caSeeder, cap.fn(),
		broker, nil,
		1, 0, nil, true, cred.ClaudeCodeProfile,
		[]cred.AgentProfile{cred.CursorAgentProfile},
		nil,
	)
	if !ok {
		t.Fatal("SeedLoop returned ok=false")
	}

	cursorHost := cred.CursorAgentProfile.CredentialedHost
	if _, hasPh := broker.Placeholder(id, cursorHost); !hasPh {
		t.Errorf("cursor placeholder not registered (MUTATION S1: extraProfiles not threaded to SeedGuestAgentForProfiles)")
	}

	combined := cap.combined()
	if !bytes.Contains(combined, []byte("CURSOR_AUTH_TOKEN=")) {
		t.Errorf("CURSOR_AUTH_TOKEN absent from SeedLoop payload (MUTATION S1)\n%s", combined)
	}
}

func TestSeedLoop_ExtraAgentPlaceholderIsNotRealToken(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	broker := cred.NewBroker()
	var id domain.SandboxID
	id[0] = 0xF3

	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	cap := &captureGuestSeeder{}
	cert := fakeCert()

	ok, _ := SeedLoop(
		context.Background(), id, &cert,
		caSeeder, cap.fn(),
		broker, nil,
		1, 0, nil, true, cred.ClaudeCodeProfile,
		[]cred.AgentProfile{cred.CursorAgentProfile},
		nil,
	)
	if !ok {
		t.Fatal("SeedLoop returned ok=false")
	}

	cursorHost := cred.CursorAgentProfile.CredentialedHost
	ph, hasPh := broker.Placeholder(id, cursorHost)
	if !hasPh {
		t.Fatalf("no placeholder for %s", cursorHost)
	}

	combined := cap.combined()
	if !bytes.Contains(combined, []byte(ph)) {
		t.Errorf("guest payload does not contain extra-agent placeholder %q (invariant: guest must hold placeholder)", ph)
	}

	const realToken = "real-cursor-never-in-guest"
	if err := broker.SetRealToken(id, cursorHost, realToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}
	if bytes.Contains(combined, []byte(realToken)) {
		t.Errorf("guest payload contains real token %q — PLACEHOLDER INVARIANT BROKEN", realToken)
	}
}

func TestResolveExtraSeedProfiles(t *testing.T) {
	t.Parallel()

	sb := domain.Sandbox{
		AgentName:       cred.ClaudeCodeProfile.Name,
		ExtraAgentNames: []string{cred.CursorAgentProfile.Name},
	}
	profiles := resolveExtraSeedProfiles(sb)
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1 (MUTATION S2: ExtraAgentNames not read)", len(profiles))
	}
	if profiles[0].Name != cred.CursorAgentProfile.Name {
		t.Errorf("profile[0].Name = %q, want %q", profiles[0].Name, cred.CursorAgentProfile.Name)
	}

	sbNone := domain.Sandbox{AgentName: cred.ClaudeCodeProfile.Name}
	if got := resolveExtraSeedProfiles(sbNone); len(got) != 0 {
		t.Errorf("no extra names: got %d profiles, want 0", len(got))
	}
}

func TestSeedAgentAndHumanSecrets_ExtraAgentPresent(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xF4
	sb := domain.Sandbox{
		ID:        id,
		AgentName: cred.ClaudeCodeProfile.Name,
	}

	broker := cred.NewBroker()
	cap := &captureGuestSeeder{}
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	cert := (*x509.Certificate)(fakeCert())

	ok, _ := seedAgentAndHumanSecrets(
		ctx, sb, cert,
		caSeeder, cap.fn(),
		broker, nil, nil,
		cred.ClaudeCodeProfile,
		[]cred.AgentProfile{cred.CursorAgentProfile},
		nil,
	)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false")
	}

	combined := cap.combined()
	if !bytes.Contains(combined, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("primary CLAUDE_CODE_OAUTH_TOKEN absent from combined payload\n%s", combined)
	}
	if !bytes.Contains(combined, []byte("CURSOR_AUTH_TOKEN=")) {
		t.Errorf("extra CURSOR_AUTH_TOKEN absent from combined payload (MUTATION S3: extraProfiles not forwarded)\n%s", combined)
	}

	if cap.calls != 1 {
		t.Errorf("seeder called %d times, want 1 (overwrite prevention)", cap.calls)
	}
}

func TestBuildSeedRouteInputs_ExtraProfilesFromSandbox(t *testing.T) {
	t.Parallel()

	sb := domain.Sandbox{
		AgentName:       cred.ClaudeCodeProfile.Name,
		ExtraAgentNames: []string{cred.CursorAgentProfile.Name},
	}

	in := buildSeedRouteInputs(
		sb,
		fakeCert(),
		extraAgentTestNoopSeeder, extraAgentTestNoopSeeder,
		nil,
		cred.NewBroker(),
		nil,
		service.CreateAndBootOptions{},
		nil,
	)

	if len(in.ExtraProfiles) != 1 {
		t.Fatalf("ExtraProfiles len=%d, want 1", len(in.ExtraProfiles))
	}
	if in.ExtraProfiles[0].Name != cred.CursorAgentProfile.Name {
		t.Errorf("ExtraProfiles[0].Name=%q, want %q", in.ExtraProfiles[0].Name, cred.CursorAgentProfile.Name)
	}
}

var extraAgentTestNoopSeeder service.GuestSeeder = func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
