package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/config"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestHerdrWorktreeSandboxCreateArgs verifies the args produced by herdrWorktreeSandboxCreateArgs.
func TestHerdrWorktreeSandboxCreateArgs(t *testing.T) {
	t.Run("no secrets no allowedRepo", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil, nil, "", nil, false)
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--secret") {
			t.Errorf("unexpected --secret in args: %v", args)
		}
		if strings.Contains(joined, "--repo") {
			t.Errorf("unexpected --repo in args: %v", args)
		}
		if strings.Contains(joined, "--no-builtin-gh") {
			t.Errorf("--no-builtin-gh must not be present: %v", args)
		}
	})

	t.Run("one GitHub secret plus allowedRepo", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil,
			[]string{"GH_TOKEN@github.com"}, "owner/repo", nil, false)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--secret GH_TOKEN@github.com") {
			t.Errorf("expected --secret GH_TOKEN@github.com in args: %v", args)
		}
		if !strings.Contains(joined, "--repo owner/repo") {
			t.Errorf("expected --repo owner/repo in args: %v", args)
		}
		if strings.Contains(joined, "--no-builtin-gh") {
			t.Errorf("--no-builtin-gh must not be present: %v", args)
		}
	})

	t.Run("GitLab secret no allowedRepo", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil,
			[]string{"GITLAB_TOKEN@gitlab.com"}, "", nil, false)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--secret GITLAB_TOKEN@gitlab.com") {
			t.Errorf("expected --secret GITLAB_TOKEN@gitlab.com in args: %v", args)
		}
		if strings.Contains(joined, "--repo") {
			t.Errorf("unexpected --repo in args: %v", args)
		}
		if strings.Contains(joined, "--no-builtin-gh") {
			t.Errorf("--no-builtin-gh must not be present: %v", args)
		}
	})

	t.Run("--file imageFlag produces docker disk flag", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("myhandle", "src:dst", "--file", "/some/dir", nil, nil, "", nil, false)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, herdrDockerDiskVolumeName("myhandle")) {
			t.Errorf("expected docker disk (--mount-named …-docker) for --file path: %v", args)
		}
		if strings.Contains(joined, "--no-builtin-gh") {
			t.Errorf("--no-builtin-gh must not be present: %v", args)
		}
	})

	t.Run("--image imageFlag does not produce docker disk flag", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("myhandle", "src:dst", "--image", "ref", nil, nil, "", nil, false)
		joined := strings.Join(args, " ")
		if strings.Contains(joined, herdrDockerDiskVolumeName("myhandle")) {
			t.Errorf("unexpected docker disk (--mount-named …-docker) for --image path: %v", args)
		}
	})
}

// TestBuildWorktreeEgressArgs verifies egress arg derivation from config.Config.
func TestBuildWorktreeEgressArgs(t *testing.T) {
	t.Run("a: non-GitHub secret, no policy needed", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Egress.Secrets = config.EgressSecrets{
			{Env: "GITLAB_TOKEN", Hosts: []string{"gitlab.com"}},
		}
		secrets, allowedRepo, _, err := buildWorktreeEgressArgs(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(secrets) != 1 || secrets[0] != "GITLAB_TOKEN@gitlab.com" {
			t.Errorf("got secrets=%v, want [GITLAB_TOKEN@gitlab.com]", secrets)
		}
		if allowedRepo != "" {
			t.Errorf("got allowedRepo=%q, want empty", allowedRepo)
		}
	})

	t.Run("b: self-hosted secret, non-GitHub, no policy needed", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Egress.Secrets = config.EgressSecrets{
			{Env: "MYTOKEN", Hosts: []string{"git.corp.example.com"}},
		}
		secrets, _, _, err := buildWorktreeEgressArgs(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(secrets) != 1 || secrets[0] != "MYTOKEN@git.corp.example.com" {
			t.Errorf("got secrets=%v, want [MYTOKEN@git.corp.example.com]", secrets)
		}
	})

	t.Run("c: GitHub secret with generic paths policy — allowed", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Egress.Policy = config.EgressPolicies{
			{Host: "api.github.com", Paths: []string{"/repos/owner/myrepo/**", "/repos/owner/myrepo", "/user"}},
		}
		cfg.Egress.Secrets = config.EgressSecrets{
			{Env: "GH_TOKEN", Hosts: []string{"api.github.com"}},
		}
		secrets, allowedRepo, pp, err := buildWorktreeEgressArgs(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(secrets) != 1 || secrets[0] != "GH_TOKEN@api.github.com" {
			t.Errorf("got secrets=%v", secrets)
		}
		if allowedRepo != "" {
			t.Errorf("got allowedRepo=%q, want empty (generic paths policy, not CLI --repo)", allowedRepo)
		}
		if pp == nil || pp[""] == nil {
			t.Errorf("expected non-nil PathPolicies, got %v", pp)
		} else if pol, ok := pp[""]["api.github.com"]; !ok || len(pol.Paths) == 0 {
			t.Errorf("expected Paths policy for api.github.com, got %v", pp)
		}
	})

	t.Run("d: GitHub secret with NO policy — D-PDE-16 error", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Egress.Secrets = config.EgressSecrets{
			{Env: "GH_TOKEN", Hosts: []string{"github.com"}},
		}
		_, _, _, err := buildWorktreeEgressArgs(cfg)
		if err == nil {
			t.Fatal("expected D-PDE-16 error for GitHub secret without any policy, got nil")
		}
		if !strings.Contains(err.Error(), "D-PDE-16") && !strings.Contains(err.Error(), "refusing create") {
			t.Errorf("error should mention D-PDE-16 or refusing create: %v", err)
		}
	})

	t.Run("e: non-API secret host plus generic paths policy", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Egress.Policy = config.EgressPolicies{
			{Host: "api.example.com", Paths: []string{"GET /v4/projects/123/**"}},
		}
		cfg.Egress.Secrets = config.EgressSecrets{
			{Env: "API_TOKEN", Hosts: []string{"api.example.com"}},
		}
		secrets, allowedRepo, pp, err := buildWorktreeEgressArgs(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(secrets) != 1 || secrets[0] != "API_TOKEN@api.example.com" {
			t.Errorf("got secrets=%v", secrets)
		}
		if allowedRepo != "" {
			t.Errorf("got allowedRepo=%q, want empty", allowedRepo)
		}
		if pp == nil || pp[""]["api.example.com"].Paths == nil {
			t.Errorf("expected Paths policy for api.example.com, got %v", pp)
		}
	})

	t.Run("f: empty config — no secrets, no policy", func(t *testing.T) {
		cfg := config.Config{}
		secrets, allowedRepo, pp, err := buildWorktreeEgressArgs(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(secrets) != 0 {
			t.Errorf("expected no secrets, got %v", secrets)
		}
		if allowedRepo != "" {
			t.Errorf("expected empty allowedRepo, got %q", allowedRepo)
		}
		if pp != nil {
			t.Errorf("expected nil pathPolicies, got %v", pp)
		}
	})
}

// egressFixtureRepo builds a real git repo with a linked worktree on a
// feature branch and returns (mainRepo, worktreeDir). mainContent, when
// non-empty, is committed as .nexus/config.yaml on the default branch;
// worktreeContent, when non-empty, is committed on the feature branch only;
// when empty and mainContent is set, the feature branch removes the file.
func egressFixtureRepo(t *testing.T, mainContent, worktreeContent string) (string, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real git repo test in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()
	mainRepo := filepath.Join(tmp, "main")
	worktreeDir := filepath.Join(tmp, "worktree")
	gitExec := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	writeCfg := func(dir, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".nexus"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, config.ConfigRelPath), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		gitExec(dir, "add", config.ConfigRelPath)
	}
	bareRepo := filepath.Join(tmp, "origin.git")
	gitExec(tmp, "init", "--bare", bareRepo)
	gitExec(tmp, "init", mainRepo)
	gitExec(mainRepo, "config", "user.email", "test@test.com")
	gitExec(mainRepo, "config", "user.name", "Test User")
	if mainContent != "" {
		writeCfg(mainRepo, mainContent)
	} else {
		if err := os.WriteFile(filepath.Join(mainRepo, "README"), []byte("x\n"), 0600); err != nil {
			t.Fatal(err)
		}
		gitExec(mainRepo, "add", "README")
	}
	gitExec(mainRepo, "commit", "-m", "initial commit")
	gitExec(mainRepo, "remote", "add", "origin", bareRepo)
	gitExec(mainRepo, "push", "origin", "HEAD:main")
	gitExec(mainRepo, "fetch", "origin")
	gitExec(mainRepo, "remote", "set-head", "origin", "main")
	gitExec(mainRepo, "worktree", "add", worktreeDir, "-b", "my-feature")
	gitExec(worktreeDir, "config", "user.email", "test@test.com")
	gitExec(worktreeDir, "config", "user.name", "Test User")
	switch {
	case worktreeContent != "":
		writeCfg(worktreeDir, worktreeContent)
		gitExec(worktreeDir, "commit", "-m", "config on feature branch")
	case mainContent != "":
		gitExec(worktreeDir, "rm", "-q", config.ConfigRelPath)
		gitExec(worktreeDir, "commit", "-m", "drop config on feature branch")
	}
	return mainRepo, worktreeDir
}

// runWorktreeSandboxCapturingEgress drives the production herdrWorktreeSandbox
// against worktreeDir and returns what reached createFn.
func runWorktreeSandboxCapturingEgress(t *testing.T, worktreeDir string) (out string, imageFlag, imageVal string, secrets []string, nested bool, err error) {
	t.Helper()
	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-egress", "w-src", "my-feature", worktreeDir),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })
	t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })
	var called bool
	create := func(_ context.Context, _, _, flag, val string, _ []string, s []string, _ string, _ domain.EgressPathPolicies, n bool) error {
		called, imageFlag, imageVal, secrets, nested = true, flag, val, s, n
		return nil
	}
	var w strings.Builder
	err = herdrWorktreeSandbox(context.Background(), "w-egress", &w, root, false, false, false, false, create, stubSandboxGet(domain.Sandbox{}, nil))
	if err == nil && !called {
		t.Fatal("createFn was never invoked")
	}
	return w.String(), imageFlag, imageVal, secrets, nested, err
}

const egressTestCfgWithSecret = "version: 1\nsandbox:\n  nested: true\negress:\n  policy:\n    - host: github.com\n      paths: [\"/repos/origin/repo/**\", \"/user\"]\n  secrets:\n    - env: GH_TOKEN\n      hosts:\n        - github.com\n"

// TestHerdrWorktreeSandbox_EgressFromCheckout proves D-12: the worktree
// checkout's .nexus/config.yaml is what grants egress and nested — even when
// the default branch has no such file.
func TestHerdrWorktreeSandbox_EgressFromCheckout(t *testing.T) {
	_, worktreeDir := egressFixtureRepo(t, "", egressTestCfgWithSecret)
	out, imageFlag, imageVal, secrets, nested, err := runWorktreeSandboxCapturingEgress(t, worktreeDir)
	if err != nil {
		t.Fatalf("herdrWorktreeSandbox: %v\n%s", err, out)
	}
	if imageFlag != "--file" || imageVal != worktreeDir {
		t.Errorf("image = %s %s, want --file %s", imageFlag, imageVal, worktreeDir)
	}
	if len(secrets) != 1 || secrets[0] != "GH_TOKEN@github.com" {
		t.Errorf("secrets = %v, want [GH_TOKEN@github.com] from the checkout config\n%s", secrets, out)
	}
	if !nested {
		t.Errorf("nested = false, want true from the checkout config\n%s", out)
	}
	want := "egress policy from " + filepath.Join(worktreeDir, config.ConfigRelPath)
	if !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

// TestHerdrWorktreeSandbox_MainOnlyConfigDoesNotGrant is the inverse: a
// .nexus/config.yaml present only on the default branch grants nothing to a
// worktree whose checkout lacks the file.
func TestHerdrWorktreeSandbox_MainOnlyConfigDoesNotGrant(t *testing.T) {
	_, worktreeDir := egressFixtureRepo(t, egressTestCfgWithSecret, "")
	out, imageFlag, imageVal, secrets, nested, err := runWorktreeSandboxCapturingEgress(t, worktreeDir)
	if err != nil {
		t.Fatalf("herdrWorktreeSandbox: %v\n%s", err, out)
	}
	if imageFlag != "--image" || imageVal != herdrDefaultImage {
		t.Errorf("image = %s %s, want --image %s", imageFlag, imageVal, herdrDefaultImage)
	}
	if len(secrets) != 0 {
		t.Errorf("secrets = %v, want none (main-only config must not grant)\n%s", secrets, out)
	}
	if nested {
		t.Errorf("nested = true, want false (main-only config must not grant)\n%s", out)
	}
	if want := config.ConfigRelPath + " absent in checkout; no egress policy or nested opt-in"; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

// TestHerdrWorktreeSandbox_MalformedCheckoutConfigIsError: a malformed file in
// the checkout is an error in explicit mode, not a silent no-grant.
func TestHerdrWorktreeSandbox_MalformedCheckoutConfigIsError(t *testing.T) {
	_, worktreeDir := egressFixtureRepo(t, "", "version: 1\negress: [not a map\n")
	out, _, _, _, _, err := runWorktreeSandboxCapturingEgress(t, worktreeDir)
	if err == nil {
		t.Fatalf("expected error for malformed checkout config, got nil\n%s", out)
	}
	if !strings.Contains(err.Error(), "load checkout config") {
		t.Errorf("error = %v, want load checkout config", err)
	}
}

// TestHerdrWorktreeSandboxCreateArgs_PathPolicies verifies that non-empty
// pathPolicies produces --egress-policy-json in the args and that empty/nil
// pathPolicies omits it entirely.
func TestHerdrWorktreeSandboxCreateArgs_PathPolicies(t *testing.T) {
	pp := domain.EgressPathPolicies{
		"": {"api.github.com": domain.EgressHostPolicy{Paths: []string{"GET /repos/**"}}},
	}

	t.Run("non-empty pathPolicies emits --egress-policy-json", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil, nil, "", pp, false)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--egress-policy-json") {
			t.Fatalf("expected --egress-policy-json in args: %v", args)
		}
		var jsonVal string
		for i, a := range args {
			if a == "--egress-policy-json" && i+1 < len(args) {
				jsonVal = args[i+1]
				break
			}
		}
		if jsonVal == "" {
			t.Fatal("--egress-policy-json flag present but has no value")
		}
		var decoded domain.EgressPathPolicies
		if err := json.Unmarshal([]byte(jsonVal), &decoded); err != nil {
			t.Fatalf("JSON decode of --egress-policy-json value: %v", err)
		}
		if !reflect.DeepEqual(pp, decoded) {
			t.Errorf("round-trip mismatch:\n  want %#v\n   got %#v", pp, decoded)
		}
	})

	t.Run("nil pathPolicies omits --egress-policy-json", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil, nil, "", nil, false)
		for _, a := range args {
			if a == "--egress-policy-json" {
				t.Errorf("unexpected --egress-policy-json in args with nil pathPolicies: %v", args)
			}
		}
	})

	t.Run("empty pathPolicies omits --egress-policy-json", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil, nil, "", domain.EgressPathPolicies{}, false)
		for _, a := range args {
			if a == "--egress-policy-json" {
				t.Errorf("unexpected --egress-policy-json in args with empty pathPolicies: %v", args)
			}
		}
	})
}

func TestHerdrWorktreeSandboxCreateArgs_AgentCfgDisk(t *testing.T) {
	const handle = "myrepo/mybranch"
	wantVolName := herdrAgentCfgDiskVolumeName(handle)
	wantMount := "/var/lib/nexus3/agentcfg"

	for _, imageFlag := range []string{"--image", "--file"} {
		imageFlag := imageFlag
		t.Run("agentcfg disk present for "+imageFlag, func(t *testing.T) {
			args := herdrWorktreeSandboxCreateArgs(handle, "src:dst", imageFlag, "/some/val", nil, nil, "", nil, false)
			joined := strings.Join(args, " ")

			if !strings.Contains(joined, wantVolName) {
				t.Errorf("agentcfg volume name %q missing from args;\n"+
					"removing this disk means the overlay upper dir lands on root ext4\n"+
					"which is not governor-visible and cannot grow (D-RAM-08);\ngot args: %v",
					wantVolName, args)
			}
			if !strings.Contains(joined, wantVolName+":"+wantMount) {
				t.Errorf("agentcfg volume not mounted at %q;\n"+
					"the supervisor constants agentCfgUpperDir/agentCfgWorkDir are rooted\n"+
					"under %q — a different mount target breaks the overlay;\ngot args: %v",
					wantMount, wantMount, args)
			}
		})
	}
}

func TestHerdrWorktreeSandboxCreateArgs_EgressOpenComposesWithPolicy(t *testing.T) {
	cfg, err := config.Parse([]byte(egressTestCfgWithSecret))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	secrets, allowedRepo, pp, err := buildWorktreeEgressArgs(cfg)
	if err != nil {
		t.Fatalf("buildWorktreeEgressArgs: %v", err)
	}
	if len(pp) == 0 {
		t.Fatalf("buildWorktreeEgressArgs produced no path policies from %q", egressTestCfgWithSecret)
	}

	args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil, secrets, allowedRepo, pp, false)

	var sawOpen, sawPolicy bool
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--egress" && args[i+1] == "open" {
			sawOpen = true
		}
		if args[i] == "--egress-policy-json" {
			sawPolicy = true
		}
	}
	if !sawOpen || !sawPolicy {
		t.Errorf("args must carry both --egress open (%v) and --egress-policy-json (%v): %v", sawOpen, sawPolicy, args)
	}

	f, perr := parseSandboxCreateArgs(args)
	if perr != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", perr)
	}
	if !f.egressExplicit || f.egressClosed {
		t.Errorf("egressExplicit=%v egressClosed=%v, want explicit open", f.egressExplicit, f.egressClosed)
	}
	if !reflect.DeepEqual(f.pathPolicies, pp) {
		t.Errorf("--egress-policy-json did not survive parse:\n  want %#v\n   got %#v", pp, f.pathPolicies)
	}

	binds, serr := resolveCreateSecrets(context.Background(), f)
	if serr != nil {
		t.Fatalf("resolveCreateSecrets: GitHub secret must be bound by the config policy: %v", serr)
	}
	if len(binds) != 1 || binds[0].Env != "GH_TOKEN" || len(binds[0].Hosts) != 1 || binds[0].Hosts[0] != "github.com" {
		t.Errorf("binds = %+v, want [GH_TOKEN@github.com]", binds)
	}

	_, _, openEgress := resolveAgentPosture(f)
	if !openEgress {
		t.Errorf("openEgress = false; --egress open must yield OpenEgress=true (public egress open) even when --egress-policy-json is present")
	}
	if _, ok := f.pathPolicies[""]["github.com"]; !ok {
		t.Errorf("wildcard policy for github.com missing after resolution: %#v", f.pathPolicies)
	}
}
