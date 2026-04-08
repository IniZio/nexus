package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/handlers"
	"github.com/inizio/nexus/packages/nexus/pkg/lifecycle"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/server/portal"
	"github.com/inizio/nexus/packages/nexus/pkg/services"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type firecrackerAgentConnector interface {
	AgentConn(ctx context.Context, workspaceID string) (net.Conn, error)
}

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
	nodeCfg             *config.NodeConfig
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
	ptyMu    sync.Mutex
	pty      map[string]*ptySession
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

type ptySession struct {
	id      string
	cmd     *exec.Cmd
	file    *os.File
	conn    net.Conn
	mu      sync.Mutex
	closing bool
	enc     *json.Encoder
	dec     *json.Decoder
	remote  bool
	done    chan struct{}
}

type ptyOpenParams struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
	Shell       string `json:"shell,omitempty"`
	Cols        int    `json:"cols,omitempty"`
	Rows        int    `json:"rows,omitempty"`
}

type ptyOpenResult struct {
	SessionID string `json:"sessionId"`
}

type ptyWriteParams struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

type ptyResizeParams struct {
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type ptyCloseParams struct {
	SessionID string `json:"sessionId"`
}

func NewServer(port int, workspaceDir string, tokenSecret string) (*Server, error) {
	ws, err := workspace.NewWorkspace(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	lifecycleMgr, err := lifecycle.NewManager(workspaceDir)
	if err != nil {
		log.Printf("[lifecycle] Warning: failed to initialize lifecycle manager: %v", err)
	} else {
		if err := lifecycleMgr.RunPreStart(); err != nil {
			return nil, fmt.Errorf("pre-start hook failed: %w", err)
		}
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

	mux := s.routes()
	addr := fmt.Sprintf(":%d", s.port)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)

	// Dev mode: reverse-proxy all UI and Vite HMR paths to the Vite dev server.
	// This makes http://localhost:8080/ui serve the live Vite dev server transparently.
	if devUI := os.Getenv("NEXUS_DEV_UI"); devUI != "" {
		target, err := url.Parse(strings.TrimRight(devUI, "/"))
		if err != nil {
			log.Printf("[portal] invalid NEXUS_DEV_UI %q: %v", devUI, err)
		} else {
			proxy := httputil.NewSingleHostReverseProxy(target)
			// Preserve the original Host so Vite doesn't reject the request.
			origDirector := proxy.Director
			proxy.Director = func(req *http.Request) {
				origDirector(req)
				req.Host = target.Host
			}
			proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("[portal-dev] proxy error for %s: %v", r.URL.Path, err)
				http.Error(w, "Vite dev server unavailable — is `task dev:ui` running?", http.StatusBadGateway)
			}
			mux.Handle("/ui/static/", proxy)
			mux.Handle("/ui/", proxy)
			mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) { proxy.ServeHTTP(w, r) })
			// Vite HMR / internal paths
			mux.Handle("/@vite/", proxy)
			mux.Handle("/@fs/", proxy)
			mux.Handle("/node_modules/", proxy)
			mux.HandleFunc("/portal/", s.handlePortalUI)
			mux.HandleFunc("/portal", s.handlePortalUI)
			mux.HandleFunc("/", s.handleWebSocket)
			return mux
		}
	}

	mux.Handle("/portal/static/", http.StripPrefix("/portal/", http.FileServer(http.FS(portal.FS))))
	if uiDist, err := fs.Sub(portal.FS, "ui_dist"); err == nil {
		mux.Handle("/ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(uiDist))))
	} else if staticDist, staticErr := fs.Sub(portal.FS, "static"); staticErr == nil {
		mux.Handle("/ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(staticDist))))
	}
	mux.HandleFunc("/portal/", s.handlePortalUI)
	mux.HandleFunc("/portal", s.handlePortalUI)
	mux.HandleFunc("/ui/", s.handlePortalUI)
	mux.HandleFunc("/ui", s.handlePortalUI)

	mux.HandleFunc("/", s.handleWebSocket)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true,"service":"workspace-daemon"}`))
}

func (s *Server) handlePortalUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/ui/api") {
		http.NotFound(w, r)
		return
	}

	f, err := portal.FS.Open("ui_dist/index.html")
	if err != nil {
		f, err = portal.FS.Open("static/index.html")
	}
	if err != nil {
		http.Error(w, "portal unavailable", http.StatusInternalServerError)
		log.Printf("[portal] failed to open ui_dist index: %v", err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, copyErr := io.Copy(w, f); copyErr != nil {
		log.Printf("[portal] failed to serve ui_dist index: %v", copyErr)
	}
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
		pty:      make(map[string]*ptySession),
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
		c.closeAllPTY()
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
	response := s.processRPC(msg, conn)
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

func (s *Server) processRPC(msg *RPCMessage, conn *Connection) *RPCResponse {
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
	case "workspace.relations.list":
		result, err = handlers.HandleWorkspaceRelationsList(ctx, msg.Params, s.workspaceMgr)
	case "workspace.remove":
		result, err = handlers.HandleWorkspaceRemove(ctx, msg.Params, s.workspaceMgr)
	case "workspace.stop":
		result, err = handlers.HandleWorkspaceStop(ctx, msg.Params, s.workspaceMgr)
	case "workspace.start":
		result, err = handlers.HandleWorkspaceStart(ctx, msg.Params, s.workspaceMgr)
	case "workspace.restore":
		result, err = handlers.HandleWorkspaceRestore(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "workspace.pause":
		result, err = handlers.HandleWorkspacePause(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "workspace.resume":
		result, err = handlers.HandleWorkspaceResume(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "workspace.fork":
		result, err = handlers.HandleWorkspaceFork(ctx, msg.Params, s.workspaceMgr, s.runtimeFactory)
	case "workspace.setLocalWorktree":
		result, err = handlers.HandleWorkspaceSetLocalWorktree(ctx, msg.Params, s.workspaceMgr)
	case "capabilities.list":
		result, err = handlers.HandleCapabilitiesList(ctx, msg.Params, s.runtimeFactory)
	case "node.info":
		result, err = handlers.HandleNodeInfo(ctx, msg.Params, s.nodeCfg, s.runtimeFactory)
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
	case "pty.open":
		result, err = s.handlePTYOpen(msg.Params, conn, workspace)
	case "pty.write":
		result, err = s.handlePTYWrite(msg.Params, conn)
	case "pty.resize":
		result, err = s.handlePTYResize(msg.Params, conn)
	case "pty.close":
		result, err = s.handlePTYClose(msg.Params, conn)
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

func (s *Server) SetNodeConfig(cfg *config.NodeConfig) {
	s.nodeCfg = cfg
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

func (s *Server) handlePTYOpen(params json.RawMessage, conn *Connection, ws *workspace.Workspace) (interface{}, *rpckit.RPCError) {
	var p ptyOpenParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	rows := p.Rows
	if rows <= 0 {
		rows = 24
	}
	cols := p.Cols
	if cols <= 0 {
		cols = 80
	}

	shell := strings.TrimSpace(p.Shell)
	if shell == "" {
		shell = "bash"
	}

	workDir := ws.Path()
	workspaceID := p.WorkspaceID
	if p.WorkspaceID != "" {
		if wsRecord, ok := s.workspaceMgr.Get(p.WorkspaceID); ok {
			if wsRecord.Backend != "" && wsRecord.Backend != "local" {
				if wsRecord.Backend == "firecracker" || wsRecord.Backend == "lxc" {
					return s.handleFirecrackerPTYOpen(conn, p, wsRecord)
				}
				return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("interactive terminal over SSH is not implemented for backend %q yet", wsRecord.Backend)}
			}
			if strings.TrimSpace(wsRecord.LocalWorktreePath) != "" {
				workDir = wsRecord.LocalWorktreePath
			}
			if workspaceID == "" {
				workspaceID = wsRecord.ID
			}
		}
	}

	cmd := exec.Command(shell)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("pty open failed: %v", err)}
	}

	sessionID := fmt.Sprintf("pty-%d", time.Now().UnixNano())
	session := &ptySession{id: sessionID, cmd: cmd, file: ptmx, done: make(chan struct{})}

	conn.ptyMu.Lock()
	conn.pty[sessionID] = session
	conn.ptyMu.Unlock()

	go s.streamPTYOutput(conn, session)

	return &ptyOpenResult{SessionID: sessionID}, nil
}

func (s *Server) handleFirecrackerPTYOpen(conn *Connection, p ptyOpenParams, wsRecord *workspacemgr.Workspace) (interface{}, *rpckit.RPCError) {
	if s.runtimeFactory == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "runtime factory unavailable"}
	}

	backend := strings.TrimSpace(wsRecord.Backend)
	if backend == "" {
		backend = "firecracker"
	}

	driverAny, ok := s.runtimeFactory.DriverForBackend(backend)
	if !ok {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("%s runtime driver not configured", backend)}
	}

	connector, ok := driverAny.(firecrackerAgentConnector)
	if !ok {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("%s runtime does not support agent connection", backend)}
	}

	openCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentConn, err := connector.AgentConn(openCtx, wsRecord.ID)
	if err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("%s agent connect failed: %v", backend, err)}
	}

	shell := strings.TrimSpace(p.Shell)
	if shell == "" {
		shell = "bash"
	}

	sessionID := fmt.Sprintf("pty-%d", time.Now().UnixNano())
	enc := json.NewEncoder(agentConn)
	dec := json.NewDecoder(agentConn)

	openReq := map[string]any{
		"id":      sessionID,
		"type":    "shell.open",
		"command": shell,
		"workdir": "/workspace",
	}

	if err := enc.Encode(openReq); err != nil {
		_ = agentConn.Close()
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("firecracker agent request failed: %v", err)}
	}
	var openResp map[string]any
	if err := dec.Decode(&openResp); err != nil {
		_ = agentConn.Close()
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("firecracker shell open failed: %v", err)}
	}
	if exitRaw, ok := openResp["exit_code"].(float64); ok && int(exitRaw) != 0 {
		_ = agentConn.Close()
		stderr, _ := openResp["stderr"].(string)
		if stderr == "" {
			stderr = "shell open failed"
		}
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: stderr}
	}

	session := &ptySession{id: sessionID, conn: agentConn, enc: enc, dec: dec, remote: true, done: make(chan struct{})}

	conn.ptyMu.Lock()
	conn.pty[sessionID] = session
	conn.ptyMu.Unlock()

	go s.streamRemoteShellOutput(conn, session)

	return &ptyOpenResult{SessionID: sessionID}, nil
}

func (s *Server) streamRemoteShellOutput(conn *Connection, session *ptySession) {
	defer close(session.done)
	for {
		var msg map[string]any
		err := session.dec.Decode(&msg)
		if err != nil {
			break
		}

		typeStr, _ := msg["type"].(string)
		if typeStr == "chunk" {
			if data, ok := msg["data"].(string); ok && data != "" {
				s.sendPTYData(conn, session.id, data)
			}
			continue
		}
		if typeStr == "result" {
			session.mu.Lock()
			closing := session.closing
			session.mu.Unlock()
			if !closing {
				continue
			}

			exitCode := 0
			if v, ok := msg["exit_code"].(float64); ok {
				exitCode = int(v)
			}
			s.sendPTYExit(conn, session.id, exitCode)
			break
		}
	}

	conn.removePTY(session.id)
	_ = session.conn.Close()
}

func (s *Server) sendPTYData(conn *Connection, sessionID, data string) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  "pty.data",
		"params": map[string]any{
			"sessionId": sessionID,
			"data":      data,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	select {
	case conn.send <- encoded:
	default:
	}
}

func (s *Server) sendPTYExit(conn *Connection, sessionID string, exitCode int) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  "pty.exit",
		"params": map[string]any{
			"sessionId": sessionID,
			"exitCode":  exitCode,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	select {
	case conn.send <- encoded:
	default:
	}
}

func (s *Server) handlePTYWrite(params json.RawMessage, conn *Connection) (interface{}, *rpckit.RPCError) {
	var p ptyWriteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.write params: %v", err)}
	}

	session := conn.getPTY(p.SessionID)
	if session == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("pty session not found: %s", p.SessionID)}
	}
	if session.conn != nil {
		if p.Data == "" {
			return map[string]bool{"ok": true}, nil
		}

		session.mu.Lock()
		defer session.mu.Unlock()
		request := map[string]any{
			"id":   session.id,
			"type": "shell.write",
			"data": p.Data,
		}
		if err := session.enc.Encode(request); err != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("firecracker shell write failed: %v", err)}
		}
		return map[string]bool{"ok": true}, nil
	}

	if _, err := session.file.Write([]byte(p.Data)); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("pty write failed: %v", err)}
	}

	return map[string]bool{"ok": true}, nil
}

func (s *Server) handlePTYResize(params json.RawMessage, conn *Connection) (interface{}, *rpckit.RPCError) {
	var p ptyResizeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.resize params: %v", err)}
	}
	if p.Cols <= 0 || p.Rows <= 0 {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "invalid pty.resize params: cols/rows must be > 0"}
	}

	session := conn.getPTY(p.SessionID)
	if session == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("pty session not found: %s", p.SessionID)}
	}
	if session.conn != nil {
		session.mu.Lock()
		_ = session.enc.Encode(map[string]any{"id": session.id, "type": "shell.resize", "cols": p.Cols, "rows": p.Rows})
		session.mu.Unlock()
		return map[string]bool{"ok": true}, nil
	}

	if err := pty.Setsize(session.file, &pty.Winsize{Rows: uint16(p.Rows), Cols: uint16(p.Cols)}); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("pty resize failed: %v", err)}
	}

	return map[string]bool{"ok": true}, nil
}

func (s *Server) handlePTYClose(params json.RawMessage, conn *Connection) (interface{}, *rpckit.RPCError) {
	var p ptyCloseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("invalid pty.close params: %v", err)}
	}

	if !conn.closePTY(p.SessionID) {
		return map[string]bool{"closed": true}, nil
	}

	return map[string]bool{"closed": true}, nil
}

func (s *Server) streamPTYOutput(conn *Connection, session *ptySession) {
	buf := make([]byte, 4096)
	for {
		n, err := session.file.Read(buf)
		if n > 0 {
			payload := map[string]any{
				"jsonrpc": "2.0",
				"method":  "pty.data",
				"params": map[string]any{
					"sessionId": session.id,
					"data":      string(buf[:n]),
				},
			}
			encoded, marshalErr := json.Marshal(payload)
			if marshalErr == nil {
				select {
				case conn.send <- encoded:
				default:
				}
			}
		}

		if err != nil {
			break
		}
	}

	conn.removePTY(session.id)
	if session.cmd.Process != nil {
		_, _ = session.cmd.Process.Wait()
	}
	if session.cmd.ProcessState != nil {
		exitCode := session.cmd.ProcessState.ExitCode()
		payload := map[string]any{
			"jsonrpc": "2.0",
			"method":  "pty.exit",
			"params": map[string]any{
				"sessionId": session.id,
				"exitCode":  exitCode,
			},
		}
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr == nil {
			select {
			case conn.send <- encoded:
			default:
			}
		}
	}
}

func (c *Connection) getPTY(id string) *ptySession {
	c.ptyMu.Lock()
	defer c.ptyMu.Unlock()
	return c.pty[id]
}

func (c *Connection) closePTY(id string) bool {
	c.ptyMu.Lock()
	session, ok := c.pty[id]
	if ok {
		delete(c.pty, id)
	}
	c.ptyMu.Unlock()
	if !ok {
		return false
	}
	if session.conn != nil {
		session.mu.Lock()
		session.closing = true
		_ = session.enc.Encode(map[string]any{"id": session.id, "type": "shell.close"})
		session.mu.Unlock()
		_ = session.conn.Close()
		if session.done != nil {
			select {
			case <-session.done:
			case <-time.After(2 * time.Second):
			}
		}
		return true
	}

	_ = session.file.Close()
	if session.cmd.Process != nil {
		_ = session.cmd.Process.Kill()
		_, _ = session.cmd.Process.Wait()
	}
	return true
}

func (c *Connection) removePTY(id string) {
	c.ptyMu.Lock()
	delete(c.pty, id)
	c.ptyMu.Unlock()
}

func (c *Connection) closeAllPTY() {
	c.ptyMu.Lock()
	ids := make([]string, 0, len(c.pty))
	for id := range c.pty {
		ids = append(ids, id)
	}
	c.ptyMu.Unlock()
	for _, id := range ids {
		_ = c.closePTY(id)
	}
}
