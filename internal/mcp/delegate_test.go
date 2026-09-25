package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/herdragent"
	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// init stubs runHostCLI/runHerdrCLI so tests without a recorder don't invoke
// os.Executable() (the test binary), which would recursively re-run the suite.
func init() {
	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		return "", fmt.Errorf("runHostCLI: no recorder installed (call installHostCLIRecorder); argv=%v", argv)
	}
	runHerdrCLI = func(_ context.Context, _ string, argv ...string) (string, error) {
		return "", fmt.Errorf("runHerdrCLI: no recorder installed (call installHostCLIRecorder); argv=%v", argv)
	}
}

// ── exec seam fake ────────────────────────────────────────────────────────────

// nexusBin is the sentinel the recorder uses for calls that went through
// runHostCLI (the nexus host binary), as opposed to the herdr binary path.
const nexusBin = "<nexus>"

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
	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		return record(nexusBin, argv)
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
		{bin: nexusBin, args: []string{"herdr", "worktree-sandbox", "wNEW"}},
		{bin: nexusBin, args: []string{"herdr", "list"}},
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
		t.Errorf("nexus calls used different bins: %q vs %q", rec.calls[2].bin, rec.calls[3].bin)
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
		t.Errorf("nexus call args[0:2] = %q, want [herdr worktree-sandbox]", call.args[:2])
	}
	for i, c := range rec.calls {
		if containsToken(c.args, "--auto") {
			t.Errorf("call[%d] passes --auto (fail-open auto mode): %q %q", i, c.bin, c.args)
		}
	}
}

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

func TestDelegateWorktreeCreate_Herdr090Format(t *testing.T) {
	repo := t.TempDir()
	newFormat := `{"id":"cli:worktree:create","result":{"workspace":{"workspace_id":"wA2","label":"nexus-probe-tmp"},"worktree":{"branch":"feat/x","is_linked_worktree":true,"open_workspace_id":"wA2","path":"/wt/path"}}}` + "\n"
	canned := happyCanned(repo, "feat/x")
	canned["worktree create"] = newFormat
	canned["herdr list"] = "label=x\tworkspace_id=wA2\thandle=repo/branch\tsandbox_id=sb-1\tpane_id=\n"
	canned["worktree list"] = `{"result":{"worktrees":[{"branch":"feat/x","path":"/wt/path","is_linked_worktree":true}]}}`

	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	rec := installHostCLIRecorder(t, canned)

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_worktree_create", map[string]any{
		"repo_path": repo,
		"branch":    "feat/x",
	})
	text := resultText(t, res)
	if res.IsError {
		t.Fatalf("tool returned error: %s", text)
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("data unmarshal: %v", err)
	}
	if got, _ := data["workspace_id"].(string); got != "wA2" {
		t.Errorf("workspace_id = %q, want %q (data=%v)", got, "wA2", data)
	}
	if _, found := rec.find("herdr worktree-sandbox"); !found {
		t.Errorf("herdr worktree-sandbox not called; calls=%+v", rec.calls)
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
	if delivered == "X" || !strings.Contains(standingOrders, "isolated nexus microVM") {
		t.Fatalf("standing orders missing or empty")
	}
}

func runDelegateTeardown(t *testing.T, canned map[string]string, ref string) (*hostCLIRecorder, map[string]any, string, bool) {
	t.Helper()
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	rec := installHostCLIRecorder(t, canned)
	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_teardown", map[string]any{"ref": ref})
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

const teardownBindingLines = "label=x\tworkspace_id=wOTHER\thandle=repo/other\tsandbox_id=sb-9\tpane_id=\n" +
	"label=x\tworkspace_id=wNEW\thandle=repo/branch\tsandbox_id=sb-1\tpane_id=\n"

// Bound ref: teardown reverses delegate_worktree_create through herdr and
// must NOT run `nexus sandbox rm` (the hook already reaped the sandbox).
func TestDelegateTeardown_BoundWorkspace_RemovesViaHerdr(t *testing.T) {
	rec, data, text, isErr := runDelegateTeardown(t, map[string]string{
		"herdr list":      teardownBindingLines,
		"worktree remove": "removed\n",
		"sandbox list":    "HANDLE  STATE  ID\nrepo/other  running  sb-9\n",
	}, "repo/branch")
	if isErr {
		t.Fatalf("teardown errored: %s", text)
	}
	rm, ok := rec.find("worktree remove")
	if !ok {
		t.Fatalf("herdr worktree remove never ran: %+v", rec.calls)
	}
	if rm.bin != "/fake/herdr" {
		t.Errorf("worktree remove ran through %q, want the herdr binary", rm.bin)
	}
	if want := []string{"worktree", "remove", "--workspace", "wNEW"}; !argsEqual(rm.args, want) {
		t.Errorf("worktree remove argv = %q, want %q", rm.args, want)
	}
	if c, found := rec.find("sandbox rm"); found {
		t.Errorf("sandbox rm ran for a bound workspace: %q", c.args)
	}
	if data["workspace_id"] != "wNEW" || data["handle"] != "repo/branch" || data["sandbox_id"] != "sb-1" {
		t.Errorf("result = %v, want workspace_id=wNEW handle=repo/branch sandbox_id=sb-1", data)
	}
	if data["removed"] != true {
		t.Errorf("removed = %v, want true", data["removed"])
	}
}

func TestDelegateTeardown_BoundBySandboxIDPrefix(t *testing.T) {
	rec, _, text, isErr := runDelegateTeardown(t, map[string]string{
		"herdr list":      teardownBindingLines,
		"worktree remove": "removed\n",
		"sandbox list":    "HANDLE  STATE  ID\n",
	}, "sb-1")
	if isErr {
		t.Fatalf("teardown errored: %s", text)
	}
	rm, ok := rec.find("worktree remove")
	if !ok || !containsToken(rm.args, "wNEW") {
		t.Fatalf("worktree remove did not target wNEW: %+v", rec.calls)
	}
	if _, found := rec.find("sandbox rm"); found {
		t.Errorf("sandbox rm ran for a bound workspace: %+v", rec.calls)
	}
}

// Unbound ref: no herdr workspace to close, so only `nexus sandbox rm` runs.
func TestDelegateTeardown_Unbound_FallsBackToSandboxRm(t *testing.T) {
	rec, data, text, isErr := runDelegateTeardown(t, map[string]string{
		"herdr list": teardownBindingLines,
		"sandbox rm": "removed sb-5\n",
	}, "repo/loose")
	if isErr {
		t.Fatalf("teardown errored: %s", text)
	}
	if _, found := rec.find("worktree remove"); found {
		t.Errorf("herdr worktree remove ran with no binding: %+v", rec.calls)
	}
	rm, ok := rec.find("sandbox rm")
	if !ok {
		t.Fatalf("sandbox rm never ran: %+v", rec.calls)
	}
	if want := []string{"sandbox", "rm", "repo/loose"}; !argsEqual(rm.args, want) {
		t.Errorf("sandbox rm argv = %q, want %q", rm.args, want)
	}
	if data["removed"] != true {
		t.Errorf("removed = %v, want true", data["removed"])
	}
}

// Hook did not reap: the sandbox is still listed after the herdr remove, so
// teardown falls back to `nexus sandbox rm` and still reports success.
// connectPairSvc is like connectPair but accepts any SandboxService.
func connectPairSvc(t *testing.T, svc SandboxService) (*gosdk.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := gosdk.NewInMemoryTransports()
	srv := NewServer(svc)
	ss, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := gosdk.NewClient(&gosdk.Implementation{Name: "test-client", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	return cs, func() { cs.Close(); ss.Wait() }
}

type execResponse struct {
	code   int32
	stdout string
	stderr string
	err    error
}

type seqExecService struct {
	*stubService
	responses []execResponse
	idx       int
}

func (s *seqExecService) Exec(_ context.Context, ref string, argv []string, env map[string]string, cwd, stdin string) (int32, string, string, error) {
	if s.idx >= len(s.responses) {
		return 1, "", "no more canned responses", nil
	}
	r := s.responses[s.idx]
	s.idx++
	return r.code, r.stdout, r.stderr, r.err
}

func TestDelegateAgentPoll_MarkerPresent_ReturnsDoneViaMarker(t *testing.T) {
	t.Helper()
	svc := &seqExecService{
		stubService: &stubService{},
		responses: []execResponse{
			{code: 0, stdout: "all tests green, PR opened\n"},
		},
	}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()
	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := data["done_via"]; got != "marker" {
		t.Errorf("done_via = %q, want %q", got, "marker")
	}
	if got := data["marker_content"]; got != "all tests green, PR opened" {
		t.Errorf("marker_content = %q, want trimmed summary", got)
	}
	if _, ok := data["git_log"]; ok {
		t.Errorf("git_log present but should be absent when marker fires")
	}
	if svc.idx != 1 {
		t.Errorf("expected exactly 1 exec call (marker check), got %d", svc.idx)
	}
}

func TestDelegateAgentPoll_MarkerAbsent_FallsBackToGit(t *testing.T) {
	t.Helper()
	svc := &seqExecService{
		stubService: &stubService{},
		responses: []execResponse{
			{code: 1, stderr: "No such file"},
			{code: 0, stdout: "abc1234 fix: thing\n"},
			{code: 0, stdout: ""},
			{code: 0, stdout: "feat/work\n"},
		},
	}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()
	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := data["done_via"]; got != "git" {
		t.Errorf("done_via = %q, want %q", got, "git")
	}
	if got, _ := data["git_log"].(string); !strings.Contains(got, "fix: thing") {
		t.Errorf("git_log = %q, want log output", got)
	}
	if svc.idx != 4 {
		t.Errorf("expected 4 exec calls (marker + 3 git), got %d", svc.idx)
	}
}

func setTeardownPollTiming(t *testing.T, interval, timeout time.Duration) {
	t.Helper()
	origI, origT := teardownPollInterval, teardownPollTimeout
	teardownPollInterval, teardownPollTimeout = interval, timeout
	t.Cleanup(func() { teardownPollInterval, teardownPollTimeout = origI, origT })
}

func TestDelegateTeardown_StillListed_FallsBackToSandboxRm(t *testing.T) {
	setTeardownPollTiming(t, 0, 0)
	rec, data, text, isErr := runDelegateTeardown(t, map[string]string{
		"herdr list":      teardownBindingLines,
		"worktree remove": "removed\n",
		"sandbox list":    "HANDLE  STATE  ID\nrepo/branch  running  sb-1\n",
		"sandbox rm":      "removed sb-1\n",
	}, "repo/branch")
	if isErr {
		t.Fatalf("teardown errored: %s", text)
	}
	if _, ok := rec.find("worktree remove"); !ok {
		t.Fatalf("herdr worktree remove never ran: %+v", rec.calls)
	}
	rm, ok := rec.find("sandbox rm")
	if !ok {
		t.Fatalf("sandbox rm never ran after sandbox stayed listed: %+v", rec.calls)
	}
	if !argsEqual(rm.args, []string{"sandbox", "rm", "repo/branch"}) {
		t.Errorf("sandbox rm argv = %q", rm.args)
	}
	if data["removed"] != true {
		t.Errorf("removed = %v, want true", data["removed"])
	}
}

func TestDelegateTeardown_HerdrRemoveFails_NoSandboxRm(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	rec := installHostCLIRecorder(t, map[string]string{"herdr list": teardownBindingLines})
	origHerdr := runHerdrCLI
	runHerdrCLI = func(_ context.Context, bin string, argv ...string) (string, error) {
		rec.calls = append(rec.calls, hostCall{bin: bin, args: argv})
		return "error: worktree dirty\n", fmt.Errorf("exit 1")
	}
	t.Cleanup(func() { runHerdrCLI = origHerdr })

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_teardown", map[string]any{"ref": "repo/branch"})
	if !res.IsError {
		t.Fatalf("expected error, got success: %s", resultText(t, res))
	}
	if text := resultText(t, res); !strings.Contains(text, "worktree dirty") {
		t.Errorf("error text %q does not surface herdr output", text)
	}
	if _, found := rec.find("sandbox rm"); found {
		t.Errorf("sandbox rm ran after herdr remove failed: %+v", rec.calls)
	}
}

func TestDelegateTeardown_DirtyWorktree_ReturnsActionableError(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "-C", repoDir, "init"},
		{"git", "-C", repoDir, "config", "user.email", "t@t.com"},
		{"git", "-C", repoDir, "config", "user.name", "T"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	untrackedFile := filepath.Join(repoDir, "untracked.txt")
	if err := os.WriteFile(untrackedFile, []byte("pending work"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyJSON := fmt.Sprintf(
		`{"error":{"code":"dirty_worktree_requires_force","message":"fatal: '%s' contains modified or untracked files, use --force to delete it"},"id":"cli:worktree:remove"}`,
		repoDir,
	)
	listJSON := fmt.Sprintf(
		`{"result":{"worktrees":[{"branch":"feat/x","path":%q,"open_workspace_id":"wNEW"}]}}`,
		repoDir,
	)

	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	rec := installHostCLIRecorder(t, map[string]string{"herdr list": teardownBindingLines})
	origHerdr := runHerdrCLI
	runHerdrCLI = func(_ context.Context, bin string, argv ...string) (string, error) {
		rec.calls = append(rec.calls, hostCall{bin: bin, args: argv})
		if len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "remove" {
			return dirtyJSON + "\n", fmt.Errorf("exit status 1")
		}
		if len(argv) >= 2 && argv[0] == "worktree" && argv[1] == "list" {
			return listJSON, nil
		}
		return "", fmt.Errorf("unexpected herdr call: %q", argv)
	}
	t.Cleanup(func() { runHerdrCLI = origHerdr })

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_teardown", map[string]any{"ref": "repo/branch"})
	if !res.IsError {
		t.Fatalf("expected error, got success: %s", resultText(t, res))
	}
	text := resultText(t, res)
	if !strings.Contains(text, "force:true") {
		t.Errorf("error text missing 'force:true': %s", text)
	}
	if !strings.Contains(text, "untracked.txt") {
		t.Errorf("error text missing porcelain line with 'untracked.txt': %s", text)
	}
	if _, found := rec.find("sandbox rm"); found {
		t.Errorf("sandbox rm ran on dirty error: %+v", rec.calls)
	}
}

func TestDelegateTeardown_Force_PassesForceFlag(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	rec := installHostCLIRecorder(t, map[string]string{
		"herdr list":      teardownBindingLines,
		"worktree remove": "removed\n",
		"sandbox list":    "HANDLE  STATE  ID\n",
	})

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_teardown", map[string]any{"ref": "repo/branch", "force": true})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}
	rm, ok := rec.find("worktree remove")
	if !ok {
		t.Fatalf("herdr worktree remove never ran: %+v", rec.calls)
	}
	if !containsToken(rm.args, "--force") {
		t.Errorf("worktree remove argv missing --force: %q", rm.args)
	}
}

func TestDelegateTeardown_PollClearsBeforeTimeout_SuccessWithoutFallback(t *testing.T) {
	setTeardownPollTiming(t, 0, 5*time.Second)
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")

	callCount := 0
	rec := installHostCLIRecorder(t, map[string]string{
		"herdr list":      teardownBindingLines,
		"worktree remove": "removed\n",
	})
	origHost := runHostCLI
	runHostCLI = func(ctx context.Context, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "sandbox" && argv[1] == "list" {
			callCount++
			if callCount >= 2 {
				return "HANDLE  STATE  ID\nrepo/other  running  sb-9\n", nil
			}
			return "HANDLE  STATE  ID\nrepo/branch  running  sb-1\n", nil
		}
		return origHost(ctx, argv...)
	}
	t.Cleanup(func() { runHostCLI = origHost })

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_teardown", map[string]any{"ref": "repo/branch"})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("data unmarshal: %v", err)
	}
	if data["how"] != "removed-by-hook" {
		t.Errorf("how = %v, want removed-by-hook", data["how"])
	}
	if _, found := rec.find("sandbox rm"); found {
		t.Errorf("sandbox rm ran after polling cleared: %+v", rec.calls)
	}
}

func TestDelegateTeardown_FallbackRmNotFound_ReportsSuccess(t *testing.T) {
	setTeardownPollTiming(t, 0, 0)
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	rec := installHostCLIRecorder(t, map[string]string{
		"herdr list":      teardownBindingLines,
		"worktree remove": "removed\n",
		"sandbox list":    "HANDLE  STATE  ID\nrepo/branch  running  sb-1\n",
	})
	origHost := runHostCLI
	runHostCLI = func(ctx context.Context, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "sandbox" && argv[1] == "rm" {
			rec.calls = append(rec.calls, hostCall{bin: "<nexus>", args: append([]string(nil), argv...)})
			return "sandbox not found\n", fmt.Errorf("exit status 1: sandbox not found")
		}
		return origHost(ctx, argv...)
	}
	t.Cleanup(func() { runHostCLI = origHost })

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_teardown", map[string]any{"ref": "repo/branch"})
	if res.IsError {
		t.Fatalf("expected success (not-found = already gone), got error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("data unmarshal: %v", err)
	}
	if data["how"] != "already-gone" {
		t.Errorf("how = %v, want already-gone", data["how"])
	}
	if data["removed"] != true {
		t.Errorf("removed = %v, want true", data["removed"])
	}
}

func TestDelegateAgentDispatch_ClearsMarkerBeforeAgent(t *testing.T) {
	var execArgvs [][]string
	rmCalled := false
	agentCalled := false
	rmBeforeAgent := false

	orig := runHostCLI
	t.Cleanup(func() { runHostCLI = orig })
	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "agent" {
			agentCalled = true
			if rmCalled {
				rmBeforeAgent = true
			}
		}
		return "dispatched", nil
	}

	recSvc := &dispatchRecordSvc{
		stubService: &stubService{},
		onExec: func(argv []string) {
			execArgvs = append(execArgvs, append([]string(nil), argv...))
			rmCalled = true
		},
	}

	cs, closeFn := connectPairSvc(t, recSvc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_dispatch", map[string]any{"ref": "proj/branch", "brief": "do work"})
	if res.IsError {
		t.Fatalf("dispatch returned error: %s", resultText(t, res))
	}
	if len(execArgvs) != 1 {
		t.Fatalf("exec call count = %d, want 1", len(execArgvs))
	}
	want := []string{"rm", "-f", delegateDoneMarker}
	if !argsEqual(execArgvs[0], want) {
		t.Errorf("exec argv = %q, want %q", execArgvs[0], want)
	}
	if !agentCalled {
		t.Fatal("herdr agent call not observed")
	}
	if !rmBeforeAgent {
		t.Error("rm was not called before the agent command")
	}
}

type dispatchRecordSvc struct {
	*stubService
	onExec func([]string)
}

func (s *dispatchRecordSvc) Exec(_ context.Context, _ string, argv []string, _ map[string]string, _, _ string) (int32, string, string, error) {
	if s.onExec != nil {
		s.onExec(argv)
	}
	return 0, "", "", nil
}

func TestDelegateAgentDispatch_MarkerClearErrorDoesNotFailDispatch(t *testing.T) {
	orig := runHostCLI
	t.Cleanup(func() { runHostCLI = orig })
	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		return "dispatched", nil
	}

	svc := &dispatchErrExecSvc{stubService: &stubService{}}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_dispatch", map[string]any{"ref": "proj/branch", "brief": "do work"})
	if res.IsError {
		t.Fatalf("dispatch must succeed even when marker clear fails: %s", resultText(t, res))
	}
}

type dispatchErrExecSvc struct {
	*stubService
}

func (s *dispatchErrExecSvc) Exec(_ context.Context, _ string, _ []string, _ map[string]string, _, _ string) (int32, string, string, error) {
	return 1, "", "no such container", fmt.Errorf("exec: sandbox not reachable")
}

// ── agent state helpers ───────────────────────────────────────────────────────

// setAgentClientZeroSleep injects zero settle sleep into herdragent.New for the test.
func setAgentClientZeroSleep(t *testing.T) {
	t.Helper()
	orig := agentClientOpts
	agentClientOpts = []herdragent.Option{
		herdragent.WithSleep(func(_ context.Context, _ time.Duration) error { return nil }),
		herdragent.WithSettle(0),
	}
	t.Cleanup(func() { agentClientOpts = orig })
}

// herdrListLine builds a tab-separated herdr list line for tests.
func herdrListLine(wsID, handle, sandboxID, paneID string) string {
	return "label=x\tworkspace_id=" + wsID + "\thandle=" + handle + "\tsandbox_id=" + sandboxID + "\tpane_id=" + paneID + "\n"
}

const (
	agentWorkingJSON = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"working","pane_id":"w9Z:p1M","revision":5,"state_change_seq":2041,"workspace_id":"w9Z"},"type":"agent_info"}}`
	agentDoneJSON10  = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"done","pane_id":"w9Z:p1M","revision":5,"state_change_seq":10,"workspace_id":"w9Z"},"type":"agent_info"}}`
	agentDoneJSON11  = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"done","pane_id":"w9Z:p1M","revision":5,"state_change_seq":11,"workspace_id":"w9Z"},"type":"agent_info"}}`
	agentIdleJSON5   = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"idle","pane_id":"w9Z:p1M","revision":5,"state_change_seq":5,"workspace_id":"w9Z"},"type":"agent_info"}}`
	agentBlockedJSON = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"blocked","pane_id":"w9Z:p1M","revision":5,"state_change_seq":11,"workspace_id":"w9Z"},"type":"agent_info"}}`
)

// gitFallbackResponses returns exec responses for marker-absent + 3 git commands.
func gitFallbackResponses() []execResponse {
	return []execResponse{
		{code: 1, stderr: "No such file"},     // marker absent
		{code: 0, stdout: "abc1234 fix: x\n"}, // git log
		{code: 0, stdout: ""},                 // git status
		{code: 0, stdout: "feat/work\n"},      // git branch
	}
}

// ── new agent-state tests ─────────────────────────────────────────────────────

func TestDelegateAgentPoll_ReportsHerdrAgentState(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"agent get":  agentWorkingJSON,
	})
	svc := &seqExecService{stubService: &stubService{}, responses: gitFallbackResponses()}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := data["agent_status"]; got != "working" {
		t.Errorf("agent_status = %q, want %q", got, "working")
	}
	if got, _ := data["state_change_seq"].(float64); got != 2041 {
		t.Errorf("state_change_seq = %v, want 2041", got)
	}
}

func TestDelegateAgentPoll_BlockedIncludesQuestion(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"agent get":  agentBlockedJSON,
		"agent read": "What library should I use?\n",
	})
	svc := &seqExecService{stubService: &stubService{}, responses: gitFallbackResponses()}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := data["agent_status"]; got != "blocked" {
		t.Errorf("agent_status = %q, want blocked", got)
	}
	q, _ := data["question"].(string)
	if !strings.Contains(q, "What library") {
		t.Errorf("question = %q, want to contain question text", q)
	}
}

func TestDelegateAgentPoll_DoneThenBlocked_Race(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)

	origHost := runHostCLI
	origHerdr := runHerdrCLI
	t.Cleanup(func() { runHostCLI = origHost; runHerdrCLI = origHerdr })

	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "herdr" && argv[1] == "list" {
			return herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"), nil
		}
		return "", fmt.Errorf("unexpected host call: %v", argv)
	}
	getCount := 0
	runHerdrCLI = func(_ context.Context, _ string, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "agent" && argv[1] == "get" {
			getCount++
			if getCount == 1 {
				return agentDoneJSON10, nil
			}
			return agentBlockedJSON, nil
		}
		if len(argv) >= 2 && argv[0] == "agent" && argv[1] == "read" {
			return "Shall I proceed?\n", nil
		}
		return "", fmt.Errorf("unexpected herdr call: %v", argv)
	}

	svc := &seqExecService{stubService: &stubService{}, responses: gitFallbackResponses()}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := data["agent_status"]; got != "blocked" {
		t.Errorf("agent_status = %q after done→blocked race, want blocked", got)
	}
	q, _ := data["question"].(string)
	if !strings.Contains(q, "Shall I proceed") {
		t.Errorf("question = %q, want race question text", q)
	}
	if got, _ := data["settled"].(bool); !got {
		t.Errorf("settled = %v, want true after settle poll", got)
	}
}

func TestDelegateAgentPoll_HerdrDoneWithoutMarker_NotComplete(t *testing.T) {
	cases := []struct {
		name      string
		agentJSON string
		wantAgent string
	}{
		{"done", agentDoneJSON11, "done"},
		{"idle", agentIdleJSON5, "idle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
			setAgentClientZeroSleep(t)
			installHostCLIRecorder(t, map[string]string{
				"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
				"agent get":  tc.agentJSON,
			})
			svc := &seqExecService{stubService: &stubService{}, responses: gitFallbackResponses()}
			cs, closeFn := connectPairSvc(t, svc)
			defer closeFn()

			res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch"})
			if res.IsError {
				t.Fatalf("unexpected error: %s", resultText(t, res))
			}
			var data map[string]any
			if err := json.Unmarshal(resultData(t, res), &data); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := data["done_via"]; got != "git" {
				t.Errorf("done_via = %q, want git; herdr %s alone must not short-circuit the git path", got, tc.name)
			}
			if got, _ := data["git_log"].(string); got != "abc1234 fix: x\n" {
				t.Errorf("git_log = %q, want canned git log (proves git path ran)", got)
			}
			if got, _ := data["branch_name"].(string); got != "feat/work\n" {
				t.Errorf("branch_name = %q, want canned branch name", got)
			}
			if got := data["agent_status"]; got != tc.wantAgent {
				t.Errorf("agent_status = %q, want %q", got, tc.wantAgent)
			}
		})
	}
}

func TestDelegateAgentPoll_HerdrUnavailable_DegradesToGit(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
	t.Setenv("PATH", t.TempDir()) // herdr not on PATH
	svc := &seqExecService{stubService: &stubService{}, responses: gitFallbackResponses()}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := data["agent_status"]; got != "unknown" {
		t.Errorf("agent_status = %q, want unknown when herdr unavailable", got)
	}
	reason, _ := data["agent_state_reason"].(string)
	if reason == "" {
		t.Errorf("agent_state_reason must be set when herdr unavailable")
	}
	if got := data["done_via"]; got != "git" {
		t.Errorf("done_via = %q, want git (degraded path)", got)
	}
}

func TestDelegateAgentPoll_Unbound_Unknown(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/other", "sb-9", "w9Z:p1M"),
	})
	svc := &seqExecService{stubService: &stubService{}, responses: gitFallbackResponses()}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/loose"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := data["agent_status"]; got != "unknown" {
		t.Errorf("agent_status = %q, want unknown for unbound ref", got)
	}
	if got := data["agent_state_reason"]; got != "no_herdr_binding" {
		t.Errorf("agent_state_reason = %q, want no_herdr_binding", got)
	}
}

func TestDelegateAgentPoll_WaitMs_UsesAgentWait(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	rec := installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"agent wait": "",
		"agent get":  agentWorkingJSON,
	})
	svc := &seqExecService{stubService: &stubService{}, responses: gitFallbackResponses()}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch", "wait_ms": 500})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	waitCall, found := rec.find("agent wait")
	if !found {
		t.Fatalf("agent wait not called; calls=%+v", rec.calls)
	}
	if !containsToken(waitCall.args, "--timeout") {
		t.Errorf("agent wait args missing --timeout: %v", waitCall.args)
	}
	// find the value after --timeout
	for i, a := range waitCall.args {
		if a == "--timeout" && i+1 < len(waitCall.args) {
			if waitCall.args[i+1] != "500" {
				t.Errorf("--timeout value = %q, want 500", waitCall.args[i+1])
			}
			break
		}
	}
}

func TestDelegateAgentPoll_ReportedOverride_Unknown(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	const reportedOverrideJSON = `{"id":"cli:agent:get","result":{"agent":{"agent":"nexus-slice-agent","agent_status":"working","pane_id":"w9Z:p1M","revision":5,"state_change_seq":9999,"workspace_id":"w9Z"},"type":"agent_info"}}`
	installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"agent get":  reportedOverrideJSON,
	})
	svc := &seqExecService{stubService: &stubService{}, responses: gitFallbackResponses()}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()

	res := callTool(t, cs, "delegate_agent_poll", map[string]any{"ref": "proj/branch"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := data["agent_status"]; got != "unknown" {
		t.Errorf("agent_status = %q, want unknown for reported_override agent", got)
	}
	if got := data["agent_state_reason"]; got != "reported_override" {
		t.Errorf("agent_state_reason = %q, want reported_override", got)
	}
}

func TestDelegateAgentDispatch_DeliveredFlag(t *testing.T) {
	t.Run("exit0_delivered_true", func(t *testing.T) {
		orig := runHostCLI
		t.Cleanup(func() { runHostCLI = orig })
		runHostCLI = func(_ context.Context, argv ...string) (string, error) {
			return "dispatched", nil
		}
		svc := &dispatchRecordSvc{stubService: &stubService{}}
		cs, closeFn := connectPairSvc(t, svc)
		defer closeFn()
		res := callTool(t, cs, "delegate_agent_dispatch", map[string]any{"ref": "p/b", "brief": "do it"})
		if res.IsError {
			t.Fatalf("dispatch returned IsError: %s", resultText(t, res))
		}
		var data map[string]any
		if err := json.Unmarshal(resultData(t, res), &data); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got, _ := data["delivered"].(bool); !got {
			t.Errorf("delivered = %v, want true on exit 0", got)
		}
	})
	t.Run("exit1_delivered_false_not_iserror", func(t *testing.T) {
		orig := runHostCLI
		t.Cleanup(func() { runHostCLI = orig })
		runHostCLI = func(_ context.Context, argv ...string) (string, error) {
			return "agent not found\n", fmt.Errorf("exit status 1")
		}
		svc := &dispatchErrExecSvc{stubService: &stubService{}}
		cs, closeFn := connectPairSvc(t, svc)
		defer closeFn()
		res := callTool(t, cs, "delegate_agent_dispatch", map[string]any{"ref": "p/b", "brief": "do it"})
		if res.IsError {
			t.Fatalf("dispatch must not set IsError when herdr exits non-0; got: %s", resultText(t, res))
		}
		var data map[string]any
		if err := json.Unmarshal(resultData(t, res), &data); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got, _ := data["delivered"].(bool); got {
			t.Errorf("delivered = %v, want false on exit non-0", got)
		}
	})
}

func TestDelegateTeardown_FallbackRmRealError_ReturnsError(t *testing.T) {
	setTeardownPollTiming(t, 0, 0)
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	rec := installHostCLIRecorder(t, map[string]string{
		"herdr list":      teardownBindingLines,
		"worktree remove": "removed\n",
		"sandbox list":    "HANDLE  STATE  ID\nrepo/branch  running  sb-1\n",
	})
	origHost := runHostCLI
	runHostCLI = func(ctx context.Context, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "sandbox" && argv[1] == "rm" {
			rec.calls = append(rec.calls, hostCall{bin: "<nexus>", args: append([]string(nil), argv...)})
			return "internal error: vm stuck\n", fmt.Errorf("exit status 2: vm stuck")
		}
		return origHost(ctx, argv...)
	}
	t.Cleanup(func() { runHostCLI = origHost })

	cs, closeFn := connectPair(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_teardown", map[string]any{"ref": "repo/branch"})
	if !res.IsError {
		t.Fatalf("expected error for real rm failure, got success: %s", resultText(t, res))
	}
	if text := resultText(t, res); !strings.Contains(text, "vm stuck") {
		t.Errorf("error text does not surface rm output: %s", text)
	}
}
