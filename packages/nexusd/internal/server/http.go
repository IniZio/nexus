package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nexus/nexus/packages/nexusd/internal/docker"
	"github.com/nexus/nexus/packages/nexusd/internal/health"
	"github.com/nexus/nexus/packages/nexusd/internal/ssh"
	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

type HTTPServer struct {
	addr         string
	mux          *http.ServeMux
	backend      *docker.DockerBackend
	workspaces   map[string]*types.Workspace
	mu           sync.RWMutex
	server       *http.Server
	sshBridges   map[string]*ssh.SSHBridge
	healthChecks map[string]*health.HealthChecker
}

func NewHTTPServer(addr string, backend *docker.DockerBackend) *HTTPServer {
	mux := http.NewServeMux()
	s := &HTTPServer{
		addr:         addr,
		mux:          mux,
		backend:      backend,
		workspaces:   make(map[string]*types.Workspace),
		sshBridges:   make(map[string]*ssh.SSHBridge),
		healthChecks: make(map[string]*health.HealthChecker),
	}
	s.registerRoutes()
	return s
}

func (s *HTTPServer) registerRoutes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/workspaces", s.handleWorkspaces)
	s.mux.HandleFunc("/api/v1/workspaces/", s.handleWorkspaceByID)
}

func (s *HTTPServer) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	s.mux.HandleFunc(pattern, handler)
}

func (s *HTTPServer) Start() error {
	s.server = &http.Server{
		Addr:    s.addr,
		Handler: s.mux,
	}
	return s.server.ListenAndServe()
}

func (s *HTTPServer) StartTLS(certFile, keyFile string) error {
	s.server = &http.Server{
		Addr:    s.addr,
		Handler: s.mux,
	}
	return s.server.ListenAndServeTLS(certFile, keyFile)
}

func (s *HTTPServer) Stop() error {
	if s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	select {
	case <-ctx.Done():
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	default:
	}

	WriteJSON(w, http.StatusOK, map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

func (s *HTTPServer) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listWorkspaces(w, r)
	case http.MethodPost:
		s.createWorkspace(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) handleWorkspaceByID(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(r.URL.Path)

	s.mu.RLock()
	ws, exists := s.workspaces[id]
	s.mu.RUnlock()

	if !exists {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getWorkspace(w, r, ws)
	case http.MethodDelete:
		s.deleteWorkspace(w, r, ws)
	default:
			subPath := r.URL.Path[len("/api/v1/workspaces/"+id):]
			switch {
			case strings.HasPrefix(subPath, "/start"):
				s.startWorkspace(w, r, ws)
			case strings.HasPrefix(subPath, "/stop"):
				s.stopWorkspace(w, r, ws)
			case strings.HasPrefix(subPath, "/exec"):
				s.execWorkspace(w, r, ws)
			case strings.HasPrefix(subPath, "/health"):
				s.getWorkspaceHealth(w, r, ws)
			case strings.HasPrefix(subPath, "/sync"):
				s.handleSync(w, r, ws)
			case strings.HasPrefix(subPath, "/sync/status"):
				s.getSyncStatus(w, r, ws)
			case strings.HasPrefix(subPath, "/sync/pause"):
				s.pauseSync(w, r, ws)
			case strings.HasPrefix(subPath, "/sync/resume"):
				s.resumeSync(w, r, ws)
			case strings.HasPrefix(subPath, "/sync/flush"):
				s.flushSync(w, r, ws)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		}
}

func (s *HTTPServer) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	workspaces := make([]*types.Workspace, 0, len(s.workspaces))
	for _, ws := range s.workspaces {
		workspaces = append(workspaces, ws)
	}

	WriteSuccess(w, map[string]interface{}{
		"workspaces": workspaces,
		"total":      len(workspaces),
	})
}

func (s *HTTPServer) createWorkspace(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("reading request body: %w", err))
		return
	}
	defer r.Body.Close()

	var req types.CreateWorkspaceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}

	wsID := req.ID
	if wsID == "" {
		wsID = fmt.Sprintf("ws-%d", time.Now().UnixNano())
	}
	req.ID = wsID

	var bridgeSocketPath string
	if s.backend != nil && req.ForwardSSH {
		bridge, err := ssh.NewBridge(wsID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating SSH bridge: %w", err))
			return
		}

		socketPath, err := bridge.Start()
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("starting SSH bridge: %w", err))
			return
		}

		bridgeSocketPath = socketPath
		s.mu.Lock()
		s.sshBridges[wsID] = bridge
		s.mu.Unlock()

		fmt.Printf("SSH bridge created for workspace %s at %s\n", wsID, socketPath)
	}

	var workspace *types.Workspace
	if s.backend != nil && bridgeSocketPath != "" {
		workspace, err = s.backend.CreateWorkspaceWithBridge(r.Context(), &req, bridgeSocketPath)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating workspace: %w", err))
			return
		}
	} else {
		workspace, err = s.backend.CreateWorkspace(r.Context(), &req)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating workspace: %w", err))
			return
		}
	}

	s.mu.Lock()
	s.workspaces[workspace.ID] = workspace
	s.mu.Unlock()

	WriteSuccess(w, workspace)
}

func (s *HTTPServer) getWorkspace(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	status, err := s.backend.GetWorkspaceStatus(r.Context(), ws.ID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("getting status: %w", err))
		return
	}

	ws.Status = status

	WriteSuccess(w, ws)
}

func (s *HTTPServer) deleteWorkspace(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	if err := s.backend.DeleteWorkspace(r.Context(), ws.ID); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("deleting workspace: %w", err))
		return
	}

	s.mu.Lock()
	delete(s.workspaces, ws.ID)
	s.mu.Unlock()

	WriteSuccess(w, map[string]bool{"success": true})
}

func (s *HTTPServer) startWorkspace(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	_, err := s.backend.StartWorkspace(r.Context(), ws.ID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("starting workspace: %w", err))
		return
	}

	ws.Status = types.StatusRunning
	WriteSuccess(w, ws)
}

func (s *HTTPServer) stopWorkspace(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	var req struct {
		TimeoutSeconds int32 `json:"timeout_seconds"`
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("reading request body: %w", err))
		return
	}
	defer r.Body.Close()

	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			WriteError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
			return
		}
	}

	_, err = s.backend.StopWorkspace(r.Context(), ws.ID, req.TimeoutSeconds)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("stopping workspace: %w", err))
		return
	}

	ws.Status = types.StatusStopped
	WriteSuccess(w, ws)
}

func (s *HTTPServer) execWorkspace(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
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

	output, err := s.backend.Exec(r.Context(), ws.ID, req.Command)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("executing command: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{
		"output": output,
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
		Error:    err.Error(),
	})
}

func WriteSuccess(w http.ResponseWriter, data interface{}) {
	WriteJSON(w, http.StatusOK, APIResponse{
		Success: true,
		Data:    data,
	})
}

type HealthHandler struct{}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	select {
	case <-ctx.Done():
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	default:
	}

	response := map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	}
	WriteJSON(w, http.StatusOK, response)
}

type Middleware func(http.Handler) http.Handler

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		fmt.Printf("%s %s %v\n", r.Method, r.URL.Path, time.Since(start))
	})
}

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *HTTPServer) handleSync(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	switch r.Method {
	case http.MethodPost:
		s.startSync(w, r, ws)
	case http.MethodDelete:
		s.stopSync(w, r, ws)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) startSync(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	if s.backend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("backend not available"))
		return
	}

	sessionID, err := s.backend.StartSync(r.Context(), ws.ID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("starting sync: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{
		"session_id": sessionID,
		"state":      "started",
	})
}

func (s *HTTPServer) stopSync(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	if s.backend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("backend not available"))
		return
	}

	if err := s.backend.StopSync(r.Context(), ws.ID); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("stopping sync: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{
		"state": "stopped",
	})
}

func (s *HTTPServer) getSyncStatus(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	if s.backend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("backend not available"))
		return
	}

	status, err := s.backend.GetSyncStatus(r.Context(), ws.ID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("getting sync status: %w", err))
		return
	}

	WriteSuccess(w, status)
}

func (s *HTTPServer) pauseSync(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	if s.backend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("backend not available"))
		return
	}

	if err := s.backend.PauseSync(r.Context(), ws.ID); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("pausing sync: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{
		"state": "paused",
	})
}

func (s *HTTPServer) resumeSync(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	if s.backend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("backend not available"))
		return
	}

	if err := s.backend.ResumeSync(r.Context(), ws.ID); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("resuming sync: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{
		"state": "resumed",
	})
}

func (s *HTTPServer) flushSync(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	if s.backend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("backend not available"))
		return
	}

	if err := s.backend.FlushSync(r.Context(), ws.ID); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("flushing sync: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{
		"state": "flushed",
	})
}

func (s *HTTPServer) getWorkspaceHealth(w http.ResponseWriter, r *http.Request, ws *types.Workspace) {
	s.mu.RLock()
	checker, exists := s.healthChecks[ws.ID]
	s.mu.RUnlock()

	if !exists {
		checker = health.NewHealthChecker(ws.ID)

		for _, port := range ws.Ports {
			if port.Name == "http" || port.Name == "web" {
				checker.AddCheck(health.HealthCheck{
					Name:    port.Name,
					Type:    "http",
					Target:  fmt.Sprintf("localhost:%d", port.HostPort),
					Timeout: 10 * time.Second,
				})
			}
		}

		s.mu.Lock()
		s.healthChecks[ws.ID] = checker
		s.mu.Unlock()
	}

	serviceName := r.URL.Query().Get("service")
	if serviceName != "" {
		for _, check := range checker.Check().Checks {
			if check.Name == serviceName {
				WriteSuccess(w, types.HealthStatus{
					Healthy: check.Healthy,
					Checks: []types.CheckResult{
						{
							Name:    check.Name,
							Healthy: check.Healthy,
							Error:   check.Error,
							Latency: check.Latency,
						},
					},
					LastCheck: time.Now(),
				})
				return
			}
		}
		WriteError(w, http.StatusNotFound, fmt.Errorf("service %s not found", serviceName))
		return
	}

	status := checker.Check()

	typeChecks := make([]types.CheckResult, len(status.Checks))
	for i, c := range status.Checks {
		typeChecks[i] = types.CheckResult{
			Name:    c.Name,
			Healthy: c.Healthy,
			Error:   c.Error,
			Latency: c.Latency,
		}
	}

	ws.Health = &types.HealthStatus{
		Healthy:   status.Healthy,
		Checks:    typeChecks,
		LastCheck: status.LastCheck,
	}

	WriteSuccess(w, ws.Health)
}
