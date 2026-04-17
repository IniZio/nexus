package workspace

import (
	"context"
	"sort"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type WorkspaceRelationsListParams struct {
	RepoID string `json:"repoId,omitempty"`
}

type WorkspaceRelationNode struct {
	WorkspaceID       string                      `json:"workspaceId"`
	ParentWorkspaceID string                      `json:"parentWorkspaceId,omitempty"`
	LineageRootID     string                      `json:"lineageRootId,omitempty"`
	DerivedFromRef    string                      `json:"derivedFromRef,omitempty"`
	WorktreeRef       string                      `json:"worktreeRef,omitempty"`
	State             workspacemgr.WorkspaceState `json:"state"`
	Backend           string                      `json:"backend,omitempty"`
	WorkspaceName     string                      `json:"workspaceName"`
	RootPath          string                      `json:"rootPath"`
	LocalWorktreePath string                      `json:"localWorktreePath,omitempty"`
	CreatedAt         string                      `json:"createdAt"`
	UpdatedAt         string                      `json:"updatedAt"`
}

type WorkspaceRelationsGroup struct {
	RepoID       string                  `json:"repoId"`
	RepoKind     string                  `json:"repoKind,omitempty"`
	Repo         string                  `json:"repo"`
	DisplayName  string                  `json:"displayName"`
	RemoteURL    string                  `json:"remoteUrl,omitempty"`
	Nodes        []WorkspaceRelationNode `json:"nodes"`
	LineageRoots []string                `json:"lineageRoots"`
}

type WorkspaceRelationsListResult struct {
	Relations []WorkspaceRelationsGroup `json:"relations"`
}

func HandleWorkspaceRelationsList(_ context.Context, p WorkspaceRelationsListParams, mgr *workspacemgr.Manager) (*WorkspaceRelationsListResult, *rpckit.RPCError) {
	all := mgr.List()
	groups := make(map[string]*WorkspaceRelationsGroup)

	for _, ws := range all {
		repoID := ws.RepoID
		if repoID == "" {
			repoID = "repo-unknown"
		}
		if p.RepoID != "" && repoID != p.RepoID {
			continue
		}

		group, ok := groups[repoID]
		if !ok {
			group = &WorkspaceRelationsGroup{
				RepoID:      repoID,
				RepoKind:    ws.RepoKind,
				Repo:        ws.Repo,
				DisplayName: ws.WorkspaceName,
				RemoteURL:   remoteURLForKind(ws.Repo, ws.RepoKind),
				Nodes:       make([]WorkspaceRelationNode, 0),
			}
			groups[repoID] = group
		}

		group.Nodes = append(group.Nodes, WorkspaceRelationNode{
			WorkspaceID:       ws.ID,
			ParentWorkspaceID: ws.ParentWorkspaceID,
			LineageRootID:     ws.LineageRootID,
			DerivedFromRef:    ws.DerivedFromRef,
			WorktreeRef:       ws.Ref,
			State:             ws.State,
			Backend:           ws.Backend,
			WorkspaceName:     ws.WorkspaceName,
			RootPath:          ws.RootPath,
			LocalWorktreePath: ws.LocalWorktreePath,
			CreatedAt:         ws.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpdatedAt:         ws.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	result := make([]WorkspaceRelationsGroup, 0, len(groups))
	for _, group := range groups {
		roots := make(map[string]struct{})
		for _, node := range group.Nodes {
			if node.LineageRootID == "" {
				roots[node.WorkspaceID] = struct{}{}
				continue
			}
			roots[node.LineageRootID] = struct{}{}
		}
		group.LineageRoots = make([]string, 0, len(roots))
		for id := range roots {
			group.LineageRoots = append(group.LineageRoots, id)
		}
		sort.Strings(group.LineageRoots)
		sort.Slice(group.Nodes, func(i, j int) bool {
			return group.Nodes[i].CreatedAt < group.Nodes[j].CreatedAt
		})
		result = append(result, *group)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].RepoID < result[j].RepoID
	})

	return &WorkspaceRelationsListResult{Relations: result}, nil
}

func remoteURLForKind(repo, kind string) string {
	if kind == "hosted" {
		return repo
	}
	return ""
}
