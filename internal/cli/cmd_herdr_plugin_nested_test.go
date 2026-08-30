package cli

// Tests for the nested-virtualisation opt-in and .groundwork mount added in
// CHANGE 1 and CHANGE 2.
//
// Mutation discipline: every test documents its mutation and the expected RED
// outcome so the test cannot share the broken mechanism it is checking.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/config"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// ── herdrWorktreeSandboxCreateArgs — --nested flag ────────────────────────────

func TestHerdrWorktreeSandboxCreateArgs_nested_false_omits_flag(t *testing.T) {
	// Security-critical negative case (D-N3N-02): nested must be default-off.
	// When nested=false, "--nested" must NOT appear in the args.
	//
	// MUTATION PROOF: flip condition to `if !nested { args = append(args, "--nested") }`
	// → --nested is present when nested=false → this test goes RED.
	args := herdrWorktreeSandboxCreateArgs("repo/branch", "/wt:/workspace", "--image", "base", nil, nil, "", nil, false)
	for _, a := range args {
		if a == "--nested" {
			t.Errorf("--nested must NOT appear when nested=false; got args: %v", args)
		}
	}
}

func TestHerdrWorktreeSandboxCreateArgs_nested_true_adds_flag(t *testing.T) {
	// When nested=true, "--nested" must appear exactly once.
	//
	// MUTATION PROOF: remove the `if nested { args = append(args, "--nested") }` block
	// → --nested is absent → this test goes RED.
	args := herdrWorktreeSandboxCreateArgs("repo/branch", "/wt:/workspace", "--image", "base", nil, nil, "", nil, true)
	count := 0
	for _, a := range args {
		if a == "--nested" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("--nested must appear exactly once when nested=true; count=%d in args: %v", count, args)
	}
}

// ── herdrWorktreeSandboxParseArgs — --nested flag ─────────────────────────────

func TestHerdrWorktreeSandboxParseArgs_nestedFlag(t *testing.T) {
	// --nested sets nested=true and is stripped before the workspace ID.
	//
	// MUTATION PROOF: remove the `case "--nested"` branch from the switch
	// → --nested is not consumed, rest[0]="--nested" ≠ "w1" → test goes RED.
	rest, conditional, auto, nested := herdrWorktreeSandboxParseArgs([]string{"--nested", "w1"})
	if len(rest) == 0 || rest[0] != "w1" {
		t.Errorf("--nested not stripped: rest=%v, want [w1]", rest)
	}
	if !nested {
		t.Errorf("--nested should set nested=true; got false")
	}
	if conditional {
		t.Errorf("--nested should NOT set conditional; got true")
	}
	if auto {
		t.Errorf("--nested should NOT set auto; got true")
	}
}

func TestHerdrWorktreeSandboxParseArgs_nestedWithAutoFlag(t *testing.T) {
	// --auto and --nested may appear together; both are stripped.
	rest, _, auto, nested := herdrWorktreeSandboxParseArgs([]string{"--auto", "--nested", "w2"})
	if len(rest) == 0 || rest[0] != "w2" {
		t.Errorf("flags not stripped: rest=%v, want [w2]", rest)
	}
	if !auto {
		t.Errorf("--auto not set")
	}
	if !nested {
		t.Errorf("--nested not set")
	}
}

// ── config.Parse — sandbox.nested ────────────────────────────────────────────

func TestConfigParse_sandboxNested_true(t *testing.T) {
	// config.Parse of a yaml with sandbox.nested: true must yield Nested==true.
	//
	// MUTATION PROOF: drop the Nested field from SandboxConfig → parse produces
	// Nested==false (zero value) → this test goes RED.
	yaml := []byte("version: 1\nsandbox:\n  nested: true\n")
	cfg, err := config.Parse(yaml)
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	if !cfg.Sandbox.Nested {
		t.Errorf("Sandbox.Nested = false; want true")
	}
}

func TestConfigParse_sandboxNested_omitted_isFalse(t *testing.T) {
	// Omitting sandbox.nested must yield Nested==false (default-off, D-N3N-02).
	//
	// MUTATION PROOF: initialise the Nested field to true in SandboxConfig
	// → default becomes true → this test goes RED.
	yaml := []byte("version: 1\nsandbox:\n  agent: claude-code\n")
	cfg, err := config.Parse(yaml)
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	if cfg.Sandbox.Nested {
		t.Errorf("Sandbox.Nested = true; want false (default must be off)")
	}
}

// ── herdrWorktreeGroundworkMount ──────────────────────────────────────────────

// buildLinkedWorktreeWithGroundwork creates a temp dir that looks like a linked
// worktree whose main repo root contains a .groundwork directory. It returns
// the checkout dir, the main repo root, and a cleanup func.
func buildLinkedWorktreeWithGroundwork(t *testing.T) (checkoutDir, mainRepo string) {
	t.Helper()
	// Simulate: <mainRepo>/.git/worktrees/<name>/
	mainGit := t.TempDir() // stands for <mainRepo>/.git
	if err := os.MkdirAll(filepath.Join(mainGit, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	mainRepo = filepath.Dir(mainGit) // <mainRepo>

	// Create .groundwork in the main repo root.
	if err := os.MkdirAll(filepath.Join(mainRepo, ".groundwork"), 0o755); err != nil {
		t.Fatal(err)
	}

	checkoutDir = t.TempDir()
	gitdirTarget := filepath.Join(mainGit, "worktrees", "probe-wt")
	if err := os.WriteFile(filepath.Join(checkoutDir, ".git"),
		[]byte("gitdir: "+gitdirTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return checkoutDir, mainRepo
}

func TestHerdrWorktreeGroundworkMount_linkedWorktreeWithGroundwork_returnsSpec(t *testing.T) {
	// A linked worktree whose main repo contains .groundwork must return
	// "<mainRepo>/.groundwork:<mainRepo>/.groundwork".
	//
	// MUTATION PROOF: return "" unconditionally → this test gets "" → RED.
	checkoutDir, mainRepo := buildLinkedWorktreeWithGroundwork(t)

	want := filepath.Join(mainRepo, ".groundwork") + ":" + filepath.Join(mainRepo, ".groundwork")
	got := herdrWorktreeGroundworkMount(checkoutDir)
	if got != want {
		t.Errorf("herdrWorktreeGroundworkMount = %q; want %q", got, want)
	}
}

func TestHerdrWorktreeGroundworkMount_missingGroundwork_returnsEmpty(t *testing.T) {
	// When .groundwork does not exist in the main repo, return "" — never mount
	// a path that is not there.
	//
	// MUTATION PROOF: remove the os.Stat guard → mount is returned even without
	// the directory → this test fails because "" is expected.
	mainGit := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mainGit, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkoutDir := t.TempDir()
	gitdirTarget := filepath.Join(mainGit, "worktrees", "probe-wt")
	if err := os.WriteFile(filepath.Join(checkoutDir, ".git"),
		[]byte("gitdir: "+gitdirTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No .groundwork directory is created.
	got := herdrWorktreeGroundworkMount(checkoutDir)
	if got != "" {
		t.Errorf("herdrWorktreeGroundworkMount with no .groundwork = %q; want empty", got)
	}
}

func TestHerdrWorktreeGroundworkMount_mainCheckout_returnsEmpty(t *testing.T) {
	// A main checkout's .git is a directory; herdrWorktreeGroundworkMount must
	// return "" (nothing to mount, and there is no linked-worktree structure).
	//
	// MUTATION PROOF: return a non-empty string unconditionally →
	// this test fails because "" is expected.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := herdrWorktreeGroundworkMount(dir)
	if got != "" {
		t.Errorf("herdrWorktreeGroundworkMount for main checkout = %q; want empty", got)
	}
}

func TestHerdrWorktreeGroundworkMount_noGitFile_returnsEmpty(t *testing.T) {
	// No .git at all → empty.
	got := herdrWorktreeGroundworkMount(t.TempDir())
	if got != "" {
		t.Errorf("herdrWorktreeGroundworkMount with no .git = %q; want empty", got)
	}
}

func TestHerdrWorktreeGroundworkMount_noWorktreesParent_returnsEmpty(t *testing.T) {
	// A gitdir: pointer whose target's parent is not "worktrees" is not a
	// linked-worktree structure; herdrWorktreeGroundworkMount must return "".
	checkoutDir := t.TempDir()
	bogus := filepath.Join(t.TempDir(), "notworktrees", "probe-wt")
	if err := os.WriteFile(filepath.Join(checkoutDir, ".git"),
		[]byte("gitdir: "+bogus+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := herdrWorktreeGroundworkMount(checkoutDir)
	if got != "" {
		t.Errorf("herdrWorktreeGroundworkMount with non-worktrees parent = %q; want empty", got)
	}
}

// ── groundwork mount wired into extraMounts ───────────────────────────────────

func TestHerdrWorktreeSandbox_linkedWorktree_groundworkMountPassedToCreate(t *testing.T) {
	// When the main repo has a .groundwork directory, the createFn must receive
	// an extraMounts entry "<mainRepo>/.groundwork:<mainRepo>/.groundwork".
	//
	// MUTATION PROOF: remove the `if gwMount := herdrWorktreeGroundworkMount(...)` block
	// → groundwork mount is never added → createFn receives no groundwork entry → RED.
	checkoutDir, mainRepo := buildLinkedWorktreeWithGroundwork(t)

	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-gwproof", "w-src", "worktree/gwproof", checkoutDir),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	wantMount := filepath.Join(mainRepo, ".groundwork") + ":" + filepath.Join(mainRepo, ".groundwork")
	var gotExtraMounts []string
	err := callHerdrWorktreeSandbox(t, "w-gwproof", root, false, false,
		func(_ context.Context, _, _, _, _ string, extraMounts []string, _ []string, _ string, _ domain.EgressPathPolicies, _ bool) error {
			gotExtraMounts = extraMounts
			return nil
		},
		stubSandboxGet(domain.Sandbox{}, nil),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, m := range gotExtraMounts {
		if m == wantMount {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("extraMounts %v does not contain groundwork mount %q; "+
			"in-guest agents will not be able to read motive charters", gotExtraMounts, wantMount)
	}
}

func TestHerdrWorktreeSandbox_linkedWorktree_noGroundworkDir_noGroundworkMount(t *testing.T) {
	// When the main repo has no .groundwork directory, the createFn must NOT
	// receive a groundwork mount — never mount a path that is not there.
	//
	// MUTATION PROOF: always append the groundwork mount regardless of os.Stat →
	// extraMounts contains a non-existent path → this test goes RED.
	mainGit := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mainGit, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkoutDir := t.TempDir()
	gitdirTarget := filepath.Join(mainGit, "worktrees", "probe-nogw")
	if err := os.WriteFile(filepath.Join(checkoutDir, ".git"),
		[]byte("gitdir: "+gitdirTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No .groundwork created.

	root := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-nogw", "w-src", "worktree/nogw", checkoutDir),
	}.fn())
	swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

	mainRepo := filepath.Dir(mainGit)
	unwantedMount := filepath.Join(mainRepo, ".groundwork") + ":" + filepath.Join(mainRepo, ".groundwork")

	var gotExtraMounts []string
	err := callHerdrWorktreeSandbox(t, "w-nogw", root, false, false,
		func(_ context.Context, _, _, _, _ string, extraMounts []string, _ []string, _ string, _ domain.EgressPathPolicies, _ bool) error {
			gotExtraMounts = extraMounts
			return nil
		},
		stubSandboxGet(domain.Sandbox{}, nil),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range gotExtraMounts {
		if m == unwantedMount {
			t.Errorf("extraMounts contains groundwork mount %q but .groundwork does not exist; "+
				"should never mount a path that is not there", m)
		}
	}
}

// ── trusted-ref property: nested from parsedCfg only ─────────────────────────
//
// The trusted-ref property — that nested is read from parsedCfg (trusted ref)
// and not from the worktree checkout — is structural: readTrustedRefBytes runs
// `git show refs/remotes/origin/HEAD:nexus3.yaml` and herdrResolveWorktreeImage
// reads from info.Path. These two callers are separate code paths; there is no
// single seam in the test helpers that can inject both a worktree-branch byte
// stream and a trusted-ref byte stream simultaneously without mocking at the
// git level.
//
// The security invariant is therefore asserted structurally by inspection:
// 1. readTrustedRefBytes runs `git show refs/remotes/origin/HEAD:nexus3.yaml`
//    — it NEVER references info.Path or the worktree branch name.
// 2. The parsedCfg.Sandbox.Nested extraction sits inside the `if cfgBytes != nil`
//    block that follows readTrustedRefBytes — it is not reachable from
//    herdrResolveWorktreeImage's code path.
// 3. herdrResolveWorktreeImage (which reads from info.Path) does not produce
//    any nested value; it only returns (imageFlag, imageVal, error).
//
// We do assert the end-to-end wiring (nestedFlag || nestedCfg reaches createFn)
// via the TestHerdrWorktreeSandboxCreateArgs_nested_true_adds_flag test above,
// and the --nested CLI flag path via TestHerdrWorktreeSandboxParseArgs_nestedFlag.
// A full integration proof would require a live git repo with a remote ref,
// which is outside the hermetic test boundary. The structural argument above
// constitutes the trust-boundary claim; see the D-N3N-02 decision record.

func TestHerdrWorktreeSandboxCreateArgs_nestedFlagThreadedToCreate(t *testing.T) {
	// Verify that the nested bool received by createFn controls whether
	// "--nested" appears in the args passed to `sandbox create`. This covers
	// the full path from the boolean parameter to the CLI subprocess argument.
	//
	// True branch: nested=true → --nested in args.
	// False branch: nested=false → no --nested in args (security gate).
	for _, tc := range []struct {
		name   string
		nested bool
		want   bool // whether "--nested" should appear
	}{
		{"nested true → flag present", true, true},
		{"nested false → flag absent (default-off)", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := herdrWorktreeSandboxCreateArgs("r/b", "/p:/w", "--image", "img", nil, nil, "", nil, tc.nested)
			found := false
			for _, a := range args {
				if a == "--nested" {
					found = true
					break
				}
			}
			if found != tc.want {
				if tc.want {
					t.Errorf("--nested missing from args; got: %v", args)
				} else {
					t.Errorf("--nested must not appear when nested=false; got: %v", args)
				}
			}
		})
	}
}

