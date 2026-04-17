package project

import (
	"context"
	"crypto/sha1"
	"fmt"
	"strings"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
)

type ProjectListParams struct{}

type ProjectCreateParams struct {
	Repo string `json:"repo"`
}

type ProjectGetParams struct {
	ID string `json:"id"`
}

type ProjectRemoveParams struct {
	ID string `json:"id"`
}

type ProjectListResult struct {
	Projects []*Project `json:"projects"`
}

type ProjectCreateResult struct {
	Project *Project `json:"project"`
}

type WorkspaceEntry struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId,omitempty"`
}

type ProjectGetResult struct {
	Project    *Project         `json:"project"`
	Workspaces []WorkspaceEntry `json:"workspaces,omitempty"`
}

type ProjectRemoveResult struct {
	Removed bool `json:"removed"`
}

type WorkspaceManager interface {
	ListEntries() []WorkspaceEntry
	RemoveWithID(id string) error
}

func HandleProjectList(_ context.Context, _ ProjectListParams, mgr *Manager) (*ProjectListResult, *rpckit.RPCError) {
	all := mgr.List()
	return &ProjectListResult{Projects: all}, nil
}

func HandleProjectCreate(_ context.Context, req ProjectCreateParams, mgr *Manager) (*ProjectCreateResult, *rpckit.RPCError) {
	repo := strings.TrimSpace(req.Repo)
	if repo == "" {
		return nil, rpckit.ErrInvalidParams
	}
	project, err := mgr.GetOrCreateForRepo(repo, deriveProjectRepoID(repo))
	if err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("project create failed: %v", err)}
	}
	return &ProjectCreateResult{Project: project}, nil
}

func HandleProjectGet(_ context.Context, req ProjectGetParams, projMgr *Manager, wsMgr WorkspaceManager) (*ProjectGetResult, *rpckit.RPCError) {
	p, ok := projMgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	var workspaces []WorkspaceEntry
	for _, ws := range wsMgr.ListEntries() {
		if ws.ProjectID == p.ID {
			workspaces = append(workspaces, ws)
		}
	}

	return &ProjectGetResult{
		Project:    p,
		Workspaces: workspaces,
	}, nil
}

func HandleProjectRemove(_ context.Context, req ProjectRemoveParams, projMgr *Manager, wsMgr WorkspaceManager) (*ProjectRemoveResult, *rpckit.RPCError) {
	for _, ws := range wsMgr.ListEntries() {
		if ws.ProjectID == req.ID {
			_ = wsMgr.RemoveWithID(ws.ID)
		}
	}

	removed := projMgr.Remove(req.ID)
	if !removed {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &ProjectRemoveResult{Removed: true}, nil
}

func deriveProjectRepoID(repo string) string {
	normalized := strings.ToLower(strings.TrimSpace(repo))
	if normalized == "" {
		return "repo-unknown"
	}
	sum := sha1.Sum([]byte(normalized))
	return fmt.Sprintf("repo-%x", sum[:8])
}
