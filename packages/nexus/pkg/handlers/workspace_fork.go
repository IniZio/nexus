package handlers

import (
	"context"
	"fmt"
	"strings"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func HandleWorkspaceFork(ctx context.Context, req WorkspaceForkParams, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceForkResult, *rpckit.RPCError) {
	requestedParent, ok := mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	forkSource := resolveProjectRootForkSource(mgr, requestedParent)
	if explicitSourceID := strings.TrimSpace(req.SourceWorkspaceID); explicitSourceID != "" {
		explicitSource, explicitOK := mgr.Get(explicitSourceID)
		if !explicitOK || explicitSource == nil {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		if strings.TrimSpace(explicitSource.ProjectID) != strings.TrimSpace(requestedParent.ProjectID) ||
			strings.TrimSpace(explicitSource.RepoID) != strings.TrimSpace(requestedParent.RepoID) {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "sourceWorkspaceId must belong to the same project and repo"}
		}
		forkSource = explicitSource
	}
	child, err := mgr.Fork(forkSource.ID, req.ChildWorkspaceName, req.ChildRef)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "workspace not found") {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: err.Error()}
	}

	if factory != nil {
		parent, ok := mgr.Get(forkSource.ID)
		if !ok {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		driver, selErr := selectDriverForWorkspaceBackend(factory, parent.Backend)
		if selErr != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", selErr)}
		}
		if forkErr := driver.Fork(context.Background(), parent.ID, child.ID); forkErr != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime fork failed: %v", forkErr)}
		}
		if snapshotter, ok := driver.(runtime.ForkSnapshotter); ok {
			if snapshotID, snapErr := snapshotter.CheckpointFork(ctx, parent.ID, child.ID); snapErr != nil {
				return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime fork checkpoint failed: %v", snapErr)}
			} else if strings.TrimSpace(snapshotID) != "" {
				if setErr := mgr.SetLineageSnapshot(child.ID, snapshotID); setErr != nil {
					return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("workspace snapshot persist failed: %v", setErr)}
				}
			}
		}
	}

	updatedChild, ok := mgr.Get(child.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	enrichWorkspaceRuntimeLabel(updatedChild)
	return &WorkspaceForkResult{Forked: true, Workspace: updatedChild}, nil
}

func resolveProjectRootForkSource(mgr *workspacemgr.Manager, requestedParent *workspacemgr.Workspace) *workspacemgr.Workspace {
	if mgr == nil || requestedParent == nil {
		return requestedParent
	}
	candidates := make([]*workspacemgr.Workspace, 0, 4)
	for _, ws := range mgr.List() {
		if ws == nil {
			continue
		}
		if strings.TrimSpace(ws.ProjectID) != strings.TrimSpace(requestedParent.ProjectID) {
			continue
		}
		if strings.TrimSpace(ws.RepoID) != strings.TrimSpace(requestedParent.RepoID) {
			continue
		}
		if strings.TrimSpace(ws.ParentWorkspaceID) != "" {
			continue
		}
		candidates = append(candidates, ws)
	}
	if len(candidates) == 0 {
		return requestedParent
	}
	best := candidates[0]
	for _, ws := range candidates[1:] {
		if ws.CreatedAt.Before(best.CreatedAt) {
			best = ws
		}
	}
	return best
}
