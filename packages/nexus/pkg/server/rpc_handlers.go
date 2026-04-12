package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/handlers"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
)

func (s *Server) newRPCRegistry() *rpc.Registry {
	r := rpc.NewRegistry()
	ctx := context.Background()

	rpc.TypedRegister(r, "fs.readFile", func(_ context.Context, req handlers.ReadFileParams) (*handlers.ReadFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleReadFile(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.writeFile", func(_ context.Context, req handlers.WriteFileParams) (*handlers.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleWriteFile(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.exists", func(_ context.Context, req handlers.ExistsParams) (*handlers.ExistsResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleExists(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.readdir", func(_ context.Context, req handlers.ReaddirParams) (*handlers.ReaddirResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleReaddir(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.mkdir", func(_ context.Context, req handlers.MkdirParams) (*handlers.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleMkdir(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.rm", func(_ context.Context, req handlers.RmParams) (*handlers.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleRm(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.stat", func(_ context.Context, req handlers.StatParams) (*handlers.StatResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleStat(ctx, req, ws)
	})
	rpc.TypedRegister(r, "exec", func(_ context.Context, req handlers.ExecParams) (*handlers.ExecResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleExecWithAuthRelay(ctx, req, ws, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "authrelay.mint", func(_ context.Context, req handlers.AuthRelayMintParams) (*handlers.AuthRelayMintResult, *rpckit.RPCError) {
		return handlers.HandleAuthRelayMint(ctx, req, s.workspaceMgr, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "authrelay.revoke", func(_ context.Context, req handlers.AuthRelayRevokeParams) (*handlers.AuthRelayRevokeResult, *rpckit.RPCError) {
		return handlers.HandleAuthRelayRevoke(ctx, req, s.authRelayBroker)
	})
	rpc.TypedRegister(r, "workspace.info", func(_ context.Context, req handlers.WorkspaceInfoParams) (map[string]interface{}, *rpckit.RPCError) {
		wid := handlers.WorkspaceInfoWorkspaceID(req)
		return handlers.HandleWorkspaceInfo(wid, s.ws, s.workspaceMgr, s.spotlightMgr), nil
	})
	rpc.TypedRegister(r, "workspace.create", func(_ context.Context, req handlers.WorkspaceCreateParams) (*handlers.WorkspaceCreateResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceCreate(ctx, req, s.workspaceMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.list", func(_ context.Context, req handlers.WorkspaceListParams) (*handlers.WorkspaceListResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceList(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.relations.list", func(_ context.Context, req handlers.WorkspaceRelationsListParams) (*handlers.WorkspaceRelationsListResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceRelationsList(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.remove", func(_ context.Context, req handlers.WorkspaceRemoveParams) (*handlers.WorkspaceRemoveResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceRemove(ctx, req, s.workspaceMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.stop", func(_ context.Context, req handlers.WorkspaceStopParams) (*handlers.WorkspaceStopResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceStop(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.start", func(_ context.Context, req handlers.WorkspaceStartParams) (*handlers.WorkspaceStartResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceStart(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "workspace.restore", func(_ context.Context, req handlers.WorkspaceRestoreParams) (*handlers.WorkspaceRestoreResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceRestore(ctx, req, s.workspaceMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.pause", func(_ context.Context, req handlers.WorkspacePauseParams) (*handlers.WorkspacePauseResult, *rpckit.RPCError) {
		return handlers.HandleWorkspacePause(ctx, req, s.workspaceMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.resume", func(_ context.Context, req handlers.WorkspaceResumeParams) (*handlers.WorkspaceResumeResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceResume(ctx, req, s.workspaceMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.fork", func(_ context.Context, req handlers.WorkspaceForkParams) (*handlers.WorkspaceForkResult, *rpckit.RPCError) {
		return handlers.HandleWorkspaceFork(ctx, req, s.workspaceMgr, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "workspace.setLocalWorktree", func(_ context.Context, req handlers.WorkspaceSetLocalWorktreeParams) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceSetLocalWorktree(ctx, req, s.workspaceMgr)
	})
	rpc.TypedRegister(r, "node.info", func(_ context.Context, _ struct{}) (*handlers.NodeInfoResult, *rpckit.RPCError) {
		return handlers.HandleNodeInfo(ctx, s.nodeCfg, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "os.pickDirectory", func(_ context.Context, req handlers.PickDirectoryParams) (*handlers.PickDirectoryResult, *rpckit.RPCError) {
		return handlers.HandlePickDirectory(ctx, req)
	})
	rpc.TypedRegister(r, "workspace.ready", func(_ context.Context, req handlers.WorkspaceReadyParams) (*handlers.WorkspaceReadyResult, *rpckit.RPCError) {
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
		if wsRecord, ok := s.workspaceMgr.Get(workspaceID); ok && strings.TrimSpace(wsRecord.RootPath) != "" {
			rootPath = wsRecord.RootPath
		}
		s.ensureComposeForwards(ctx, workspaceID, rootPath)
		return handlers.HandleWorkspaceReady(ctx, req, workspace, s.serviceMgr)
	})
	rpc.TypedRegister(r, "git.command", func(_ context.Context, req handlers.GitCommandParams) (map[string]interface{}, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleGitCommand(ctx, req, ws)
	})
	rpc.TypedRegister(r, "service.command", func(_ context.Context, req handlers.ServiceCommandParams) (map[string]interface{}, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return handlers.HandleServiceCommand(ctx, req, ws, s.serviceMgr)
	})
	rpc.TypedRegister(r, "spotlight.expose", func(_ context.Context, req handlers.SpotlightExposeParams) (*handlers.SpotlightExposeResult, *rpckit.RPCError) {
		return handlers.HandleSpotlightExpose(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.list", func(_ context.Context, req handlers.SpotlightListParams) (*handlers.SpotlightListResult, *rpckit.RPCError) {
		return handlers.HandleSpotlightList(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.close", func(_ context.Context, req handlers.SpotlightCloseParams) (*handlers.SpotlightCloseResult, *rpckit.RPCError) {
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
		return pty.HandleWrite(params, conn.(*Connection))
	})
	r.Register("pty.resize", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		return pty.HandleResize(params, conn.(*Connection))
	})
	r.Register("pty.close", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		return pty.HandleClose(params, conn.(*Connection))
	})

	return r
}

func (s *Server) ptyDeps() *pty.Deps {
	return &pty.Deps{
		WorkspaceMgr:   s.workspaceMgr,
		RuntimeFactory: s.runtimeFactory,
		AuthRelay:      s.authRelayBroker,
		RequireStarted: s.requireWorkspaceStarted,
	}
}
