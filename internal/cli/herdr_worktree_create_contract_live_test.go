//go:build herdr_live

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/herdrout"
)

func TestHerdrContract_WorktreeCreateIsolated(t *testing.T) {
	probeHome, sess := startIsolatedHerdr(t)

	if _, err := exec.LookPath("git"); err != nil {
		liveSkip(t, "git not found on PATH: %v", err)
	}

	repoDir := t.TempDir()
	gitCmds := [][]string{
		{"git", "-C", repoDir, "init"},
		{"git", "-C", repoDir, "config", "user.email", "test@example.com"},
		{"git", "-C", repoDir, "config", "user.name", "Test"},
	}
	initFile := filepath.Join(repoDir, "README")
	if err := os.WriteFile(initFile, []byte("init\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitCmds = append(gitCmds,
		[]string{"git", "-C", repoDir, "add", "."},
		[]string{"git", "-C", repoDir, "commit", "-m", "init"},
	)
	for _, args := range gitCmds {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	wsCreateOut, err := herdrRun(probeHome, sess, "workspace", "create", "--cwd", repoDir, "--no-focus")
	t.Logf("workspace create output:\n%s", wsCreateOut)
	if err != nil {
		t.Fatalf("herdr workspace create: %v\n%s", err, wsCreateOut)
	}

	wsID := extractWorkspaceCreateID(t, wsCreateOut)
	if wsID == "" {
		t.Fatalf("could not extract workspace_id from workspace create output:\n%s", wsCreateOut)
	}
	t.Logf("workspace_id = %s", wsID)

	createOut, err := herdrRun(probeHome, sess, "worktree", "create", "--workspace", wsID, "--branch", "contract-probe", "--no-focus")
	t.Logf("worktree create output:\n%s", createOut)
	if err != nil {
		t.Fatalf("herdr worktree create: %v\n%s", err, createOut)
	}

	gotWS := herdrout.WorktreeCreateWorkspaceID(string(createOut))
	if gotWS == "" {
		t.Fatalf("WorktreeCreateWorkspaceID returned empty for output:\n%s", createOut)
	}
	t.Logf("WorktreeCreateWorkspaceID = %s", gotWS)

	t.Cleanup(func() {
		rmOut, rmErr := herdrRun(probeHome, sess, "worktree", "remove", "--workspace", gotWS, "--force")
		t.Logf("worktree remove --workspace %s --force: err=%v\n%s", gotWS, rmErr, rmOut)
		closeOut, closeErr := herdrRun(probeHome, sess, "workspace", "close", wsID, "--group")
		t.Logf("workspace close --group %s: err=%v\n%s", wsID, closeErr, closeOut)
	})

	listOut, err := herdrRun(probeHome, sess, "worktree", "list", "--json")
	t.Logf("worktree list output:\n%s", listOut)
	if err != nil {
		t.Fatalf("herdr worktree list --json: %v\n%s", err, listOut)
	}

	openWSID := extractOpenWorkspaceID(t, listOut, "contract-probe")
	if openWSID == "" {
		t.Fatalf("could not find open_workspace_id for contract-probe in:\n%s", listOut)
	}
	if gotWS != openWSID {
		t.Errorf("WorktreeCreateWorkspaceID=%q, want open_workspace_id=%q from list", gotWS, openWSID)
	}

	wtPath := herdrout.WorktreePath(string(listOut), "contract-probe")
	if wtPath == "" {
		t.Fatalf("WorktreePath returned empty for contract-probe in:\n%s", listOut)
	}
	t.Logf("WorktreePath = %s", wtPath)
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree path %q does not exist on disk: %v", wtPath, err)
	}
}

func extractWorkspaceCreateID(t *testing.T, raw []byte) string {
	t.Helper()
	var env struct {
		Result struct {
			Workspace struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspace"`
		} `json:"result"`
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &env) == nil {
			if env.Result.Workspace.WorkspaceID != "" {
				return env.Result.Workspace.WorkspaceID
			}
		}
	}
	return ""
}

func extractOpenWorkspaceID(t *testing.T, raw []byte, branch string) string {
	t.Helper()
	var parsed struct {
		Result struct {
			Worktrees []struct {
				Branch          string `json:"branch"`
				OpenWorkspaceID string `json:"open_workspace_id"`
			} `json:"worktrees"`
		} `json:"result"`
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &parsed) == nil {
			break
		}
	}
	for _, wt := range parsed.Result.Worktrees {
		if wt.Branch == branch {
			return wt.OpenWorkspaceID
		}
	}
	return ""
}
