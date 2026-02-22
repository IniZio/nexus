package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/docker"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/types"
)

type HTTPServer struct {
	addr      string
	mux       *http.ServeMux
	backend   *docker.DockerBackend
	workspaces map[string]*types.Workspace
	mu        sync.RWMutex
}

func NewHTTPServer(addr string, backend *docker.DockerBackend) *HTTPServer {
	mux := http.NewServeMux()
	s := &HTTPServer{
		addr:       addr,
		mux:        mux,
		backend:    backend,
		workspaces: make(map[string]*types.Workspace),
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
	return http.ListenAndServe(s.addr, s.mux)
}

func (s *HTTPServer) StartTLS(certFile, keyFile string) error {
	return http.ListenAndServeTLS(s.addr, certFile, keyFile, s.mux)
}

func (s *HTTPServer) Stop(ctx context.Context) error {
	return nil
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
		switch subPath {
		case "/start":
			s.startWorkspace(w, r, ws)
		case "/stop":
			s.stopWorkspace(w, r, ws)
		case "/exec":
			s.execWorkspace(w, r, ws)
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

	workspace, err := s.backend.CreateWorkspace(r.Context(), &req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating workspace: %w", err))
		return
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
