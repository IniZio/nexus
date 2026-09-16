package mcp

import (
	"context"
	"strings"
	"testing"
)

func TestDelegateWorktreeCreate_RejectsAllowedBranches(t *testing.T) {
	args := delegateWorktreeCreateArgs{
		RepoPath:        "/tmp/testrepo",
		Handle:          "testproject/test-branch",
		AllowedBranches: []string{"refs/heads/**"},
	}
	err := validateDelegateWorktreeCreate(args)
	if err == nil {
		t.Fatal("expected error when allowed_branches is set, got nil")
	}
	if !strings.Contains(err.Error(), "allowed_branches") {
		t.Errorf("error %q does not mention allowed_branches", err.Error())
	}
}

func TestDelegateWorktreeCreate_ValidArgs_Pass(t *testing.T) {
	args := delegateWorktreeCreateArgs{
		RepoPath: "/tmp/testrepo",
		Handle:   "testproject/test-branch",
		ImageRef: "nexus3:latest",
	}
	if err := validateDelegateWorktreeCreate(args); err != nil {
		t.Errorf("valid args should not fail: %v", err)
	}
}

func TestDelegateWorktreeCreate_MissingRepoPath(t *testing.T) {
	args := delegateWorktreeCreateArgs{Handle: "proj/name"}
	if err := validateDelegateWorktreeCreate(args); err == nil {
		t.Fatal("expected error for missing repo_path")
	}
}

func TestDelegateWorktreeCreate_MissingHandle(t *testing.T) {
	args := delegateWorktreeCreateArgs{RepoPath: "/tmp/repo"}
	if err := validateDelegateWorktreeCreate(args); err == nil {
		t.Fatal("expected error for missing handle")
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
