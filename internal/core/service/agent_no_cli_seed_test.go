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
	"errors"
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

// The GitHub-secret refusal must still reach a sandbox that declared its agent
// by profile rather than by UseAgentSeed (D-PD-23).
func TestCreateAndBoot_NamedAgent_StillRefusesGitHubSecret(t *testing.T) {
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
		Secrets:      []SecretBind{{Env: "GH_TOKEN", Hosts: []string{"api.github.com"}}},
		// AllowedRepo satisfies the earlier D-PD-36 guard, so the failure this
		// test observes is the agent-specific refusal and not the unbound-token
		// one that fires first for any sandbox.
		AllowedRepo: "owner/name",
	}

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "agent-gh", opts)
	if !errors.Is(err, ErrAgentGitHubSecret) {
		t.Fatalf("expected ErrAgentGitHubSecret for an agent sandbox with a GitHub bind, got %v", err)
	}
}
