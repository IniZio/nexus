package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// markerExecService answers the done-marker cat according to marker, and
// canned git output otherwise.
type markerExecService struct {
	*stubService
	marker string
}

func (s *markerExecService) Exec(_ context.Context, _ string, argv []string, _ map[string]string, _, _ string) (int32, string, string, error) {
	if len(argv) > 0 && argv[0] == "cat" {
		if s.marker == "" {
			return 1, "", "no such file", nil
		}
		return 0, s.marker, "", nil
	}
	return 0, "abc1234 fix: x\n", "", nil
}

func fastWait(t *testing.T) {
	t.Helper()
	orig := agentWaitInterval
	agentWaitInterval = time.Millisecond
	t.Cleanup(func() { agentWaitInterval = orig })
}

func callWait(t *testing.T, svc SandboxService, args map[string]any) map[string]any {
	t.Helper()
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()
	res := callTool(t, cs, "delegate_agent_wait", args)
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return data
}

func TestDelegateAgentWait_Marker_Done(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	fastWait(t)
	installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"agent get":  agentDoneJSON11,
		"agent read": "",
	})
	data := callWait(t, &markerExecService{stubService: &stubService{}, marker: "all green\n"}, map[string]any{"ref": "proj/branch"})
	if data["outcome"] != "done" || data["done_via"] != "marker" || data["marker_content"] != "all green" {
		t.Fatalf("got %v", data)
	}
}

func TestDelegateAgentWait_Blocked_ReturnsQuestion(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	fastWait(t)
	installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"agent get":  agentBlockedJSON,
		"agent read": "What library should I use?\n",
	})
	data := callWait(t, &markerExecService{stubService: &stubService{}}, map[string]any{"ref": "proj/branch"})
	if data["outcome"] != "blocked" {
		t.Fatalf("outcome = %v, want blocked", data["outcome"])
	}
	if q, _ := data["question"].(string); !strings.Contains(q, "What library") {
		t.Errorf("question = %q", q)
	}
}

func TestDelegateAgentWait_IdleWithoutMarker(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	fastWait(t)
	installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"agent get":  agentIdleJSON5,
		"agent read": "",
	})
	data := callWait(t, &markerExecService{stubService: &stubService{}}, map[string]any{"ref": "proj/branch"})
	if data["outcome"] != "idle_without_marker" || data["done_via"] != "git" {
		t.Fatalf("got %v", data)
	}
}

func TestDelegateAgentWait_Working_Timeout(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	fastWait(t)
	installHostCLIRecorder(t, map[string]string{
		"herdr list": herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"agent get":  strings.Replace(agentIdleJSON5, `"idle"`, `"working"`, 1),
	})
	data := callWait(t, &markerExecService{stubService: &stubService{}}, map[string]any{"ref": "proj/branch", "timeout_s": 1})
	if data["outcome"] != "timeout" || data["agent_status"] != "working" {
		t.Fatalf("got %v", data)
	}
}

func installFollowupRunner(t *testing.T, agentJSON string, screens []string) *[][]string {
	t.Helper()
	var calls [][]string
	reads := 0
	origHost, origHerdr := runHostCLI, runHerdrCLI
	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		return herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"), nil
	}
	runHerdrCLI = func(_ context.Context, _ string, argv ...string) (string, error) {
		calls = append(calls, append([]string(nil), argv...))
		switch argv[0] + " " + argv[1] {
		case "agent get":
			return agentJSON, nil
		case "pane read":
			s := screens[min(reads, len(screens)-1)]
			reads++
			return s, nil
		case "pane send-text", "pane send-keys":
			return "", nil
		}
		return "", fmt.Errorf("unexpected %v", argv)
	}
	t.Cleanup(func() { runHostCLI, runHerdrCLI = origHost, origHerdr })
	return &calls
}

func fastFollowup(t *testing.T) {
	t.Helper()
	a, b := followupSettleDelay, followupConfirmDelay
	followupSettleDelay, followupConfirmDelay = 0, 0
	t.Cleanup(func() { followupSettleDelay, followupConfirmDelay = a, b })
}

func callFollowup(t *testing.T) map[string]any {
	t.Helper()
	cs, closeFn := connectPairSvc(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_agent_followup", map[string]any{"ref": "proj/branch", "text": "please also add tests"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	var data map[string]any
	if err := json.Unmarshal(resultData(t, res), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return data
}

func TestDelegateAgentFollowup_Submitted(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	fastFollowup(t)
	working := strings.Replace(agentIdleJSON5, `"idle"`, `"working"`, 1)
	calls := installFollowupRunner(t, working, []string{"> please also add tests\n"})
	data := callFollowup(t)
	if data["submitted"] != true {
		t.Fatalf("got %v", data)
	}
	var sawText, sawEnter bool
	for _, c := range *calls {
		if c[0] == "pane" && c[1] == "send-text" && c[3] == "please also add tests" {
			sawText = true
		}
		if c[0] == "pane" && c[1] == "send-keys" && c[3] == "Enter" {
			sawEnter = true
		}
	}
	if !sawText || !sawEnter {
		t.Errorf("calls = %v", *calls)
	}
}

func TestDelegateAgentFollowup_NotSubmitted_RetriesThenReports(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	setAgentClientZeroSleep(t)
	fastFollowup(t)
	calls := installFollowupRunner(t, agentIdleJSON5, []string{"> please also add tests\n"})
	data := callFollowup(t)
	if data["submitted"] != false {
		t.Fatalf("got %v", data)
	}
	enters := 0
	for _, c := range *calls {
		if c[1] == "send-keys" {
			enters++
		}
	}
	if enters != followupEnterRetries+1 {
		t.Errorf("Enter presses = %d, want %d", enters, followupEnterRetries+1)
	}
}

func TestDelegateWorktreeCreate_BriefPath_InstallsBriefAndExclude(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	repo := t.TempDir()
	wt := t.TempDir()
	exclude := filepath.Join(t.TempDir(), "info", "exclude")
	brief := filepath.Join(t.TempDir(), "b.md")
	if err := os.WriteFile(brief, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canned := happyCanned(repo, "feat/x")
	canned["worktree list"] = fmt.Sprintf(`{"result":{"worktrees":[{"branch":"feat/x","path":%q,"is_linked_worktree":true}]}}`, wt)
	installHostCLIRecorder(t, canned)
	origGit := runGitCLI
	runGitCLI = func(_ context.Context, argv ...string) (string, error) {
		if strings.Join(argv[2:], " ") != "rev-parse --git-path info/exclude" || argv[1] != wt {
			t.Errorf("unexpected git argv %v", argv)
		}
		return exclude + "\n", nil
	}
	t.Cleanup(func() { runGitCLI = origGit })

	cs, closeFn := connectPairSvc(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_worktree_create", map[string]any{"repo_path": repo, "branch": "feat/x", "brief_path": brief})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	got, err := os.ReadFile(filepath.Join(wt, ".brief.md"))
	if err != nil || string(got) != "do the thing\n" {
		t.Fatalf(".brief.md = %q, %v", got, err)
	}
	ex, _ := os.ReadFile(exclude)
	if string(ex) != ".brief.md\n.slice-report.md\n" {
		t.Errorf("exclude = %q", ex)
	}
	// Idempotent.
	if err := installBrief(context.Background(), brief, wt); err != nil {
		t.Fatal(err)
	}
	ex, _ = os.ReadFile(exclude)
	if string(ex) != ".brief.md\n.slice-report.md\n" {
		t.Errorf("exclude after re-install = %q", ex)
	}
}

func TestDelegateAgentDispatch_BriefPath_TellsAgentToReadBrief(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	wt := t.TempDir()
	exclude := filepath.Join(t.TempDir(), "exclude")
	brief := filepath.Join(t.TempDir(), "b.md")
	if err := os.WriteFile(brief, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := installHostCLIRecorder(t, map[string]string{
		"herdr list":    herdrListLine("wWS", "proj/branch", "sb-1", "w9Z:p1M"),
		"worktree list": fmt.Sprintf(`{"result":{"worktrees":[{"open_workspace_id":"wWS","branch":"b","path":%q}]}}`, wt),
		"herdr agent":   "ok\n",
	})
	origGit := runGitCLI
	runGitCLI = func(_ context.Context, _ ...string) (string, error) { return exclude, nil }
	t.Cleanup(func() { runGitCLI = origGit })

	cs, closeFn := connectPairSvc(t, &stubService{})
	defer closeFn()
	res := callTool(t, cs, "delegate_agent_dispatch", map[string]any{"ref": "proj/branch", "brief_path": brief})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	if _, err := os.Stat(filepath.Join(wt, ".brief.md")); err != nil {
		t.Errorf(".brief.md not installed: %v", err)
	}
	call, ok := rec.find("herdr agent")
	if !ok || !strings.Contains(call.args[len(call.args)-1], "/workspace/.brief.md") {
		t.Errorf("dispatch call = %v", call)
	}
}
