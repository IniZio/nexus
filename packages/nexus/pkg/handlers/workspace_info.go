package handlers

import (
	"path/filepath"

	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

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
			result["workspace_path"] = filepath.Clean(ws.RootPath)
			result["spotlight"] = spotlightMgr.List(workspaceID)
		}
	}

	return result
}
