//go:build e2e

package workspace_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/test/e2e/harness"
)

func makeLocalGitRepo(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nexus-e2e-repo-"+name+"-*")
	if err != nil {
		t.Fatalf("makeLocalGitRepo: mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("makeLocalGitRepo %v: %v\n%s", args, err, out)
		}
	}

	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		cmd2 := exec.Command("git", "init")
		cmd2.Dir = dir
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			t.Fatalf("makeLocalGitRepo git init: %v\n%s", err2, out2)
		}
		_ = out
		run("git", "checkout", "-b", "main")
	}
	run("git", "config", "user.email", "test@test.local")
	run("git", "config", "user.name", "Test")
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# test\n"), 0644); err != nil {
		t.Fatalf("makeLocalGitRepo: write README: %v", err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	return dir
}

func TestWorkspaceFork(t *testing.T) {
	h := harness.New(t)

	repoPath := makeLocalGitRepo(t, "fork")

	// 1. Create base workspace
	var createRes struct {
		Workspace struct {
			ID  string `json:"id"`
			Ref string `json:"ref"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.create", map[string]any{
		"spec": map[string]any{
			"repo":          repoPath,
			"ref":           "main",
			"workspaceName": "fork-base",
		},
	}, &createRes)
	baseID := createRes.Workspace.ID
	if baseID == "" {
		t.Fatal("create: got empty workspace id")
	}
	t.Cleanup(func() {
		var res struct {
			Removed bool `json:"removed"`
		}
		h.MustCall("workspace.remove", map[string]any{"id": baseID}, &res)
	})

	// 2. Fork the base workspace
	var forkRes struct {
		Forked    bool `json:"forked"`
		Workspace struct {
			ID                string `json:"id"`
			ParentWorkspaceID string `json:"parentWorkspaceId"`
			LineageRootID     string `json:"lineageRootId"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.fork", map[string]any{
		"id":                 baseID,
		"childWorkspaceName": "fork-child",
		"childRef":           "feature-branch",
	}, &forkRes)

	if !forkRes.Forked {
		t.Fatal("fork: expected forked=true")
	}
	forkID := forkRes.Workspace.ID
	if forkID == "" {
		t.Fatal("fork: got empty workspace id")
	}
	t.Cleanup(func() {
		var res struct {
			Removed bool `json:"removed"`
		}
		h.MustCall("workspace.remove", map[string]any{"id": forkID}, &res)
	})

	// 3. Verify fork has a different ID
	if forkID == baseID {
		t.Fatalf("fork: expected different id from base %q, got same", baseID)
	}

	// 4. Verify fork references the parent
	if forkRes.Workspace.ParentWorkspaceID != baseID {
		t.Fatalf("fork: expected parentWorkspaceId=%q, got %q", baseID, forkRes.Workspace.ParentWorkspaceID)
	}

	// 5. Verify via workspace.info
	var infoRes struct {
		Workspace struct {
			ID                string `json:"id"`
			ParentWorkspaceID string `json:"parentWorkspaceId"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.info", map[string]any{"id": forkID}, &infoRes)
	if infoRes.Workspace.ParentWorkspaceID != baseID {
		t.Fatalf("info: fork parentWorkspaceId=%q, want %q", infoRes.Workspace.ParentWorkspaceID, baseID)
	}
}
