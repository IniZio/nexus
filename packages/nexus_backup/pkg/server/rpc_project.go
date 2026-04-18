package server

import (
	"context"

	"github.com/inizio/nexus/packages/nexus/pkg/project"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func registerProjectRPCs(r *rpc.Registry, s *Server) {
	rpc.TypedRegister(r, "project.list", func(ctx context.Context, req project.ProjectListParams) (*project.ProjectListResult, *rpckit.RPCError) {
		if s.projectMgr == nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "project manager unavailable"}
		}
		return project.HandleProjectList(ctx, req, s.projectMgr)
	})
	rpc.TypedRegister(r, "project.create", func(ctx context.Context, req project.ProjectCreateParams) (*project.ProjectCreateResult, *rpckit.RPCError) {
		if s.projectMgr == nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "project manager unavailable"}
		}
		return project.HandleProjectCreate(ctx, req, s.projectMgr)
	})
	rpc.TypedRegister(r, "project.get", func(ctx context.Context, req project.ProjectGetParams) (*project.ProjectGetResult, *rpckit.RPCError) {
		if s.projectMgr == nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "project manager unavailable"}
		}
		return project.HandleProjectGet(ctx, req, s.projectMgr, wsManagerAdapter{s.workspaceMgr})
	})
	rpc.TypedRegister(r, "project.remove", func(ctx context.Context, req project.ProjectRemoveParams) (*project.ProjectRemoveResult, *rpckit.RPCError) {
		if s.projectMgr == nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "project manager unavailable"}
		}
		return project.HandleProjectRemove(ctx, req, s.projectMgr, wsManagerAdapter{s.workspaceMgr})
	})
}

type wsManagerAdapter struct{ mgr *workspacemgr.Manager }

func (a wsManagerAdapter) ListEntries() []project.WorkspaceEntry {
	all := a.mgr.List()
	entries := make([]project.WorkspaceEntry, 0, len(all))
	for _, ws := range all {
		entries = append(entries, project.WorkspaceEntry{ID: ws.ID, ProjectID: ws.ProjectID})
	}
	return entries
}

func (a wsManagerAdapter) RemoveWithID(id string) error {
	_, err := a.mgr.RemoveWithOptions(id, workspacemgr.RemoveOptions{DeleteHostPath: false})
	return err
}
