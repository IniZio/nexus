package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type WorkspaceHTTPHandler struct {
	manager  *WorkspaceManager
	mux      *http.ServeMux
	server   *http.Server
	listener net.Listener
	mu       sync.RWMutex
}

func NewWorkspaceHTTPHandler(manager *WorkspaceManager, port int) *WorkspaceHTTPHandler {
	h := &WorkspaceHTTPHandler{
		manager: manager,
		mux:     http.NewServeMux(),
	}

	h.registerRoutes()

	h.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      h.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return h
}

func (h *WorkspaceHTTPHandler) registerRoutes() {
	h.mux.HandleFunc("/api/v1/workspaces/create", h.handleCreateWorkspace)
	h.mux.HandleFunc("/api/v1/workspaces", h.handleListWorkspaces)
	h.mux.HandleFunc("/api/v1/workspaces/", h.handleWorkspaceAction)
	h.mux.HandleFunc("/api/v1/workspaces/status/", h.handleGetWorkspaceStatus)
	h.mux.HandleFunc("/api/v1/health", h.handleHealth)
}

func (h *WorkspaceHTTPHandler) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", h.server.Addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", h.server.Addr, err)
	}

	h.mu.Lock()
	h.listener = listener
	h.mu.Unlock()

	go func() {
		if err := h.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Workspace HTTP server error: %v", err)
		}
	}()

	log.Printf("Workspace HTTP handler listening on %s", h.server.Addr)
	return nil
}

func (h *WorkspaceHTTPHandler) Stop(ctx context.Context) error {
	return h.server.Shutdown(ctx)
}

func (h *WorkspaceHTTPHandler) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var cmd CreateWorkspaceCommand
	if err := json.Unmarshal(body, &cmd); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse request: %v", err), http.StatusBadRequest)
		return
	}

	if err := cmd.Validate(); err != nil {
		http.Error(w, fmt.Sprintf("validation failed: %v", err), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := h.manager.CreateWorkspace(ctx, &cmd)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create workspace: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if result.Status == WorkspaceStatusError {
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		w.WriteHeader(http.StatusCreated)
	}

	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}

func (h *WorkspaceHTTPHandler) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.manager.mu.RLock()
	workspaces := make([]WorkspaceStatusUpdate, 0, len(h.manager.workspaces))
	for _, ws := range h.manager.workspaces {
		status := h.manager.GetWorkspaceStatus(ws.Command.WorkspaceID)
		if status != nil {
			workspaces = append(workspaces, *status)
		}
	}
	h.manager.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"workspaces": workspaces,
		"count":      len(workspaces),
	}); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}

func (h *WorkspaceHTTPHandler) handleWorkspaceAction(w http.ResponseWriter, r *http.Request) {
	parts := r.URL.Path[len("/api/v1/workspaces/"):]

	switch {
	case r.Method == http.MethodDelete:
		h.handleDeleteWorkspace(w, r, parts)
	case r.Method == http.MethodPost && r.URL.Query().Get("action") == "start-services":
		h.handleStartServices(w, r, parts)
	case r.Method == http.MethodPost && r.URL.Query().Get("action") == "stop":
		h.handleStopWorkspace(w, r, parts)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (h *WorkspaceHTTPHandler) handleGetWorkspaceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	workspaceID := r.URL.Path[len("/api/v1/workspaces/status/"):]
	if workspaceID == "" {
		http.Error(w, "workspace_id required", http.StatusBadRequest)
		return
	}

	status := h.manager.GetWorkspaceStatus(workspaceID)
	if status == nil {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}

func (h *WorkspaceHTTPHandler) handleStartServices(w http.ResponseWriter, r *http.Request, workspaceID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	statusUpdate, err := h.manager.StartServices(ctx, workspaceID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to start services: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(statusUpdate); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}

func (h *WorkspaceHTTPHandler) handleStopWorkspace(w http.ResponseWriter, r *http.Request, workspaceID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := h.manager.StopWorkspace(ctx, workspaceID); err != nil {
		http.Error(w, fmt.Sprintf("failed to stop workspace: %v", err), http.StatusInternalServerError)
		return
	}

	status := h.manager.GetWorkspaceStatus(workspaceID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}

func (h *WorkspaceHTTPHandler) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request, workspaceID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := h.manager.DeleteWorkspace(ctx, workspaceID); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete workspace: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *WorkspaceHTTPHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.manager.mu.RLock()
	workspaceCount := len(h.manager.workspaces)
	h.manager.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "ok",
		"timestamp":  time.Now().Format(time.RFC3339),
		"workspaces": workspaceCount,
	}); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}
