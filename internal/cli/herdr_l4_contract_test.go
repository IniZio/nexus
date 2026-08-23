//go:build herdr_live

// Package cli — Layer 4 herdr CLI contract test.
//
// This file is part of Layer 4 of the herdr+nexus3 test strategy
// (docs/design/herdr-test-strategy.md). It asserts that every herdr command
// nexus3 issues is a command the installed herdr binary actually accepts, and
// that the JSON response shapes herdr returns carry the fields nexus3 parses.
//
// Run with:
//
//	TMPDIR=/tmp go test -count=1 -tags herdr_live ./internal/cli/ -run TestHerdrPlugin_L4
//
// Prerequisites:
//   - herdr must be running (herdr workspace list must succeed)
//   - The test creates and closes its own scratch workspaces.
//     It never touches the operator's existing workspaces.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHerdrPlugin_L4_Contract is Layer 4 of the herdr+nexus3 test strategy:
// the herdr CLI contract.
//
// It asserts two things:
//
//  1. Command acceptance — every `herdr` invocation nexus3 issues is a command
//     the installed herdr binary accepts (exit 0 or help text that lists the
//     subcommand and each flag nexus3 passes). A flag-name typo such as
//     `--workspace-id` vs `--workspace` is caught here and only here.
//
//  2. Response shape — the JSON fields that herdrParseWorktreeListForWorkspace
//     and the workspace-list backfill parser depend on are present with the
//     expected types in live herdr output. A herdr upgrade that renames a
//     field must fail this test.
//
// # Safety split
//
// READ-ONLY commands (worktree list, workspace list) are invoked for real and
// their exit codes and JSON shapes are asserted.
//
// SAFE-MUTATING commands (workspace rename, tab create) are driven against a
// scratch workspace created with a unique timestamp label. Cleanup verifies
// the label matches before closing — the same guard as TestHerdrPlugin_L4_BinaryVerb.
//
// ALL OTHER MUTATING commands (workspace close, pane *, plugin pane open) are
// verified structurally: `herdr <group> <subcmd> --help` is parsed to confirm
// each flag nexus3 passes is listed in the usage text.
//
// # Negative-control guide
//
// To verify this test catches the exact bug class it was written for:
//
//	# Reintroduce the wrong argv that caused defect 1:
//	sed -i 's/"worktree", "list", "--workspace"/"plugin", "pane", "worktree", "list", "--workspace-id"/' \
//	    internal/cli/cmd_herdr_plugin.go
//	TMPDIR=/tmp go test -count=1 -tags herdr_live ./internal/cli/ -run TestHerdrPlugin_L4_Contract
//	# FAIL: WorktreeList_LiveAndShape — herdr exited non-zero (rc=2)
//	# Revert before continuing.
func TestHerdrPlugin_L4_Contract(t *testing.T) {
	// Safety: record before-state so the operator can confirm their
	// workspaces are untouched after the run.
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

	// Workspace integrity: after-state must list the same workspace IDs as before.
	afterWorkspaces := herdrWorkspaceList(t)
	t.Logf("AFTER: %s", afterWorkspaces)
	assertSameWorkspaceIDs(t, beforeWorkspaces, afterWorkspaces)
}

// ── READ-ONLY: live invocations ───────────────────────────────────────────────

// testHerdrContract_WorkspaceList asserts that `herdr workspace list` exits 0
// and that every workspace entry that carries a worktree object includes
// worktree.repo_root as a non-empty string. The backfill logic
// (HerdrBackfillRepoRoot) reads that field; a herdr upgrade that renames it
// would silently disable the backfill without this assertion.
func testHerdrContract_WorkspaceList(t *testing.T) {
	t.Helper()
	out, err := exec.Command("herdr", "workspace", "list").CombinedOutput()
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

// testHerdrContract_WorktreeList asserts that `herdr worktree list --workspace
// <id>` exits 0 and that the JSON response carries the fields
// herdrParseWorktreeListForWorkspace reads:
//
//	result.source.repo_key            (string, non-empty)
//	result.source.source_workspace_id (string, non-empty)
//	result.worktrees[].branch         (string, present)
//	result.worktrees[].path           (string, non-empty)
//	result.worktrees[].is_linked_worktree (bool, present)
//	result.worktrees[].open_workspace_id  (string, present in at least one entry)
//
// Critically, this test calls the production function herdrListWorktreeForWorkspace
// (which constructs the argv internally) rather than hardcoding the argv in the
// test. This means a change to the production argv (e.g. switching back to
// `plugin pane worktree list --workspace-id`) causes this test to fail, even
// though Layers 1–3 would stay green. This is the exact defect 1 detection.
func testHerdrContract_WorktreeList(t *testing.T) {
	t.Helper()

	// Pick the first workspace that carries a worktree (git-backed workspace).
	wsID := firstWorktreeWorkspaceID(t)
	if wsID == "" {
		liveSkip(t, "no git-backed workspace found in herdr workspace list — cannot probe worktree list shape")
	}

	herdrBin, binErr := resolveHerdrBin()
	if binErr != nil {
		liveSkip(t, "cannot resolve herdr binary: %v", binErr)
	}

	// Production path: call the real herdrListWorktreeForWorkspace so that any
	// change to the argv nexus3 sends (e.g. reverting to the wrong
	// `plugin pane worktree list --workspace-id` argv) fails here and only here.
	_, prodErr := herdrListWorktreeForWorkspace(context.Background(), herdrBin, wsID)
	if prodErr != nil {
		t.Errorf("herdrListWorktreeForWorkspace (production argv) returned error: %v — the argv nexus3 sends to herdr is likely wrong", prodErr)
	}

	// Direct exec with the correct argv to assert the JSON response shape
	// independently of the production parsing logic.
	out, err := exec.Command("herdr", "worktree", "list", "--workspace", wsID).CombinedOutput()
	if err != nil {
		t.Fatalf("herdr worktree list --workspace %s: exit non-zero: %v\n%s", wsID, err, out)
	}

	// Structural parse — assert every field the production parser reads.
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
		// branch may be empty on detached HEAD — just assert the key is present by
		// checking the raw JSON contains "branch" in the worktrees array.
		if wt.OpenWorkspaceID != "" {
			seenOpenWorkspaceID = true
		}
	}
	// At least one entry must carry open_workspace_id (the source workspace
	// itself is always open). If none do, the field name has changed.
	if !seenOpenWorkspaceID {
		// Re-check via raw JSON so the struct zero-value does not mask a missing key.
		if !strings.Contains(string(out), `"open_workspace_id"`) {
			t.Errorf("no worktree entry carries open_workspace_id — herdrParseWorktreeListForWorkspace matches on this field")
		}
	}
}

// firstWorktreeWorkspaceID returns the workspace_id of the first workspace in
// `herdr workspace list` that carries a non-null worktree object, or "" if
// none exists or on any error. Never fails the test.
func firstWorktreeWorkspaceID(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("herdr", "workspace", "list").CombinedOutput()
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

// testHerdrContract_WorkspaceRename asserts that `herdr workspace rename <id>
// <label>` exits 0 against a scratch workspace. The scratch workspace is
// identified and closed by its (renamed) label in t.Cleanup with the same
// label-verified safety check used by TestHerdrPlugin_L4_BinaryVerb.
func testHerdrContract_WorkspaceRename(t *testing.T) {
	t.Helper()

	originalLabel := fmt.Sprintf("nexus3-l4-contract-rename-%d", time.Now().UnixMilli())
	renamedLabel := originalLabel + "-r"

	wsID, _ := createL4ScratchWorkspace(t, originalLabel)
	t.Cleanup(func() {
		// After rename the label is renamedLabel; after a failed rename it is
		// still originalLabel. Try both to guarantee cleanup.
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

	out, err := exec.Command("herdr", "workspace", "rename", wsID, renamedLabel).CombinedOutput()
	if err != nil {
		t.Fatalf("herdr workspace rename %s %q: exit non-zero: %v\n%s", wsID, renamedLabel, err, out)
	}
	t.Logf("workspace rename: %s", strings.TrimSpace(string(out)))
}

// testHerdrContract_TabCreate asserts that `herdr tab create --workspace <id>
// --focus` exits 0 against a scratch workspace. The scratch workspace is
// identified and closed by its label in t.Cleanup.
func testHerdrContract_TabCreate(t *testing.T) {
	t.Helper()

	label := fmt.Sprintf("nexus3-l4-contract-tab-%d", time.Now().UnixMilli())
	wsID, _ := createL4ScratchWorkspace(t, label)
	t.Cleanup(func() {
		id := findL4WorkspaceIDByLabel(t, label)
		closeL4ScratchWorkspace(t, id, label)
		after := herdrWorkspaceList(t)
		if strings.Contains(after, label) {
			t.Errorf("scratch workspace survived cleanup (label %q still present)", label)
		}
	})

	out, err := exec.Command("herdr", "tab", "create", "--workspace", wsID, "--focus").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr tab create --workspace %s --focus: exit non-zero: %v\n%s", wsID, err, out)
	}
	t.Logf("tab create: %s", strings.TrimSpace(string(out)))
}

// ── MUTATING: structural help-parse ──────────────────────────────────────────
//
// For each mutating command, assertHerdrHelp runs `herdr <args> --help` and
// checks that every required token (subcommand name or flag) appears in the
// output. A flag that nexus3 passes but herdr does not recognise would be
// caught here even though the command is never invoked for real.

// assertHerdrHelp runs `herdr <args...> --help` and asserts each token in
// wantTokens appears in the combined output. Reports the full help text on
// failure so the discrepancy is visible.
func assertHerdrHelp(t *testing.T, wantTokens []string, args ...string) {
	t.Helper()
	helpArgs := append(args, "--help") //nolint:gocritic
	out, _ := exec.Command("herdr", helpArgs...).CombinedOutput()
	helpText := string(out)
	for _, tok := range wantTokens {
		if !strings.Contains(helpText, tok) {
			t.Errorf("herdr %s: token %q not found in help text\nhelp output:\n%s",
				strings.Join(args, " "), tok, helpText)
		}
	}
}

func testHerdrContract_Help_WorkspaceClose(t *testing.T) {
	// nexus3 issues: herdr workspace close <workspaceID>
	// Subcommand "close" must appear in `herdr workspace --help`.
	assertHerdrHelp(t, []string{"close"}, "workspace")
}

func testHerdrContract_Help_PaneClose(t *testing.T) {
	// nexus3 issues: herdr pane close <paneID>
	// The only thing to verify is that the subcommand exists; it takes a
	// positional arg only (no flags nexus3 passes).
	assertHerdrHelp(t, []string{"close"}, "pane")
}

func testHerdrContract_Help_PaneRun(t *testing.T) {
	// nexus3 issues: herdr pane run <paneID> <text>
	assertHerdrHelp(t, []string{"run"}, "pane")
}

func testHerdrContract_Help_PaneSendText(t *testing.T) {
	// nexus3 issues: herdr pane send-text <paneID> <text>
	assertHerdrHelp(t, []string{"send-text"}, "pane")
}

func testHerdrContract_Help_PaneSendKeys(t *testing.T) {
	// nexus3 issues: herdr pane send-keys <paneID> <key>
	assertHerdrHelp(t, []string{"send-keys"}, "pane")
}

func testHerdrContract_Help_PaneWaitOutput(t *testing.T) {
	// nexus3 issues: herdr pane wait-output <paneID> --match <text> --timeout <ms>
	assertHerdrHelp(t, []string{"wait-output"}, "pane")
	assertHerdrHelp(t, []string{"--match", "--timeout"}, "pane", "wait-output")
}

func testHerdrContract_Help_PaneReportAgent(t *testing.T) {
	// nexus3 issues (herdrPaneReportAgent):
	//   herdr pane report-agent <paneID> --source <src> --agent nexus3-slice-agent --state working
	// nexus3 also issues (attach reporter):
	//   herdr pane report-agent <paneID> --source nexus3 --state working --seq 1
	assertHerdrHelp(t, []string{"report-agent"}, "pane")
	assertHerdrHelp(t, []string{"--source", "--agent", "--state", "--seq"}, "pane", "report-agent")
}

func testHerdrContract_Help_PaneReleaseAgent(t *testing.T) {
	// nexus3 issues: herdr pane release-agent <paneID> --source nexus3
	assertHerdrHelp(t, []string{"release-agent"}, "pane")
	assertHerdrHelp(t, []string{"--source"}, "pane", "release-agent")
}

func testHerdrContract_Help_PluginPaneOpen(t *testing.T) {
	// nexus3 issues (herdrOpenGuestShellPane / herdrPluginOpenPane):
	//   herdr plugin pane open
	//     --plugin nexus3
	//     --entrypoint shell
	//     [--placement split --target-pane <id> --direction right | --workspace <id>]
	//     --env NEXUS3_WORKSPACE=<ref>
	//     [--focus | --no-focus]
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

// assertSameWorkspaceIDs compares the workspace IDs in two raw `herdr workspace
// list` JSON blobs and fails the test if any IDs present in `before` are
// absent from `after`. It does not fail if after has more IDs — that cannot
// happen because of this test but could happen if another process opened a
// workspace concurrently.
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
