package handlers

import (
	"context"
	"encoding/json"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

// WorkspaceSetLocalWorktreeParams is the payload for workspace.setLocalWorktree.
type WorkspaceSetLocalWorktreeParams struct {
	// ID is the workspace identifier.
	ID string `json:"id"`
	// LocalWorktreePath is the absolute host-side path of the checked-out worktree.
	LocalWorktreePath string `json:"localWorktreePath"`
	// MutagenSessionID is the name of the mutagen sync session, or empty if none.
	MutagenSessionID string `json:"mutagenSessionId,omitempty"`
}

// HandleWorkspaceSetLocalWorktree stores the local worktree path and optional
// mutagen session ID on the workspace record.
//
// This is called by the nexus CLI after it has set up the local worktree,
// so the daemon record stays in sync with what the CLI has provisioned.
func HandleWorkspaceSetLocalWorktree(
	_ context.Context,
	rawParams json.RawMessage,
	mgr *workspacemgr.Manager,
) (interface{}, *rpckit.RPCError) {
	var params WorkspaceSetLocalWorktreeParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return nil, rpckit.ErrInvalidParams
	}
	if params.ID == "" {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "id is required"}
	}
	if params.LocalWorktreePath == "" {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "localWorktreePath is required"}
	}

	if _, ok := mgr.Get(params.ID); !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	if err := mgr.SetLocalWorktree(params.ID, params.LocalWorktreePath, params.MutagenSessionID); err != nil {
		return nil, &rpckit.RPCError{Code: -32603, Message: "failed to update workspace: " + err.Error()}
	}

	ws, _ := mgr.Get(params.ID)
	return map[string]interface{}{"ok": true, "workspace": ws}, nil
}
