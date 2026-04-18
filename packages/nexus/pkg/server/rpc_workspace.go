package server

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

func registerWorkspaceRPCs(r *rpc.Registry, s *Server) {
	rpc.TypedRegister(r, "workspace.info", func(_ context.Context, req workspace.WorkspaceInfoParams) (map[string]interface{}, *rpckit.RPCError) {
		wid := workspace.WorkspaceInfoWorkspaceID(req)
		return workspace.HandleWorkspaceInfo(wid, s.ws, s.workspaceMgr, s.spotlightMgr), nil
	})
	rpc.TypedRegister(r, "workspace.create", func(ctx context.Context, req workspace.WorkspaceCreateParams) (*workspace.WorkspaceCreateResult, *rpckit.RPCError) {
		return workspace.HandleWorkspaceCreateWithProjects(ctx, req, s.workspaceMgr, s.projectMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.list", func(ctx context.Context, req workspace.WorkspaceListParams) (*workspace.WorkspaceListResult, *rpckit.RPCError) {
		return workspace.HandleWorkspaceList(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.relations.list", func(ctx context.Context, req workspace.WorkspaceRelationsListParams) (*workspace.WorkspaceRelationsListResult, *rpckit.RPCError) {
		return workspace.HandleWorkspaceRelationsList(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.remove", func(ctx context.Context, req workspace.WorkspaceRemoveParams) (*workspace.WorkspaceRemoveResult, *rpckit.RPCError) {
		result, rpcErr := workspace.HandleWorkspaceRemove(ctx, req, s.workspaceMgr, s.runtimeFactory)
		if rpcErr == nil {
			s.StopWorkspaceTunnels(req.ID)
		}
		return result, rpcErr
	})
	rpc.TypedRegister(r, "workspace.stop", func(ctx context.Context, req workspace.WorkspaceStopParams) (*workspace.WorkspaceStopResult, *rpckit.RPCError) {
		result, rpcErr := workspace.HandleWorkspaceStopWithRuntime(ctx, req, s.workspaceMgr, s.runtimeFactory)
		if rpcErr == nil {
			s.StopPortMonitoring(req.ID)
			s.StopWorkspaceTunnels(req.ID)
		}
		return result, rpcErr
	})
	rpc.TypedRegister(r, "workspace.start", func(ctx context.Context, req workspace.WorkspaceStartParams) (*workspace.WorkspaceStartResult, *rpckit.RPCError) {
		result, rpcErr := workspace.HandleWorkspaceStart(ctx, req, s.workspaceMgr, s.runtimeFactory)
		if rpcErr == nil {
			_ = s.StartPortMonitoring(req.ID)
		}
		return result, rpcErr
	})
	rpc.TypedRegister(r, "workspace.restore", func(ctx context.Context, req workspace.WorkspaceRestoreParams) (*workspace.WorkspaceRestoreResult, *rpckit.RPCError) {
		result, rpcErr := workspace.HandleWorkspaceRestore(ctx, req, s.workspaceMgr, s.runtimeFactory)
		if rpcErr == nil {
			_ = s.StartPortMonitoring(req.ID)
		}
		return result, rpcErr
	})
	rpc.TypedRegister(r, "workspace.fork", func(ctx context.Context, req workspace.WorkspaceForkParams) (*workspace.WorkspaceForkResult, *rpckit.RPCError) {
		return workspace.HandleWorkspaceFork(ctx, req, s.workspaceMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.checkout", func(ctx context.Context, req workspace.WorkspaceCheckoutParams) (*workspace.WorkspaceCheckoutResult, *rpckit.RPCError) {
		return workspace.HandleWorkspaceCheckout(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.setLocalWorktree", func(ctx context.Context, req workspace.WorkspaceSetLocalWorktreeParams) (interface{}, *rpckit.RPCError) {
		return workspace.HandleWorkspaceSetLocalWorktree(ctx, req, s.workspaceMgr)
	})
}
