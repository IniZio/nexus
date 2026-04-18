//go:build e2e

package pty_test

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

func TestPTY(t *testing.T) {
	h := harness.New(t)

	repoPath := makeLocalGitRepo(t, "pty")

	// Create a workspace first — pty.create requires a workspaceId.
	var createRes struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.create", map[string]any{
		"spec": map[string]any{
			"repo":          repoPath,
			"ref":           "main",
			"workspaceName": "pty-test",
		},
	}, &createRes)
	wsID := createRes.Workspace.ID
	if wsID == "" {
		t.Fatal("workspace.create: got empty workspace id")
	}
	t.Cleanup(func() {
		_ = h.Call("workspace.remove", map[string]any{"id": wsID}, nil)
	})

	// Create a PTY session.
	var sessionRes struct {
		ID   string `json:"id"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	h.MustCall("pty.create", map[string]any{
		"workspaceId": wsID,
		"name":        "test-session",
		"cols":        80,
		"rows":        24,
	}, &sessionRes)
	sessionID := sessionRes.ID
	if sessionID == "" {
		t.Fatal("pty.create: got empty session id")
	}
	t.Cleanup(func() {
		var closeRes struct {
			Ok bool `json:"ok"`
		}
		if err := h.Call("pty.close", map[string]any{"sessionId": sessionID}, &closeRes); err != nil {
			t.Errorf("pty.close cleanup: %v", err)
			return
		}
		if !closeRes.Ok {
			t.Error("pty.close: expected ok=true")
		}
	})

	// Verify session appears in list.
	// pty.list returns a raw JSON array, not an envelope.
	var listRes []struct {
		ID string `json:"id"`
	}
	h.MustCall("pty.list", map[string]any{"workspaceId": wsID}, &listRes)
	found := false
	for _, s := range listRes {
		if s.ID == sessionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("pty.list: session %s not found after create", sessionID)
	}

	// PTY I/O (write/read) is not yet exposed via RPC — skip that portion.
	// close lifecycle is validated in t.Cleanup above.
	t.Skip("pty write/read not yet implemented via RPC; session lifecycle verified above")
}
