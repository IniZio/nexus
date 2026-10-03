package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	mcpsrv "github.com/IniZio/nexus/internal/mcp"
)

func TestHerdrWorktreeCreate_PrintsJSONResult(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := t.TempDir()
	var calls []string
	worktreeCreateRunners = func() mcpsrv.WorktreeRunners {
		return mcpsrv.WorktreeRunners{
			Herdr: func(_ context.Context, _ string, argv ...string) (string, error) {
				calls = append(calls, "herdr "+strings.Join(argv, " "))
				switch argv[0] + " " + argv[1] {
				case "workspace list":
					return `{"result":{"workspaces":[{"workspace_id":"wPARENT","worktree":{"checkout_path":"` + repo + `"}}]}}`, nil
				case "worktree create":
					return `{"ws":"wNEW"}`, nil
				}
				return `{"result":{"worktrees":[{"branch":"feat/x","path":"/wt/path"}]}}`, nil
			},
			Host: func(_ context.Context, argv ...string) (string, error) {
				calls = append(calls, "nexus "+strings.Join(argv, " "))
				if argv[1] == "list" {
					return "label=x\tworkspace_id=wNEW\thandle=repo/branch\tsandbox_id=sb-1\tpane_id=\n", nil
				}
				return "", nil
			},
		}
	}
	t.Cleanup(func() { worktreeCreateRunners = mcpsrv.DefaultWorktreeRunners })

	var stdout, stderr bytes.Buffer
	err := runHerdrWorktreeCreate(context.Background(), []string{"--repo", repo, "--branch", "feat/x"}, NewOutput(&stdout, &stderr, false))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	wantCalls := []string{
		"herdr workspace list",
		"herdr worktree create --workspace wPARENT --branch feat/x --no-focus",
		"nexus herdr worktree-sandbox wNEW",
		"nexus herdr list",
		"herdr worktree list --workspace wNEW --json",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %q, want %q", calls, wantCalls)
	}
	var got mcpsrv.WorktreeSandboxResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		t.Fatalf("stdout not one JSON object: %v (%q)", err, stdout.String())
	}
	if got.WorkspaceID != "wNEW" || got.Branch != "feat/x" || got.SandboxID != "sb-1" || got.Handle != "repo/branch" || got.WorktreePath != "/wt/path" {
		t.Errorf("result = %+v", got)
	}
}
