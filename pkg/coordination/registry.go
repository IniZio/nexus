package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nexus/nexus/pkg/feedback"
	"github.com/nexus/nexus/pkg/github"
	"github.com/nexus/nexus/pkg/insights"
	"github.com/nexus/nexus/pkg/metrics"
	"github.com/nexus/nexus/pkg/provider"
	"github.com/nexus/nexus/pkg/provider/docker"
	"github.com/nexus/nexus/pkg/provider/lxc"
	"github.com/nexus/nexus/pkg/slack"
)

// Node represents a remote node in the coordination system
type Node struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Provider     string                 `json:"provider"`
	Status       string                 `json:"status"`
	Address      string                 `json:"address"`
	Port         int                    `json:"port"`
	LastSeen     time.Time              `json:"last_seen"`
	Labels       map[string]string      `json:"labels,omitempty"`
	Capabilities map[string]interface{} `json:"capabilities,omitempty"`
	Services     map[string]NodeService `json:"services,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

type NodeService struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Status   string            `json:"status"`
	Port     int               `json:"port"`
	Endpoint string            `json:"endpoint,omitempty"`
	Health   *HealthStatus     `json:"health,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// HealthStatus represents the health status of a service or node
type HealthStatus struct {
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	LastCheck time.Time `json:"last_check"`
	URL       string    `json:"url,omitempty"`
}

// User represents a user in the coordination system
type User struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	PublicKey   string    `json:"public_key"`
	WorkspaceID string    `json:"workspace_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Command represents a command to be executed on a node
type Command struct {
	ID      string                 `json:"id"`
	Type    string                 `json:"type"`   // exec, service, config
	Target  string                 `json:"target"` // node or service name
	Action  string                 `json:"action"`
	Params  map[string]interface{} `json:"params,omitempty"`
	Timeout time.Duration          `json:"timeout,omitempty"`
	User    string                 `json:"user,omitempty"`
	Created time.Time              `json:"created"`
}

// CommandResult represents the result of a command execution
type CommandResult struct {
	ID       string        `json:"id"`
	NodeID   string        `json:"node_id"`
	Command  Command       `json:"command"`
	Status   string        `json:"status"` // success, error, timeout
	Output   string        `json:"output,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
	Finished time.Time     `json:"finished"`
}

// Registry manages node registration and tracking
type Registry interface {
	Register(node *Node) error
	Unregister(id string) error
	Get(id string) (*Node, error)
	List() ([]*Node, error)
	Update(id string, updates map[string]interface{}) error
	SetStatus(id, status string) error
	GetByLabel(key, value string) ([]*Node, error)
	GetByCapability(capability string) ([]*Node, error)
	GetUserRegistry() UserRegistry
}

// UserRegistry manages user registration and tracking
type UserRegistry interface {
	Register(user *User) error
	GetByUsername(username string) (*User, error)
	GetByWorkspace(workspaceID string) ([]*User, error)
	List() ([]*User, error)
	Delete(username string) error
}

// InMemoryRegistry provides an in-memory implementation of Registry
type InMemoryRegistry struct {
	nodes        map[string]*Node
	userRegistry UserRegistry
	nodeMutex    sync.RWMutex
}

// NewInMemoryRegistry creates a new in-memory node registry
func NewInMemoryRegistry() *InMemoryRegistry {
	return &InMemoryRegistry{
		nodes:        make(map[string]*Node),
		userRegistry: NewInMemoryUserRegistry(),
	}
}

func (r *InMemoryRegistry) Register(node *Node) error {
	r.nodeMutex.Lock()
	defer r.nodeMutex.Unlock()

	if node.ID == "" {
		return fmt.Errorf("node ID cannot be empty")
	}

	now := time.Now()
	node.CreatedAt = now
	node.UpdatedAt = now
	node.LastSeen = now

	r.nodes[node.ID] = node
	return nil
}

func (r *InMemoryRegistry) Unregister(id string) error {
	r.nodeMutex.Lock()
	defer r.nodeMutex.Unlock()

	delete(r.nodes, id)
	return nil
}

func (r *InMemoryRegistry) Get(id string) (*Node, error) {
	r.nodeMutex.RLock()
	defer r.nodeMutex.RUnlock()

	node, exists := r.nodes[id]
	if !exists {
		return nil, fmt.Errorf("node not found: %s", id)
	}
	return node, nil
}

func (r *InMemoryRegistry) List() ([]*Node, error) {
	r.nodeMutex.RLock()
	defer r.nodeMutex.RUnlock()

	nodes := make([]*Node, 0, len(r.nodes))
	for _, node := range r.nodes {
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (r *InMemoryRegistry) Update(id string, updates map[string]interface{}) error {
	r.nodeMutex.Lock()
	defer r.nodeMutex.Unlock()

	node, exists := r.nodes[id]
	if !exists {
		return fmt.Errorf("node not found: %s", id)
	}

	for key, value := range updates {
		switch key {
		case "status":
			node.Status = value.(string)
		case "address":
			node.Address = value.(string)
		case "port":
			node.Port = value.(int)
		case "labels":
			if m, ok := value.(map[string]string); ok {
				node.Labels = m
			}
		case "capabilities":
			if m, ok := value.(map[string]interface{}); ok {
				node.Capabilities = m
			}
		case "services":
			if m, ok := value.(map[string]NodeService); ok {
				node.Services = m
			}
		case "metadata":
			if m, ok := value.(map[string]interface{}); ok {
				node.Metadata = m
			}
		}
	}

	node.UpdatedAt = time.Now()
	node.LastSeen = time.Now()
	return nil
}

func (r *InMemoryRegistry) SetStatus(id, status string) error {
	return r.Update(id, map[string]interface{}{"status": status})
}

func (r *InMemoryRegistry) GetByLabel(key, value string) ([]*Node, error) {
	r.nodeMutex.RLock()
	defer r.nodeMutex.RUnlock()

	var nodes []*Node
	for _, node := range r.nodes {
		if node.Labels[key] == value {
			nodes = append(nodes, node)
		}
	}
	return nodes, nil
}

func (r *InMemoryRegistry) GetByCapability(capability string) ([]*Node, error) {
	r.nodeMutex.RLock()
	defer r.nodeMutex.RUnlock()

	var nodes []*Node
	for _, node := range r.nodes {
		if _, exists := node.Capabilities[capability]; exists {
			nodes = append(nodes, node)
		}
	}
	return nodes, nil
}
func (r *InMemoryRegistry) GetUserRegistry() UserRegistry {
	return r.userRegistry
}

// InMemoryUserRegistry provides an in-memory implementation of UserRegistry
type InMemoryUserRegistry struct {
	users map[string]*User
	mutex sync.RWMutex
}

// NewInMemoryUserRegistry creates a new in-memory user registry
func NewInMemoryUserRegistry() *InMemoryUserRegistry {
	return &InMemoryUserRegistry{
		users: make(map[string]*User),
	}
}

// Register registers a new user
func (r *InMemoryUserRegistry) Register(user *User) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if user.Username == "" {
		return fmt.Errorf("username cannot be empty")
	}

	if user.ID == "" {
		user.ID = fmt.Sprintf("user_%d_%s", time.Now().Unix(), user.Username)
	}

	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now

	r.users[user.Username] = user
	return nil
}

// GetByUsername retrieves a user by username
func (r *InMemoryUserRegistry) GetByUsername(username string) (*User, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	user, exists := r.users[username]
	if !exists {
		return nil, fmt.Errorf("user not found: %s", username)
	}
	return user, nil
}

// GetByWorkspace retrieves all users for a workspace
func (r *InMemoryUserRegistry) GetByWorkspace(workspaceID string) ([]*User, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	var users []*User
	for _, user := range r.users {
		if user.WorkspaceID == workspaceID {
			users = append(users, user)
		}
	}
	return users, nil
}

// List retrieves all users
func (r *InMemoryUserRegistry) List() ([]*User, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	users := make([]*User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, user)
	}
	return users, nil
}

// Delete removes a user by username
func (r *InMemoryUserRegistry) Delete(username string) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	delete(r.users, username)
	return nil
}

// Server represents the coordination server
type Server struct {
	config                *Config
	registry              Registry
	workspaceRegistry     WorkspaceRegistry
	httpSrv               *http.Server
	router                *http.ServeMux
	clients               map[chan Event]bool
	clientsMu             sync.Mutex
	commandCh             chan CommandResult
	provider              provider.Provider
	appConfig             *github.AppConfig
	oauthStateStore       *OAuthStateStore
	gitHubInstallations   map[string]*GitHubInstallation
	gitHubInstallationsMu sync.RWMutex
	pulseSyncRegistry     *PulseSyncRegistry
	pulseSync             *PulseSync
	// Feedback storage
	feedbackCollector   FeedbackCollector
	feedbackStore       map[string]*feedback.Feedback
	feedbackMu          sync.RWMutex

	// Analytics service
	analyticsService *CombinedAnalyticsService

	// Workflow tracker
	workflowTracker *metrics.WorkflowTracker

	// AI Insights service
	insightsService *InsightsService

	// Slack integration
	slackWebhookHandler *slack.WebhookHandler
}

// OAuthStateStore stores OAuth state tokens with expiration for CSRF protection
type OAuthStateStore struct {
	states map[string]time.Time
	mu     sync.RWMutex
	ttl    time.Duration
}

// NewOAuthStateStore creates a new OAuth state store
func NewOAuthStateStore(ttl time.Duration) *OAuthStateStore {
	store := &OAuthStateStore{
		states: make(map[string]time.Time),
		ttl:    ttl,
	}
	go store.cleanupExpired()
	return store
}

// Store saves an OAuth state token
func (s *OAuthStateStore) Store(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[state] = time.Now().Add(s.ttl)
}

// Validate checks if a state token is valid and removes it
func (s *OAuthStateStore) Validate(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	expiry, exists := s.states[state]
	if !exists || time.Now().After(expiry) {
		delete(s.states, state)
		return false
	}

	delete(s.states, state)
	return true
}

// cleanupExpired removes expired state tokens periodically
func (s *OAuthStateStore) cleanupExpired() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for state, expiry := range s.states {
			if now.After(expiry) {
				delete(s.states, state)
			}
		}
		s.mu.Unlock()
	}
}

// Event represents a server event for broadcasting
type Event struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// initializeRegistry creates a registry based on configuration
func initializeRegistry(cfg *Config) Registry {
	storageType := cfg.Registry.Storage.Type
	if storageType == "" {
		storageType = "sqlite"
	}

	storagePath := cfg.Registry.Storage.Path
	if storagePath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			storagePath = filepath.Join(home, ".nexus-runtime", "data", "nexus.db")
		} else {
			storagePath = ".nexus-runtime/data/nexus.db"
		}
	}

	if envPath := os.Getenv("DB_PATH"); envPath != "" {
		storagePath = envPath
	}

	switch storageType {
	case "sqlite":
		sqliteRegistry, err := NewSQLiteRegistry(storagePath)
		if err != nil {
			fmt.Printf("Warning: failed to initialize SQLite registry at %s, falling back to in-memory: %v\n", storagePath, err)
			return NewInMemoryRegistry()
		}
		fmt.Printf("Using SQLite registry at: %s\n", storagePath)
		return sqliteRegistry
	case "memory":
		fmt.Printf("Using in-memory registry\n")
		return NewInMemoryRegistry()
	default:
		fmt.Printf("Warning: unknown storage type %s, falling back to in-memory\n", storageType)
		return NewInMemoryRegistry()
	}
}

// NewServer creates a new coordination server
func NewServer(cfg *Config) *Server {
	registry := initializeRegistry(cfg)

	srv := &Server{
		config:              cfg,
		registry:            registry,
		workspaceRegistry:   NewInMemoryWorkspaceRegistry(),
		router:              http.NewServeMux(),
		clients:             make(map[chan Event]bool),
		commandCh:           make(chan CommandResult, 100),
		oauthStateStore:     NewOAuthStateStore(5 * time.Minute),
		gitHubInstallations: make(map[string]*GitHubInstallation),
	}

	if err := srv.initializeProvider(); err != nil {
		fmt.Printf("Warning: failed to initialize provider: %v\n", err)
	}

	appConfig, err := github.NewAppConfig()
	if err != nil {
		fmt.Printf("Warning: GitHub App not configured: %v\n", err)
	} else {
		srv.appConfig = appConfig
	}

	if testToken := os.Getenv("GITHUB_TOKEN"); testToken != "" {
		fmt.Printf("TEST MODE: Injecting GitHub token for development\n")
		srv.gitHubInstallations["IniZio"] = &GitHubInstallation{
			Token:          testToken,
			GitHubUsername: "IniZio",
			GitHubUserID:   123456,
			UserID:         "test-user-id",
			TokenExpiresAt: time.Now().Add(24 * time.Hour),
		}
	}

	srv.setupRoutes()

	// Initialize Pulse sync registry
	srv.initPulseSync()

	// Initialize analytics service
	srv.initAnalytics()

	// Initialize insights service
	srv.insightsService = NewInsightsService()

	// Initialize Slack webhook handler
	srv.initSlackHandler()

	return srv
}

func (s *Server) initializeProvider() error {
	providerType := s.config.Provider.Type
	if providerType == "" {
		providerType = "docker"
	}

	switch providerType {
	case "lxc":
		prv, err := lxc.NewLXCProvider()
		if err != nil {
			return fmt.Errorf("failed to initialize LXC provider: %w", err)
		}
		s.provider = prv
	case "docker":
		prv, err := docker.NewDockerProvider()
		if err != nil {
			return fmt.Errorf("failed to initialize Docker provider: %w", err)
		}
		s.provider = prv
	default:
		return fmt.Errorf("unsupported provider type: %s", providerType)
	}

	return nil
}

func (s *Server) setupRoutes() {
	// Node management
	s.router.HandleFunc("/api/v1/nodes", s.handleNodesRequest)
	s.router.HandleFunc("/api/v1/nodes/", s.handleNodeRequest)

	// Command dispatch
	s.router.HandleFunc("/api/v1/commands/", s.handleCommandResultRequest)

	// Service discovery
	s.router.HandleFunc("/api/v1/services", s.handleListServices)

	s.router.HandleFunc("/api/v1/users", s.handleUsersRequest)
	s.router.HandleFunc("/api/v1/users/", s.handleUserRequest)
	s.router.HandleFunc("/api/v1/workspaces/", s.handleM4WorkspacesRouter)

	s.router.HandleFunc("/health", s.handleHealth)
	s.router.HandleFunc("/api/health", s.handleHealth)
	s.router.HandleFunc("/metrics", s.handleMetrics)

	s.router.HandleFunc("/ws", s.handleWebSocket)

	s.router.HandleFunc("/api/v1/users/register-github", s.handleM4RegisterGitHub)
	s.router.HandleFunc("/api/v1/workspaces/create-from-repo", s.handleM4CreateWorkspace)
	s.router.HandleFunc("/api/v1/workspaces", s.handleM4ListWorkspacesRouter)

	// GitHub OAuth
	s.router.HandleFunc("/auth/github/callback", s.handleGitHubOAuthCallback)
	s.router.HandleFunc("/api/github/token", s.handleGetGitHubToken)
	s.router.HandleFunc("/api/github/oauth-url", s.handleGetGitHubOAuthURL)
	s.router.HandleFunc("/workspace/auth-success", s.handleAuthSuccess)
	s.router.HandleFunc("/workspace/auth-error", s.handleAuthError)

	// Pulse sync endpoints
	s.router.HandleFunc("/api/pulse/sync", s.PulseSyncHandler)
	s.router.HandleFunc("/api/pulse/operations", s.PulseOperationsHandler)
	s.router.HandleFunc("/api/pulse/sync/ws", s.PulseWebSocketSyncHandler)
	s.router.HandleFunc("/api/pulse/sync/status", s.GetPulseSyncStatus)

	// Feedback endpoints
	s.router.HandleFunc("/api/feedback", s.handleFeedbackRequest)
	s.router.HandleFunc("/api/feedback/", s.handleFeedbackItemRequest)
	s.router.HandleFunc("/api/feedback/stats", s.handleFeedbackStats)
	s.router.HandleFunc("/api/feedback/session-rate", s.handleSessionRate)

	// Analytics endpoints
	s.router.HandleFunc("/api/analytics/dashboard", s.handleAnalyticsDashboard)
	s.router.HandleFunc("/api/analytics/usage", s.handleUsageAnalytics)
	s.router.HandleFunc("/api/analytics/pulse", s.handlePulseAnalytics)
	s.router.HandleFunc("/api/analytics/workflow", s.handleWorkflowAnalytics)
	s.router.HandleFunc("/api/analytics/recommendations", s.handleRecommendations)

	// Workflow tracking endpoints
	s.router.HandleFunc("/api/metrics/workflow/start", s.handleWorkflowStart)
	s.router.HandleFunc("/api/metrics/workflow/event", s.handleWorkflowEvent)
	s.router.HandleFunc("/api/metrics/workflow/skill", s.handleWorkflowSkill)
	s.router.HandleFunc("/api/metrics/workflow/complete", s.handleWorkflowComplete)

	// AI Insights endpoints
	s.router.HandleFunc("/api/insights/predict", s.handlePredictSatisfaction)
	s.router.HandleFunc("/api/insights/recommend", s.handleRecommendSkills)
	s.router.HandleFunc("/api/insights/anomaly", s.handleCheckAnomaly)

	// Slack slash command endpoint
	s.router.HandleFunc("/api/slack/slash", s.handleSlackSlashCommand)
}

// Request routing helpers
func (s *Server) handleNodesRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleRegisterNode(w, r)
	case http.MethodGet:
		s.handleListNodes(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleNodeRequest(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/")
	if path == "" {
		http.Error(w, "Node ID required", http.StatusBadRequest)
		return
	}

	parts := strings.Split(path, "/")
	nodeID := parts[0]

	// Check if this is a command request
	if len(parts) >= 3 && parts[1] == "commands" {
		switch r.Method {
		case http.MethodPost:
			s.handleSendCommand(w, r, nodeID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		if strings.HasSuffix(r.URL.Path, "/status") {
			s.handleGetNodeStatus(w, r, nodeID)
		} else {
			s.handleGetNode(w, r, nodeID)
		}
	case http.MethodPut:
		s.handleUpdateNode(w, r, nodeID)
	case http.MethodDelete:
		s.handleUnregisterNode(w, r, nodeID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleNodeCommandRequest(w http.ResponseWriter, r *http.Request) {
	// This function is handled by handleNodeRequest which processes both /nodes/{id} and /nodes/{id}/commands
	http.Error(w, "Not implemented", http.StatusNotFound)
}

func (s *Server) handleCommandResultRequest(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/commands/")
	if path == "" {
		http.Error(w, "Command ID required", http.StatusBadRequest)
		return
	}

	commandID := strings.Split(path, "/")[0]

	switch r.Method {
	case http.MethodPost:
		if strings.HasSuffix(r.URL.Path, "/result") {
			s.handleCommandResult(w, r, commandID)
		} else {
			http.Error(w, "Invalid endpoint", http.StatusBadRequest)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Start starts the coordination server
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port)

	s.httpSrv = &http.Server{
		Addr:         addr,
		Handler:      s.corsMiddleware(s.authMiddleware(s.loggingMiddleware(s.router))),
		ReadTimeout:  s.parseTimeout(s.config.Server.ReadTimeout),
		WriteTimeout: s.parseTimeout(s.config.Server.WriteTimeout),
		IdleTimeout:  s.parseTimeout(s.config.Server.IdleTimeout),
	}

	// Start event broadcaster
	go s.broadcastResults()

	return s.httpSrv.ListenAndServe()
}

// Stop stops the coordination server
func (s *Server) Stop(ctx context.Context) error {
	if s.httpSrv != nil {
		return s.httpSrv.Shutdown(ctx)
	}
	return nil
}

func (s *Server) parseTimeout(timeout string) time.Duration {
	if duration, err := time.ParseDuration(timeout); err == nil {
		return duration
	}
	return 30 * time.Second // default
}

// GetRegistry returns the node registry
func (s *Server) GetRegistry() Registry {
	return s.registry
}

func (s *Server) broadcastResults() {
	for result := range s.commandCh {
		s.broadcastEvent("command_result", result)
	}
}

func (s *Server) broadcastEvent(eventType string, data interface{}) {
	event := Event{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
	}

	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	for client, _ := range s.clients {
		select {
		case client <- event:
		default:
			close(client)
			delete(s.clients, client)
		}
	}
}

// initAnalytics initializes the analytics service
func (s *Server) initAnalytics() {
	// Initialize workflow tracker
	s.workflowTracker = metrics.NewWorkflowTracker("")

	s.analyticsService = NewCombinedAnalyticsService(
		s.workflowTracker,
		s.feedbackCollector,
		nil, // pulseClient
	)
}

// Workflow tracking handlers

// handleWorkflowStart handles POST /api/metrics/workflow/start
func (s *Server) handleWorkflowStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string   `json:"session_id"`
		TaskID    string   `json:"task_id,omitempty"`
		Skills    []string `json:"skills,omitempty"`
		UserID    string   `json:"user_id,omitempty"`
		Model     string   `json:"model,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	if req.UserID == "" {
		req.UserID = "anonymous"
	}
	if req.Model == "" {
		req.Model = "unknown"
	}

	if err := s.workflowTracker.StartSession(req.SessionID, req.UserID, req.Model); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start session: %v", err), http.StatusInternalServerError)
		return
	}

	// Record skills if provided
	for _, skill := range req.Skills {
		if err := s.workflowTracker.RecordSkillUsage(req.SessionID, skill, 0); err != nil {
			log.Printf("Warning: failed to record skill %s: %v", skill, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "started",
		"session_id": req.SessionID,
	})
}

// handleWorkflowEvent handles POST /api/metrics/workflow/event
func (s *Server) handleWorkflowEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string                 `json:"session_id"`
		EventType string                 `json:"event_type"`
		Metadata  map[string]interface{} `json:"metadata,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" || req.EventType == "" {
		http.Error(w, "session_id and event_type are required", http.StatusBadRequest)
		return
	}

	if err := s.workflowTracker.RecordEvent(req.SessionID, req.EventType, req.Metadata); err != nil {
		http.Error(w, fmt.Sprintf("Failed to record event: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "recorded",
	})
}

// handleWorkflowSkill handles POST /api/metrics/workflow/skill
func (s *Server) handleWorkflowSkill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
		Skill    string `json:"skill"`
		Duration int64  `json:"duration_ms,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" || req.Skill == "" {
		http.Error(w, "session_id and skill are required", http.StatusBadRequest)
		return
	}

	if err := s.workflowTracker.RecordSkillUsage(req.SessionID, req.Skill, req.Duration); err != nil {
		http.Error(w, fmt.Sprintf("Failed to record skill: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "recorded",
	})
}

// handleWorkflowComplete handles POST /api/metrics/workflow/complete
func (s *Server) handleWorkflowComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
		Success  bool   `json:"success"`
		Duration int64  `json:"duration_seconds,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	duration := req.Duration
	if duration == 0 {
		duration = 60 // default
	}

	if err := s.workflowTracker.CompleteSession(req.SessionID, req.Success, duration); err != nil {
		http.Error(w, fmt.Sprintf("Failed to complete session: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "completed",
		"session_id": req.SessionID,
		"success":    req.Success,
	})
}

// InsightsService wraps all insight components
type InsightsService struct {
	predictor    *insights.SatisfactionPredictor
	recommender   *insights.SkillRecommender
	detector     *insights.AnomalyDetector
}

// NewInsightsService creates a new insights service wrapper
func NewInsightsService() *InsightsService {
	return &InsightsService{
		predictor:  insights.NewSatisfactionPredictor(),
		recommender: insights.NewSkillRecommender(),
		detector:   insights.NewAnomalyDetector(),
	}
}

// PredictSatisfaction predicts user satisfaction from feedback
func (s *InsightsService) PredictSatisfaction(category feedback.FeedbackType, message string) float64 {
	return s.predictor.Predict(category, message)
}

// RecommendSkills returns skill recommendations for a task type
func (s *InsightsService) RecommendSkills(taskType string) []string {
	return s.recommender.Recommend(taskType)
}

// CheckAnomaly checks if a value is anomalous based on history
func (s *InsightsService) CheckAnomaly(value float64, history []float64) bool {
	return s.detector.IsAnomalous(value, history)
}

// AI Insights handlers

// handlePredictSatisfaction handles POST /api/insights/predict
func (s *Server) handlePredictSatisfaction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Category string `json:"category"`
		Message  string `json:"message"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Category == "" || req.Message == "" {
		http.Error(w, "category and message are required", http.StatusBadRequest)
		return
	}

	category := feedback.FeedbackType(req.Category)
	prediction := s.insightsService.PredictSatisfaction(category, req.Message)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"prediction": prediction,
		"category":   req.Category,
		"message":    req.Message,
	})
}

// handleRecommendSkills handles POST /api/insights/recommend
func (s *Server) handleRecommendSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TaskType string `json:"task_type"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.TaskType == "" {
		http.Error(w, "task_type is required", http.StatusBadRequest)
		return
	}

	recommendations := s.insightsService.RecommendSkills(req.TaskType)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"task_type":       req.TaskType,
		"recommendations": recommendations,
	})
}

// handleCheckAnomaly handles POST /api/insights/anomaly
func (s *Server) handleCheckAnomaly(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Value   float64   `json:"value"`
		History []float64 `json:"history"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	isAnomaly := s.insightsService.CheckAnomaly(req.Value, req.History)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"value":      req.Value,
		"history":    req.History,
		"is_anomaly": isAnomaly,
	})
}

// initSlackHandler initializes the Slack webhook handler
func (s *Server) initSlackHandler() {
	// Create workspace registry adapter for Slack commands
	workspaceReg := &SlackWorkspaceRegistry{
		server: s,
	}

	// Create slash command handler
	cmdHandler := slack.NewSlashCommandHandler(workspaceReg)
	adapter := slack.NewSlashCommandAdapter(cmdHandler)

	// Create webhook handler with Slack integration
	s.slackWebhookHandler = slack.NewWebhookHandlerWithAdapter(adapter)
}

// handleSlackSlashCommand handles POST /api/slack/slash
func (s *Server) handleSlackSlashCommand(w http.ResponseWriter, r *http.Request) {
	if s.slackWebhookHandler == nil {
		http.Error(w, "Slack not configured", http.StatusServiceUnavailable)
		return
	}
	s.slackWebhookHandler.HandleSlashCommand(w, r)
}

// SlackWorkspaceRegistry implements slack.NexusWorkspaceRegistry for coordination server
type SlackWorkspaceRegistry struct {
	server *Server
}

func (r *SlackWorkspaceRegistry) List() ([]*slack.NexusWorkspaceInfo, error) {
	// This would need to list actual workspaces from the registry
	// For now, return empty list
	return []*slack.NexusWorkspaceInfo{}, nil
}

func (r *SlackWorkspaceRegistry) Get(id string) (*slack.NexusWorkspaceInfo, error) {
	// This would get a specific workspace
	return nil, fmt.Errorf("workspace not found: %s", id)
}
