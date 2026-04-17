package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/auth"
	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	"github.com/inizio/nexus/packages/nexus/pkg/compose"
	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/lifecycle"
	"github.com/inizio/nexus/packages/nexus/pkg/project"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/server/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
	"github.com/inizio/nexus/packages/nexus/pkg/services"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type Server struct {
	port                  int
	workspaceDir          string
	authProvider          auth.Provider
	upgrader              websocket.Upgrader
	connections           map[string]*Connection
	ws                    *workspace.Workspace
	workspaceMgr          *workspacemgr.Manager
	projectMgr            *project.Manager
	serviceMgr            *services.Manager
	spotlightMgr          *spotlight.Manager
	portMonitor           *spotlight.PortMonitor
	lifecycle             *lifecycle.Manager
	runtimeFactory        *runtime.Factory
	nodeCfg               *config.NodeConfig
	authRelayBroker       *authrelay.Broker
	autoComposeForwards   map[string]bool
	composePortHints      map[string]map[int]int
	activeTunnelWorkspace string
	rpcReg                *rpc.Registry
	ptyRegistry           *pty.Registry // Global PTY session registry for multi-tab support
	ptyStore              *pty.Store
	mu                    sync.RWMutex
	shutdownCh            chan struct{}
}

type Connection struct {
	conn     *websocket.Conn
	send     chan []byte
	closed   bool
	clientID string
	identity *auth.Identity
	ptyMu    sync.Mutex
	pty      map[string]*pty.Session
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

var newSpotlightManagerForServer = func(workspaceMgr *workspacemgr.Manager) (*spotlight.Manager, error) {
	return spotlight.NewManagerWithRepository(workspaceMgr.SpotlightRepository())
}

func NewServer(port int, workspaceDir string, tokenSecret string) (*Server, error) {
	ws, err := workspace.NewWorkspace(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	workspaceMgr := workspacemgr.NewManager(workspaceDir)
	projectMgr := project.NewManager(workspaceDir, workspaceMgr.ProjectRepository())
	workspaceMgr.SetProjectManager(projectMgr)

	spotlightMgr, err := newSpotlightManagerForServer(workspaceMgr)
	if err != nil {
		log.Printf("[spotlight] Warning: failed to initialize sqlite-backed spotlight manager, falling back to in-memory manager: %v", err)
		spotlightMgr = spotlight.NewManager()
	}
	// Tunnels are now runtime-only and require explicit activation.
	// Clear any legacy persisted forwards so defaults are detect-only.
	for _, fwd := range spotlightMgr.List("") {
		_ = spotlightMgr.Close(fwd.ID)
	}

	lifecycleMgr, err := lifecycle.NewManager(workspaceDir)
	if err != nil {
		log.Printf("[lifecycle] Warning: failed to initialize lifecycle manager: %v", err)
	} else {
		if err := lifecycleMgr.RunPreStart(); err != nil {
			return nil, fmt.Errorf("pre-start hook failed: %w", err)
		}
	}

	srv := &Server{
		port:         port,
		workspaceDir: workspaceDir,
		authProvider: auth.NewLocalTokenProvider(tokenSecret),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		connections:         make(map[string]*Connection),
		ws:                  ws,
		workspaceMgr:        workspaceMgr,
		projectMgr:          projectMgr,
		serviceMgr:          services.NewManager(),
		spotlightMgr:        spotlightMgr,
		lifecycle:           lifecycleMgr,
		authRelayBroker:     authrelay.NewBroker(),
		autoComposeForwards: make(map[string]bool),
		composePortHints:    make(map[string]map[int]int),
		ptyRegistry:         pty.NewRegistry(), // Initialize global PTY session registry
		ptyStore:            pty.NewStore(workspaceDir),
		shutdownCh:          make(chan struct{}),
	}
	srv.rpcReg = srv.newRPCRegistry()
	return srv, nil
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

func (s *Server) resolveWorkspace(params json.RawMessage) *workspace.Workspace {
	workspaceID := extractWorkspaceID(params)
	if workspaceID == "" {
		return s.ws
	}

	wsRecord, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return s.ws
	}

	resolvedPath := preferredWorkspaceRoot(wsRecord)
	if resolvedPath == "" {
		return s.ws
	}

	resolved, err := workspace.NewWorkspace(resolvedPath)
	if err != nil {
		return s.ws
	}

	return resolved
}

func (s *Server) resolveWorkspaceTyped(v any) *workspace.Workspace {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return s.ws
	}
	return s.resolveWorkspace(raw)
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

func (s *Server) ensureComposeHints(ctx context.Context, workspaceID, rootPath string) {
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

	published, err := compose.DiscoverPublishedPorts(ctx, rootPath)
	if err != nil {
		if err == compose.ErrComposeFileNotFound {
			return
		}
		log.Printf("[ports] failed to inspect compose ports for %s: %v", workspaceID, err)
		s.mu.Lock()
		s.autoComposeForwards[workspaceID] = false
		s.mu.Unlock()
		return
	}
	hints := make(map[int]int, len(published))
	for _, p := range published {
		if p.HostPort <= 0 || p.HostPort > 65535 || p.TargetPort <= 0 || p.TargetPort > 65535 {
			continue
		}
		hints[p.HostPort] = p.TargetPort
	}
	s.mu.Lock()
	if len(hints) > 0 {
		s.composePortHints[workspaceID] = hints
	}
	s.mu.Unlock()
}

func (s *Server) composeTargetPort(workspaceID string, port int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if byWorkspace := s.composePortHints[workspaceID]; byWorkspace != nil {
		if target, ok := byWorkspace[port]; ok && target > 0 {
			return target
		}
	}
	return port
}

func (s *Server) SetRuntimeFactory(factory *runtime.Factory) {
	s.runtimeFactory = factory
}

func (s *Server) SetAuthProvider(provider auth.Provider) {
	s.authProvider = provider
}

func (s *Server) WorkspaceIDs() []string {
	all := s.workspaceMgr.List()
	ids := make([]string, len(all))
	for i, ws := range all {
		ids[i] = ws.ID
	}
	return ids
}

// RunningWorkspaceIDs returns IDs of workspaces currently in the running state.
func (s *Server) RunningWorkspaceIDs() []string {
	all := s.workspaceMgr.List()
	ids := make([]string, 0, len(all))
	for _, ws := range all {
		if ws.State == workspacemgr.StateRunning {
			ids = append(ids, ws.ID)
		}
	}
	return ids
}

// ResumeRunningWorkspaces restarts port monitoring and re-applies compose port
// declarations for every workspace that is already in the running state.  Call
// this once during daemon startup so clients reconnecting after a daemon
// restart see ports without needing to trigger a workspace event first.
func (s *Server) ResumeRunningWorkspaces(ctx context.Context) {
	for _, ws := range s.workspaceMgr.List() {
		if ws.State != workspacemgr.StateRunning {
			continue
		}
		_ = s.StartPortMonitoring(ws.ID)
		rootPath := preferredWorkspaceRoot(ws)
		if rootPath != "" {
			go s.ensureComposeHints(ctx, ws.ID, rootPath)
		}
	}
}

// PrunePersistedPTYSessions removes stale persisted tmux PTY records.
func (s *Server) PrunePersistedPTYSessions(_ context.Context) int {
	return pty.PruneStalePersistedSessions(s.ptyDeps())
}

// StartPTYMaintenance starts periodic cleanup for persisted PTY session metadata.
func (s *Server) StartPTYMaintenance(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.shutdownCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				removed := s.PrunePersistedPTYSessions(context.Background())
				if removed > 0 {
					log.Printf("[pty] pruned %d stale persisted tmux session(s)", removed)
				}
			}
		}
	}()
}

func (s *Server) SetNodeConfig(cfg *config.NodeConfig) {
	s.nodeCfg = cfg
}

// SetPortMonitor sets the port monitor for live port detection.
func (s *Server) SetPortMonitor(pm *spotlight.PortMonitor) {
	s.portMonitor = pm
}

// SpotlightManager returns the spotlight manager.
func (s *Server) SpotlightManager() *spotlight.Manager {
	return s.spotlightMgr
}

// StartPortMonitoring begins live port monitoring for a workspace.
func (s *Server) StartPortMonitoring(workspaceID string) error {
	if s.portMonitor == nil {
		return nil
	}
	return s.portMonitor.StartWorkspace(workspaceID)
}

// StopPortMonitoring stops live port monitoring for a workspace.
func (s *Server) StopPortMonitoring(workspaceID string) {
	if s.portMonitor == nil {
		return
	}
	s.portMonitor.StopWorkspace(workspaceID)
}

type WorkspacePortState struct {
	Port       int    `json:"port"`
	RemotePort int    `json:"remotePort"`
	Process    string `json:"process,omitempty"`
	Preferred  bool   `json:"preferred"`
	Tunneled   bool   `json:"tunneled"`
}

func (s *Server) WorkspacePortStates(workspaceID string) ([]WorkspacePortState, string) {
	stateByPort := map[int]*WorkspacePortState{}

	if s.portMonitor != nil {
		for _, p := range s.portMonitor.ListDiscovered(workspaceID) {
			if p.Port <= 0 || p.Port > 65535 {
				continue
			}
			stateByPort[p.Port] = &WorkspacePortState{
				Port:       p.Port,
				RemotePort: p.Port,
				Process:    strings.TrimSpace(p.Process),
			}
		}
	}

	s.mu.RLock()
	for hostPort, targetPort := range s.composePortHints[workspaceID] {
		if hostPort <= 0 || hostPort > 65535 || targetPort <= 0 || targetPort > 65535 {
			continue
		}
		existing, ok := stateByPort[hostPort]
		if !ok {
			stateByPort[hostPort] = &WorkspacePortState{
				Port:       hostPort,
				RemotePort: targetPort,
			}
			continue
		}
		existing.RemotePort = targetPort
	}
	activeWorkspaceID := s.activeTunnelWorkspace
	s.mu.RUnlock()

	if ws, ok := s.workspaceMgr.Get(workspaceID); ok {
		// First-time default: auto-add all detected ports as preferred so
		// activation is ready without requiring manual Add clicks.
		if len(ws.TunnelPorts) == 0 && len(stateByPort) > 0 {
			defaultPorts := make([]int, 0, len(stateByPort))
			for p := range stateByPort {
				if p > 0 && p <= 65535 {
					defaultPorts = append(defaultPorts, p)
				}
			}
			sort.Slice(defaultPorts, func(i, j int) bool { return defaultPorts[i] < defaultPorts[j] })
			if len(defaultPorts) > 0 {
				if err := s.workspaceMgr.SetTunnelPorts(workspaceID, defaultPorts); err == nil {
					ws.TunnelPorts = defaultPorts
				}
			}
		}
		for _, p := range ws.TunnelPorts {
			if p <= 0 || p > 65535 {
				continue
			}
			entry, ok := stateByPort[p]
			if !ok {
				entry = &WorkspacePortState{Port: p, RemotePort: s.composeTargetPort(workspaceID, p)}
				stateByPort[p] = entry
			}
			entry.Preferred = true
		}
	}

	for _, fwd := range s.spotlightMgr.List(workspaceID) {
		entry, ok := stateByPort[fwd.LocalPort]
		if !ok {
			entry = &WorkspacePortState{Port: fwd.LocalPort, RemotePort: fwd.RemotePort}
			stateByPort[fwd.LocalPort] = entry
		}
		entry.Tunneled = true
		if entry.RemotePort == 0 {
			entry.RemotePort = fwd.RemotePort
		}
	}

	items := make([]WorkspacePortState, 0, len(stateByPort))
	for _, st := range stateByPort {
		items = append(items, *st)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Port < items[j].Port })
	return items, activeWorkspaceID
}

func (s *Server) SetWorkspaceTunnelPreference(workspaceID string, port int, enabled bool) error {
	ws, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return fmt.Errorf("workspace not found")
	}
	ports := append([]int(nil), ws.TunnelPorts...)
	if enabled {
		ports = append(ports, port)
	} else {
		next := make([]int, 0, len(ports))
		for _, p := range ports {
			if p != port {
				next = append(next, p)
			}
		}
		ports = next
	}
	if err := s.workspaceMgr.SetTunnelPorts(workspaceID, ports); err != nil {
		return err
	}

	s.mu.RLock()
	activeWorkspace := s.activeTunnelWorkspace
	s.mu.RUnlock()
	if activeWorkspace == workspaceID {
		if enabled {
			return s.ensureTunnelPort(workspaceID, port)
		}
		s.closeTunnelPort(workspaceID, port)
	}
	return nil
}

func (s *Server) ensureTunnelPort(workspaceID string, port int) error {
	remotePort := s.composeTargetPort(workspaceID, port)
	_, err := s.spotlightMgr.Expose(context.Background(), spotlight.ExposeSpec{
		WorkspaceID: workspaceID,
		Service:     "",
		RemotePort:  remotePort,
		LocalPort:   port,
		Host:        "127.0.0.1",
	})
	if err != nil && !strings.Contains(err.Error(), "already in use") {
		return err
	}
	return nil
}

func (s *Server) closeTunnelPort(workspaceID string, port int) {
	for _, fwd := range s.spotlightMgr.List(workspaceID) {
		if fwd.LocalPort == port {
			_ = s.spotlightMgr.Close(fwd.ID)
		}
	}
}

func (s *Server) StartWorkspaceTunnels(workspaceID string) error {
	s.mu.Lock()
	existing := s.activeTunnelWorkspace
	s.mu.Unlock()

	if existing != "" && existing != workspaceID {
		s.StopWorkspaceTunnels(existing)
	}

	s.mu.Lock()
	s.activeTunnelWorkspace = workspaceID
	s.mu.Unlock()

	ws, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return fmt.Errorf("workspace not found")
	}
	for _, p := range ws.TunnelPorts {
		if p <= 0 || p > 65535 {
			continue
		}
		if err := s.ensureTunnelPort(workspaceID, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) StopWorkspaceTunnels(workspaceID string) {
	for _, fwd := range s.spotlightMgr.List(workspaceID) {
		_ = s.spotlightMgr.Close(fwd.ID)
	}
	s.mu.Lock()
	if s.activeTunnelWorkspace == workspaceID {
		s.activeTunnelWorkspace = ""
	}
	s.mu.Unlock()
}

func (s *Server) requireWorkspaceStarted(workspaceID string) *rpckit.RPCError {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return rpckit.ErrInvalidParams
	}

	wsRecord, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return rpckit.ErrWorkspaceNotFound
	}
	if wsRecord.State != workspacemgr.StateRunning {
		return rpckit.ErrWorkspaceNotStarted
	}

	return nil
}
