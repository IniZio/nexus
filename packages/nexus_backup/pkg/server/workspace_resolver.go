package server

import (
	"encoding/json"

	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

func (s *Server) resolveWorkspace(params json.RawMessage) *workspace.Workspace {
	workspaceID := extractWorkspaceID(params)
	if workspaceID == "" {
		return s.ws
	}

	wsRecord, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return s.ws
	}

	resolvedPath := preferredWorkspaceRoot(wsRecord)
	if resolvedPath == "" {
		return s.ws
	}

	resolved, err := workspace.NewWorkspace(resolvedPath)
	if err != nil {
		return s.ws
	}

	return resolved
}

func (s *Server) resolveWorkspaceTyped(v any) *workspace.Workspace {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return s.ws
	}
	return s.resolveWorkspace(raw)
}

func extractWorkspaceID(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(params, &payload); err != nil {
		return ""
	}

	if id, ok := payload["workspaceId"].(string); ok {
		return id
	}

	if rawSpec, ok := payload["spec"].(map[string]any); ok {
		if id, ok := rawSpec["workspaceId"].(string); ok {
			return id
		}
	}

	if id, ok := payload["id"].(string); ok {
		return id
	}

	return ""
}
