package server

import (
	"context"
	"encoding/json"

	"github.com/inizio/nexus/packages/nexus/pkg/handlers"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
)

func (s *Server) newRPCRegistry() *rpc.Registry {
	r := rpc.NewRegistry()

	rpc.TypedRegister(r, "fs.readFile", func(ctx context.Context, req handlers.ReadFileParams) (*handlers.ReadFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleReadFile(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.writeFile", func(ctx context.Context, req handlers.WriteFileParams) (*handlers.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleWriteFile(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.exists", func(ctx context.Context, req handlers.ExistsParams) (*handlers.ExistsResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleExists(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.readdir", func(ctx context.Context, req handlers.ReaddirParams) (*handlers.ReaddirResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleReaddir(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.mkdir", func(ctx context.Context, req handlers.MkdirParams) (*handlers.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleMkdir(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.rm", func(ctx context.Context, req handlers.RmParams) (*handlers.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleRm(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.stat", func(ctx context.Context, req handlers.StatParams) (*handlers.StatResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleStat(ctx, req, ws)
	})
	rpc.TypedRegister(r, "exec", func(ctx context.Context, req handlers.ExecParams) (*handlers.ExecResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleExecWithAuthRelay(ctx, req, ws, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "authrelay.mint", func(ctx context.Context, req handlers.AuthRelayMintParams) (*handlers.AuthRelayMintResult, *rpckit.RPCError) {
		return handlers.HandleAuthRelayMint(ctx, req, s.workspaceMgr, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "authrelay.revoke", func(ctx context.Context, req handlers.AuthRelayRevokeParams) (*handlers.AuthRelayRevokeResult, *rpckit.RPCError) {
		return handlers.HandleAuthRelayRevoke(ctx, req, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "workspace.info", func(_ context.Context, req handlers.WorkspaceInfoParams) (map[string]interface{}, *rpckit.RPCError) {
		wid := handlers.WorkspaceInfoWorkspaceID(req)
		return handlers.HandleWorkspaceInfo(wid, s.ws, s.workspaceMgr, s.spotlightMgr), nil
	})
	rpc.TypedRegister(r, "workspace.create", func(ctx context.Context, req handlers.WorkspaceCreateParams) (*handlers.WorkspaceCreateResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceCreateWithProjects(ctx, req, s.workspaceMgr, s.projectMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "daemon.settings.get", func(ctx context.Context, req handlers.DaemonSettingsGetParams) (*handlers.DaemonSettingsGetResult, *rpckit.RPCError) {
		return handlers.HandleDaemonSettingsGet(ctx, req, s.workspaceMgr.SandboxResourceSettingsRepository())
	})
	rpc.TypedRegister(r, "daemon.settings.update", func(ctx context.Context, req handlers.DaemonSettingsUpdateParams) (*handlers.DaemonSettingsUpdateResult, *rpckit.RPCError) {
		return handlers.HandleDaemonSettingsUpdate(ctx, req, s.workspaceMgr.SandboxResourceSettingsRepository())
	})
	rpc.TypedRegister(r, "project.list", func(ctx context.Context, req handlers.ProjectListParams) (*handlers.ProjectListResult, *rpckit.RPCError) {
		if s.projectMgr == nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "project manager unavailable"}
		}
		return handlers.HandleProjectList(ctx, req, s.projectMgr)
	})
	rpc.TypedRegister(r, "project.create", func(ctx context.Context, req handlers.ProjectCreateParams) (*handlers.ProjectCreateResult, *rpckit.RPCError) {
		if s.projectMgr == nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "project manager unavailable"}
		}
		return handlers.HandleProjectCreate(ctx, req, s.projectMgr)
	})
	rpc.TypedRegister(r, "project.get", func(ctx context.Context, req handlers.ProjectGetParams) (*handlers.ProjectGetResult, *rpckit.RPCError) {
		if s.projectMgr == nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "project manager unavailable"}
		}
		return handlers.HandleProjectGet(ctx, req, s.projectMgr, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "project.remove", func(ctx context.Context, req handlers.ProjectRemoveParams) (*handlers.ProjectRemoveResult, *rpckit.RPCError) {
		if s.projectMgr == nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "project manager unavailable"}
		}
		return handlers.HandleProjectRemove(ctx, req, s.projectMgr, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.list", func(ctx context.Context, req handlers.WorkspaceListParams) (*handlers.WorkspaceListResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceList(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.relations.list", func(ctx context.Context, req handlers.WorkspaceRelationsListParams) (*handlers.WorkspaceRelationsListResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceRelationsList(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.remove", func(ctx context.Context, req handlers.WorkspaceRemoveParams) (*handlers.WorkspaceRemoveResult, *rpckit.RPCError) {
		result, rpcErr := handlers.HandleWorkspaceRemove(ctx, req, s.workspaceMgr, s.runtimeFactory)
		if rpcErr == nil {
			s.DeactivateWorkspaceTunnels(req.ID)
		}
		return result, rpcErr
	})
	rpc.TypedRegister(r, "workspace.stop", func(ctx context.Context, req handlers.WorkspaceStopParams) (*handlers.WorkspaceStopResult, *rpckit.RPCError) {
		result, rpcErr := handlers.HandleWorkspaceStopWithRuntime(ctx, req, s.workspaceMgr, s.runtimeFactory)
		if rpcErr == nil {
			s.StopPortMonitoring(req.ID)
			s.DeactivateWorkspaceTunnels(req.ID)
		}
		return result, rpcErr
	})
	rpc.TypedRegister(r, "workspace.start", func(ctx context.Context, req handlers.WorkspaceStartParams) (*handlers.WorkspaceStartResult, *rpckit.RPCError) {
		result, rpcErr := handlers.HandleWorkspaceStart(ctx, req, s.workspaceMgr, s.runtimeFactory)
		if rpcErr == nil {
			_ = s.StartPortMonitoring(req.ID)
		}
		return result, rpcErr
	})
	rpc.TypedRegister(r, "workspace.restore", func(ctx context.Context, req handlers.WorkspaceRestoreParams) (*handlers.WorkspaceRestoreResult, *rpckit.RPCError) {
		result, rpcErr := handlers.HandleWorkspaceRestore(ctx, req, s.workspaceMgr, s.runtimeFactory)
		if rpcErr == nil {
			_ = s.StartPortMonitoring(req.ID)
		}
		return result, rpcErr
	})
	rpc.TypedRegister(r, "workspace.fork", func(ctx context.Context, req handlers.WorkspaceForkParams) (*handlers.WorkspaceForkResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceFork(ctx, req, s.workspaceMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.checkout", func(ctx context.Context, req handlers.WorkspaceCheckoutParams) (*handlers.WorkspaceCheckoutResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceCheckout(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.setLocalWorktree", func(ctx context.Context, req handlers.WorkspaceSetLocalWorktreeParams) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceSetLocalWorktree(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "node.info", func(ctx context.Context, _ struct{}) (*handlers.NodeInfoResult, *rpckit.RPCError) {
		return handlers.HandleNodeInfo(ctx, s.nodeCfg, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "os.pickDirectory", func(ctx context.Context, req handlers.PickDirectoryParams) (*handlers.PickDirectoryResult, *rpckit.RPCError) {
		return handlers.HandlePickDirectory(ctx, req)
	})
	rpc.TypedRegister(r, "workspace.ready", func(ctx context.Context, req handlers.WorkspaceReadyParams) (*handlers.WorkspaceReadyResult, *rpckit.RPCError) {
		raw, _ := json.Marshal(req)
		workspaceID := extractWorkspaceID(raw)
		if workspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		if accessErr := s.requireWorkspaceStarted(workspaceID); accessErr != nil {
			return nil, accessErr
		}
		workspace := s.resolveWorkspace(raw)
		rootPath := workspace.Path()
		if wsRecord, ok := s.workspaceMgr.Get(workspaceID); ok {
			if preferred := preferredWorkspaceRoot(wsRecord); preferred != "" {
				rootPath = preferred
			}
		}
		s.ensureComposeHints(ctx, workspaceID, rootPath)
		return handlers.HandleWorkspaceReady(ctx, req, workspace, s.serviceMgr)
	})
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
	rpc.TypedRegister(r, "workspace.tunnels.activate", func(_ context.Context, req struct {
		WorkspaceID string `json:"workspaceId"`
	}) (map[string]any, *rpckit.RPCError) {
		if req.WorkspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		other, err := s.ActivateWorkspaceTunnels(req.WorkspaceID)
		if err != nil {
			return map[string]any{
				"active":            false,
				"activeWorkspaceId": other,
			}, nil
		}
		return map[string]any{
			"active":            true,
			"activeWorkspaceId": req.WorkspaceID,
		}, nil
	})
	rpc.TypedRegister(r, "workspace.tunnels.deactivate", func(_ context.Context, req struct {
		WorkspaceID string `json:"workspaceId"`
	}) (map[string]any, *rpckit.RPCError) {
		if req.WorkspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		s.DeactivateWorkspaceTunnels(req.WorkspaceID)
		return map[string]any{
			"active":            false,
			"activeWorkspaceId": "",
		}, nil
	})
	rpc.TypedRegister(r, "git.command", func(ctx context.Context, req handlers.GitCommandParams) (map[string]interface{}, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleGitCommand(ctx, req, ws)
	})
	rpc.TypedRegister(r, "service.command", func(ctx context.Context, req handlers.ServiceCommandParams) (map[string]interface{}, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleServiceCommand(ctx, req, ws, s.serviceMgr)
	})
	rpc.TypedRegister(r, "spotlight.expose", func(ctx context.Context, req handlers.SpotlightExposeParams) (*handlers.SpotlightExposeResult, *rpckit.RPCError) {
		return handlers.HandleSpotlightExpose(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.list", func(ctx context.Context, req handlers.SpotlightListParams) (*handlers.SpotlightListResult, *rpckit.RPCError) {
		return handlers.HandleSpotlightList(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.close", func(ctx context.Context, req handlers.SpotlightCloseParams) (*handlers.SpotlightCloseResult, *rpckit.RPCError) {
		return handlers.HandleSpotlightClose(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.applyComposePorts", func(ctx context.Context, req handlers.SpotlightApplyComposePortsParams) (*handlers.SpotlightApplyComposePortsResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		rootPath := ws.Path()
		return handlers.HandleSpotlightApplyComposePorts(ctx, req, rootPath, s.spotlightMgr)
	})

	r.Register("pty.open", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		c := conn.(*Connection)
		workspace := s.resolveWorkspace(params)
		return pty.HandleOpen(s.ptyDeps(), c, params, workspace)
	})
	r.Register("pty.write", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		return pty.HandleWrite(s.ptyDeps(), params, conn.(*Connection))
	})
	r.Register("pty.resize", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		return pty.HandleResize(s.ptyDeps(), params, conn.(*Connection))
	})
	r.Register("pty.close", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		return pty.HandleClose(s.ptyDeps(), params, conn.(*Connection))
	})
	r.Register("pty.attach", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		return pty.HandleAttach(s.ptyDeps(), params, conn.(*Connection))
	})
	r.Register("pty.list", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return pty.HandleList(s.ptyDeps(), params)
	})
	r.Register("pty.get", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return pty.HandleGet(s.ptyDeps(), params)
	})
	r.Register("pty.rename", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return pty.HandleRename(s.ptyDeps(), params)
	})
	r.Register("pty.tmux", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		return pty.HandleTmuxCommand(s.ptyDeps(), conn.(*Connection), params)
	})

	return r
}

func (s *Server) ptyDeps() *pty.Deps {
	return &pty.Deps{
		WorkspaceMgr:   s.workspaceMgr,
		RuntimeFactory: s.runtimeFactory,
		AuthRelay:      s.authRelayBroker,
		RequireStarted: s.requireWorkspaceStarted,
		Registry:       s.ptyRegistry,
		SessionStore:   s.ptyStore,
	}
}
