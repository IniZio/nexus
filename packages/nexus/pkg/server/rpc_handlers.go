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

	r.Register("fs.readFile", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleReadFile(ctx, params, workspace)
	})
	r.Register("fs.writeFile", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleWriteFile(ctx, params, workspace)
	})
	r.Register("fs.exists", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleExists(ctx, params, workspace)
	})
	r.Register("fs.readdir", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleReaddir(ctx, params, workspace)
	})
	r.Register("fs.mkdir", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleMkdir(ctx, params, workspace)
	})
	r.Register("fs.rm", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleRm(ctx, params, workspace)
	})
	r.Register("fs.stat", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleStat(ctx, params, workspace)
	})
	r.Register("exec", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleExecWithAuthRelay(ctx, params, workspace, s.authRelayBroker)
	})
	r.Register("authrelay.mint", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleAuthRelayMint(ctx, params, s.workspaceMgr, s.authRelayBroker)
	})
	r.Register("authrelay.revoke", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleAuthRelayRevoke(ctx, params, s.authRelayBroker)
	})
	r.Register("workspace.info", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceInfo(extractWorkspaceID(params), s.ws, s.workspaceMgr, s.spotlightMgr), nil
	})
	r.Register("workspace.create", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceCreate(ctx, params, s.workspaceMgr, s.runtimeFactory)
	})
	r.Register("workspace.open", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceOpen(ctx, params, s.workspaceMgr)
	})
	r.Register("workspace.list", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceList(ctx, params, s.workspaceMgr)
	})
	r.Register("workspace.relations.list", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceRelationsList(ctx, params, s.workspaceMgr)
	})
	r.Register("workspace.remove", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceRemove(ctx, params, s.workspaceMgr, s.runtimeFactory)
	})
	r.Register("workspace.stop", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceStop(ctx, params, s.workspaceMgr)
	})
	r.Register("workspace.start", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceStart(ctx, params, s.workspaceMgr)
	})
	r.Register("workspace.restore", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceRestore(ctx, params, s.workspaceMgr, s.runtimeFactory)
	})
	r.Register("workspace.pause", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspacePause(ctx, params, s.workspaceMgr, s.runtimeFactory)
	})
	r.Register("workspace.resume", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceResume(ctx, params, s.workspaceMgr, s.runtimeFactory)
	})
	r.Register("workspace.fork", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceFork(ctx, params, s.workspaceMgr, s.runtimeFactory)
	})
	r.Register("workspace.setLocalWorktree", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleWorkspaceSetLocalWorktree(ctx, params, s.workspaceMgr)
	})
	r.Register("capabilities.list", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleCapabilitiesList(ctx, params, s.runtimeFactory)
	})
	r.Register("node.info", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleNodeInfo(ctx, params, s.nodeCfg, s.runtimeFactory)
	})
	r.Register("os.pickDirectory", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandlePickDirectory(ctx, params)
	})
	r.Register("workspace.ready", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspaceID := extractWorkspaceID(params)
		if workspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		if accessErr := s.requireWorkspaceStarted(workspaceID); accessErr != nil {
			return nil, accessErr
		}
		workspace := s.resolveWorkspace(params)
		rootPath := workspace.Path()
		if wsRecord, ok := s.workspaceMgr.Get(workspaceID); ok && strings.TrimSpace(wsRecord.RootPath) != "" {
			rootPath = wsRecord.RootPath
		}
		s.ensureComposeForwards(ctx, workspaceID, rootPath)
		return handlers.HandleWorkspaceReady(ctx, params, workspace, s.serviceMgr)
	})
	r.Register("git.command", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleGitCommand(ctx, params, workspace)
	})
	r.Register("service.command", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		return handlers.HandleServiceCommand(ctx, params, workspace, s.serviceMgr)
	})
	r.Register("spotlight.expose", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleSpotlightExpose(ctx, params, s.spotlightMgr)
	})
	r.Register("spotlight.list", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleSpotlightList(ctx, params, s.spotlightMgr)
	})
	r.Register("spotlight.close", func(_ context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		return handlers.HandleSpotlightClose(ctx, params, s.spotlightMgr)
	})
	r.Register("spotlight.applyDefaults", func(ctx context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		rootPath := workspace.Path()
		return handlers.HandleSpotlightApplyDefaults(ctx, params, rootPath, s.spotlightMgr)
	})
	r.Register("spotlight.applyComposePorts", func(ctx context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		workspace := s.resolveWorkspace(params)
		rootPath := workspace.Path()
		return handlers.HandleSpotlightApplyComposePorts(ctx, params, rootPath, s.spotlightMgr)
	})

	deps := s.ptyDeps()
	r.Register("pty.open", func(_ context.Context, _ string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
		c := conn.(*Connection)
		workspace := s.resolveWorkspace(params)
		return pty.HandleOpen(deps, c, params, workspace)
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
