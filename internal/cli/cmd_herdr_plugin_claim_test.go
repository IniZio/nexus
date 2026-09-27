package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/vault"
)

// ── orphan adoption: principal guard ─────────────────────────────────────────

func TestHerdrWorktreeSandbox_AdoptOrphan_MismatchedPrincipal_Refused(t *testing.T) {
	root := t.TempDir()
	const workspaceID = "w-orphan-mismatch"
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto(workspaceID, "feat/orphan",
			"/srv/wt/nexus/feat-orphan", "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	orphan := domain.Sandbox{
		ID:        domain.NewSandboxID(),
		State:     domain.Running,
		Principal: "local:newman",
		LiveMounts: []domain.LiveMount{
			{HostPath: "/srv/wt/nexus/feat-orphan", GuestPath: "/workspace"},
		},
	}
	t.Setenv(vault.PrincipalEnv, "slack:T:U999")

	createCalled := false
	err := callHerdrWorktreeSandbox(t, workspaceID, root, false, false,
		func(_ context.Context, _, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
			createCalled = true
			return errors.New("sandbox create: already exists")
		},
		stubSandboxGet(orphan, nil),
	)
	if err == nil {
		t.Fatal("expected error on orphan principal mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "principal mismatch") {
		t.Errorf("error = %v; want 'principal mismatch'", err)
	}
	// Binding must NOT be written.
	_, lookupErr := HerdrSpaceGetByHandle(context.Background(), root, "nexus/feat-orphan")
	if lookupErr == nil {
		t.Error("binding must not be written when principal mismatches on orphan adopt")
	}
	_ = createCalled
}

func TestHerdrWorktreeSandbox_AdoptOrphan_MatchingPrincipal_Adopted(t *testing.T) {
	root := t.TempDir()
	const workspaceID = "w-orphan-match"
	const wantHandle = "nexus/feat-orphan"
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto(workspaceID, "feat/orphan",
			"/srv/wt/nexus/feat-orphan", "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	orphan := domain.Sandbox{
		ID:        domain.NewSandboxID(),
		State:     domain.Running,
		Principal: "slack:T:U999",
		LiveMounts: []domain.LiveMount{
			{HostPath: "/srv/wt/nexus/feat-orphan", GuestPath: "/workspace"},
		},
	}
	wantSandboxID := orphan.ID.String()
	t.Setenv(vault.PrincipalEnv, "slack:T:U999")

	err := callHerdrWorktreeSandbox(t, workspaceID, root, false, false,
		func(_ context.Context, _, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
			return errors.New("sandbox create: already exists")
		},
		stubSandboxGet(orphan, nil),
	)
	if err != nil {
		t.Fatalf("unexpected error on matching principal orphan adopt: %v", err)
	}
	binding, lookupErr := HerdrSpaceGetByHandle(context.Background(), root, wantHandle)
	if lookupErr != nil {
		t.Fatalf("binding not written after orphan adopt: %v", lookupErr)
	}
	if binding.SandboxID != wantSandboxID {
		t.Errorf("binding.SandboxID = %q; want %q", binding.SandboxID, wantSandboxID)
	}
}

// ── controller claim: absent -> proceeds ─────────────────────────────────────

func TestHerdrWorktreeSandbox_NoClaim_AutoProceeds(t *testing.T) {
	root := t.TempDir()
	// No claim file written — claimsDir may not even exist.
	// Seed a sibling binding so herdrWorktreeAutoBindDecision returns true (repoBound).
	seedBindingWithRepoRoot(t, root, "w-src", "nexus/main", "/srv/repos/nexus")
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto("w-noclaim", "feat/newbranch",
			"/srv/wt/nexus/feat-newbranch", "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	createCalled := false
	err := callHerdrWorktreeSandbox(t, "w-noclaim", root, false, true, /*auto*/
		func(_ context.Context, _, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
			createCalled = true
			return nil
		},
		stubSandboxGet(domain.Sandbox{ID: domain.NewSandboxID()}, nil),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !createCalled {
		t.Error("createFn must be called when no controller claim exists in auto mode")
	}
}
