package server

import (
	"context"
	"encoding/json"

	"github.com/inizio/nexus/packages/nexus/pkg/credentials"
	"github.com/inizio/nexus/packages/nexus/pkg/daemon"
	"github.com/inizio/nexus/packages/nexus/pkg/project"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func (s *Server) newRPCRegistry() *rpc.Registry {
	r := rpc.NewRegistry()

	rpc.TypedRegister(r, "fs.readFile", func(ctx context.Context, req workspace.ReadFileParams) (*workspace.ReadFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleReadFile(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.writeFile", func(ctx context.Context, req workspace.WriteFileParams) (*workspace.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleWriteFile(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.exists", func(ctx context.Context, req workspace.ExistsParams) (*workspace.ExistsResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleExists(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.readdir", func(ctx context.Context, req workspace.ReaddirParams) (*workspace.ReaddirResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleReaddir(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.mkdir", func(ctx context.Context, req workspace.MkdirParams) (*workspace.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleMkdir(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.rm", func(ctx context.Context, req workspace.RmParams) (*workspace.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleRm(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.stat", func(ctx context.Context, req workspace.StatParams) (*workspace.StatResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleStat(ctx, req, ws)
	})
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
	rpc.TypedRegister(r, "workspace.info", func(_ context.Context, req workspace.WorkspaceInfoParams) (map[string]interface{}, *rpckit.RPCError) {
		wid := workspace.WorkspaceInfoWorkspaceID(req)
		return workspace.HandleWorkspaceInfo(wid, s.ws, s.workspaceMgr, s.spotlightMgr), nil
	})
	rpc.TypedRegister(r, "workspace.create", func(ctx context.Context, req workspace.WorkspaceCreateParams) (*workspace.WorkspaceCreateResult, *rpckit.RPCError) {
		return workspace.HandleWorkspaceCreateWithProjects(ctx, req, s.workspaceMgr, s.projectMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "daemon.settings.get", func(ctx context.Context, req daemon.DaemonSettingsGetParams) (*daemon.DaemonSettingsGetResult, *rpckit.RPCError) {
		return daemon.HandleDaemonSettingsGet(ctx, req, s.workspaceMgr.SandboxResourceSettingsRepository())
	})
	rpc.TypedRegister(r, "daemon.settings.update", func(ctx context.Context, req daemon.DaemonSettingsUpdateParams) (*daemon.DaemonSettingsUpdateResult, *rpckit.RPCError) {
		return daemon.HandleDaemonSettingsUpdate(ctx, req, s.workspaceMgr.SandboxResourceSettingsRepository())
	})
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
	rpc.TypedRegister(r, "node.info", func(ctx context.Context, _ struct{}) (*daemon.NodeInfoResult, *rpckit.RPCError) {
		return daemon.HandleNodeInfo(ctx, s.nodeCfg, s.runtimeFactory)
	})
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
	rpc.TypedRegister(r, "git.command", func(ctx context.Context, req workspace.GitCommandParams) (map[string]interface{}, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleGitCommand(ctx, req, ws)
	})
	rpc.TypedRegister(r, "service.command", func(ctx context.Context, req daemon.ServiceCommandParams) (map[string]interface{}, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return daemon.HandleServiceCommand(ctx, req, ws, s.serviceMgr)
	})
	rpc.TypedRegister(r, "spotlight.start", func(ctx context.Context, req spotlight.SpotlightExposeParams) (*spotlight.SpotlightExposeResult, *rpckit.RPCError) {
		return spotlight.HandleSpotlightExpose(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.list", func(ctx context.Context, req spotlight.SpotlightListParams) (*spotlight.SpotlightListResult, *rpckit.RPCError) {
		return spotlight.HandleSpotlightList(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.close", func(ctx context.Context, req spotlight.SpotlightCloseParams) (*spotlight.SpotlightCloseResult, *rpckit.RPCError) {
		return spotlight.HandleSpotlightClose(ctx, req, s.spotlightMgr)
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
