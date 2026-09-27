package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/vault"
)

func seedBindingWithPrincipal(t *testing.T, storeRoot, workspaceID, sandboxHandle, principal string) {
	t.Helper()
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus:" + sandboxHandle,
		HerdrWorkspaceID: workspaceID,
		SandboxHandle:    sandboxHandle,
		SandboxID:        "sb-seed-principal",
		Principal:        principal,
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, b); err != nil {
		t.Fatalf("seedBindingWithPrincipal: %v", err)
	}
}

func TestHerdrWorktreeSandbox_ReuseRejectsWrongPrincipal(t *testing.T) {
	root := t.TempDir()
	const handle = "nexus/main"
	seedBindingWithPrincipal(t, root, "w-a", handle, "local:newman")

	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto("w-b", "main", "/srv/wt/nexus/main", "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	swapHerdrWorkspaceList(t, "w-a", "w-b")
	t.Setenv(vault.PrincipalEnv, "slack:T:U999")

	// Sandbox record must carry the authoritative principal (create.go sets this).
	reuseRecord := domain.Sandbox{ID: domain.NewSandboxID(), Principal: "local:newman"}
	var w strings.Builder
	err := herdrWorktreeSandbox(context.Background(), "w-b", &w, root, false, false, false, false,
		noopCreate, stubSandboxGet(reuseRecord, nil))
	if err == nil {
		t.Fatal("expected error on principal mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "principal mismatch") {
		t.Errorf("error = %v; want 'principal mismatch'", err)
	}
}

func TestHerdrWorktreeSandbox_ReuseAllowsSamePrincipal(t *testing.T) {
	root := t.TempDir()
	const handle = "nexus/main"
	seedBindingWithPrincipal(t, root, "w-a", handle, "slack:T:U999")

	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto("w-b", "main", "/srv/wt/nexus/main", "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	swapHerdrWorkspaceList(t, "w-a", "w-b")
	t.Setenv(vault.PrincipalEnv, "slack:T:U999")

	// Sandbox record carries authoritative principal; reuse compares against it.
	reuseRecord := domain.Sandbox{ID: domain.NewSandboxID(), Principal: "slack:T:U999"}
	var w strings.Builder
	err := herdrWorktreeSandbox(context.Background(), "w-b", &w, root, false, false, false, false,
		noopCreate, stubSandboxGet(reuseRecord, nil))
	if err != nil {
		t.Errorf("unexpected error when principals match: %v\n%s", err, w.String())
	}
}

func TestHerdrWorktreeSandbox_ControllerClaimedBranchSkipsAuto(t *testing.T) {
	root := t.TempDir()

	claimsDir := herdrControllerClaimsDir(root)
	if err := os.MkdirAll(claimsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	branch := "ctrl/myproj-abc123-ffffff"
	safeBranch := "ctrl-myproj-abc123-ffffff"
	if err := os.WriteFile(filepath.Join(claimsDir, safeBranch), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfoAuto("w-ctrl", branch, "/srv/wt/nexus/"+safeBranch, "/srv/repos/nexus/.git"),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")

	createCalled := false
	create := func(_ context.Context, _, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
		createCalled = true
		return nil
	}
	var w strings.Builder
	err := herdrWorktreeSandbox(context.Background(), "w-ctrl", &w, root, false, false, true /*auto*/, false,
		create, stubSandboxGet(domain.Sandbox{}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, w.String())
	}
	if createCalled {
		t.Error("createFn must not be called for controller-claimed branch in auto mode")
	}
	if !strings.Contains(w.String(), "controller-claimed") {
		t.Errorf("output missing 'controller-claimed'; got: %s", w.String())
	}
}
