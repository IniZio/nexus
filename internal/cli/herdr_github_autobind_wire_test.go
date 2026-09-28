package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/vault"
)

// runWorktreeSandboxCapturingAutoBind drives herdrWorktreeSandbox against
// worktreeDir and returns the secrets and pathPolicies that reached createFn.
func runWorktreeSandboxCapturingAutoBind(t *testing.T, worktreeDir string) (secrets []string, pathPolicies domain.EgressPathPolicies, out string, err error) {
	t.Helper()
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-autobind", "w-src", "my-feature", worktreeDir),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })
	var called bool
	create := func(_ context.Context, _, _, _, _ string, _ []string, s []string, _ string, pp domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
		called, secrets, pathPolicies = true, s, pp
		return nil
	}
	var w strings.Builder
	err = herdrWorktreeSandbox(context.Background(), "w-autobind", &w, root, false, false, false, false, create, stubSandboxGet(domain.Sandbox{}, nil))
	if err == nil && !called {
		t.Fatal("createFn was never invoked")
	}
	return secrets, pathPolicies, w.String(), err
}

// TestHerdrWorktreeSandbox_GitHubAutoBind_Linked: no config, github origin,
// linked=true → auto-bind injects secret and path policies.
func TestHerdrWorktreeSandbox_GitHubAutoBind_Linked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real git repo test in short mode")
	}
	_, worktreeDir := egressFixtureRepo(t, "", "")
	t.Setenv(vault.PrincipalEnv, "slack:T1:U1")
	old := herdrOriginURLFn
	herdrOriginURLFn = func(_ context.Context, _ string) string { return "git@github.com:acme/widget.git" }
	t.Cleanup(func() { herdrOriginURLFn = old })
	oldLinked := herdrGitHubLinkedFn
	herdrGitHubLinkedFn = func(_, _ string) (bool, error) { return true, nil }
	t.Cleanup(func() { herdrGitHubLinkedFn = oldLinked })

	secrets, pp, out, err := runWorktreeSandboxCapturingAutoBind(t, worktreeDir)
	if err != nil {
		t.Fatalf("herdrWorktreeSandbox: %v\n%s", err, out)
	}
	if len(secrets) != 1 || secrets[0] != herdrGitHubAutoBindSecret {
		t.Errorf("secrets = %v, want [%s]\n%s", secrets, herdrGitHubAutoBindSecret, out)
	}
	wildcard, ok := pp[""]
	if !ok {
		t.Fatalf("pathPolicies missing wildcard key \"\"; got %v\n%s", pp, out)
	}
	for _, host := range []string{"github.com", "api.github.com", "uploads.github.com"} {
		if _, hasHost := wildcard[host]; !hasHost {
			t.Errorf("pathPolicies[\"\"] missing host %q; got %v\n%s", host, wildcard, out)
		}
	}
}

// TestHerdrWorktreeSandbox_GitHubAutoBind_Unlinked: linked=false → no bind.
func TestHerdrWorktreeSandbox_GitHubAutoBind_Unlinked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real git repo test in short mode")
	}
	_, worktreeDir := egressFixtureRepo(t, "", "")
	t.Setenv(vault.PrincipalEnv, "slack:T1:U1")
	old := herdrOriginURLFn
	herdrOriginURLFn = func(_ context.Context, _ string) string { return "git@github.com:acme/widget.git" }
	t.Cleanup(func() { herdrOriginURLFn = old })
	oldLinked := herdrGitHubLinkedFn
	herdrGitHubLinkedFn = func(_, _ string) (bool, error) { return false, nil }
	t.Cleanup(func() { herdrGitHubLinkedFn = oldLinked })

	secrets, pp, out, err := runWorktreeSandboxCapturingAutoBind(t, worktreeDir)
	if err != nil {
		t.Fatalf("herdrWorktreeSandbox: %v\n%s", err, out)
	}
	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none (unlinked)\n%s", secrets, out)
	}
	if len(pp[""]["github.com"].Paths) != 0 {
		t.Errorf("pathPolicies should have no github.com paths (unlinked); got %v\n%s", pp, out)
	}
}

// TestHerdrWorktreeSandbox_GitHubAutoBind_NonGitHubOrigin: non-github origin → no bind.
func TestHerdrWorktreeSandbox_GitHubAutoBind_NonGitHubOrigin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real git repo test in short mode")
	}
	_, worktreeDir := egressFixtureRepo(t, "", "")
	t.Setenv(vault.PrincipalEnv, "slack:T1:U1")
	old := herdrOriginURLFn
	herdrOriginURLFn = func(_ context.Context, _ string) string { return "git@gitlab.com:acme/widget.git" }
	t.Cleanup(func() { herdrOriginURLFn = old })
	oldLinked := herdrGitHubLinkedFn
	herdrGitHubLinkedFn = func(_, _ string) (bool, error) { return true, nil }
	t.Cleanup(func() { herdrGitHubLinkedFn = oldLinked })

	secrets, pp, out, err := runWorktreeSandboxCapturingAutoBind(t, worktreeDir)
	if err != nil {
		t.Fatalf("herdrWorktreeSandbox: %v\n%s", err, out)
	}
	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none (non-github origin)\n%s", secrets, out)
	}
	if _, ok := pp[""]["github.com"]; ok {
		t.Errorf("pathPolicies should have no github.com entry (non-github origin); got %v\n%s", pp, out)
	}
}

// TestHerdrWorktreeSandbox_GitHubAutoBind_RepoConfigWins: when checkout config
// already declares a GitHub secret/policy, herdrApplyGitHubAutoBind is a no-op
// and herdrGitHubLinkedFn is NOT called.
func TestHerdrWorktreeSandbox_GitHubAutoBind_RepoConfigWins(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real git repo test in short mode")
	}
	_, worktreeDir := egressFixtureRepo(t, "", egressTestCfgWithSecret)
	t.Setenv(vault.PrincipalEnv, "slack:T1:U1")
	old := herdrOriginURLFn
	herdrOriginURLFn = func(_ context.Context, _ string) string { return "git@github.com:acme/widget.git" }
	t.Cleanup(func() { herdrOriginURLFn = old })
	linkedCalled := false
	oldLinked := herdrGitHubLinkedFn
	herdrGitHubLinkedFn = func(_, _ string) (bool, error) {
		linkedCalled = true
		return true, nil
	}
	t.Cleanup(func() { herdrGitHubLinkedFn = oldLinked })

	secrets, pp, out, err := runWorktreeSandboxCapturingAutoBind(t, worktreeDir)
	if err != nil {
		t.Fatalf("herdrWorktreeSandbox: %v\n%s", err, out)
	}
	// Config declares GH_TOKEN@github.com exactly; no uploads/api entries added.
	if len(secrets) != 1 || secrets[0] != "GH_TOKEN@github.com" {
		t.Errorf("secrets = %v, want [GH_TOKEN@github.com] from config\n%s", secrets, out)
	}
	if _, ok := pp[""]["api.github.com"]; ok {
		t.Errorf("pathPolicies should not have api.github.com (config wins, no auto-bind): %v\n%s", pp, out)
	}
	if _, ok := pp[""]["uploads.github.com"]; ok {
		t.Errorf("pathPolicies should not have uploads.github.com (config wins, no auto-bind): %v\n%s", pp, out)
	}
	if linkedCalled {
		t.Errorf("herdrGitHubLinkedFn must NOT be called when config already declares GitHub\n%s", out)
	}
}

// TestHerdrWorktreeSandbox_GitHubAutoBind_NoPrincipalEnv: empty NEXUS_PRINCIPAL →
// no bind and linkedFn is NOT called (auto-bind is for controller sandboxes only).
func TestHerdrWorktreeSandbox_GitHubAutoBind_NoPrincipalEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real git repo test in short mode")
	}
	_, worktreeDir := egressFixtureRepo(t, "", "")
	t.Setenv(vault.PrincipalEnv, "") // empty principal = no controller context
	old := herdrOriginURLFn
	herdrOriginURLFn = func(_ context.Context, _ string) string { return "git@github.com:acme/widget.git" }
	t.Cleanup(func() { herdrOriginURLFn = old })
	linkedCalled := false
	oldLinked := herdrGitHubLinkedFn
	herdrGitHubLinkedFn = func(_, _ string) (bool, error) {
		linkedCalled = true
		return true, nil
	}
	t.Cleanup(func() { herdrGitHubLinkedFn = oldLinked })

	secrets, pp, out, err := runWorktreeSandboxCapturingAutoBind(t, worktreeDir)
	if err != nil {
		t.Fatalf("herdrWorktreeSandbox: %v\n%s", err, out)
	}
	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none (no NEXUS_PRINCIPAL)\n%s", secrets, out)
	}
	if _, ok := pp[""]["github.com"]; ok {
		t.Errorf("pathPolicies should have no github.com entry (no principal); got %v\n%s", pp, out)
	}
	if linkedCalled {
		t.Errorf("herdrGitHubLinkedFn must NOT be called when %s is empty\n%s", vault.PrincipalEnv, out)
	}
}

// TestHerdrWorktreeSandbox_GitHubAutoBind_PassesSandboxProject: linkedFn receives
// the correct principal and the project derived from the sandbox handle.
func TestHerdrWorktreeSandbox_GitHubAutoBind_PassesSandboxProject(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real git repo test in short mode")
	}
	_, worktreeDir := egressFixtureRepo(t, "", "")
	t.Setenv(vault.PrincipalEnv, "slack:T1:U1")
	old := herdrOriginURLFn
	herdrOriginURLFn = func(_ context.Context, _ string) string { return "git@github.com:acme/widget.git" }
	t.Cleanup(func() { herdrOriginURLFn = old })

	var capturedPrincipal, capturedProject string
	oldLinked := herdrGitHubLinkedFn
	herdrGitHubLinkedFn = func(principal, project string) (bool, error) {
		capturedPrincipal, capturedProject = principal, project
		return true, nil
	}
	t.Cleanup(func() { herdrGitHubLinkedFn = oldLinked })

	// Inline setup to capture the handle from createFn (first string arg).
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-autobind", "w-src", "my-feature", worktreeDir),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	oldExec := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	t.Cleanup(func() { herdrExecCommandContext = oldExec })

	var capturedHandle string
	var called bool
	create := func(_ context.Context, handle, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
		called, capturedHandle = true, handle
		return nil
	}
	var w strings.Builder
	err := herdrWorktreeSandbox(context.Background(), "w-autobind", &w, root, false, false, false, false, create, stubSandboxGet(domain.Sandbox{}, nil))
	if err == nil && !called {
		t.Fatal("createFn was never invoked")
	}
	if err != nil {
		t.Fatalf("herdrWorktreeSandbox: %v\n%s", err, w.String())
	}

	wantProject, _, parseErr := domain.ParseHandle(capturedHandle)
	if parseErr != nil {
		t.Fatalf("domain.ParseHandle(%q) error: %v", capturedHandle, parseErr)
	}
	if wantProject == "" {
		t.Fatalf("wantProject is empty for handle %q — handle must be <project>/<name>", capturedHandle)
	}
	if capturedProject != wantProject {
		t.Errorf("linkedFn project = %q, want %q (first segment of handle %q)", capturedProject, wantProject, capturedHandle)
	}
	if capturedPrincipal != "slack:T1:U1" {
		t.Errorf("linkedFn principal = %q, want %q", capturedPrincipal, "slack:T1:U1")
	}
}
