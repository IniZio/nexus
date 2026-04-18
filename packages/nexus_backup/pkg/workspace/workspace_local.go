package workspace

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type WorkspaceSetLocalWorktreeParams struct {
	ID                string `json:"id"`
	LocalWorktreePath string `json:"localWorktreePath"`
	MutagenSessionID  string `json:"mutagenSessionId,omitempty"`
}

func HandleWorkspaceSetLocalWorktree(
	_ context.Context,
	params WorkspaceSetLocalWorktreeParams,
	mgr *workspacemgr.Manager,
) (interface{}, *rpckit.RPCError) {
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

	if params.MutagenSessionID != "" {
		existing := mgr.List()
		for _, candidate := range existing {
			if candidate == nil || candidate.ID == params.ID {
				continue
			}
			if candidate.MutagenSessionID == params.MutagenSessionID {
				_ = mgr.SetLocalWorktree(candidate.ID, candidate.LocalWorktreePath, "")
			}
		}
	}

	ws, _ := mgr.Get(params.ID)
	return map[string]interface{}{"ok": true, "workspace": ws}, nil
}
