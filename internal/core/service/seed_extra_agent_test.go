package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func TestSeedGuestAgentForProfiles_ExtraPlaceholderRegistered(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	broker := cred.NewBroker()
	id := seedTestID(0xE1)
	cap := &captureSeeder{}

	_, err := SeedGuestAgentForProfiles(
		context.Background(), broker, id, cap.fn(),
		cred.MustProfileByName(cred.ClaudeCodeProfileName),
		[]cred.AgentProfile{cred.MustProfileByName(cred.CursorAgentProfileName)},
	)
	if err != nil {
		t.Fatalf("SeedGuestAgentForProfiles: %v", err)
	}

	if _, ok := broker.Placeholder(id, AnthropicAPIHost); !ok {
		t.Errorf("primary placeholder not registered for %s", AnthropicAPIHost)
	}
	cursorHost := cred.MustProfileByName(cred.CursorAgentProfileName).CredentialedHost
	if _, ok := broker.Placeholder(id, cursorHost); !ok {
		t.Errorf("extra agent placeholder not registered for %s (MUTATION B1: extras loop removed)", cursorHost)
	}
}

func TestSeedGuestAgentForProfiles_OneWrite_BothVarsPresent(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	broker := cred.NewBroker()
	id := seedTestID(0xE2)
	cap := &captureSeeder{}

	_, err := SeedGuestAgentForProfiles(
		context.Background(), broker, id, cap.fn(),
		cred.MustProfileByName(cred.ClaudeCodeProfileName),
		[]cred.AgentProfile{cred.MustProfileByName(cred.CursorAgentProfileName)},
	)
	if err != nil {
		t.Fatalf("SeedGuestAgentForProfiles: %v", err)
	}

	if cap.calls != 1 {
		t.Errorf("seeder called %d times, want exactly 1 (overwrite prevention)", cap.calls)
	}
	payload := cap.payload

	if !bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("primary var CLAUDE_CODE_OAUTH_TOKEN must be in payload (broker placeholder path)\n%s", payload)
	}
	// Mutation guard: NODE_EXTRA_CA_CERTS proves primary agent seeding path ran.
	if !bytes.Contains(payload, []byte("NODE_EXTRA_CA_CERTS=")) {
		t.Errorf("primary agent NODE_EXTRA_CA_CERTS absent from payload (MUTATION: primary seed not called)\n%s", payload)
	}
	if !bytes.Contains(payload, []byte("CURSOR_AUTH_TOKEN=")) {
		t.Errorf("extra var CURSOR_AUTH_TOKEN absent from payload (MUTATION B2: extra payload not appended)\n%s", payload)
	}
}

func TestSeedGuestAgentForProfiles_PlaceholderInvariant(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	broker := cred.NewBroker()
	id := seedTestID(0xE3)
	cap := &captureSeeder{}

	_, err := SeedGuestAgentForProfiles(
		context.Background(), broker, id, cap.fn(),
		cred.MustProfileByName(cred.ClaudeCodeProfileName),
		[]cred.AgentProfile{cred.MustProfileByName(cred.CursorAgentProfileName)},
	)
	if err != nil {
		t.Fatalf("SeedGuestAgentForProfiles: %v", err)
	}

	cursorHost := cred.MustProfileByName(cred.CursorAgentProfileName).CredentialedHost
	ph, hasPh := broker.Placeholder(id, cursorHost)
	if !hasPh {
		t.Fatalf("no placeholder for %s after seeding", cursorHost)
	}

	if !bytes.Contains(cap.payload, []byte(ph)) {
		t.Errorf("guest payload does not contain extra agent placeholder %q (placeholder invariant violated)", ph)
	}

	const realToken = "real-cursor-token-not-in-guest"
	if err := broker.SetRealToken(id, cursorHost, realToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}
	if bytes.Contains(cap.payload, []byte(realToken)) {
		t.Errorf("guest payload contains real token %q (PLACEHOLDER INVARIANT BROKEN)", realToken)
	}
	if ph == realToken {
		t.Errorf("placeholder equals real token — broker did not generate a distinct value")
	}
}

func TestSeedGuestAgentForProfiles_NilExtrasIdenticalToSingleProfile(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	id := seedTestID(0xE4)

	brokerA := cred.NewBroker()
	capA := &captureSeeder{}
	if _, err := SeedGuestAgentForProfile(context.Background(), brokerA, id, capA.fn(), cred.MustProfileByName(cred.ClaudeCodeProfileName)); err != nil {
		t.Fatalf("SeedGuestAgentForProfile: %v", err)
	}

	brokerB := cred.NewBroker()
	capB := &captureSeeder{}
	if _, err := SeedGuestAgentForProfiles(context.Background(), brokerB, id, capB.fn(), cred.MustProfileByName(cred.ClaudeCodeProfileName), nil); err != nil {
		t.Fatalf("SeedGuestAgentForProfiles(nil extras): %v", err)
	}

	for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN=", "NODE_EXTRA_CA_CERTS="} {
		if bytes.Contains(capA.payload, []byte(key)) != bytes.Contains(capB.payload, []byte(key)) {
			t.Errorf("key %q presence differs: single-profile=%v multi-nil=%v (MUTATION B4)",
				key, bytes.Contains(capA.payload, []byte(key)), bytes.Contains(capB.payload, []byte(key)))
		}
	}
	if bytes.Contains(capB.payload, []byte("CURSOR_AUTH_TOKEN=")) {
		t.Errorf("nil extras injected CURSOR_AUTH_TOKEN into payload (regression)")
	}
}

func TestExtraAgentNamesFromProfilesRoundTrip(t *testing.T) {
	t.Parallel()

	profiles := []cred.AgentProfile{cred.MustProfileByName(cred.ClaudeCodeProfileName), cred.MustProfileByName(cred.CursorAgentProfileName)}
	names := extraAgentNamesFromProfiles(profiles)

	if len(names) != len(profiles) {
		t.Fatalf("got %d names, want %d", len(names), len(profiles))
	}
	for i, p := range profiles {
		if names[i] != p.Name {
			t.Errorf("names[%d] = %q, want %q (MUTATION B5: Name not extracted)", i, names[i], p.Name)
		}
		if _, ok := cred.ProfileByName(names[i]); !ok {
			t.Errorf("ProfileByName(%q) not found — name does not round-trip", names[i])
		}
	}

	if got := extraAgentNamesFromProfiles(nil); got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
}
