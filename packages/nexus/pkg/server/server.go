package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	"github.com/inizio/nexus/packages/nexus/pkg/handlers"
	"github.com/inizio/nexus/packages/nexus/pkg/lifecycle"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/services"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type Server struct {
	port                int
	workspaceDir        string
	tokenSecret         string
	upgrader            websocket.Upgrader
	connections         map[string]*Connection
	ws                  *workspace.Workspace
	workspaceMgr        *workspacemgr.Manager
	serviceMgr          *services.Manager
	spotlightMgr        *spotlight.Manager
	lifecycle           *lifecycle.Manager
	runtimeFactory      *runtime.Factory
	authRelayBroker     *authrelay.Broker
	autoComposeForwards map[string]bool
	mu                  sync.RWMutex
	shutdownCh          chan struct{}
}

type Connection struct {
	conn     *websocket.Conn
	send     chan []byte
	closed   bool
	clientID string
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

func NewServer(port int, workspaceDir string, tokenSecret string) (*Server, error) {
	ws, err := workspace.NewWorkspace(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	lifecycleMgr, err := lifecycle.NewManager(workspaceDir)
	if err != nil {
		log.Printf("[lifecycle] Warning: failed to initialize lifecycle manager: %v", err)
	}

	if err := lifecycleMgr.RunPreStart(); err != nil {
		return nil, fmt.Errorf("pre-start hook failed: %w", err)
	}

	return &Server{
		port:         port,
		workspaceDir: workspaceDir,
		tokenSecret:  tokenSecret,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		connections:         make(map[string]*Connection),
		ws:                  ws,
		workspaceMgr:        workspacemgr.NewManager(workspaceDir),
		serviceMgr:          services.NewManager(),
		spotlightMgr:        spotlight.NewManager(),
		lifecycle:           lifecycleMgr,
		authRelayBroker:     authrelay.NewBroker(),
		autoComposeForwards: make(map[string]bool),
		shutdownCh:          make(chan struct{}),
	}, nil
}

func (s *Server) Start() error {
	if s.lifecycle != nil {
		if err := s.lifecycle.RunPostStart(); err != nil {
			log.Printf("[lifecycle] Post-start hook error: %v", err)
		}
	}

	http.HandleFunc("/", s.handleWebSocket)
	http.HandleFunc("/healthz", s.handleHealthz)
	addr := fmt.Sprintf(":%d", s.port)
	return http.ListenAndServe(addr, nil)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true,"service":"workspace-daemon"}`))
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

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.validateToken(token) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}

	clientID := fmt.Sprintf("client-%d", time.Now().UnixNano())
	clientConn := &Connection{
		conn:     conn,
		send:     make(chan []byte, 256),
		clientID: clientID,
	}

	s.mu.Lock()
	s.connections[clientID] = clientConn
	s.mu.Unlock()

	go clientConn.writePump()
	clientConn.readPump(s)
}

func (s *Server) validateToken(token string) bool {
	if token == "" {
		return false
	}

	if token == s.tokenSecret {
		return true
	}

	parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(s.tokenSecret), nil
	})

	return err == nil && parsedToken.Valid
}

func (c *Connection) readPump(srv *Server) {
	defer func() {
		c.conn.Close()
		srv.mu.Lock()
		delete(srv.connections, c.clientID)
		srv.mu.Unlock()
	}()

	c.conn.SetReadLimit(512 * 1024)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		var rpcMsg RPCMessage
		if err := json.Unmarshal(message, &rpcMsg); err != nil {
			response := srv.createErrorResponse("", rpckit.ErrInvalidParams)
			responseJSON, _ := json.Marshal(response)
			c.send <- responseJSON
			continue
		}

		go srv.handleMessage(&rpcMsg, c)
	}
}

func (c *Connection) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleMessage(msg *RPCMessage, conn *Connection) {
	response := s.processRPC(msg)
	responseJSON, err := json.Marshal(response)
	if err != nil {
		log.Printf("Failed to marshal response: %v", err)
		return
	}

	select {
	case conn.send <- responseJSON:
	default:
		log.Printf("Failed to send response to %s", conn.clientID)
	}
}

func (s *Server) processRPC(msg *RPCMessage) *RPCResponse {
	ctx := context.Background()

	var result interface{}
	var err *rpckit.RPCError
	workspace := s.resolveWorkspace(msg.Params)

	switch msg.Method {
	case "fs.readFile":
		result, err = handlers.HandleReadFile(ctx, msg.Params, workspace)
	case "fs.writeFile":
		result, err = handlers.HandleWriteFile(ctx, msg.Params, workspace)
	case "fs.exists":
		result, err = handlers.HandleExists(ctx, msg.Params, workspace)
	case "fs.readdir":
		result, err = handlers.HandleReaddir(ctx, msg.Params, workspace)
	case "fs.mkdir":
		result, err = handlers.HandleMkdir(ctx, msg.Params, workspace)
	case "fs.rm":
		result, err = handlers.HandleRm(ctx, msg.Params, workspace)
	case "fs.stat":
		result, err = handlers.HandleStat(ctx, msg.Params, workspace)
	case "exec":
		result, err = handlers.HandleExecWithAuthRelay(ctx, msg.Params, workspace, s.authRelayBroker)
	case "authrelay.mint":
		result, err = handlers.HandleAuthRelayMint(ctx, msg.Params, s.workspaceMgr, s.authRelayBroker)
	case "authrelay.revoke":
		result, err = handlers.HandleAuthRelayRevoke(ctx, msg.Params, s.authRelayBroker)
	case "workspace.info":
		result = s.handleWorkspaceInfo(msg.Params)
	case "workspace.create":
		result, err = handlers.HandleWorkspaceCreate(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "workspace.open":
		result, err = handlers.HandleWorkspaceOpen(ctx, msg.Params, s.workspaceMgr)
	case "workspace.list":
		result, err = handlers.HandleWorkspaceList(ctx, msg.Params, s.workspaceMgr)
	case "workspace.remove":
		result, err = handlers.HandleWorkspaceRemove(ctx, msg.Params, s.workspaceMgr)
	case "workspace.stop":
		result, err = handlers.HandleWorkspaceStop(ctx, msg.Params, s.workspaceMgr)
	case "workspace.restore":
		result, err = handlers.HandleWorkspaceRestore(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "workspace.pause":
		result, err = handlers.HandleWorkspacePause(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "workspace.resume":
		result, err = handlers.HandleWorkspaceResume(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "workspace.fork":
		result, err = handlers.HandleWorkspaceFork(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "capabilities.list":
		result, err = handlers.HandleCapabilitiesList(ctx, msg.Params, s.runtimeFactory)
	case "workspace.ready":
		workspaceID := extractWorkspaceID(msg.Params)
		if workspaceID == "" {
			workspaceID = workspace.ID()
		}
		s.ensureComposeForwards(ctx, workspaceID, workspace.Path())
		result, err = handlers.HandleWorkspaceReady(ctx, msg.Params, workspace, s.serviceMgr)
	case "git.command":
		result, err = handlers.HandleGitCommand(ctx, msg.Params, workspace)
	case "service.command":
		result, err = handlers.HandleServiceCommand(ctx, msg.Params, workspace, s.serviceMgr)
	case "spotlight.expose":
		result, err = handlers.HandleSpotlightExpose(ctx, msg.Params, s.spotlightMgr)
	case "spotlight.list":
		result, err = handlers.HandleSpotlightList(ctx, msg.Params, s.spotlightMgr)
	case "spotlight.close":
		result, err = handlers.HandleSpotlightClose(ctx, msg.Params, s.spotlightMgr)
	case "spotlight.applyDefaults":
		rootPath := workspace.Path()
		paramsMap := map[string]any{}
		_ = json.Unmarshal(msg.Params, &paramsMap)
		paramsMap["rootPath"] = rootPath
		updated, _ := json.Marshal(paramsMap)
		result, err = handlers.HandleSpotlightApplyDefaults(ctx, updated, s.spotlightMgr)
	case "spotlight.applyComposePorts":
		rootPath := workspace.Path()
		paramsMap := map[string]any{}
		_ = json.Unmarshal(msg.Params, &paramsMap)
		paramsMap["rootPath"] = rootPath
		updated, _ := json.Marshal(paramsMap)
		result, err = handlers.HandleSpotlightApplyComposePorts(ctx, updated, s.spotlightMgr)
	default:
		err = rpckit.ErrMethodNotFound
	}

	if err != nil {
		return &RPCResponse{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   err,
		}
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  result,
	}
}

func (s *Server) createErrorResponse(id string, rpcErr *rpckit.RPCError) *RPCResponse {
	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   rpcErr,
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

	payload, _ := json.Marshal(map[string]any{
		"workspaceId": workspaceID,
		"rootPath":    rootPath,
	})

	if _, rpcErr := handlers.HandleSpotlightApplyComposePorts(ctx, payload, s.spotlightMgr); rpcErr != nil {
		log.Printf("[spotlight] failed to auto-apply compose ports for %s: %+v", workspaceID, rpcErr)
		s.mu.Lock()
		s.autoComposeForwards[workspaceID] = false
		s.mu.Unlock()
	}
}

func (s *Server) SetRuntimeFactory(factory *runtime.Factory) {
	s.runtimeFactory = factory
}

func (s *Server) handleWorkspaceInfo(params json.RawMessage) map[string]interface{} {
	workspaceID := extractWorkspaceID(params)
	result := map[string]interface{}{
		"workspace_id":   s.ws.ID(),
		"workspace_path": s.ws.Path(),
		"workspaces":     s.workspaceMgr.List(),
		"spotlight":      s.spotlightMgr.List(""),
	}

	if workspaceID != "" {
		if ws, ok := s.workspaceMgr.Get(workspaceID); ok {
			result["workspace"] = ws
			result["workspace_id"] = ws.ID
			result["workspace_path"] = filepath.Clean(ws.RootPath)
			result["spotlight"] = s.spotlightMgr.List(workspaceID)
		}
	}

	return result
}
