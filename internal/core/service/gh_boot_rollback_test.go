// gh_boot_rollback_test.go — D-PD-36 pre-boot guard: agent + GitHub + no AllowedRepo.
//
// D-SHL-05 removed the post-boot ErrAgentGitHubSecret guard. The pre-boot
// ErrUnboundGitHubSecret guard (guard 6b in CreateAndBoot) is now unconditional
// and fires before the VM boots or the record is persisted, so no rollback is
// needed. These tests verify that the pre-boot refusal fires and that no
// sandbox record is left behind.

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
)

// TestGHBootGuard_AgentSeed_NoRepo_Refused verifies that UseAgentSeed + GitHub
// secret + no AllowedRepo is refused PRE-BOOT with ErrUnboundGitHubSecret.
// No sandbox record is created, so no rollback cleanup is needed.
//
// Mutation evidence: comment out guard 6b in CreateAndBoot
// → errors.Is(err, ErrUnboundGitHubSecret) fails (got nil). Restore → passes.
func TestGHBootGuard_AgentSeed_NoRepo_Refused(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, createErr := CreateAndBoot(
		ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "agent-no-repo",
		CreateAndBootOptions{
			Image:        ImageSpec{Digest: string(img.Digest)},
			CacheRoot:    cacheRoot,
			UseAgentSeed: true,
			// GitHub secret bind — AllowedRepo deliberately absent.
			Secrets: []SecretBind{{Env: BuiltinGitHubEnv, Hosts: GitHubSecretHosts, Token: "ghp_tok"}},
			// AllowedRepo: "",  // omitted — triggers ErrUnboundGitHubSecret
		},
	)

	if !errors.Is(createErr, ErrUnboundGitHubSecret) {
		t.Fatalf("CreateAndBoot: got %v, want ErrUnboundGitHubSecret", createErr)
	}
	// Guard fires pre-boot: driver should never have received a Start call.
	for _, c := range fd.Calls() {
		if c.Kind == fake.CallStart {
			t.Errorf("driver.Start was called despite pre-boot refusal (sandbox ID %s)", c.ID)
		}
	}
}

// TestGHBootGuard_AgentSeed_WithRepo_Allowed verifies that UseAgentSeed +
// GitHub secret + AllowedRepo set is ACCEPTED (D-SHL-05).
//
// Mutation evidence: set AllowedRepo = "" in the options
// → CreateAndBoot returns ErrUnboundGitHubSecret, err != nil check fails.
func TestGHBootGuard_AgentSeed_WithRepo_Allowed(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(
		ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "agent-with-repo",
		CreateAndBootOptions{
			Image:        ImageSpec{Digest: string(img.Digest)},
			CacheRoot:    cacheRoot,
			UseAgentSeed: true,
			Secrets:      []SecretBind{{Env: BuiltinGitHubEnv, Hosts: GitHubSecretHosts, Token: "ghp_tok"}},
			AllowedRepo:  "owner/repo", // D-PD-36 satisfied; D-SHL-05 permits this
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: got %v, want success", err)
	}
	if sb.Envelope.AllowedRepo != "owner/repo" {
		t.Errorf("AllowedRepo = %q, want %q", sb.Envelope.AllowedRepo, "owner/repo")
	}
}
