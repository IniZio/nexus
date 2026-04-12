package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/auth"
	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/handlers"
	"github.com/inizio/nexus/packages/nexus/pkg/lifecycle"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/server/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
	"github.com/inizio/nexus/packages/nexus/pkg/services"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)


type Server struct {
	port                int
	workspaceDir        string
	authProvider        auth.Provider
	upgrader            websocket.Upgrader
	connections         map[string]*Connection
	ws                  *workspace.Workspace
	workspaceMgr        *workspacemgr.Manager
	serviceMgr          *services.Manager
	spotlightMgr        *spotlight.Manager
	lifecycle           *lifecycle.Manager
	runtimeFactory      *runtime.Factory
	nodeCfg             *config.NodeConfig
	authRelayBroker     *authrelay.Broker
	autoComposeForwards map[string]bool
	rpcReg              *rpc.Registry
	mu                  sync.RWMutex
	shutdownCh          chan struct{}
}

type Connection struct {
	conn     *websocket.Conn
	send     chan []byte
	closed   bool
	clientID string
	identity *auth.Identity
	ptyMu    sync.Mutex
	pty      map[string]*pty.Session
}

type RPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type RPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      string           `json:"id"`
	Result  interface{}      `json:"result,omitempty"`
	Error   *rpckit.RPCError `json:"error,omitempty"`
}

var newSpotlightManagerForServer = func(workspaceMgr *workspacemgr.Manager) (*spotlight.Manager, error) {
	return spotlight.NewManagerWithRepository(workspaceMgr.SpotlightRepository())
}

func NewServer(port int, workspaceDir string, tokenSecret string) (*Server, error) {
	ws, err := workspace.NewWorkspace(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	workspaceMgr := workspacemgr.NewManager(workspaceDir)

	spotlightMgr, err := newSpotlightManagerForServer(workspaceMgr)
	if err != nil {
		log.Printf("[spotlight] Warning: failed to initialize sqlite-backed spotlight manager, falling back to in-memory manager: %v", err)
		spotlightMgr = spotlight.NewManager()
	}

	lifecycleMgr, err := lifecycle.NewManager(workspaceDir)
	if err != nil {
		log.Printf("[lifecycle] Warning: failed to initialize lifecycle manager: %v", err)
	} else {
		if err := lifecycleMgr.RunPreStart(); err != nil {
			return nil, fmt.Errorf("pre-start hook failed: %w", err)
		}
	}

	srv := &Server{
		port:           port,
		workspaceDir:   workspaceDir,
		authProvider:   auth.NewLocalTokenProvider(tokenSecret),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		connections:         make(map[string]*Connection),
		ws:                  ws,
		workspaceMgr:        workspaceMgr,
		serviceMgr:          services.NewManager(),
		spotlightMgr:        spotlightMgr,
		lifecycle:           lifecycleMgr,
		authRelayBroker:     authrelay.NewBroker(),
		autoComposeForwards: make(map[string]bool),
		shutdownCh:          make(chan struct{}),
	}
	srv.rpcReg = srv.newRPCRegistry()
	return srv, nil
}

func (s *Server) Shutdown() {
	if s.lifecycle != nil {
		if err := s.lifecycle.RunPreStop(); err != nil {
			log.Printf("[lifecycle] Pre-stop hook error: %v", err)
		}
	}

	close(s.shutdownCh)
	s.mu.Lock()
	for _, conn := range s.connections {
		close(conn.send)
		conn.conn.Close()
	}
	s.mu.Unlock()

	if s.lifecycle != nil {
		if err := s.lifecycle.RunPostStop(); err != nil {
			log.Printf("[lifecycle] Post-stop hook error: %v", err)
		}
	}
}

func (s *Server) resolveWorkspace(params json.RawMessage) *workspace.Workspace {
	workspaceID := extractWorkspaceID(params)
	if workspaceID == "" {
		return s.ws
	}

	wsRecord, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return s.ws
	}

	resolved, err := workspace.NewWorkspace(wsRecord.RootPath)
	if err != nil {
		return s.ws
	}

	return resolved
}

func (s *Server) resolveWorkspaceTyped(v any) *workspace.Workspace {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return s.ws
	}
	return s.resolveWorkspace(raw)
}

func extractWorkspaceID(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(params, &payload); err != nil {
		return ""
	}

	if id, ok := payload["workspaceId"].(string); ok {
		return id
	}

	if rawSpec, ok := payload["spec"].(map[string]any); ok {
		if id, ok := rawSpec["workspaceId"].(string); ok {
			return id
		}
	}

	if id, ok := payload["id"].(string); ok {
		return id
	}

	return ""
}

func (s *Server) ensureComposeForwards(ctx context.Context, workspaceID, rootPath string) {
	if workspaceID == "" || rootPath == "" {
		return
	}

	s.mu.Lock()
	if s.autoComposeForwards[workspaceID] {
		s.mu.Unlock()
		return
	}
	s.autoComposeForwards[workspaceID] = true
	s.mu.Unlock()

	if _, rpcErr := handlers.HandleSpotlightApplyComposePorts(ctx, handlers.SpotlightApplyComposePortsParams{WorkspaceID: workspaceID}, rootPath, s.spotlightMgr); rpcErr != nil {
		log.Printf("[spotlight] failed to auto-apply compose ports for %s: %+v", workspaceID, rpcErr)
		s.mu.Lock()
		s.autoComposeForwards[workspaceID] = false
		s.mu.Unlock()
	}
}

func (s *Server) SetRuntimeFactory(factory *runtime.Factory) {
	s.runtimeFactory = factory
}

func (s *Server) SetAuthProvider(provider auth.Provider) {
	s.authProvider = provider
}

func (s *Server) WorkspaceIDs() []string {
	all := s.workspaceMgr.List()
	ids := make([]string, len(all))
	for i, ws := range all {
		ids[i] = ws.ID
	}
	return ids
}

func (s *Server) SetNodeConfig(cfg *config.NodeConfig) {
	s.nodeCfg = cfg
}

func (s *Server) requireWorkspaceStarted(workspaceID string) *rpckit.RPCError {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return rpckit.ErrInvalidParams
	}

	wsRecord, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return rpckit.ErrWorkspaceNotFound
	}
	if wsRecord.State != workspacemgr.StateRunning {
		return rpckit.ErrWorkspaceNotStarted
	}

	return nil
}

