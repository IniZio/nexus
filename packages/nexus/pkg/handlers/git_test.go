package handlers

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

func createGitWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	root := t.TempDir()
	ws, err := workspace.NewWorkspace(root)
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}

	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = ws.Path()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Test User")

	file := filepath.Join(ws.Path(), "README.md")
	if err := os.WriteFile(file, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	run("git", "add", "README.md")
	run("git", "commit", "-m", "init")

	return ws
}

func TestHandleGitCommand_Status(t *testing.T) {
	ws := createGitWorkspace(t)
	params, _ := json.Marshal(map[string]any{
		"action": "status",
	})

	result, rpcErr := HandleGitCommand(context.Background(), params, ws)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	out, _ := result["stdout"].(string)
	if !strings.Contains(out, "##") {
		t.Fatalf("expected git status output, got %q", out)
	}
}

func TestHandleGitCommand_RevParse(t *testing.T) {
	ws := createGitWorkspace(t)
	params, _ := json.Marshal(map[string]any{
		"action": "revParse",
		"params": map[string]any{"ref": "HEAD"},
	})

	result, rpcErr := HandleGitCommand(context.Background(), params, ws)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	out, _ := result["stdout"].(string)
	if len(strings.TrimSpace(out)) == 0 {
		t.Fatal("expected non-empty rev-parse output")
	}
}
