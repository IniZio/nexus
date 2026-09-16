package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ── exec seam fake ────────────────────────────────────────────────────────────

// nexus3Bin is the sentinel the recorder uses for calls that went through
// runHostCLI (the nexus3 host binary), as opposed to the herdr binary path.
const nexus3Bin = "<nexus3>"

type hostCall struct {
	bin  string
	args []string
}

type hostCLIRecorder struct {
	t      *testing.T
	calls  []hostCall
	canned map[string]string
}

func installHostCLIRecorder(t *testing.T, canned map[string]string) *hostCLIRecorder {
	t.Helper()
	rec := &hostCLIRecorder{t: t, canned: canned}
	record := func(bin string, args []string) (string, error) {
		rec.calls = append(rec.calls, hostCall{bin: bin, args: append([]string(nil), args...)})
		if len(args) < 2 {
			rec.t.Errorf("host CLI called with fewer than two args: bin=%q args=%q", bin, args)
			return "", fmt.Errorf("unexpected host call %q %q", bin, args)
		}
		key := args[0] + " " + args[1]
		out, ok := rec.canned[key]
		if !ok {
			rec.t.Errorf("host CLI: no canned output for %q (bin=%q args=%q)", key, bin, args)
			return "", fmt.Errorf("unexpected host call %q %q", bin, args)
		}
		return out, nil
	}
	origHost, origHerdr := runHostCLI, runHerdrCLI
	// runHostCLI resolves the nexus3 binary itself; record it under the
	// nexus3Bin sentinel so the ordered-argv assertions can tell nexus3 calls
	// from herdr calls without depending on os.Executable.
	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		return record(nexus3Bin, argv)
	}
	runHerdrCLI = func(_ context.Context, herdrBin string, argv ...string) (string, error) {
		return record(herdrBin, argv)
	}
	t.Cleanup(func() { runHostCLI, runHerdrCLI = origHost, origHerdr })
	return rec
}

func (r *hostCLIRecorder) find(verb string) (hostCall, bool) {
	for _, c := range r.calls {
		if len(c.args) >= 2 && c.args[0]+" "+c.args[1] == verb {
			return c, true
		}
	}
	return hostCall{}, false
}

func happyCanned(repoPath, branch string) map[string]string {
	return map[string]string{
		"workspace list": fmt.Sprintf(
			`{"result":{"workspaces":[{"workspace_id":"wPARENT","worktree":{"checkout_path":%q}}]}}`, repoPath),
		"worktree create":        `{"ws":"wNEW","linked":true}` + "\n",
		"herdr worktree-sandbox": "sandbox bound\n",
		"herdr list":             "label=x\tworkspace_id=wNEW\thandle=repo/branch\tsandbox_id=sb-1\tpane_id=\n",
		"worktree list": fmt.Sprintf(
			`{"result":{"worktrees":[{"branch":%q,"path":"/wt/path","is_linked_worktree":true}]}}`, branch),
	}
}

func argsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsToken(args []string, tok string) bool {
	for _, a := range args {
		if a == tok {
			return true
		}
	}
	return false
}

func runDelegateCreate(t *testing.T, args map[string]any) (*hostCLIRecorder, map[string]any, string, bool) {
	t.Helper()
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	repoPath, _ := args["repo_path"].(string)
	branch, _ := args["branch"].(string)
	rec := installHostCLIRecorder(t, happyCanned(repoPath, branch))

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_worktree_create", args)
	text := resultText(t, res)
	if res.IsError {
		return rec, nil, text, true
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("data unmarshal: %v (text=%q)", err, text)
	}
	return rec, data, text, false
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestDelegateWorktreeCreate_TakesHerdrWorktreeSandboxPath(t *testing.T) {
	repo := t.TempDir()
	rec, data, text, isErr := runDelegateCreate(t, map[string]any{
		"repo_path": repo,
		"branch":    "feat/x",
	})
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}

	want := []hostCall{
		{bin: "/fake/herdr", args: []string{"workspace", "list"}},
		{bin: "/fake/herdr", args: []string{"worktree", "create", "--workspace", "wPARENT", "--branch", "feat/x", "--no-focus"}},
		{bin: nexus3Bin, args: []string{"herdr", "worktree-sandbox", "wNEW"}},
		{bin: nexus3Bin, args: []string{"herdr", "list"}},
		{bin: "/fake/herdr", args: []string{"worktree", "list", "--json"}},
	}
	if len(rec.calls) != len(want) {
		t.Fatalf("call count = %d, want %d; calls=%+v", len(rec.calls), len(want), rec.calls)
	}
	for i := range want {
		got := rec.calls[i]
		if got.bin != want[i].bin || !argsEqual(got.args, want[i].args) {
			t.Errorf("call[%d] = %q %q, want %q %q", i, got.bin, got.args, want[i].bin, want[i].args)
		}
	}
	if rec.calls[2].bin != rec.calls[3].bin {
		t.Errorf("nexus3 calls used different bins: %q vs %q", rec.calls[2].bin, rec.calls[3].bin)
	}

	wantData := map[string]string{
		"workspace_id":  "wNEW",
		"worktree_path": "/wt/path",
		"branch":        "feat/x",
		"handle":        "repo/branch",
		"sandbox_id":    "sb-1",
	}
	for k, v := range wantData {
		if got, _ := data[k].(string); got != v {
			t.Errorf("data[%q] = %q, want %q (data=%v)", k, got, v, data)
		}
	}
	if _, ok := data["output"]; !ok {
		t.Errorf("data missing %q field: %v", "output", data)
	}
}

// Same verb as the herdr hook (plugins/herdr/bin/pane.sh); the hook may run
// `--auto` (fail-open auto-bind), the tool binds explicitly and never passes it.
func TestDelegateWorktreeCreate_VerbMatchesHerdrHook(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	paneSh := filepath.Join(filepath.Dir(thisFile), "..", "..", "plugins", "herdr", "bin", "pane.sh")
	hook, err := os.ReadFile(paneSh)
	if err != nil {
		t.Fatalf("read herdr hook %s: %v", paneSh, err)
	}
	if !strings.Contains(string(hook), "herdr worktree-sandbox") {
		t.Fatalf("%s does not shell `herdr worktree-sandbox`; the hook and the tool have diverged", paneSh)
	}

	repo := t.TempDir()
	rec, _, text, isErr := runDelegateCreate(t, map[string]any{
		"repo_path": repo,
		"branch":    "feat/x",
	})
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}
	call, found := rec.find("herdr worktree-sandbox")
	if !found {
		t.Fatalf("no `herdr worktree-sandbox` call recorded; calls=%+v", rec.calls)
	}
	if !argsEqual(call.args[:2], []string{"herdr", "worktree-sandbox"}) {
		t.Errorf("nexus3 call args[0:2] = %q, want [herdr worktree-sandbox]", call.args[:2])
	}
	for i, c := range rec.calls {
		if containsToken(c.args, "--auto") {
			t.Errorf("call[%d] passes --auto (fail-open auto mode): %q %q", i, c.bin, c.args)
		}
	}
}

// --egress-policy-json derivation from .nexus/config.yaml is the verb's job,
// proven in internal/cli/cmd_herdr_plugin_egress_test.go.
func TestDelegateWorktreeCreate_NoOpenEgress_EvenWithPolicyConfig(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy := "egress:\n  policy:\n    - host: api.github.com\n      paths: [\"GET /repos/**\"]\n"
	if err := os.WriteFile(filepath.Join(repo, ".nexus", "config.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, _, text, isErr := runDelegateCreate(t, map[string]any{
		"repo_path": repo,
		"branch":    "feat/x",
	})
	if isErr {
		t.Fatalf("tool returned error: %s", text)
	}
	if len(rec.calls) == 0 {
		t.Fatal("no host calls recorded")
	}
	for i, c := range rec.calls {
		for j, a := range c.args {
			if a == "--egress" {
				t.Errorf("call[%d] contains --egress at position %d: %q", i, j, c.args)
			}
			if a == "open" && j > 0 && c.args[j-1] == "--egress" {
				t.Errorf("call[%d] contains `--egress open`: %q", i, c.args)
			}
		}
		if len(c.args) >= 2 && c.args[0] == "sandbox" && c.args[1] == "create" {
			t.Errorf("call[%d] is a hand-built `sandbox create`: %q %q", i, c.bin, c.args)
		}
	}
}

func TestDelegateWorktreeCreate_BaseFlagOnlyWhenGiven(t *testing.T) {
	t.Run("with base", func(t *testing.T) {
		repo := t.TempDir()
		rec, _, text, isErr := runDelegateCreate(t, map[string]any{
			"repo_path": repo,
			"branch":    "feat/x",
			"base":      "develop",
		})
		if isErr {
			t.Fatalf("tool returned error: %s", text)
		}
		call, found := rec.find("worktree create")
		if !found {
			t.Fatalf("no `worktree create` call; calls=%+v", rec.calls)
		}
		want := []string{"worktree", "create", "--workspace", "wPARENT", "--branch", "feat/x", "--base", "develop", "--no-focus"}
		if !argsEqual(call.args, want) {
			t.Errorf("create args = %q, want %q", call.args, want)
		}
	})
	t.Run("without base", func(t *testing.T) {
		repo := t.TempDir()
		rec, _, text, isErr := runDelegateCreate(t, map[string]any{
			"repo_path": repo,
			"branch":    "feat/x",
		})
		if isErr {
			t.Fatalf("tool returned error: %s", text)
		}
		call, found := rec.find("worktree create")
		if !found {
			t.Fatalf("no `worktree create` call; calls=%+v", rec.calls)
		}
		if containsToken(call.args, "--base") {
			t.Errorf("create args contain --base without base given: %q", call.args)
		}
		want := []string{"worktree", "create", "--workspace", "wPARENT", "--branch", "feat/x", "--no-focus"}
		if !argsEqual(call.args, want) {
			t.Errorf("create args = %q, want %q", call.args, want)
		}
	})
}

func TestDelegateWorktreeCreate_RejectsUnsupportedArgs(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"image_ref", map[string]any{"image_ref": "ghcr.io/x/y:1"}, "image_ref is not supported"},
		{"memory_mib", map[string]any{"memory_mib": 2048}, "not supported by the herdr worktree-sandbox path"},
		{"vcpus", map[string]any{"vcpus": 2}, "not supported by the herdr worktree-sandbox path"},
		{"allowed_branches", map[string]any{"allowed_branches": []string{"main"}}, "allowed_branches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{
				"repo_path": t.TempDir(),
				"branch":    "feat/x",
			}
			for k, v := range tc.extra {
				args[k] = v
			}
			rec, _, text, isErr := runDelegateCreate(t, args)
			if !isErr {
				t.Fatalf("expected IsError result, got success: %s", text)
			}
			if !strings.Contains(text, tc.want) {
				t.Errorf("error text %q does not contain %q", text, tc.want)
			}
			if len(rec.calls) != 0 {
				t.Errorf("runHostCLI called %d times, want 0: %+v", len(rec.calls), rec.calls)
			}
		})
	}
}

func TestDelegateWorktreeCreate_RepoNotOpenInHerdr(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	t.Setenv("HERDR_WORKSPACE_ID", "")
	repo := t.TempDir()
	canned := happyCanned("/somewhere/else", "feat/x")
	rec := installHostCLIRecorder(t, canned)

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_worktree_create", map[string]any{
		"repo_path": repo,
		"branch":    "feat/x",
	})
	text := resultText(t, res)
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %s", text)
	}
	if !strings.Contains(text, "not open as a herdr workspace") {
		t.Errorf("error text %q does not contain %q", text, "not open as a herdr workspace")
	}
	if _, found := rec.find("worktree create"); found {
		t.Errorf("worktree create was attempted despite unresolved workspace: %+v", rec.calls)
	}
	if _, found := rec.find("herdr worktree-sandbox"); found {
		t.Errorf("worktree-sandbox was attempted despite unresolved workspace: %+v", rec.calls)
	}
}

func TestDelegateWorktreeCreate_HerdrMissing(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
	t.Setenv("PATH", t.TempDir())
	rec := installHostCLIRecorder(t, map[string]string{})

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_worktree_create", map[string]any{
		"repo_path": t.TempDir(),
		"branch":    "feat/x",
	})
	text := resultText(t, res)
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %s", text)
	}
	if !strings.Contains(text, "herdr not found") {
		t.Errorf("error text %q does not contain %q", text, "herdr not found")
	}
	if len(rec.calls) != 0 {
		t.Errorf("runHostCLI called %d times, want 0: %+v", len(rec.calls), rec.calls)
	}
}

// TestDelegateAgentDispatch_PrependsStandingOrders drives delegate_agent_dispatch
// through the MCP server with a fake host executor and asserts the brief the
// guest receives starts with the standing orders and ends with the caller's text.
func TestDelegateAgentDispatch_PrependsStandingOrders(t *testing.T) {
	orig := runHostCLI
	t.Cleanup(func() { runHostCLI = orig })
	var got []string
	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		got = append([]string(nil), argv...)
		return "dispatched", nil
	}

	cs, done := connectPair(t, &stubService{})
	defer done()

	res := callTool(t, cs, "delegate_agent_dispatch", map[string]any{"ref": "proj/slice", "brief": "X"})
	if res.IsError {
		t.Fatalf("dispatch returned error: %s", resultText(t, res))
	}
	if len(got) == 0 {
		t.Fatal("host executor was never invoked")
	}
	want := []string{"herdr", "agent", "--autonomous", "--no-focus", "proj/slice"}
	if len(got) != len(want)+1 {
		t.Fatalf("argv = %q, want %d elements", got, len(want)+1)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], w)
		}
	}
	delivered := got[len(got)-1]
	if !strings.HasPrefix(delivered, standingOrders) {
		t.Fatalf("delivered brief does not start with standing orders:\n%s", delivered)
	}
	if !strings.HasSuffix(delivered, "X") {
		t.Fatalf("delivered brief does not end with caller brief:\n%s", delivered)
	}
	if delivered == "X" || !strings.Contains(standingOrders, "isolated nexus3 microVM") {
		t.Fatalf("standing orders missing or empty")
	}
}
