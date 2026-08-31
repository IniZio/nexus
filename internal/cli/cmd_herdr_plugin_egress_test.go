package cli

import (
	"encoding/json"
	"fmt"
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
	withGitRunner := func(t *testing.T, fn func(dir string, args ...string) ([]byte, error)) func() {
		t.Helper()
		old := worktreeGitRunner
		worktreeGitRunner = fn
		return func() { worktreeGitRunner = old }
	}

	t.Run("a: non-GitHub secret, no policy needed", func(t *testing.T) {
		defer withGitRunner(t, func(dir string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("not called")
		})()
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
		defer withGitRunner(t, func(dir string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("not called")
		})()
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
		defer withGitRunner(t, func(dir string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("not called")
		})()
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
		// Config path never sets allowedRepo — that's CLI-only.
		if allowedRepo != "" {
			t.Errorf("got allowedRepo=%q, want empty (generic paths policy, not CLI --repo)", allowedRepo)
		}
		// PathPolicies should have a paths entry for api.github.com.
		if pp == nil || pp[""] == nil {
			t.Errorf("expected non-nil PathPolicies, got %v", pp)
		} else if pol, ok := pp[""]["api.github.com"]; !ok || len(pol.Paths) == 0 {
			t.Errorf("expected Paths policy for api.github.com, got %v", pp)
		}
	})

	t.Run("d: GitHub secret with NO policy — D-PDE-16 error", func(t *testing.T) {
		defer withGitRunner(t, func(dir string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("not called")
		})()
		cfg := config.Config{}
		// No egress.policy entries — GitHub host must be refused.
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
		defer withGitRunner(t, func(dir string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("not called")
		})()
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
		defer withGitRunner(t, func(dir string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("not called")
		})()
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

// TestReadTrustedRefBytes_FailClosed is the adversarial test proving Finding A:
// nexus3.yaml committed on the worktree's local branch does NOT grant access.
// Only the content from refs/remotes/origin/HEAD (origin default branch) is returned.
func TestReadTrustedRefBytes_FailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real git repo test in short mode")
	}

	// Verify git is available.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmp := t.TempDir()
	mainRepo := filepath.Join(tmp, "main")
	bareClone := filepath.Join(tmp, "bare.git")
	worktreeDir := filepath.Join(tmp, "worktree")

	gitExec := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	// 1. Init main repo.
	gitExec(t, tmp, "init", mainRepo)
	gitExec(t, mainRepo, "config", "user.email", "test@test.com")
	gitExec(t, mainRepo, "config", "user.name", "Test User")

	// 2. Commit nexus3.yaml on the default branch.
	originContent := "version: 1\negress:\n  policy:\n    - host: github.com\n      paths: [\"/repos/origin/repo/**\", \"/user\"]\n  secrets:\n    - env: GH_TOKEN\n      hosts:\n        - github.com\n"
	if err := os.WriteFile(filepath.Join(mainRepo, "nexus3.yaml"), []byte(originContent), 0600); err != nil {
		t.Fatal(err)
	}
	gitExec(t, mainRepo, "add", "nexus3.yaml")
	gitExec(t, mainRepo, "commit", "-m", "initial commit")

	// Find the default branch name.
	defaultBranch := "main"
	cmd := exec.Command("git", "-C", mainRepo, "symbolic-ref", "--short", "HEAD")
	if out, err := cmd.Output(); err == nil {
		defaultBranch = strings.TrimSpace(string(out))
	}

	// 3. Clone as bare to simulate origin.
	gitExec(t, tmp, "clone", "--bare", mainRepo, bareClone)

	// 4. Add bare clone as remote origin.
	gitExec(t, mainRepo, "remote", "add", "origin", bareClone)
	gitExec(t, mainRepo, "fetch", "origin")
	// Set origin/HEAD.
	gitExec(t, mainRepo, "remote", "set-head", "origin", defaultBranch)

	// 5. Create a linked worktree on a feature branch.
	gitExec(t, mainRepo, "worktree", "add", worktreeDir, "-b", "my-feature")

	// 6. In the worktree: modify nexus3.yaml with EXTRA secrets and commit.
	featureContent := originContent + "    - env: EVIL_TOKEN\n      hosts:\n        - evil.example.com\n"
	if err := os.WriteFile(filepath.Join(worktreeDir, "nexus3.yaml"), []byte(featureContent), 0600); err != nil {
		t.Fatal(err)
	}
	gitExec(t, worktreeDir, "add", "nexus3.yaml")
	gitExec(t, worktreeDir, "config", "user.email", "test@test.com")
	gitExec(t, worktreeDir, "config", "user.name", "Test User")
	gitExec(t, worktreeDir, "commit", "-m", "add evil token on feature branch")

	// 7. Resolve commonGitDir from the worktree.
	commonGitDir := worktreeCommonGitDir(worktreeDir)
	if commonGitDir == "" {
		t.Fatal("worktreeCommonGitDir returned empty; linked worktree not set up correctly")
	}
	expectedGitDir := filepath.Join(mainRepo, ".git")
	if commonGitDir != expectedGitDir {
		t.Fatalf("commonGitDir=%q, want %q", commonGitDir, expectedGitDir)
	}

	// 8. Call readTrustedRefBytes.
	data, err := readTrustedRefBytes(commonGitDir)
	if err != nil {
		t.Fatalf("readTrustedRefBytes returned error: %v", err)
	}
	if data == nil {
		t.Fatal("readTrustedRefBytes returned nil; expected origin/HEAD content")
	}

	// 9. Verify: must NOT contain the feature-branch EVIL_TOKEN.
	if strings.Contains(string(data), "EVIL_TOKEN") {
		t.Errorf("readTrustedRefBytes returned feature-branch content (EVIL_TOKEN present); "+
			"trusted-ref guard FAILED\ngot:\n%s", data)
	}

	// 10. Verify: must contain the origin content (GH_TOKEN from origin branch).
	if !strings.Contains(string(data), "GH_TOKEN") {
		t.Errorf("readTrustedRefBytes did not return origin content (GH_TOKEN missing)\ngot:\n%s", data)
	}

	// 11. Parse and verify it decodes cleanly.
	parsed, parseErr := config.Parse(data)
	if parseErr != nil {
		t.Fatalf("config.Parse of trusted ref bytes failed: %v", parseErr)
	}
	if len(parsed.Egress.Secrets) == 0 {
		t.Error("parsed config has no egress secrets; expected at least one from origin branch")
	}

	// 12. Verify fail-closed: remove origin/HEAD and expect (nil, nil).
	gitExec(t, mainRepo, "remote", "set-head", "origin", "--delete")
	dataNoHead, errNoHead := readTrustedRefBytes(commonGitDir)
	if errNoHead != nil {
		t.Fatalf("readTrustedRefBytes with no origin/HEAD returned error: %v", errNoHead)
	}
	if dataNoHead != nil {
		t.Errorf("readTrustedRefBytes with no origin/HEAD should return nil (fail closed), got data")
	}
}

// TestReadTrustedRefBytes_NoOriginHead verifies fail-closed when symbolic-ref fails.
func TestReadTrustedRefBytes_NoOriginHead(t *testing.T) {
	old := worktreeGitRunner
	defer func() { worktreeGitRunner = old }()
	worktreeGitRunner = func(dir string, args ...string) ([]byte, error) {
		if args[0] == "symbolic-ref" {
			return nil, fmt.Errorf("fatal: ref refs/remotes/origin/HEAD is not a symbolic ref")
		}
		return nil, fmt.Errorf("unexpected call: %v", args)
	}

	data, err := readTrustedRefBytes("/fake/git")
	if err != nil {
		t.Fatalf("expected nil error (fail closed), got %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data (fail closed), got %v", data)
	}
}

// TestReadTrustedRefBytes_FileAbsent verifies fail-closed when nexus3.yaml is absent on the trusted ref.
func TestReadTrustedRefBytes_FileAbsent(t *testing.T) {
	old := worktreeGitRunner
	defer func() { worktreeGitRunner = old }()
	worktreeGitRunner = func(dir string, args ...string) ([]byte, error) {
		if args[0] == "symbolic-ref" {
			return []byte("refs/remotes/origin/main\n"), nil
		}
		if args[0] == "show" {
			return nil, fmt.Errorf("fatal: Path 'nexus3.yaml' does not exist in 'refs/remotes/origin/main'")
		}
		return nil, fmt.Errorf("unexpected call: %v", args)
	}

	data, err := readTrustedRefBytes("/fake/git")
	if err != nil {
		t.Fatalf("expected nil error (fail closed), got %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data (fail closed), got %v", data)
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

// TestHerdrWorktreeSandboxCreateArgs_AgentCfgDisk is the mutation guard for
// the D-RAM-08 Option B decision: the agentcfg overlay volume must appear
// unconditionally in the sandbox create args so that both the overlayfs upper
// and work dirs live on a governor-visible disk rather than root ext4.
//
// Mutations that MUST turn this RED:
//   - Remove the "--mount-named …-agentcfg:/var/lib/nexus3/agentcfg…" line
//   - Change the mount path to something other than /var/lib/nexus3/agentcfg
//   - Gate the flag behind "--file" (it must be unconditional — unlike the
//     docker disk, the agentcfg volume is needed for every agent sandbox)
func TestHerdrWorktreeSandboxCreateArgs_AgentCfgDisk(t *testing.T) {
	const handle = "myrepo/mybranch"
	wantVolName := herdrAgentCfgDiskVolumeName(handle)
	wantMount := "/var/lib/nexus3/agentcfg"

	for _, imageFlag := range []string{"--image", "--file"} {
		imageFlag := imageFlag
		t.Run("agentcfg disk present for "+imageFlag, func(t *testing.T) {
			args := herdrWorktreeSandboxCreateArgs(handle, "src:dst", imageFlag, "/some/val", nil, nil, "", nil, false)
			joined := strings.Join(args, " ")

			// Volume name must appear in a --mount-named value.
			if !strings.Contains(joined, wantVolName) {
				t.Errorf("agentcfg volume name %q missing from args;\n"+
					"removing this disk means the overlay upper dir lands on root ext4\n"+
					"which is not governor-visible and cannot grow (D-RAM-08);\ngot args: %v",
					wantVolName, args)
			}

			// The mount target must be the named volume mount point that
			// agentCfgUpperDir and agentCfgWorkDir are rooted under.
			if !strings.Contains(joined, wantVolName+":"+wantMount) {
				t.Errorf("agentcfg volume not mounted at %q;\n"+
					"the supervisor constants agentCfgUpperDir/agentCfgWorkDir are rooted\n"+
					"under %q — a different mount target breaks the overlay;\ngot args: %v",
					wantMount, wantMount, args)
			}
		})
	}
}
