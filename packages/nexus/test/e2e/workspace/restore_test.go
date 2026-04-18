//go:build e2e

package workspace_test

import (
	"testing"

	"github.com/inizio/nexus/packages/nexus/test/e2e/harness"
)

// TestWorkspaceRestore verifies that a stopped workspace can be restored via
// workspace.restore and ends up in the restored state.
//
// Note: workspace.restore resumes from a snapshot taken during workspace.stop
// (the daemon persists VM state on stop). There is no separate snapshot RPC.
func TestWorkspaceRestore(t *testing.T) {
	h := harness.New(t)

	repoPath := makeLocalGitRepo(t, "restore")

	// 1. Create
	var createRes struct {
		Workspace struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.create", map[string]any{
		"spec": map[string]any{
			"repo":          repoPath,
			"ref":           "main",
			"workspaceName": "restore-test",
		},
	}, &createRes)
	id := createRes.Workspace.ID
	if id == "" {
		t.Fatal("create: got empty workspace id")
	}

	// 2. Start
	var startRes struct {
		Workspace struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.start", map[string]any{"id": id}, &startRes)

	// 3. Verify running
	var infoRes struct {
		Workspace struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.info", map[string]any{"id": id}, &infoRes)
	if infoRes.Workspace.State != "running" {
		t.Fatalf("after start: expected state running, got %q", infoRes.Workspace.State)
	}

	// 4. Stop (persists snapshot state)
	var stopRes struct {
		Stopped bool `json:"stopped"`
	}
	h.MustCall("workspace.stop", map[string]any{"id": id}, &stopRes)
	if !stopRes.Stopped {
		t.Fatal("stop: expected stopped=true")
	}

	// 5. Restore from snapshot
	var restoreRes struct {
		Restored  bool `json:"restored"`
		Workspace struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"workspace"`
	}
	h.MustCall("workspace.restore", map[string]any{"id": id}, &restoreRes)
	if !restoreRes.Restored {
		t.Fatal("restore: expected restored=true")
	}

	// 6. Verify state after restore — daemon sets state to "restored"
	h.MustCall("workspace.info", map[string]any{"id": id}, &infoRes)
	if infoRes.Workspace.State != "restored" {
		t.Fatalf("after restore: expected state restored, got %q", infoRes.Workspace.State)
	}

	// 7. Cleanup
	h.MustCall("workspace.stop", map[string]any{"id": id}, &stopRes)
	var removeRes struct {
		Removed bool `json:"removed"`
	}
	h.MustCall("workspace.remove", map[string]any{"id": id}, &removeRes)
	if !removeRes.Removed {
		t.Fatal("remove: expected removed=true")
	}
}
