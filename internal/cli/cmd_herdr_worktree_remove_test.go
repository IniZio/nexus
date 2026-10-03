package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	wt "github.com/IniZio/nexus/internal/herdrworktree"
)

func fakeRemoveRunners(dirty bool, calls *[]string) func() wt.Runners {
	return func() wt.Runners {
		return wt.Runners{
			PollTimeout:  1,
			PollInterval: 1,
			Herdr: func(_ context.Context, _ string, argv ...string) (string, error) {
				*calls = append(*calls, "herdr "+strings.Join(argv, " "))
				if argv[0] == "worktree" && argv[1] == "list" {
					return `{"result":{"worktrees":[{"path":"/wt/path","open_workspace_id":"wNEW"}]}}`, nil
				}
				if dirty && argv[len(argv)-1] != "--force" {
					return `{"error":{"code":"dirty_worktree_requires_force","message":"dirty '/wt/path'"}}`, errors.New("exit 1")
				}
				return "", nil
			},
			Host: func(_ context.Context, argv ...string) (string, error) {
				if argv[0] == "herdr" {
					return "label=x\tworkspace_id=wNEW\thandle=repo/branch\tsandbox_id=sb-1\tpane_id=\n", nil
				}
				return "", nil
			},
			Git: func(_ context.Context, argv ...string) (string, error) {
				return " M a.go\n?? b.go\n", nil
			},
		}
	}
}

func TestHerdrWorktreeRemove_ByPath(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	var calls []string
	worktreeRemoveRunners = fakeRemoveRunners(false, &calls)
	t.Cleanup(func() { worktreeRemoveRunners = wt.DefaultRunners })
	var stdout, stderr bytes.Buffer
	if err := runHerdrWorktreeRemove(context.Background(), []string{"--ref", "/wt/path"}, NewOutput(&stdout, &stderr, false)); err != nil {
		t.Fatalf("err = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		t.Fatalf("stdout: %v (%q)", err, stdout.String())
	}
	if got["removed"] != true || got["workspace_id"] != "wNEW" || got["handle"] != "repo/branch" || got["sandbox_id"] != "sb-1" {
		t.Errorf("result = %v", got)
	}
}

func TestHerdrWorktreeRemove_DirtyWithoutForce(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	var calls []string
	worktreeRemoveRunners = fakeRemoveRunners(true, &calls)
	t.Cleanup(func() { worktreeRemoveRunners = wt.DefaultRunners })
	var stdout, stderr bytes.Buffer
	err := runHerdrWorktreeRemove(context.Background(), []string{"--ref", "repo/branch"}, NewOutput(&stdout, &stderr, false))
	if err == nil {
		t.Fatal("want error")
	}
	var got struct {
		Removed bool     `json:"removed"`
		Error   string   `json:"error"`
		Files   []string `json:"files"`
	}
	if jerr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); jerr != nil {
		t.Fatalf("stdout: %v (%q)", jerr, stdout.String())
	}
	if got.Removed || got.Error != "dirty_worktree" || len(got.Files) != 2 {
		t.Errorf("got %+v", got)
	}
	stdout.Reset()
	if err := runHerdrWorktreeRemove(context.Background(), []string{"--ref", "repo/branch", "--force"}, NewOutput(&stdout, &stderr, false)); err != nil {
		t.Fatalf("force err = %v", err)
	}
}
