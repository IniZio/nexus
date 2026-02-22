package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/handlers"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/lifecycle"
	rpckit "github.com/nexus/nexus/packages/workspace-daemon/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/workspace"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/docker"
)

type Server struct {
	port          int
	workspaceDir string
	tokenSecret  string
	upgrader     websocket.Upgrader
	connections  map[string]*Connection
	ws           *workspace.Workspace
	lifecycle    *lifecycle.Manager
	mu           sync.RWMutex
	shutdownCh   chan struct{}
	mux          *http.ServeMux
	workspaces   map[string]*WorkspaceState
	dockerBackend *docker.DockerBackend
}

type WorkspaceState struct {
	ID          string
	Name        string
	Status      string
	Backend     string
	Ports       []PortMapping
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type PortMapping struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	Visibility    string `json:"visibility"`
	URL           string `json:"url,omitempty"`
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

	var dockerBackend *docker.DockerBackend
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Printf("[docker] Warning: failed to create docker client: %v", err)
	} else {
		dockerBackend = docker.NewDockerBackend(dockerClient, workspaceDir)
	}

	return &Server{
		port:          port,
		workspaceDir: workspaceDir,
		tokenSecret:  tokenSecret,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		connections:   make(map[string]*Connection),
		ws:            ws,
		lifecycle:     lifecycleMgr,
		shutdownCh:    make(chan struct{}),
		mux:           http.NewServeMux(),
		workspaces:    make(map[string]*WorkspaceState),
		dockerBackend: dockerBackend,
	}, nil
}

func (s *Server) Start() error {
	if s.lifecycle != nil {
		if err := s.lifecycle.RunPostStart(); err != nil {
			log.Printf("[lifecycle] Post-start hook error: %v", err)
		}
	}

	s.registerHTTPRoutes()

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.mux,
	}

	go func() {
		log.Printf("HTTP server listening on port %d", s.port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return nil
}

func (s *Server) registerHTTPRoutes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/workspaces", s.handleWorkspaces)
	s.mux.HandleFunc("/api/v1/workspaces/", s.handleWorkspaceByID)
	s.mux.HandleFunc("/ws", s.handleWebSocket)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func WriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func WriteError(w http.ResponseWriter, status int, err error) {
	WriteJSON(w, status, APIResponse{
		Success: false,
		Error:   err.Error(),
	})
}

func WriteSuccess(w http.ResponseWriter, data interface{}) {
	WriteJSON(w, http.StatusOK, APIResponse{
		Success: true,
		Data:    data,
	})
}

type CreateWorkspaceRequest struct {
	Name          string            `json:"name"`
	DisplayName   string            `json:"display_name,omitempty"`
	RepositoryURL string            `json:"repository_url,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	Backend       string            `json:"backend,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listWorkspaces(w, r)
	case http.MethodPost:
		s.createWorkspace(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleWorkspaceByID(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[len("/api/v1/workspaces/"):]
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		http.Error(w, "workspace ID required", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	ws, exists := s.workspaces[id]
	s.mu.RUnlock()

	if !exists {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}

	subPath := ""
	if len(parts) > 1 {
		subPath = "/" + parts[1]
	}

	switch r.Method {
	case http.MethodGet:
		switch subPath {
		case "/logs":
			s.getWorkspaceLogs(w, r, id)
		default:
			WriteSuccess(w, ws)
		}
	case http.MethodDelete:
		s.deleteWorkspace(w, r, id)
	case http.MethodPost:
		switch subPath {
		case "/start":
			s.startWorkspace(w, r, id)
		case "/stop":
			s.stopWorkspace(w, r, id)
		case "/exec":
			s.execWorkspace(w, r, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getWorkspaceLogs(w http.ResponseWriter, r *http.Request, id string) {
	tail := 100
	if t := r.URL.Query().Get("tail"); t != "" {
		fmt.Sscanf(t, "%d", &tail)
	}

	logs := fmt.Sprintf("Logs for workspace %s (last %d lines)\n", id, tail)
	logs += "2024-01-01T00:00:00Z Container started\n"
	logs += "2024-01-01T00:00:01Z Ready to accept connections\n"

	WriteSuccess(w, map[string]string{"logs": logs})
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	workspaces := make([]*WorkspaceState, 0, len(s.workspaces))
	for _, ws := range s.workspaces {
		workspaces = append(workspaces, ws)
	}

	WriteSuccess(w, map[string]interface{}{
		"workspaces": workspaces,
		"total":      len(workspaces),
	})
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("reading request body: %w", err))
		return
	}
	defer r.Body.Close()

	var req CreateWorkspaceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}

	ws := &WorkspaceState{
		ID:        fmt.Sprintf("ws-%d", time.Now().UnixNano()),
		Name:      req.Name,
		Status:    "running",
		Backend:   req.Backend,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	s.mu.Lock()
	s.workspaces[ws.ID] = ws
	s.mu.Unlock()

	WriteSuccess(w, ws)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	delete(s.workspaces, id)
	s.mu.Unlock()

	WriteSuccess(w, map[string]bool{"success": true})
}

func (s *Server) startWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ws, exists := s.workspaces[id]; exists {
		ws.Status = "running"
		ws.UpdatedAt = time.Now()
		WriteSuccess(w, ws)
		return
	}
	WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
}

func (s *Server) stopWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}

	body, err := io.ReadAll(r.Body)
	if err != nil && len(body) > 0 {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("reading request body: %w", err))
		return
	}
	defer r.Body.Close()

	if len(body) > 0 {
		json.Unmarshal(body, &req)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if ws, exists := s.workspaces[id]; exists {
		ws.Status = "stopped"
		ws.UpdatedAt = time.Now()
		WriteSuccess(w, ws)
		return
	}
	WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
}

func (s *Server) execWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Command []string `json:"command"`
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("reading request body: %w", err))
		return
	}
	defer r.Body.Close()

	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}

	if len(req.Command) == 0 {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("command is required"))
		return
	}

	ctx := context.Background()
	params := map[string]interface{}{
		"command": req.Command[0],
		"args":    req.Command[1:],
	}
	jsonParams, _ := json.Marshal(params)
	result, rpcErr := handlers.HandleExec(ctx, jsonParams, s.ws, s.dockerBackend)
	if rpcErr != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("executing command: %v", rpcErr))
		return
	}

	WriteSuccess(w, map[string]string{"output": result.Stdout})
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

	switch msg.Method {
	case "fs.readFile":
		result, err = handlers.HandleReadFile(ctx, msg.Params, s.ws)
	case "fs.writeFile":
		result, err = handlers.HandleWriteFile(ctx, msg.Params, s.ws)
	case "fs.exists":
		result, err = handlers.HandleExists(ctx, msg.Params, s.ws)
	case "fs.readdir":
		result, err = handlers.HandleReaddir(ctx, msg.Params, s.ws)
	case "fs.mkdir":
		result, err = handlers.HandleMkdir(ctx, msg.Params, s.ws)
	case "fs.rm":
		result, err = handlers.HandleRm(ctx, msg.Params, s.ws)
	case "fs.stat":
		result, err = handlers.HandleStat(ctx, msg.Params, s.ws)
	case "exec":
		result, err = handlers.HandleExec(ctx, msg.Params, s.ws, s.dockerBackend)
	case "workspace.info":
		result = s.handleWorkspaceInfo()
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

func (s *Server) handleWorkspaceInfo() map[string]interface{} {
	return map[string]interface{}{
		"workspace_id":   s.ws.ID(),
		"workspace_path": s.ws.Path(),
	}
}
