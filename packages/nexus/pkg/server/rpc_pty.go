package server

import (
	"context"
	"encoding/json"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
)

func registerPTYRPCs(r *rpc.Registry, s *Server) {
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
