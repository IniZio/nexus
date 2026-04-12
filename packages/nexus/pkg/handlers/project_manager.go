package handlers

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type ProjectListParams struct{}

type ProjectGetParams struct {
	ID string `json:"id"`
}

type ProjectRemoveParams struct {
	ID string `json:"id"`
}

type ProjectListResult struct {
	Projects []*projectmgr.Project `json:"projects"`
}

type ProjectGetResult struct {
	Project    *projectmgr.Project       `json:"project"`
	Workspaces []*workspacemgr.Workspace `json:"workspaces,omitempty"`
}

type ProjectRemoveResult struct {
	Removed bool `json:"removed"`
}

func HandleProjectList(_ context.Context, _ ProjectListParams, mgr *projectmgr.Manager) (*ProjectListResult, *rpckit.RPCError) {
	all := mgr.List()
	return &ProjectListResult{Projects: all}, nil
}

func HandleProjectGet(_ context.Context, req ProjectGetParams, projMgr *projectmgr.Manager, wsMgr *workspacemgr.Manager) (*ProjectGetResult, *rpckit.RPCError) {
	p, ok := projMgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	// Get workspaces for this project
	var workspaces []*workspacemgr.Workspace
	allWorkspaces := wsMgr.List()
	for _, ws := range allWorkspaces {
		if ws.ProjectID == p.ID {
			workspaces = append(workspaces, ws)
		}
	}

	return &ProjectGetResult{
		Project:    p,
		Workspaces: workspaces,
	}, nil
}

func HandleProjectRemove(_ context.Context, req ProjectRemoveParams, projMgr *projectmgr.Manager, wsMgr *workspacemgr.Manager) (*ProjectRemoveResult, *rpckit.RPCError) {
	// First remove all workspaces in this project
	allWorkspaces := wsMgr.List()
	for _, ws := range allWorkspaces {
		if ws.ProjectID == req.ID {
			_ = wsMgr.Remove(ws.ID)
		}
	}

	removed := projMgr.Remove(req.ID)
	if !removed {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &ProjectRemoveResult{Removed: true}, nil
}
