package handlers

import (
	"path/filepath"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type WorkspaceInfoParams struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
	ID          string `json:"id,omitempty"`
	Spec        *struct {
		WorkspaceID string `json:"workspaceId"`
	} `json:"spec,omitempty"`
}

func WorkspaceInfoWorkspaceID(p WorkspaceInfoParams) string {
	if p.WorkspaceID != "" {
		return p.WorkspaceID
	}
	if p.Spec != nil && p.Spec.WorkspaceID != "" {
		return p.Spec.WorkspaceID
	}
	return p.ID
}

func HandleWorkspaceInfo(
	workspaceID string,
	defaultWS *workspace.Workspace,
	workspaceMgr *workspacemgr.Manager,
	spotlightMgr *spotlight.Manager,
) map[string]interface{} {
	result := map[string]interface{}{
		"workspace_id":   defaultWS.ID(),
		"workspace_path": defaultWS.Path(),
		"workspaces":     workspaceMgr.List(),
		"spotlight":      spotlightMgr.List(""),
	}

	if workspaceID != "" {
		if ws, ok := workspaceMgr.Get(workspaceID); ok {
			result["workspace"] = ws
			result["workspace_id"] = ws.ID
			// Prefer the host-side worktree path (the user-visible directory)
			// over the daemon's internal storage path.
			hostPath := strings.TrimSpace(ws.LocalWorktreePath)
			if hostPath == "" {
				hostPath = ws.RootPath
			}
			result["workspace_path"] = filepath.Clean(hostPath)
			result["spotlight"] = spotlightMgr.List(workspaceID)
		}
	}

	return result
}
