package server

import (
	"context"
	"encoding/json"

	"github.com/inizio/nexus/packages/nexus/pkg/credentials"
	"github.com/inizio/nexus/packages/nexus/pkg/daemon"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

func (s *Server) newRPCRegistry() *rpc.Registry {
	r := rpc.NewRegistry()
	registerFsRPCs(r, s)
	registerWorkspaceRPCs(r, s)
	registerWorkspaceReadyRPC(r, s)
	registerPortsTunnelsRPCs(r, s)
	registerProjectRPCs(r, s)
	registerDaemonRPCs(r, s)
	registerSpotlightRPCs(r, s)
	registerPTYRPCs(r, s)
	registerMiscRPCs(r, s)
	return r
}

func registerMiscRPCs(r *rpc.Registry, s *Server) {
	rpc.TypedRegister(r, "exec", func(ctx context.Context, req workspace.ExecParams) (*workspace.ExecResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleExecWithAuthRelay(ctx, req, ws, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "authrelay.mint", func(ctx context.Context, req credentials.AuthRelayMintParams) (*credentials.AuthRelayMintResult, *rpckit.RPCError) {
		return credentials.HandleAuthRelayMint(ctx, req, s.workspaceMgr, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "authrelay.revoke", func(ctx context.Context, req credentials.AuthRelayRevokeParams) (*credentials.AuthRelayRevokeResult, *rpckit.RPCError) {
		return credentials.HandleAuthRelayRevoke(ctx, req, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "git.command", func(ctx context.Context, req workspace.GitCommandParams) (map[string]interface{}, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleGitCommand(ctx, req, ws)
	})
	rpc.TypedRegister(r, "service.command", func(ctx context.Context, req daemon.ServiceCommandParams) (map[string]interface{}, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return daemon.HandleServiceCommand(ctx, req, ws, s.serviceMgr)
	})
}

func registerPortsTunnelsRPCs(r *rpc.Registry, s *Server) {
	rpc.TypedRegister(r, "workspace.ports.list", func(_ context.Context, req struct {
		WorkspaceID string `json:"workspaceId"`
	}) (map[string]any, *rpckit.RPCError) {
		if req.WorkspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		items, activeWorkspaceID := s.WorkspacePortStates(req.WorkspaceID)
		return map[string]any{
			"items":             items,
			"activeWorkspaceId": activeWorkspaceID,
		}, nil
	})
	rpc.TypedRegister(r, "workspace.ports.add", func(_ context.Context, req struct {
		WorkspaceID string `json:"workspaceId"`
		Port        int    `json:"port"`
	}) (map[string]any, *rpckit.RPCError) {
		if req.WorkspaceID == "" || req.Port <= 0 || req.Port > 65535 {
			return nil, rpckit.ErrInvalidParams
		}
		if err := s.SetWorkspaceTunnelPreference(req.WorkspaceID, req.Port, true); err != nil {
			return nil, rpckit.ErrInvalidParams
		}
		items, activeWorkspaceID := s.WorkspacePortStates(req.WorkspaceID)
		return map[string]any{"items": items, "activeWorkspaceId": activeWorkspaceID}, nil
	})
	rpc.TypedRegister(r, "workspace.ports.remove", func(_ context.Context, req struct {
		WorkspaceID string `json:"workspaceId"`
		Port        int    `json:"port"`
	}) (map[string]any, *rpckit.RPCError) {
		if req.WorkspaceID == "" || req.Port <= 0 || req.Port > 65535 {
			return nil, rpckit.ErrInvalidParams
		}
		if err := s.SetWorkspaceTunnelPreference(req.WorkspaceID, req.Port, false); err != nil {
			return nil, rpckit.ErrInvalidParams
		}
		items, activeWorkspaceID := s.WorkspacePortStates(req.WorkspaceID)
		return map[string]any{"items": items, "activeWorkspaceId": activeWorkspaceID}, nil
	})
	rpc.TypedRegister(r, "workspace.tunnels.start", func(_ context.Context, req struct {
		WorkspaceID string `json:"workspaceId"`
	}) (map[string]any, *rpckit.RPCError) {
		if req.WorkspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		if err := s.StartWorkspaceTunnels(req.WorkspaceID); err != nil {
			return map[string]any{
				"active":            false,
				"activeWorkspaceId": "",
			}, nil
		}
		return map[string]any{
			"active":            true,
			"activeWorkspaceId": req.WorkspaceID,
		}, nil
	})
	rpc.TypedRegister(r, "workspace.tunnels.stop", func(_ context.Context, req struct {
		WorkspaceID string `json:"workspaceId"`
	}) (map[string]any, *rpckit.RPCError) {
		if req.WorkspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		s.StopWorkspaceTunnels(req.WorkspaceID)
		return map[string]any{
			"active":            false,
			"activeWorkspaceId": "",
		}, nil
	})
}

func registerWorkspaceReadyRPC(r *rpc.Registry, s *Server) {
	rpc.TypedRegister(r, "workspace.ready", func(ctx context.Context, req workspace.WorkspaceReadyParams) (*workspace.WorkspaceReadyResult, *rpckit.RPCError) {
		raw, _ := json.Marshal(req)
		workspaceID := extractWorkspaceID(raw)
		if workspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		if accessErr := s.requireWorkspaceStarted(workspaceID); accessErr != nil {
			return nil, accessErr
		}
		ws := s.resolveWorkspace(raw)
		rootPath := ws.Path()
		if wsRecord, ok := s.workspaceMgr.Get(workspaceID); ok {
			if preferred := preferredWorkspaceRoot(wsRecord); preferred != "" {
				rootPath = preferred
			}
		}
		s.ensureComposeHints(ctx, workspaceID, rootPath)
		return workspace.HandleWorkspaceReady(ctx, req, ws, s.serviceMgr)
	})
}
