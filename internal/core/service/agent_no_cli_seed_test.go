package service

// Naming an agent must NOT request a CLI-side credential seed.
//
// `sandbox create --agent` records the agent and lets the detached supervisor
// seed the guest, because the supervisor re-boots the VM and the seed lives
// under /run (tmpfs) — anything minted before the handoff is discarded by it,
// leaving the guest and the proxy disagreeing about which placeholder to swap.
//
// The GitHub-secret refusal (D-PD-23) DOES apply to both kinds of agent
// sandbox, which is why the two conditions look similar and must not be merged:
// an earlier version of this file gated the whole seeding block on
// "UseAgentSeed OR a named profile" and so seeded on the --agent path too.

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

func TestCreateAndBoot_NamedAgent_DoesNotSeedFromTheCLI(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	var seeded bool
	seeder := GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
		seeded = true
		return nil
	})

	svc := newTestSvc(t, fake.New())
	opts := CreateAndBootOptions{
		Image:     ImageSpec{Digest: string(img.Digest)},
		CacheRoot: cacheRoot,
		DiskDir:   t.TempDir(),
		// The --agent shape: a named profile, a broker and seeder available,
		// but no request for the CLI-side seed.
		AgentProfile: cred.ClaudeCodeProfile,
		Broker:       cred.NewBroker(),
		Seeder:       seeder,
	}

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "named-agent", opts)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.AgentName != cred.ClaudeCodeProfileName {
		t.Fatalf("AgentName = %q, want %q", sb.AgentName, cred.ClaudeCodeProfileName)
	}
	if seeded {
		t.Error("naming an agent seeded the guest from the CLI; the supervisor's reboot would discard it and mint a conflicting placeholder")
	}
}

// D-SHL-05: an agent sandbox named by profile (--agent) WITH AllowedRepo set
// must now SUCCEED — the MITM proxy scopes the real token to that one repo.
// The old ErrAgentGitHubSecret refusal is gone; AllowedRepo is the sole guard.
func TestCreateAndBoot_NamedAgent_GitHubSecretWithRepo_Allowed(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	svc := newTestSvc(t, fake.New())
	opts := CreateAndBootOptions{
		Image:        ImageSpec{Digest: string(img.Digest)},
		CacheRoot:    cacheRoot,
		DiskDir:      t.TempDir(),
		AgentProfile: cred.ClaudeCodeProfile,
		Secrets:      []SecretBind{{Env: "GH_TOKEN", Hosts: []string{"api.github.com"}, Token: "ghp_test"}},
		AllowedRepo:  "owner/name", // D-PD-36 satisfied; D-SHL-05 permits this combination
	}

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "agent-gh", opts)
	if err != nil {
		t.Fatalf("CreateAndBoot: got error %v, want success (agent+GitHub+AllowedRepo must be accepted per D-SHL-05)", err)
	}
	if sb.Envelope.AllowedRepo != "owner/name" {
		t.Errorf("AllowedRepo = %q, want %q", sb.Envelope.AllowedRepo, "owner/name")
	}
}
