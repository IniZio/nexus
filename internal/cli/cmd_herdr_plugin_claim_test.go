package cli

import (
	"context"
	"errors"
	"os"
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

// ── effective-principal regression: local human with NEXUS_PRINCIPAL unset ───

// TestHerdrWorktreeSandbox_AdoptOrphan_LocalPrincipal_EnvUnset verifies that a
// sandbox created by create.go (Principal=local:<user>) can be adopted by the
// herdr hook when NEXUS_PRINCIPAL is not set in the environment. This was the
// live regression: create.go wrote "local:newman" but the hook compared raw
// os.Getenv (== "") against the sandbox record → mismatch → refused.
func TestHerdrWorktreeSandbox_AdoptOrphan_LocalPrincipal_EnvUnset(t *testing.T) {
	root := t.TempDir()
	const workspaceID = "w-orphan-local"
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto(workspaceID, "feat/orphan",
			"/srv/wt/nexus/feat-orphan", "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	lp, err := vault.LocalPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	orphan := domain.Sandbox{
		ID:    domain.NewSandboxID(),
		State: domain.Running,
		// create.go records local:<user> when NEXUS_PRINCIPAL is unset.
		Principal:  lp,
		LiveMounts: []domain.LiveMount{{HostPath: "/srv/wt/nexus/feat-orphan", GuestPath: "/workspace"}},
	}
	// Human operator: NEXUS_PRINCIPAL not set.
	os.Unsetenv(vault.PrincipalEnv)
	err = callHerdrWorktreeSandbox(t, workspaceID, root, false, false,
		func(_ context.Context, _, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
			return errors.New("sandbox create: already exists")
		},
		stubSandboxGet(orphan, nil),
	)
	if err != nil {
		t.Fatalf("local adopt with NEXUS_PRINCIPAL unset refused: %v", err)
	}
}

// TestHerdrWorktreeSandbox_ReuseBinding_LegacyEmptyPrincipal verifies that a
// binding written with Principal="" (old prod format) does NOT block reuse when
// the sandbox record holds the correct local principal and NEXUS_PRINCIPAL is
// unset. The binding's Principal field is informational; the record is
// authoritative.
func TestHerdrWorktreeSandbox_ReuseBinding_LegacyEmptyPrincipal(t *testing.T) {
	root := t.TempDir()
	const handle = "nexus/feat-legacy"
	// Old-format binding: Principal="" (written before this fix).
	seedBindingWithPrincipal(t, root, "w-old", handle, "")

	lp, err := vault.LocalPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto("w-new", "feat/legacy",
			"/srv/wt/nexus/feat-legacy", "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	swapHerdrWorkspaceList(t, "w-old", "w-new")
	// Sandbox record has the authoritative principal (set by create.go).
	reuseRecord := domain.Sandbox{ID: domain.NewSandboxID(), Principal: lp}
	os.Unsetenv(vault.PrincipalEnv)

	var w strings.Builder
	err = herdrWorktreeSandbox(context.Background(), "w-new", &w, root, false, false, false, false,
		noopCreate, stubSandboxGet(reuseRecord, nil))
	if err != nil {
		t.Errorf("legacy empty-principal binding must not block reuse: %v\n%s", err, w.String())
	}
}

// TestHerdrWorktreeSandbox_AdoptOrphan_SlackVsLocal_Refused verifies that a
// sandbox owned by a different principal (slack agent vs local human) is still
// refused even after the effective-principal fix.
func TestHerdrWorktreeSandbox_AdoptOrphan_SlackVsLocal_Refused(t *testing.T) {
	root := t.TempDir()
	const workspaceID = "w-orphan-cross"
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto(workspaceID, "feat/cross",
			"/srv/wt/nexus/feat-cross", "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	orphan := domain.Sandbox{
		ID:         domain.NewSandboxID(),
		State:      domain.Running,
		Principal:  "local:newman",
		LiveMounts: []domain.LiveMount{{HostPath: "/srv/wt/nexus/feat-cross", GuestPath: "/workspace"}},
	}
	t.Setenv(vault.PrincipalEnv, "slack:T:U999")
	err := callHerdrWorktreeSandbox(t, workspaceID, root, false, false,
		func(_ context.Context, _, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
			return errors.New("sandbox create: already exists")
		},
		stubSandboxGet(orphan, nil),
	)
	if err == nil {
		t.Fatal("expected principal mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "principal mismatch") {
		t.Errorf("error = %v; want 'principal mismatch'", err)
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
