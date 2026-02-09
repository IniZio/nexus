package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nexus/nexus/pkg/github"
)

// handleRegisterNode handles node registration
func (s *Server) handleRegisterNode(w http.ResponseWriter, r *http.Request) {
	// First decode into a generic map to handle both array and map formats
	var rawData map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&rawData); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Convert capabilities from array to map if needed
	if caps, ok := rawData["capabilities"].([]interface{}); ok {
		capMap := make(map[string]interface{})
		for _, c := range caps {
			if capStr, ok := c.(string); ok {
				capMap[capStr] = true
			}
		}
		rawData["capabilities"] = capMap
	}

	// Now decode the processed data into Node
	var node Node
	if err := json.Unmarshal(jsonEncode(rawData), &node); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if err := s.registry.Register(&node); err != nil {
		http.Error(w, fmt.Sprintf("Failed to register node: %v", err), http.StatusInternalServerError)
		return
	}

	s.broadcastEvent("node_registered", map[string]interface{}{
		"node": node,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(node)
}

// jsonEncode encodes a map to JSON bytes
func jsonEncode(data map[string]interface{}) []byte {
	b, _ := json.Marshal(data)
	return b
}

// handleListNodes handles listing all nodes
func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.registry.List()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list nodes: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"nodes": nodes,
		"count": len(nodes),
	})
}

// handleGetNode handles getting a specific node
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request, nodeID string) {
	node, err := s.registry.Get(nodeID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Node not found: %v", err), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(node)
}

// handleGetNodeStatus handles getting node status
func (s *Server) handleGetNodeStatus(w http.ResponseWriter, r *http.Request, nodeID string) {
	node, err := s.registry.Get(nodeID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Node not found: %v", err), http.StatusNotFound)
		return
	}

	status := map[string]interface{}{
		"node_id":   node.ID,
		"status":    node.Status,
		"last_seen": node.LastSeen,
		"uptime":    time.Since(node.CreatedAt).String(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleUpdateNode handles updating a node
func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request, nodeID string) {
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if err := s.registry.Update(nodeID, updates); err != nil {
		http.Error(w, fmt.Sprintf("Failed to update node: %v", err), http.StatusInternalServerError)
		return
	}

	node, err := s.registry.Get(nodeID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to retrieve updated node: %v", err), http.StatusInternalServerError)
		return
	}

	s.broadcastEvent("node_updated", map[string]interface{}{
		"node": node,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(node)
}

// handleUnregisterNode handles unregistering a node
func (s *Server) handleUnregisterNode(w http.ResponseWriter, r *http.Request, nodeID string) {
	if err := s.registry.Unregister(nodeID); err != nil {
		http.Error(w, fmt.Sprintf("Failed to unregister node: %v", err), http.StatusInternalServerError)
		return
	}

	s.broadcastEvent("node_unregistered", map[string]interface{}{
		"node_id": nodeID,
	})

	w.WriteHeader(http.StatusNoContent)
}

// handleSendCommand handles sending commands to nodes
func (s *Server) handleSendCommand(w http.ResponseWriter, r *http.Request, nodeID string) {
	var command Command
	if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Generate command ID if not provided
	if command.ID == "" {
		command.ID = fmt.Sprintf("cmd_%d_%s", time.Now().Unix(), nodeID)
	}
	command.Created = time.Now()

	// Verify node exists
	_, err := s.registry.Get(nodeID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Node not found: %v", err), http.StatusNotFound)
		return
	}

	// In a real implementation, this would dispatch to the node
	// For now, we'll simulate a response
	result := CommandResult{
		ID:       command.ID,
		NodeID:   nodeID,
		Command:  command,
		Status:   "success",
		Output:   "Command executed successfully",
		Duration: time.Millisecond * 100,
		Finished: time.Now(),
	}

	// Send result through channel for broadcasting
	select {
	case s.commandCh <- result:
	default:
		log.Printf("Command result channel full, dropping result for command %s", command.ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleCommandResult handles receiving command results from nodes
func (s *Server) handleCommandResult(w http.ResponseWriter, r *http.Request, commandID string) {
	var result CommandResult
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if result.ID != commandID {
		http.Error(w, "Command ID mismatch", http.StatusBadRequest)
		return
	}

	// Send result through channel for broadcasting
	select {
	case s.commandCh <- result:
	default:
		log.Printf("Command result channel full, dropping result for command %s", commandID)
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleListServices handles listing all services across all nodes
func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.registry.List()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list nodes: %v", err), http.StatusInternalServerError)
		return
	}

	services := make(map[string][]NodeService)
	for _, node := range nodes {
		if len(node.Services) > 0 {
			services[node.ID] = make([]NodeService, 0, len(node.Services))
			for _, service := range node.Services {
				services[node.ID] = append(services[node.ID], service)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"services": services,
		"nodes":    len(nodes),
	})
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.registry.List()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "unhealthy",
			"error":  err.Error(),
		})
		return
	}

	activeNodes := 0
	for _, node := range nodes {
		if node.Status == "active" {
			activeNodes++
		}
	}

	health := map[string]interface{}{
		"status":       "healthy",
		"timestamp":    time.Now(),
		"total_nodes":  len(nodes),
		"active_nodes": activeNodes,
		"version":      "1.0.0",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

// handleMetrics handles metrics requests
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.registry.List()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list nodes: %v", err), http.StatusInternalServerError)
		return
	}

	statusCounts := make(map[string]int)
	providerCounts := make(map[string]int)
	var totalServices int

	for _, node := range nodes {
		statusCounts[node.Status]++
		providerCounts[node.Provider]++
		totalServices += len(node.Services)
	}

	s.clientsMu.Lock()
	connectedClients := len(s.clients)
	s.clientsMu.Unlock()

	metrics := map[string]interface{}{
		"timestamp": time.Now(),
		"nodes": map[string]interface{}{
			"total":       len(nodes),
			"by_status":   statusCounts,
			"by_provider": providerCounts,
		},
		"services": map[string]interface{}{
			"total": totalServices,
		},
		"websocket": map[string]interface{}{
			"connected_clients": connectedClients,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// handleWebSocket handles WebSocket connections for real-time updates
// Simplified version using Server-Sent Events since WebSocket upgrade requires gorilla/websocket
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") == "websocket" {
		// For WebSocket support, gorilla/websocket would be needed
		// For now, we'll use Server-Sent Events as fallback
		s.handleServerSentEvents(w, r)
		return
	}

	// Server-Sent Events endpoint
	s.handleServerSentEvents(w, r)
}

// handleServerSentEvents provides SSE streaming for real-time updates
func (s *Server) handleServerSentEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create client channel
	clientChan := make(chan Event, 10)

	s.clientsMu.Lock()
	s.clients[clientChan] = true
	s.clientsMu.Unlock()

	defer func() {
		s.clientsMu.Lock()
		delete(s.clients, clientChan)
		s.clientsMu.Unlock()
		close(clientChan)
	}()

	// Send initial state
	nodes, _ := s.registry.List()
	initialEvent := Event{
		Type:      "initial_state",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"nodes": nodes,
		},
	}

	eventData, _ := json.Marshal(initialEvent)
	fmt.Fprintf(w, "data: %s\n\n", eventData)
	flusher.Flush()

	// Stream events
	for {
		select {
		case event := <-clientChan:
			eventData, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", eventData)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Middleware functions

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	if !s.config.Auth.Enabled {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		// Simple Bearer token validation
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid authorization header format", http.StatusUnauthorized)
			return
		}

		token := parts[1]
		if token != s.config.Server.AuthToken && token != s.config.Auth.JWTSecret {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start)
		log.Printf("%s %s %d %v", r.Method, r.URL.Path, wrapped.statusCode, duration)
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
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

// User management handlers

func (s *Server) handleUsersRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleRegisterUser(w, r)
	case http.MethodGet:
		s.handleListUsers(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleUserRequest(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/users/")
	if path == "" {
		http.Error(w, "Username required", http.StatusBadRequest)
		return
	}

	username := strings.Split(path, "/")[0]

	switch r.Method {
	case http.MethodGet:
		s.handleGetUser(w, r, username)
	case http.MethodDelete:
		s.handleDeleteUser(w, r, username)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleWorkspaceUsersRequest(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workspaces/")
	if path == "" {
		http.Error(w, "Workspace ID required", http.StatusBadRequest)
		return
	}

	parts := strings.Split(path, "/")
	workspaceID := parts[0]

	if len(parts) >= 2 && parts[1] == "users" {
		switch r.Method {
		case http.MethodGet:
			s.handleGetWorkspaceUsers(w, r, workspaceID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	if len(parts) >= 2 && parts[1] == "services" {
		switch r.Method {
		case http.MethodGet:
			s.handleGetWorkspaceServices(w, r, workspaceID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	http.Error(w, "Invalid endpoint", http.StatusNotFound)
}

func (s *Server) handleRegisterUser(w http.ResponseWriter, r *http.Request) {
	var user User
	if err := json.NewDecoder(r.Body).Decode(&user); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	userRegistry := s.registry.GetUserRegistry()
	if err := userRegistry.Register(&user); err != nil {
		http.Error(w, fmt.Sprintf("Failed to register user: %v", err), http.StatusInternalServerError)
		return
	}

	s.broadcastEvent("user_registered", map[string]interface{}{"user": user})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	userRegistry := s.registry.GetUserRegistry()
	users, err := userRegistry.List()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list users: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"users": users, "count": len(users)})
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request, username string) {
	userRegistry := s.registry.GetUserRegistry()
	user, err := userRegistry.GetByUsername(username)
	if err != nil {
		http.Error(w, fmt.Sprintf("User not found: %v", err), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, username string) {
	userRegistry := s.registry.GetUserRegistry()
	if err := userRegistry.Delete(username); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete user: %v", err), http.StatusInternalServerError)
		return
	}

	s.broadcastEvent("user_deleted", map[string]interface{}{"username": username})

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetWorkspaceUsers(w http.ResponseWriter, r *http.Request, workspaceID string) {
	userRegistry := s.registry.GetUserRegistry()
	users, err := userRegistry.GetByWorkspace(workspaceID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get workspace users: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"users": users, "count": len(users)})
}
func (s *Server) handleGetWorkspaceServices(w http.ResponseWriter, r *http.Request, workspaceID string) {
	// For now, return mock services. In a real implementation, this would query
	// the workspace services from the local nexus instance or coordination registry
	services := []map[string]interface{}{}

	// Mock services for demonstration
	services = append(services, map[string]interface{}{
		"name":       "web",
		"port":       3000,
		"local_port": 23000,
		"url":        "http://localhost:23000",
	})
	services = append(services, map[string]interface{}{
		"name":       "api",
		"port":       4000,
		"local_port": 23001,
		"url":        "http://localhost:23001",
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"workspace": workspaceID,
		"services":  services,
	})
}

// responseWriter is a wrapper to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// GitHubOAuthCallbackRequest is the OAuth callback query parameters
type GitHubOAuthCallbackRequest struct {
	Code  string `json:"code"`
	State string `json:"state"`
	Error string `json:"error"`
}

// GitHubOAuthCallbackResponse is the OAuth callback response
type GitHubOAuthCallbackResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// handleGitHubOAuthCallback handles GitHub OAuth callback
// POST /auth/github/callback
// Query params: code (authorization code), state (CSRF token)
// Returns: JSON response with status or redirects to success/error page
func (s *Server) handleGitHubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errMsg := r.URL.Query().Get("error")

	if code == "" {
		if errMsg != "" {
			log.Printf("GitHub OAuth error: %s", errMsg)
			http.Redirect(w, r, "/workspace/auth-error?error="+errMsg, http.StatusSeeOther)
		} else {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
		}
		return
	}

	// Validate CSRF token
	if !s.oauthStateStore.Validate(state) {
		log.Printf("Invalid OAuth state token: %s", state)
		resp := GitHubOAuthCallbackResponse{
			Success: false,
			Message: "Invalid state token (possibly expired)",
			Status:  "state_validation_failed",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Exchange authorization code for access token
	appConfig := s.appConfig
	if appConfig == nil {
		log.Printf("GitHub App configuration not initialized")
		http.Error(w, "GitHub App not configured", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	installation, err := github.ExchangeCodeForToken(ctx, appConfig.ClientID, appConfig.ClientSecret, code)
	if err != nil {
		log.Printf("Failed to exchange OAuth code: %v", err)
		resp := GitHubOAuthCallbackResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to authorize: %v", err),
			Status:  "exchange_failed",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Store GitHub installation in database
	// For now, we'll store it in memory via the workspace registry
	// In a production system, this would be a database operation
	gitHubInstallation := &GitHubInstallation{
		InstallationID: installation.InstallationID,
		UserID:         installation.GitHubUsername,
		GitHubUserID:   installation.GitHubUserID,
		GitHubUsername: installation.GitHubUsername,
		Token:          installation.AccessToken,
		TokenExpiresAt: installation.ExpiresAt,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	// Validate the installation
	if err := gitHubInstallation.Validate(); err != nil {
		log.Printf("Invalid GitHub installation data: %v", err)
		resp := GitHubOAuthCallbackResponse{
			Success: false,
			Message: fmt.Sprintf("Invalid installation data: %v", err),
			Status:  "validation_failed",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(resp)
		return
	}

	s.gitHubInstallationsMu.Lock()
	s.gitHubInstallations[installation.GitHubUsername] = gitHubInstallation
	s.gitHubInstallationsMu.Unlock()
	log.Printf("Stored GitHub installation in memory for user: %s", installation.GitHubUsername)

	if sqliteReg, ok := s.registry.(*SQLiteRegistry); ok {
		log.Printf("DEBUG: Registry is SQLiteRegistry, attempting to persist...")
		if err := sqliteReg.StoreGitHubInstallation(gitHubInstallation); err != nil {
			log.Printf("ERROR: Failed to persist GitHub installation to database: %v", err)
		} else {
			log.Printf("SUCCESS: Persisted GitHub installation to database for user: %s", installation.GitHubUsername)
		}
	} else {
		log.Printf("WARNING: Registry is not SQLiteRegistry (type: %T), cannot persist to database", s.registry)
	}

	userRegistry := s.registry.GetUserRegistry()
	_, err = userRegistry.GetByUsername(installation.GitHubUsername)
	if err != nil {
		newUser := &User{
			ID:        installation.GitHubUsername,
			Username:  installation.GitHubUsername,
			PublicKey: "",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := userRegistry.Register(newUser); err != nil {
			log.Printf("Warning: failed to auto-register user %s: %v", installation.GitHubUsername, err)
		} else {
			log.Printf("Auto-registered user: %s", installation.GitHubUsername)
		}
	}

	// Success response
	resp := GitHubOAuthCallbackResponse{
		Success: true,
		Message: "GitHub installation successful",
		Status:  "authorized",
	}

	// Return JSON if Accept header is application/json, otherwise redirect
	acceptHeader := r.Header.Get("Accept")
	if strings.Contains(acceptHeader, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	} else {
		// Redirect to success page
		http.Redirect(w, r, "/workspace/auth-success?user="+installation.GitHubUsername, http.StatusSeeOther)
	}
}

// handleAuthSuccess displays a success page after GitHub OAuth
func (s *Server) handleAuthSuccess(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	html := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head><title>GitHub Authentication Successful</title></head>
<body>
	<h1>✅ GitHub Authentication Successful!</h1>
	<p>User: <strong>%s</strong></p>
	<p>Your GitHub account has been successfully linked to Nexus.</p>
	<p>You can now close this window and return to the CLI.</p>
</body>
</html>
`, user)
	w.Write([]byte(html))
}

// handleAuthError displays an error page after GitHub OAuth failure
func (s *Server) handleAuthError(w http.ResponseWriter, r *http.Request) {
	errMsg := r.URL.Query().Get("error")
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	html := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head><title>GitHub Authentication Failed</title></head>
<body>
	<h1>❌ GitHub Authentication Failed</h1>
	<p>Error: <strong>%s</strong></p>
	<p>Please try again or contact support.</p>
</body>
</html>
`, errMsg)
	w.Write([]byte(html))
}

// handleGetGitHubToken retrieves a valid GitHub installation access token
// GET /api/github/token
// Returns: JSON with token and expiration time
func (s *Server) handleGetGitHubToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract user ID from request context or header
	// In a real implementation, this would come from authenticated user context
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		http.Error(w, "User ID required", http.StatusUnauthorized)
		return
	}

	s.gitHubInstallationsMu.RLock()
	installation, exists := s.gitHubInstallations[userID]
	s.gitHubInstallationsMu.RUnlock()

	if !exists {
		resp := map[string]interface{}{
			"error": "GitHub installation not found for user",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp := map[string]interface{}{
		"token":       installation.Token,
		"expires_at":  installation.TokenExpiresAt,
		"user_id":     installation.UserID,
		"github_user": installation.GitHubUsername,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// GitHubOAuthURLRequest is the request to generate an OAuth authorization URL
type GitHubOAuthURLRequest struct {
	RepoFullName string `json:"repo_full_name"`
}

// GitHubOAuthURLResponse is the OAuth authorization URL
type GitHubOAuthURLResponse struct {
	AuthURL string `json:"auth_url"`
	State   string `json:"state"`
}

// handleGetGitHubOAuthURL generates a GitHub OAuth authorization URL
// POST /api/github/oauth-url
// Body: {repo_full_name: "owner/repo"}
// Returns: GitHub OAuth authorization URL
func (s *Server) handleGetGitHubOAuthURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req GitHubOAuthURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	appConfig := s.appConfig
	if appConfig == nil {
		http.Error(w, "GitHub App not configured", http.StatusInternalServerError)
		return
	}

	// Generate state token for CSRF protection
	state := fmt.Sprintf("state_%d_%s", time.Now().UnixNano(), req.RepoFullName)
	s.oauthStateStore.Store(state)

	// Build OAuth authorization URL for user-based authentication
	// This allows the user to authorize the app to act on their behalf
	authURL := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&state=%s&scope=repo",
		appConfig.ClientID,
		url.QueryEscape(appConfig.RedirectURL),
		state,
	)

	resp := GitHubOAuthURLResponse{
		AuthURL: authURL,
		State:   state,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
