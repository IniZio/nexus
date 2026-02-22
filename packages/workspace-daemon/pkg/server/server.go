package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/idle"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/state"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/handlers"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/lifecycle"
	rpckit "github.com/nexus/nexus/packages/workspace-daemon/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/workspace"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/checkpoint"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/docker"
	wsTypes "github.com/nexus/nexus/packages/workspace-daemon/internal/types"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/ssh"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/sync/mutagen"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/interfaces"
)

type Server struct {
	port            int
	workspaceDir    string
	tokenSecret     string
	upgrader        websocket.Upgrader
	connections     map[string]*Connection
	ws              *workspace.Workspace
	lifecycle       interfaces.LifecycleManager
	mu              sync.RWMutex
	shutdownCh      chan struct{}
	mux             *http.ServeMux
	workspaces      map[string]*WorkspaceState
	dockerBackend   interfaces.Backend
	sshBridges      map[string]*ssh.SSHBridge
	stateStore      interfaces.StateStore
	checkpointStore *checkpoint.FileCheckpointStore
	httpServer      *http.Server
	mutagenDaemon   interfaces.MutagenClient
	sessionManagers map[string]*mutagen.SessionManager
	idleDetectors  map[string]*idle.IdleDetector
	idleConfig      *IdleConfig
}

type WorkspaceState struct {
	ID          string
	Name        string
	Status      string
	Backend     string
	Ports       []PortMapping
	CreatedAt   time.Time
	UpdatedAt   time.Time
	IdleTime    time.Duration `json:"idle_time,omitempty"`
	AutoPause   bool          `json:"auto_pause,omitempty"`
}

type IdleConfig struct {
	DefaultTimeout time.Duration
	AutoPause      bool
	AutoResume     bool
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

func NewServerWithDeps(
	port int,
	workspaceDir string,
	tokenSecret string,
	stateStore interfaces.StateStore,
	backend interfaces.Backend,
	lifecycleMgr interfaces.LifecycleManager,
	mutagenClient interfaces.MutagenClient,
	checkpointStore *checkpoint.FileCheckpointStore,
) (*Server, error) {
	ws, err := workspace.NewWorkspace(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	srv := &Server{
		port:            port,
		workspaceDir:    workspaceDir,
		tokenSecret:     tokenSecret,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		connections:     make(map[string]*Connection),
		ws:              ws,
		lifecycle:       lifecycleMgr,
		shutdownCh:      make(chan struct{}),
		mux:             http.NewServeMux(),
		workspaces:      make(map[string]*WorkspaceState),
		stateStore:      stateStore,
		checkpointStore: checkpointStore,
		mutagenDaemon:   mutagenClient,
		sessionManagers: make(map[string]*mutagen.SessionManager),
		idleDetectors:  make(map[string]*idle.IdleDetector),
		idleConfig: &IdleConfig{
			DefaultTimeout: 30 * time.Minute,
			AutoPause:      true,
			AutoResume:     true,
		},
	}

	var dockerBackend interfaces.Backend
	if backend != nil {
		dockerBackend = backend
		srv.dockerBackend = dockerBackend
	}

	if checkpointStore != nil {
		if err := srv.LoadCheckpoints(); err != nil {
			log.Printf("[checkpoint] Warning: failed to load checkpoints: %v", err)
		}
	}

	if stateStore != nil {
		if err := srv.LoadWorkspaces(); err != nil {
			log.Printf("[state] Warning: failed to load workspaces: %v", err)
		}

		if err := srv.LoadCheckpoints(); err != nil {
			log.Printf("[state] Warning: failed to load checkpoints: %v", err)
		}

		if backend != nil {
			if err := srv.cleanupDanglingWorkspaces(); err != nil {
				log.Printf("[state] Warning: failed to cleanup dangling workspaces: %v", err)
			}
		}
	}

	return srv, nil
}

func NewServer(port int, workspaceDir string, tokenSecret string) (*Server, error) {
	lifecycleMgr, err := lifecycle.NewManager(workspaceDir)
	if err != nil {
		log.Printf("[lifecycle] Warning: failed to initialize lifecycle manager: %v", err)
	}

	if lifecycleMgr != nil {
		if err := lifecycleMgr.RunPreStart(); err != nil {
			return nil, fmt.Errorf("pre-start hook failed: %w", err)
		}
	}

	var stateStore interfaces.StateStore
	stateDir := filepath.Join(workspaceDir, ".nexus", "state")
	store, err := state.NewStateStore(stateDir)
	if err != nil {
		log.Printf("[state] Warning: failed to create state store: %v", err)
	} else {
		stateStore = store
	}

	var checkpointStore *checkpoint.FileCheckpointStore
	checkpointDir := filepath.Join(workspaceDir, ".nexus", "checkpoints")
	cpStore, err := checkpoint.NewFileCheckpointStore(checkpointDir)
	if err != nil {
		log.Printf("[checkpoint] Warning: failed to create checkpoint store: %v", err)
	} else {
		checkpointStore = cpStore
	}

	var backend interfaces.Backend
	var dockerBackend *docker.DockerBackend

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation(), client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		log.Printf("[docker] Warning: failed to create docker client: %v", err)
	} else {
		dockerBackend = docker.NewDockerBackend(dockerClient, workspaceDir)
		backend = dockerBackend
		log.Printf("[docker] Docker backend initialized successfully")
	}

	var mutagenClient interfaces.MutagenClient
	mutagenDataDir := filepath.Join(os.Getenv("HOME"), ".nexus", "mutagen")
	embeddedDaemon := mutagen.NewEmbeddedDaemon(mutagenDataDir)
	if err := embeddedDaemon.Start(context.Background()); err != nil {
		log.Printf("[mutagen] Warning: failed to start embedded daemon: %v", err)
	} else {
		log.Printf("[mutagen] Embedded daemon started at %s", mutagenDataDir)
		mutagenClient = embeddedDaemon
	}

	return NewServerWithDeps(
		port,
		workspaceDir,
		tokenSecret,
		stateStore,
		backend,
		lifecycleMgr,
		mutagenClient,
		checkpointStore,
	)
}

func (s *Server) LoadWorkspaces() error {
	if s.stateStore == nil {
		return nil
	}

	workspaces, err := s.stateStore.ListWorkspaces()
	if err != nil {
		return fmt.Errorf("listing workspaces: %w", err)
	}

	for _, ws := range workspaces {
		s.workspaces[ws.ID] = &WorkspaceState{
			ID:        ws.ID,
			Name:      ws.Name,
			Status:    ws.Status.String(),
			Backend:   ws.Backend.String(),
			CreatedAt: ws.CreatedAt,
			UpdatedAt: ws.UpdatedAt,
		}
	}

	log.Printf("[state] Loaded %d workspaces from state", len(workspaces))
	return nil
}

func (s *Server) LoadCheckpoints() error {
	if s.checkpointStore == nil {
		return nil
	}

	if err := s.checkpointStore.LoadAll(); err != nil {
		return fmt.Errorf("loading checkpoints: %w", err)
	}

	log.Printf("[checkpoint] Loaded checkpoints from disk")
	return nil
}

func (s *Server) cleanupDanglingWorkspaces() error {
	if s.dockerBackend == nil {
		return nil
	}

	knownWorkspaceIDs := make(map[string]bool)
	for id := range s.workspaces {
		knownWorkspaceIDs[id] = true
	}

	type dockerContainerLister interface {
		ListContainersByLabel(ctx context.Context, label string) ([]interface{}, error)
		RemoveContainer(ctx context.Context, containerID string) error
	}

	dockerBackend, ok := s.dockerBackend.(dockerContainerLister)
	if !ok {
		log.Printf("[cleanup] Backend does not support docker-specific operations")
		return nil
	}

	containers, err := dockerBackend.ListContainersByLabel(context.Background(), "nexus.workspace")
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	log.Printf("[cleanup] Found %d containers with nexus label", len(containers))

	type containerInfo struct {
		Labels map[string]string
		ID     string
	}

	for _, c := range containers {
		container, ok := c.(containerInfo)
		if !ok {
			continue
		}
		wsID := container.Labels["nexus.workspace.id"]
		if wsID == "" {
			continue
		}

		if knownWorkspaceIDs[wsID] {
			log.Printf("[cleanup] Container %s matches known workspace %s, resuming...", container.ID[:12], wsID)
			s.mu.Lock()
			if ws, exists := s.workspaces[wsID]; exists {
				ws.Status = "running"
				ws.UpdatedAt = time.Now()
			}
			s.mu.Unlock()
			continue
		}

		log.Printf("[cleanup] Found orphaned container %s for unknown workspace %s, removing...", container.ID[:12], wsID)
		if err := dockerBackend.RemoveContainer(context.Background(), container.ID); err != nil {
			log.Printf("[cleanup] Warning: failed to remove orphaned container %s: %v", container.ID[:12], err)
			continue
		}
		log.Printf("[cleanup] Removed orphaned container %s", container.ID[:12])
	}

	if err := s.saveWorkspaces(); err != nil {
		log.Printf("[cleanup] Warning: failed to save state after cleanup: %v", err)
	}

	return nil
}

func (s *Server) saveWorkspaces() error {
	if s.stateStore == nil {
		return nil
	}

	for _, ws := range s.workspaces {
		wsType := wsTypes.WorkspaceStatusFromString(ws.Status)
		backendType := wsTypes.BackendTypeFromString(ws.Backend)

		ws := &wsTypes.Workspace{
			ID:        ws.ID,
			Name:      ws.Name,
			Status:    wsType,
			Backend:   backendType,
			CreatedAt: ws.CreatedAt,
			UpdatedAt: time.Now(),
		}

		if err := s.stateStore.SaveWorkspace(ws); err != nil {
			log.Printf("[state] Failed to save workspace %s: %v", ws.ID, err)
		}
	}

	return nil
}

func (s *Server) Start() error {
	if s.lifecycle != nil {
		if err := s.lifecycle.RunPostStart(); err != nil {
			log.Printf("[lifecycle] Post-start hook error: %v", err)
		}
	}

	s.registerHTTPRoutes()

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.mux,
	}

	go func() {
		log.Printf("HTTP server listening on port %d", s.port)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return nil
}

func (s *Server) registerHTTPRoutes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/workspaces", s.handleWorkspaces)
	s.mux.HandleFunc("/api/v1/config", s.handleConfig)
	s.mux.HandleFunc("/ws", s.handleWebSocket)
	s.mux.HandleFunc("/ws/ssh-agent", s.handleSSHAgent)
	s.setupCheckpointRoutes()
	s.setupPortRoutes()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getConfig(w, r)
	case http.MethodPost:
		s.setConfig(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.GetIdleConfig()
	WriteSuccess(w, map[string]interface{}{
		"idle_timeout": cfg.DefaultTimeout.String(),
		"auto_pause":   cfg.AutoPause,
		"auto_resume":  cfg.AutoResume,
	})
}

func (s *Server) setConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("reading request body: %w", err))
		return
	}
	defer r.Body.Close()

	var configMap map[string]string
	if err := json.Unmarshal(body, &configMap); err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}

	cfg := s.GetIdleConfig()

	if val, ok := configMap["idle_timeout"]; ok {
		if duration, err := time.ParseDuration(val); err == nil {
			cfg.DefaultTimeout = duration
		}
	}
	if val, ok := configMap["auto_pause"]; ok {
		cfg.AutoPause = val == "true" || val == "1"
	}
	if val, ok := configMap["auto_resume"]; ok {
		cfg.AutoResume = val == "true" || val == "1"
	}

	s.SetIdleConfig(cfg)

	WriteSuccess(w, map[string]string{"status": "updated"})
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
	ForwardSSH    bool              `json:"forward_ssh,omitempty"`
	ID            string            `json:"id,omitempty"`
	WorktreePath  string            `json:"worktree_path,omitempty"`
	DinD          bool              `json:"dind,omitempty"`
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
		s.mu.RLock()
		for _, w := range s.workspaces {
			if w.Name == id {
				ws = w
				id = w.ID
				break
			}
		}
		s.mu.RUnlock()
		if ws == nil {
			http.Error(w, "workspace not found", http.StatusNotFound)
			return
		}
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
		case "/status":
			s.getWorkspaceStatus(w, r, id)
		case "/sync/status":
			s.getSyncStatus(w, r, id)
		default:
			if s.dockerBackend != nil && ws.Backend == "docker" {
				dockerStatus, err := s.dockerBackend.GetWorkspaceStatus(r.Context(), id)
				if err == nil {
					ws.Status = dockerStatus.String()
				}
				sshPort, err := s.dockerBackend.GetSSHPort(r.Context(), id)
				if err == nil {
					found := false
					for i, p := range ws.Ports {
						if p.Name == "ssh" {
							ws.Ports[i].HostPort = int(sshPort)
							found = true
							break
						}
					}
					if !found {
						ws.Ports = append(ws.Ports, PortMapping{
							Name:          "ssh",
							Protocol:      "tcp",
							ContainerPort: 22,
							HostPort:      int(sshPort),
							Visibility:    "public",
						})
					}
				}
			}
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
		case "/sync/pause":
			s.pauseSync(w, r, id)
		case "/sync/resume":
			s.resumeSync(w, r, id)
		case "/sync/flush":
			s.flushSync(w, r, id)
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

func (s *Server) getWorkspaceStatus(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.RLock()
	ws, exists := s.workspaces[id]
	s.mu.RUnlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	if s.dockerBackend != nil && ws.Backend == "docker" {
		dockerStatus, err := s.dockerBackend.GetWorkspaceStatus(r.Context(), id)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("getting docker status: %w", err))
			return
		}
		ws.Status = dockerStatus.String()
	}

	if idleTime, ok := s.GetIdleInfo(id); ok {
		ws.IdleTime = idleTime
	}

	WriteSuccess(w, ws)
}

func (s *Server) getSyncStatus(w http.ResponseWriter, r *http.Request, id string) {
	if s.dockerBackend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("docker backend not available"))
		return
	}

	status, err := s.dockerBackend.GetSyncStatus(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("getting sync status: %w", err))
		return
	}

	WriteSuccess(w, status)
}

func (s *Server) pauseSync(w http.ResponseWriter, r *http.Request, id string) {
	if s.dockerBackend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("docker backend not available"))
		return
	}

	if err := s.dockerBackend.PauseSync(r.Context(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("pausing sync: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{"state": "paused"})
}

func (s *Server) resumeSync(w http.ResponseWriter, r *http.Request, id string) {
	if s.dockerBackend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("docker backend not available"))
		return
	}

	if err := s.dockerBackend.ResumeSync(r.Context(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("resuming sync: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{"state": "resumed"})
}

func (s *Server) flushSync(w http.ResponseWriter, r *http.Request, id string) {
	if s.dockerBackend == nil {
		WriteError(w, http.StatusNotImplemented, fmt.Errorf("docker backend not available"))
		return
	}

	if err := s.dockerBackend.FlushSync(r.Context(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("flushing sync: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{"state": "flushed"})
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

	backend := req.Backend
	if backend == "" {
		backend = "docker"
	}

	wsID := fmt.Sprintf("ws-%d", time.Now().UnixNano())

	var bridgeSocketPath string

	if s.dockerBackend != nil && backend == "docker" && req.ForwardSSH {
		bridge, err := ssh.NewBridge(wsID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating SSH bridge: %w", err))
			return
		}

		bridge.SetActivityCallback(func() {
			s.RecordActivity(wsID, idle.ActivitySSH)
		})

		socketPath, err := bridge.Start()
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("starting SSH bridge: %w", err))
			return
		}

		bridgeSocketPath = socketPath
		s.mu.Lock()
		s.sshBridges[wsID] = bridge
		s.mu.Unlock()

		log.Printf("SSH bridge created for workspace %s at %s", wsID, socketPath)
	}

	var createdDockerWS *wsTypes.Workspace
	if s.dockerBackend != nil && backend == "docker" {
		dockerReq := &wsTypes.CreateWorkspaceRequest{
			Name:          req.Name,
			DisplayName:   req.DisplayName,
			RepositoryURL: req.RepositoryURL,
			Branch:        req.Branch,
			Labels:        req.Labels,
			ID:            wsID,
			WorktreePath:  req.WorktreePath,
			DinD:          req.DinD,
			Config: &wsTypes.WorkspaceConfig{
				Env: map[string]string{},
			},
		}

		if bridgeSocketPath != "" {
			dockerReq.Config.Env["SSH_AUTH_SOCK"] = "/ssh-agent"
		}

		createdWS, err := s.dockerBackend.CreateWorkspaceWithBridge(r.Context(), dockerReq, bridgeSocketPath)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating docker workspace: %w", err))
			return
		}
		wsID = createdWS.ID
		createdDockerWS = createdWS
	}

	ws := &WorkspaceState{
		ID:        wsID,
		Name:      req.Name,
		Status:    "running",
		Backend:   backend,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if createdDockerWS != nil && len(createdDockerWS.Ports) > 0 {
		ws.Ports = make([]PortMapping, len(createdDockerWS.Ports))
		for i, p := range createdDockerWS.Ports {
			ws.Ports[i] = PortMapping{
				Name:          p.Name,
				Protocol:      p.Protocol,
				ContainerPort: int(p.ContainerPort),
				HostPort:      int(p.HostPort),
				Visibility:    p.Visibility,
				URL:           p.URL,
			}
		}
	}

	s.mu.Lock()
	s.workspaces[ws.ID] = ws
	s.mu.Unlock()

	if err := s.saveWorkspaces(); err != nil {
		log.Printf("[state] Failed to save workspace: %v", err)
	}

	s.startIdleDetection(ws.ID)

	WriteSuccess(w, ws)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	ws, exists := s.workspaces[id]
	s.mu.Unlock()

	if !exists {
		WriteSuccess(w, map[string]string{"status": "deleted"})
		return
	}

	if s.dockerBackend != nil && ws.Backend == "docker" {
		if err := s.dockerBackend.DeleteWorkspace(r.Context(), id); err != nil {
			log.Printf("[workspace] Warning: failed to delete docker workspace %s: %v", id, err)
		}
	}

	s.cleanupSSHBridge(id)

	s.stopIdleDetection(id)

	s.mu.Lock()
	if mgr, ok := s.sessionManagers[id]; ok {
		sessions, err := mgr.ListSessions()
		if err == nil {
			for _, sess := range sessions {
				if err := mgr.TerminateSession(r.Context(), sess.ID); err != nil {
					log.Printf("[mutagen] Warning: failed to terminate session %s: %v", sess.ID, err)
				}
			}
		}
		delete(s.sessionManagers, id)
	}
	delete(s.workspaces, id)
	s.mu.Unlock()

	if err := s.saveWorkspaces(); err != nil {
		log.Printf("[state] Failed to save after delete: %v", err)
	}

	WriteSuccess(w, map[string]string{"status": "deleted"})
}

func (s *Server) startWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	ws, exists := s.workspaces[id]
	s.mu.Unlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	if s.dockerBackend != nil && ws.Backend == "docker" {
		_, err := s.dockerBackend.StartWorkspace(r.Context(), id)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("starting docker workspace: %w", err))
			return
		}
	}

	s.mu.Lock()
	if mgr, ok := s.sessionManagers[id]; ok {
		sessions, err := mgr.ListSessions()
		if err == nil {
			for _, sess := range sessions {
				if err := mgr.ResumeSession(r.Context(), sess.ID); err != nil {
					log.Printf("[mutagen] Warning: failed to resume session %s: %v", sess.ID, err)
				}
			}
		}
	}
	if ws, exists := s.workspaces[id]; exists {
		ws.Status = "running"
		ws.UpdatedAt = time.Now()
	}
	s.mu.Unlock()

	if err := s.saveWorkspaces(); err != nil {
		log.Printf("[state] Failed to save after start: %v", err)
	}

	WriteSuccess(w, map[string]string{"status": "running"})
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
	ws, exists := s.workspaces[id]
	s.mu.Unlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	if s.dockerBackend != nil && ws.Backend == "docker" {
		timeout := int32(req.TimeoutSeconds)
		_, err := s.dockerBackend.StopWorkspace(r.Context(), id, timeout)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("stopping docker workspace: %w", err))
			return
		}
	}

	s.mu.Lock()
	if mgr, ok := s.sessionManagers[id]; ok {
		sessions, err := mgr.ListSessions()
		if err == nil {
			for _, sess := range sessions {
				if err := mgr.PauseSession(r.Context(), sess.ID); err != nil {
					log.Printf("[mutagen] Warning: failed to pause session %s: %v", sess.ID, err)
				}
			}
		}
	}
	if ws, exists := s.workspaces[id]; exists {
		ws.Status = "stopped"
		ws.UpdatedAt = time.Now()
	}
	s.mu.Unlock()

	if err := s.saveWorkspaces(); err != nil {
		log.Printf("[state] Failed to save after stop: %v", err)
	}

	WriteSuccess(w, map[string]string{"status": "stopped"})
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

	s.mu.RLock()
	ws, exists := s.workspaces[id]
	s.mu.RUnlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	if s.idleConfig != nil && s.idleConfig.AutoResume && ws.Status == "sleeping" {
		log.Printf("[auto-resume] Resuming workspace %s on exec access", id)
		s.resumeWorkspace(id)
	}

	var output string
	if s.dockerBackend != nil && ws.Backend == "docker" {
		cmd := []string{req.Command[0]}
		cmd = append(cmd, req.Command[1:]...)
		output, err = s.dockerBackend.Exec(ctx, id, cmd)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("executing command: %w", err))
			return
		}
	} else {
		result, rpcErr := handlers.HandleExec(ctx, jsonParams, s.ws, s.dockerBackend)
		if rpcErr != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("executing command: %v", rpcErr))
			return
		}
		output = result.Stdout
	}

	s.RecordActivity(id, idle.ActivitySSH)

	WriteSuccess(w, map[string]string{"output": output})
}

func (s *Server) Shutdown() {
	log.Printf("[shutdown] Starting graceful shutdown...")

	if s.lifecycle != nil {
		if err := s.lifecycle.RunPreStop(); err != nil {
			log.Printf("[lifecycle] Pre-stop hook error: %v", err)
		}
	}

	log.Printf("[state] Saving workspace state before shutdown...")
	if err := s.saveWorkspaces(); err != nil {
		log.Printf("[state] Error saving workspaces on shutdown: %v", err)
	}

	log.Printf("[shutdown] Stopping running workspaces...")
	s.stopAllWorkspaces()

	log.Printf("[shutdown] Stopping Mutagen sessions...")
	s.stopAllMutagenSessions()

	log.Printf("[shutdown] Closing SSH bridges...")
	s.closeAllSSHBridges()

	close(s.shutdownCh)
	s.mu.Lock()
	for _, conn := range s.connections {
		close(conn.send)
		conn.conn.Close()
	}
	s.mu.Unlock()

	if s.httpServer != nil {
		log.Printf("[http] Starting HTTP server shutdown...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(ctx); err != nil {
			log.Printf("[http] Error during graceful shutdown: %v", err)
		}
		log.Printf("[http] HTTP server shutdown complete")
	}

	if s.mutagenDaemon != nil {
		log.Printf("[mutagen] Stopping embedded daemon...")
		if err := s.mutagenDaemon.Stop(context.Background()); err != nil {
			log.Printf("[mutagen] Error stopping embedded daemon: %v", err)
		}
		log.Printf("[mutagen] Embedded daemon stopped")
	}

	if s.lifecycle != nil {
		if err := s.lifecycle.RunPostStop(); err != nil {
			log.Printf("[lifecycle] Post-stop hook error: %v", err)
		}
	}

	log.Printf("[shutdown] Graceful shutdown complete")
}

func (s *Server) stopAllWorkspaces() {
	s.mu.RLock()
	workspaces := make([]*WorkspaceState, 0, len(s.workspaces))
	for _, ws := range s.workspaces {
		workspaces = append(workspaces, ws)
	}
	s.mu.RUnlock()

	for _, ws := range workspaces {
		if ws.Status != "running" {
			continue
		}

		if s.dockerBackend != nil && ws.Backend == "docker" {
			log.Printf("[shutdown] Stopping workspace %s...", ws.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := s.dockerBackend.StopWorkspace(ctx, ws.ID, 30); err != nil {
				log.Printf("[shutdown] Error stopping workspace %s: %v", ws.ID, err)
				cancel()
				continue
			}
			cancel()
			log.Printf("[shutdown] Workspace %s stopped", ws.ID)
		}

		s.mu.Lock()
		if w, exists := s.workspaces[ws.ID]; exists {
			w.Status = "stopped"
			w.UpdatedAt = time.Now()
		}
		s.mu.Unlock()
	}
}

func (s *Server) stopAllMutagenSessions() {
	s.mu.RLock()
	workspaceIDs := make([]string, 0, len(s.sessionManagers))
	for id := range s.sessionManagers {
		workspaceIDs = append(workspaceIDs, id)
	}
	s.mu.RUnlock()

	for _, id := range workspaceIDs {
		s.mu.RLock()
		mgr, exists := s.sessionManagers[id]
		s.mu.RUnlock()

		if !exists || mgr == nil {
			continue
		}

		sessions, err := mgr.ListSessions()
		if err != nil {
			log.Printf("[mutagen] Warning: failed to list sessions for workspace %s: %v", id, err)
			continue
		}

		for _, sess := range sessions {
			log.Printf("[mutagen] Pausing session %s...", sess.ID)
			if err := mgr.PauseSession(context.Background(), sess.ID); err != nil {
				log.Printf("[mutagen] Warning: failed to pause session %s: %v", sess.ID, err)
			}

			log.Printf("[mutagen] Terminating session %s...", sess.ID)
			if err := mgr.TerminateSession(context.Background(), sess.ID); err != nil {
				log.Printf("[mutagen] Warning: failed to terminate session %s: %v", sess.ID, err)
			}
		}
		log.Printf("[mutagen] Sessions for workspace %s terminated", id)
	}
}

func (s *Server) closeAllSSHBridges() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, bridge := range s.sshBridges {
		log.Printf("[ssh] Closing bridge for workspace %s...", id)
		bridge.Close()
	}
	s.sshBridges = make(map[string]*ssh.SSHBridge)
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

func (s *Server) handleSSHAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace")
	if workspaceID == "" {
		http.Error(w, "workspace ID required", http.StatusBadRequest)
		return
	}

	token := r.URL.Query().Get("token")
	if !s.validateToken(token) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade SSH agent connection: %v", err)
		return
	}

	s.mu.Lock()
	bridge, exists := s.sshBridges[workspaceID]
	s.mu.Unlock()

	if !exists {
		bridge, err = ssh.NewBridge(workspaceID)
		if err != nil {
			log.Printf("Failed to create SSH bridge: %v", err)
			conn.Close()
			return
		}

		bridge.SetActivityCallback(func() {
			s.RecordActivity(workspaceID, idle.ActivitySSH)
		})

		socketPath, err := bridge.Start()
		if err != nil {
			log.Printf("Failed to start SSH bridge: %v", err)
			conn.Close()
			return
		}

		log.Printf("SSH bridge started for workspace %s at %s", workspaceID, socketPath)

		s.mu.Lock()
		s.sshBridges[workspaceID] = bridge
		s.mu.Unlock()
	}

	bridge.SetWebSocket(conn)

	go func() {
		bridge.HandleConnections()

		s.mu.Lock()
		delete(s.sshBridges, workspaceID)
		s.mu.Unlock()

		bridge.Close()
		log.Printf("SSH bridge closed for workspace %s", workspaceID)
	}()
}

func (s *Server) GetBridgeSocket(workspaceID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if bridge, ok := s.sshBridges[workspaceID]; ok {
		return bridge.GetSocketPath()
	}
	return ""
}

func (s *Server) cleanupSSHBridge(workspaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if bridge, ok := s.sshBridges[workspaceID]; ok {
		bridge.Close()
		delete(s.sshBridges, workspaceID)
	}
}

func (s *Server) startIdleDetection(workspaceID string) {
	if s.idleConfig == nil || !s.idleConfig.AutoPause {
		return
	}

	timeout := s.idleConfig.DefaultTimeout
	if ws, ok := s.workspaces[workspaceID]; ok {
		if ws.AutoPause {
			timeout = s.idleConfig.DefaultTimeout
		}
	}

	detector := idle.NewIdleDetector(workspaceID, timeout)
	detector.SetOnIdle(func() {
		log.Printf("[idle] Workspace %s is idle, pausing...", workspaceID)
		s.pauseWorkspace(workspaceID)
	})
	detector.SetOnActive(func() {
		log.Printf("[idle] Workspace %s is active, resuming...", workspaceID)
		s.resumeWorkspace(workspaceID)
	})

	detector.Start()

	s.mu.Lock()
	s.idleDetectors[workspaceID] = detector
	s.mu.Unlock()

	log.Printf("[idle] Started idle detection for workspace %s with timeout %v", workspaceID, timeout)
}

func (s *Server) stopIdleDetection(workspaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if detector, ok := s.idleDetectors[workspaceID]; ok {
		detector.Stop()
		delete(s.idleDetectors, workspaceID)
		log.Printf("[idle] Stopped idle detection for workspace %s", workspaceID)
	}
}

func (s *Server) RecordActivity(workspaceID string, activity idle.ActivityType) {
	s.mu.RLock()
	detector, ok := s.idleDetectors[workspaceID]
	s.mu.RUnlock()

	if ok {
		detector.RecordActivity(activity)
	}
}

func (s *Server) GetIdleInfo(workspaceID string) (time.Duration, bool) {
	s.mu.RLock()
	detector, ok := s.idleDetectors[workspaceID]
	s.mu.RUnlock()

	if !ok {
		return 0, false
	}

	return detector.GetIdleDuration(), detector.IsIdle()
}

func (s *Server) SetIdleConfig(config *IdleConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.idleConfig = config
}

func (s *Server) GetIdleConfig() *IdleConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.idleConfig
}

func (s *Server) pauseWorkspace(workspaceID string) {
	s.mu.RLock()
	ws, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()

	if !exists || ws.Status == "stopped" || ws.Status == "sleeping" {
		return
	}

	if s.dockerBackend != nil && ws.Backend == "docker" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.dockerBackend.StopWorkspace(ctx, workspaceID, 30); err != nil {
			log.Printf("[idle] Failed to pause workspace %s: %v", workspaceID, err)
			return
		}
	}

	s.mu.Lock()
	if ws, exists := s.workspaces[workspaceID]; exists {
		ws.Status = "sleeping"
		ws.UpdatedAt = time.Now()
	}
	s.mu.Unlock()

	log.Printf("[idle] Workspace %s paused (sleeping)", workspaceID)
}

func (s *Server) resumeWorkspace(workspaceID string) {
	s.mu.RLock()
	ws, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()

	if !exists || ws.Status == "running" {
		return
	}

	if s.dockerBackend != nil && ws.Backend == "docker" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := s.dockerBackend.StartWorkspace(ctx, workspaceID); err != nil {
			log.Printf("[idle] Failed to resume workspace %s: %v", workspaceID, err)
			return
		}
	}

	s.mu.Lock()
	if ws, exists := s.workspaces[workspaceID]; exists {
		ws.Status = "running"
		ws.UpdatedAt = time.Now()
	}
	s.mu.Unlock()

	log.Printf("[idle] Workspace %s resumed", workspaceID)
}

func (s *Server) setupPortRoutes() {
	s.mux.HandleFunc("/api/v1/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[len("/api/v1/workspaces/"):]
		parts := strings.SplitN(path, "/", 2)
		if len(parts) < 2 {
			http.Error(w, "workspace ID and path required", http.StatusBadRequest)
			return
		}
		workspaceID := parts[0]
		subPath := parts[1]

		switch subPath {
		case "ports":
			s.handleWorkspacePorts(w, r, workspaceID)
		case "":
			s.handleWorkspaceByID(w, r)
		default:
			if strings.HasPrefix(subPath, "ports/") {
				s.handleWorkspacePortByID(w, r, workspaceID)
			} else {
				s.handleWorkspaceByID(w, r)
			}
		}
	})
}

func (s *Server) handleWorkspacePorts(w http.ResponseWriter, r *http.Request, workspaceID string) {
	switch r.Method {
	case http.MethodGet:
		s.listPorts(w, r, workspaceID)
	case http.MethodPost:
		s.addPort(w, r, workspaceID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleWorkspacePortByID(w http.ResponseWriter, r *http.Request, workspaceID string) {
	path := r.URL.Path
	parts := strings.Split(path, "/")
	if len(parts) < 1 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	hostPortStr := parts[len(parts)-1]
	hostPort, err := strconv.Atoi(hostPortStr)
	if err != nil {
		http.Error(w, "invalid host port", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.removePort(w, r, workspaceID, hostPort)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) addPort(w http.ResponseWriter, r *http.Request, workspaceID string) {
	s.mu.RLock()
	ws, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("reading request body: %w", err))
		return
	}
	defer r.Body.Close()

	var req struct {
		ContainerPort int `json:"container_port"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}

	if req.ContainerPort <= 0 || req.ContainerPort > 65535 {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("invalid container port"))
		return
	}

	var hostPort int
	if s.dockerBackend != nil && ws.Backend == "docker" {
		allocatedPort, err := s.dockerBackend.AllocatePort()
		if err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("allocating port: %w", err))
			return
		}
		hostPort = int(allocatedPort)

		ctx := context.Background()
		err = s.dockerBackend.AddPortBinding(ctx, workspaceID, int32(req.ContainerPort), int32(hostPort))
		if err != nil {
			log.Printf("[port] Failed to add port binding: %v", err)
		}
	} else {
		hostPort = 32800 + len(ws.Ports)
	}

	s.mu.Lock()
	ws.Ports = append(ws.Ports, PortMapping{
		Name:          fmt.Sprintf("port-%d", req.ContainerPort),
		Protocol:      "tcp",
		ContainerPort: req.ContainerPort,
		HostPort:      hostPort,
		Visibility:    "public",
	})
	s.mu.Unlock()

	if err := s.saveWorkspaces(); err != nil {
		log.Printf("[state] Failed to save workspace: %v", err)
	}

	WriteSuccess(w, map[string]int{"host_port": hostPort})
}

func (s *Server) listPorts(w http.ResponseWriter, r *http.Request, workspaceID string) {
	s.mu.RLock()
	ws, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	WriteSuccess(w, ws.Ports)
}

func (s *Server) removePort(w http.ResponseWriter, r *http.Request, workspaceID string, hostPort int) {
	s.mu.RLock()
	ws, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	newPorts := make([]PortMapping, 0)
	for _, p := range ws.Ports {
		if p.HostPort == hostPort {
			found = true
			continue
		}
		newPorts = append(newPorts, p)
	}

	if !found {
		WriteError(w, http.StatusNotFound, fmt.Errorf("port not found"))
		return
	}

	ws.Ports = newPorts

	if s.dockerBackend != nil && ws.Backend == "docker" {
		s.dockerBackend.ReleasePort(int32(hostPort))
	}

	if err := s.saveWorkspaces(); err != nil {
		log.Printf("[state] Failed to save workspace: %v", err)
	}

	WriteSuccess(w, map[string]string{"status": "removed"})
}
