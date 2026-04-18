package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/secrets/vending"
)

// HostServer is a singleton HTTP server that serves tokens to all workspaces.
// It uses workspaceID from requests to scope tokens appropriately.
type HostServer struct {
	port    uint32
	service *vending.HostVendingService
	server  *http.Server
	mu      sync.RWMutex
	running bool
}

var (
	hostServerInstance *HostServer
	hostServerOnce     sync.Once
	hostServerInitErr  error
)

// GetHostServer returns the singleton host server, initializing if needed.
func GetHostServer(port uint32) (*HostServer, error) {
	hostServerOnce.Do(func() {
		svc, err := vending.GetHostVendingService()
		if err != nil {
			hostServerInitErr = fmt.Errorf("failed to get vending service: %w", err)
			return
		}

		hostServerInstance = &HostServer{
			port:    port,
			service: svc,
		}
	})

	return hostServerInstance, hostServerInitErr
}

// Start begins listening for token requests.
// Safe to call multiple times - only starts once.
func (h *HostServer) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.running {
		return nil // Already running
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/token", h.handleTokenRequest)
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/providers", h.handleListProviders)

	h.server = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", h.port),
		Handler: mux,
	}

	h.running = true

	// Start in background
	go func() {
		log.Printf("[vending] Host server listening on %s", h.server.Addr)
		if err := h.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[vending] Server error: %v", err)
		}
	}()

	return nil
}

// Stop shuts down the server.
func (h *HostServer) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.running {
		return nil
	}

	h.running = false

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return h.server.Shutdown(ctx)
}

// IsRunning returns true if server is active.
func (h *HostServer) IsRunning() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.running
}

// Port returns the configured port.
func (h *HostServer) Port() uint32 {
	return h.port
}

// handleTokenRequest serves tokens to workspaces.
// POST /token with JSON body: {"workspace_id": "...", "user_id": "...", "provider": "..."}
func (h *HostServer) handleTokenRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		WorkspaceID string `json:"workspace_id"`
		UserID      string `json:"user_id"` // For future multi-tenant
		Provider    string `json:"provider"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if req.WorkspaceID == "" || req.Provider == "" {
		http.Error(w, "Missing workspace_id or provider", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	token, err := h.service.GetToken(ctx, req.WorkspaceID, req.UserID, req.Provider)

	resp := struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Error     string `json:"error,omitempty"`
	}{}

	if err != nil {
		resp.Error = err.Error()
		w.WriteHeader(http.StatusNotFound)
	} else {
		resp.Token = token.Value
		resp.ExpiresAt = token.ExpiresAt.Unix()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleHealth returns server health status.
func (h *HostServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	providers := h.service.ListProviders("")

	resp := struct {
		Status    string   `json:"status"`
		Providers []string `json:"providers"`
	}{
		Status:    "healthy",
		Providers: providers,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleListProviders returns available providers.
func (h *HostServer) handleListProviders(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id") // For future multi-tenant
	providers := h.service.ListProviders(userID)

	resp := struct {
		Providers []string `json:"providers"`
	}{
		Providers: providers,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
