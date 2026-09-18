package herdrout

import (
	"encoding/json"
	"strings"
)

// WorktreeCreateWorkspaceID scans herdr worktree create output for the workspace ID (legacy and 0.9.0+ envelopes).
func WorktreeCreateWorkspaceID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var v struct {
			WS     string `json:"ws"`
			Result struct {
				WorkspaceID string `json:"workspace_id"`
				Workspace   struct {
					WorkspaceID string `json:"workspace_id"`
				} `json:"workspace"`
				Worktree struct {
					OpenWorkspaceID string `json:"open_workspace_id"`
				} `json:"worktree"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		if v.WS != "" {
			return v.WS
		}
		if v.Result.Workspace.WorkspaceID != "" {
			return v.Result.Workspace.WorkspaceID
		}
		if v.Result.Worktree.OpenWorkspaceID != "" {
			return v.Result.Worktree.OpenWorkspaceID
		}
		if v.Result.WorkspaceID != "" {
			return v.Result.WorkspaceID
		}
	}
	return ""
}

// WorktreePath returns the checkout path for the given branch from herdr worktree list --json output.
func WorktreePath(out, branch string) string {
	var parsed struct {
		Result struct {
			Worktrees []struct {
				Branch string `json:"branch"`
				Path   string `json:"path"`
			} `json:"worktrees"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &parsed) == nil {
				break
			}
		}
	}
	for _, wt := range parsed.Result.Worktrees {
		if wt.Branch == branch {
			return wt.Path
		}
	}
	return ""
}

func WorktreePathByWorkspaceID(out, workspaceID string) string {
	var parsed struct {
		Result struct {
			Worktrees []struct {
				Path            string `json:"path"`
				OpenWorkspaceID string `json:"open_workspace_id"`
			} `json:"worktrees"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &parsed) == nil {
				break
			}
		}
	}
	for _, wt := range parsed.Result.Worktrees {
		if wt.OpenWorkspaceID == workspaceID {
			return wt.Path
		}
	}
	return ""
}

func ParseHerdrErrorCode(out string) (code, msg string, ok bool) {
	var v struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		if json.Unmarshal([]byte(line), &v) == nil && v.Error.Code != "" {
			return v.Error.Code, v.Error.Message, true
		}
	}
	return "", "", false
}
