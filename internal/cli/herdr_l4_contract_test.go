//go:build herdr_live

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHerdrPlugin_L4_Contract(t *testing.T) {
	startIsolatedHerdr(t)

	beforeWorkspaces := herdrWorkspaceList(t)
	t.Logf("BEFORE: %s", beforeWorkspaces)
	if !strings.Contains(beforeWorkspaces, "workspace_list") {
		liveSkip(t, "herdr is not running (workspace list returned no workspace_list field)")
	}

	t.Run("WorkspaceList_LiveAndShape", testHerdrContract_WorkspaceList)
	t.Run("WorktreeList_LiveAndShape", testHerdrContract_WorktreeList)
	t.Run("WorkspaceRename_Driven", testHerdrContract_WorkspaceRename)
	t.Run("TabCreate_Driven", testHerdrContract_TabCreate)
	t.Run("Help_WorkspaceClose", testHerdrContract_Help_WorkspaceClose)
	t.Run("Help_PaneClose", testHerdrContract_Help_PaneClose)
	t.Run("Help_PaneRun", testHerdrContract_Help_PaneRun)
	t.Run("Help_PaneSendText", testHerdrContract_Help_PaneSendText)
	t.Run("Help_PaneSendKeys", testHerdrContract_Help_PaneSendKeys)
	t.Run("Help_PaneWaitOutput", testHerdrContract_Help_PaneWaitOutput)
	t.Run("Help_PaneReportAgent", testHerdrContract_Help_PaneReportAgent)
	t.Run("Help_PaneReleaseAgent", testHerdrContract_Help_PaneReleaseAgent)
	t.Run("Help_PluginPaneOpen", testHerdrContract_Help_PluginPaneOpen)

	afterWorkspaces := herdrWorkspaceList(t)
	t.Logf("AFTER: %s", afterWorkspaces)
	assertSameWorkspaceIDs(t, beforeWorkspaces, afterWorkspaces)
}

// ── READ-ONLY: live invocations ───────────────────────────────────────────────

func testHerdrContract_WorkspaceList(t *testing.T) {
	t.Helper()
	out, err := herdrExec("workspace", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr workspace list: exit non-zero: %v\n%s", err, out)
	}

	var resp struct {
		Result struct {
			Type       string `json:"type"`
			Workspaces []struct {
				WorkspaceID string `json:"workspace_id"`
				Worktree    *struct {
					RepoRoot string `json:"repo_root"`
					RepoKey  string `json:"repo_key"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("herdr workspace list: parse JSON: %v\nraw: %s", err, out)
	}
	if resp.Result.Type != "workspace_list" {
		t.Errorf("result.type = %q, want %q", resp.Result.Type, "workspace_list")
	}
	for _, ws := range resp.Result.Workspaces {
		if ws.Worktree == nil {
			continue // workspaces without a repo are fine
		}
		if ws.Worktree.RepoRoot == "" {
			t.Errorf("workspace %s: worktree.repo_root is empty — backfill parser depends on this field", ws.WorkspaceID)
		}
		if ws.Worktree.RepoKey == "" {
			t.Errorf("workspace %s: worktree.repo_key is empty — binding matcher depends on this field", ws.WorkspaceID)
		}
	}
}

func testHerdrContract_WorktreeList(t *testing.T) {
	t.Helper()

	wsID := firstWorktreeWorkspaceID(t)
	if wsID == "" {
		liveSkip(t, "no git-backed workspace found in herdr workspace list — cannot probe worktree list shape")
	}

	herdrBin, binErr := resolveHerdrBin()
	if binErr != nil {
		liveSkip(t, "cannot resolve herdr binary: %v", binErr)
	}

	_, prodErr := herdrListWorktreeForWorkspace(context.Background(), herdrBin, wsID)
	if prodErr != nil {
		t.Errorf("herdrListWorktreeForWorkspace (production argv) returned error: %v — the argv nexus sends to herdr is likely wrong", prodErr)
	}

	out, err := herdrExec("worktree", "list", "--workspace", wsID).CombinedOutput()
	if err != nil {
		t.Fatalf("herdr worktree list --workspace %s: exit non-zero: %v\n%s", wsID, err, out)
	}

	var resp struct {
		Result struct {
			Source struct {
				RepoKey           string `json:"repo_key"`
				SourceWorkspaceID string `json:"source_workspace_id"`
			} `json:"source"`
			Worktrees []struct {
				Branch           string `json:"branch"`
				Path             string `json:"path"`
				IsLinkedWorktree *bool  `json:"is_linked_worktree"`
				OpenWorkspaceID  string `json:"open_workspace_id"`
			} `json:"worktrees"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("herdr worktree list: parse JSON: %v\nraw: %s", err, out)
	}

	if resp.Result.Source.RepoKey == "" {
		t.Errorf("result.source.repo_key is empty — herdrParseWorktreeListForWorkspace reads this field")
	}
	if resp.Result.Source.SourceWorkspaceID == "" {
		t.Errorf("result.source.source_workspace_id is empty — herdrParseWorktreeListForWorkspace reads this field")
	}
	if len(resp.Result.Worktrees) == 0 {
		t.Errorf("result.worktrees is empty — nothing to validate shapes against")
		return
	}

	seenOpenWorkspaceID := false
	for i, wt := range resp.Result.Worktrees {
		if wt.Path == "" {
			t.Errorf("worktrees[%d].path is empty", i)
		}
		if wt.IsLinkedWorktree == nil {
			t.Errorf("worktrees[%d].is_linked_worktree is absent", i)
		}
		if wt.OpenWorkspaceID != "" {
			seenOpenWorkspaceID = true
		}
	}
	if !seenOpenWorkspaceID {
		if !strings.Contains(string(out), `"open_workspace_id"`) {
			t.Errorf("no worktree entry carries open_workspace_id — herdrParseWorktreeListForWorkspace matches on this field")
		}
	}
}

func firstWorktreeWorkspaceID(t *testing.T) string {
	t.Helper()
	out, err := herdrExec("workspace", "list").CombinedOutput()
	if err != nil {
		return ""
	}
	var resp struct {
		Result struct {
			Workspaces []struct {
				WorkspaceID string           `json:"workspace_id"`
				Worktree    *json.RawMessage `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return ""
	}
	for _, ws := range resp.Result.Workspaces {
		if ws.Worktree != nil {
			return ws.WorkspaceID
		}
	}
	return ""
}

// ── SAFE-MUTATING: driven against scratch workspaces ─────────────────────────

func testHerdrContract_WorkspaceRename(t *testing.T) {
	t.Helper()

	originalLabel := fmt.Sprintf("nexus-l4-contract-rename-%d", time.Now().UnixMilli())
	renamedLabel := originalLabel + "-r"

	wsID, _ := createL4ScratchWorkspace(t, originalLabel)
	t.Cleanup(func() {
		if id := findL4WorkspaceIDByLabel(t, renamedLabel); id != "" {
			closeL4ScratchWorkspace(t, id, renamedLabel)
		}
		if id := findL4WorkspaceIDByLabel(t, originalLabel); id != "" {
			closeL4ScratchWorkspace(t, id, originalLabel)
		}
		after := herdrWorkspaceList(t)
		if strings.Contains(after, originalLabel) || strings.Contains(after, renamedLabel) {
			t.Errorf("scratch workspace survived cleanup (label %q or %q still present)", originalLabel, renamedLabel)
		}
	})

	out, err := herdrExec("workspace", "rename", wsID, renamedLabel).CombinedOutput()
	if err != nil {
		t.Fatalf("herdr workspace rename %s %q: exit non-zero: %v\n%s", wsID, renamedLabel, err, out)
	}
	t.Logf("workspace rename: %s", strings.TrimSpace(string(out)))
}

func testHerdrContract_TabCreate(t *testing.T) {
	t.Helper()

	label := fmt.Sprintf("nexus-l4-contract-tab-%d", time.Now().UnixMilli())
	wsID, _ := createL4ScratchWorkspace(t, label)
	t.Cleanup(func() {
		id := findL4WorkspaceIDByLabel(t, label)
		closeL4ScratchWorkspace(t, id, label)
		after := herdrWorkspaceList(t)
		if strings.Contains(after, label) {
			t.Errorf("scratch workspace survived cleanup (label %q still present)", label)
		}
	})

	out, err := herdrExec("tab", "create", "--workspace", wsID, "--focus").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr tab create --workspace %s --focus: exit non-zero: %v\n%s", wsID, err, out)
	}
	t.Logf("tab create: %s", strings.TrimSpace(string(out)))
}

// ── MUTATING: structural help-parse ──────────────────────────────────────────
//
// For each mutating command, assertHerdrHelp runs `herdr <args> --help` and
// checks that every required token (subcommand name or flag) appears in the
// output. A flag that nexus passes but herdr does not recognise would be
// caught here even though the command is never invoked for real.

func assertHerdrHelp(t *testing.T, wantTokens []string, args ...string) {
	t.Helper()
	helpArgs := append(args, "--help") //nolint:gocritic
	out, _ := herdrExec(helpArgs...).CombinedOutput()
	helpText := string(out)
	for _, tok := range wantTokens {
		if !strings.Contains(helpText, tok) {
			t.Errorf("herdr %s: token %q not found in help text\nhelp output:\n%s",
				strings.Join(args, " "), tok, helpText)
		}
	}
}

func testHerdrContract_Help_WorkspaceClose(t *testing.T) {
	assertHerdrHelp(t, []string{"close"}, "workspace")
}

func testHerdrContract_Help_PaneClose(t *testing.T) {
	assertHerdrHelp(t, []string{"close"}, "pane")
}

func testHerdrContract_Help_PaneRun(t *testing.T) {
	assertHerdrHelp(t, []string{"run"}, "pane")
}

func testHerdrContract_Help_PaneSendText(t *testing.T) {
	assertHerdrHelp(t, []string{"send-text"}, "pane")
}

func testHerdrContract_Help_PaneSendKeys(t *testing.T) {
	assertHerdrHelp(t, []string{"send-keys"}, "pane")
}

func testHerdrContract_Help_PaneWaitOutput(t *testing.T) {
	assertHerdrHelp(t, []string{"wait-output"}, "pane")
	assertHerdrHelp(t, []string{"--match", "--timeout"}, "pane", "wait-output")
}

func testHerdrContract_Help_PaneReportAgent(t *testing.T) {
	assertHerdrHelp(t, []string{"report-agent"}, "pane")
	assertHerdrHelp(t, []string{"--source", "--agent", "--state", "--seq"}, "pane", "report-agent")
}

func testHerdrContract_Help_PaneReleaseAgent(t *testing.T) {
	assertHerdrHelp(t, []string{"release-agent"}, "pane")
	assertHerdrHelp(t, []string{"--source"}, "pane", "release-agent")
}

func testHerdrContract_Help_PluginPaneOpen(t *testing.T) {
	assertHerdrHelp(t, []string{"open"}, "plugin", "pane")
	assertHerdrHelp(t, []string{
		"--plugin",
		"--entrypoint",
		"--placement",
		"--workspace",
		"--target-pane",
		"--direction",
		"--env",
		"--focus",
		"--no-focus",
	}, "plugin", "pane", "open")
}

// ── Safety helper ─────────────────────────────────────────────────────────────

func assertSameWorkspaceIDs(t *testing.T, before, after string) {
	t.Helper()
	extract := func(raw string) map[string]struct{} {
		ids := map[string]struct{}{}
		var resp struct {
			Result struct {
				Workspaces []struct {
					WorkspaceID string `json:"workspace_id"`
				} `json:"workspaces"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			return ids
		}
		for _, ws := range resp.Result.Workspaces {
			ids[ws.WorkspaceID] = struct{}{}
		}
		return ids
	}
	bIDs := extract(before)
	aIDs := extract(after)
	for id := range bIDs {
		if _, ok := aIDs[id]; !ok {
			t.Errorf("workspace %s was present before the test but absent after — workspace may have been inadvertently closed", id)
		}
	}
}
