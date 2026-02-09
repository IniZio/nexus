package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PulseSync manages workspace operations synchronization with vector clocks
type PulseSync struct {
	mu           sync.RWMutex
	workspaceOps map[string][]*Operation       // workspaceID -> operations
	vectorClocks map[string]map[string]int64   // nodeID -> (entityID -> clock)
	wsClients    map[string]*wsClient          // nodeID -> client
	upgrader     websocket.Upgrader
	ctx          context.Context
	cancel       context.CancelFunc
}

// Operation represents a synchronized operation in the Pulse system
type Operation struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`         // "create", "update", "delete", "move", etc.
	Entity     string                 `json:"entity"`       // "issue", "task", "comment", "project", etc.
	EntityID   string                 `json:"entity_id"`
	Data       map[string]interface{} `json:"data"`
	VectorClock map[string]int64      `json:"vector_clock"`
	NodeID     string                 `json:"node_id"`
	Timestamp  time.Time              `json:"timestamp"`
}

// SyncRequest represents a sync request from a Pulse client
type SyncRequest struct {
	NodeID       string                `json:"node_id"`
	WorkspaceID  string                `json:"workspace_id"`
	VectorClock  map[string]int64      `json:"vector_clock"`
	Operations   []*Operation         `json:"operations"`
}

// SyncResponse represents a sync response to a Pulse client
type SyncResponse struct {
	NodeID      string       `json:"node_id"`
	Operations  []*Operation `json:"operations"`
	VectorClock map[string]int64 `json:"vector_clock"`
}

// wsClient represents a WebSocket client connection
type wsClient struct {
	nodeID string
	conn   *websocket.Conn
	send   chan []byte
}

// NewPulseSync creates a new PulseSync instance
func NewPulseSync() *PulseSync {
	ctx, cancel := context.WithCancel(context.Background())
	return &PulseSync{
		workspaceOps: make(map[string][]*Operation),
		vectorClocks: make(map[string]map[string]int64),
		wsClients:    make(map[string]*wsClient),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start starts background cleanup goroutine
func (ps *PulseSync) Start() {
	go ps.cleanupLoop()
}

// Stop stops the PulseSync instance
func (ps *PulseSync) Stop() {
	ps.cancel()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, client := range ps.wsClients {
		client.conn.Close()
	}
}

// RegisterOperation registers an operation and returns operations to broadcast
func (ps *PulseSync) RegisterOperation(workspaceID string, op *Operation) []*Operation {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Initialize workspace operations if needed
	if ps.workspaceOps[workspaceID] == nil {
		ps.workspaceOps[workspaceID] = []*Operation{}
	}

	// Append operation to workspace
	ps.workspaceOps[workspaceID] = append(ps.workspaceOps[workspaceID], op)

	// Update vector clock for node
	if ps.vectorClocks[op.NodeID] == nil {
		ps.vectorClocks[op.NodeID] = make(map[string]int64)
	}
	ps.vectorClocks[op.NodeID][op.EntityID]++

	// Broadcast to all other connected clients
	var toBroadcast []*Operation
	for nodeID, client := range ps.wsClients {
		if nodeID != op.NodeID {
			clientClock := ps.vectorClocks[nodeID]
			if clientClock == nil {
				clientClock = make(map[string]int64)
			}
			// Client needs operation if their clock is behind
			if clientClock[op.EntityID] < ps.vectorClocks[op.NodeID][op.EntityID] {
				toBroadcast = append(toBroadcast, op)
				select {
				case client.send <- mustMarshal(op):
				default:
					// Client buffer full, skip
				}
			}
		}
	}

	return toBroadcast
}

// GetOperationsSince returns operations that the client needs based on their clock
func (ps *PulseSync) GetOperationsSince(workspaceID string, clock map[string]int64) []*Operation {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	ops := ps.workspaceOps[workspaceID]
	if ops == nil {
		return nil
	}

	var needed []*Operation
	for _, op := range ops {
		opSeq := clock[op.EntityID]
		if clock == nil || opSeq < ps.vectorClocks[op.NodeID][op.EntityID] {
			needed = append(needed, op)
		}
	}

	return needed
}

// GetVectorClock returns the vector clock for a node
func (ps *PulseSync) GetVectorClock(nodeID string) map[string]int64 {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	if clock, ok := ps.vectorClocks[nodeID]; ok {
		result := make(map[string]int64)
		for k, v := range clock {
			result[k] = v
		}
		return result
	}
	return make(map[string]int64)
}

// Subscribe creates a WebSocket subscription for a client
func (ps *PulseSync) Subscribe(nodeID string, w http.ResponseWriter, r *http.Request) error {
	conn, err := ps.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("failed to upgrade websocket: %w", err)
	}

	client := &wsClient{
		nodeID: nodeID,
		conn:   conn,
		send:   make(chan []byte, 256),
	}

	ps.mu.Lock()
	ps.wsClients[nodeID] = client
	if ps.vectorClocks[nodeID] == nil {
		ps.vectorClocks[nodeID] = make(map[string]int64)
	}
	ps.mu.Unlock()

	// Start writer goroutine
	go client.writePump(ps.ctx)

	// Start reader goroutine
	client.readPump(ps, nodeID)

	return nil
}

// Unsubscribe removes a client subscription
func (ps *PulseSync) Unsubscribe(nodeID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if client, ok := ps.wsClients[nodeID]; ok {
		client.conn.Close()
		delete(ps.wsClients, nodeID)
	}
}

// cleanupLoop runs periodic cleanup
func (ps *PulseSync) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ps.ctx.Done():
			return
		case <-ticker.C:
			ps.mu.Lock()
			// Cleanup old operations (keep last 10000 per workspace)
			for workspaceID, ops := range ps.workspaceOps {
				if len(ops) > 10000 {
					ps.workspaceOps[workspaceID] = ops[len(ops)-10000:]
				}
			}
			ps.mu.Unlock()
		}
	}
}

// HTTP Handlers

// handlePulseSync handles POST /api/pulse/sync
func (s *Server) handlePulseSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req SyncRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Register any incoming operations
	for _, op := range req.Operations {
		if op.ID != "" {
			s.pulseSync.RegisterOperation(req.WorkspaceID, op)
		}
	}

	// Get operations the client needs based on their clock
	neededOps := s.pulseSync.GetOperationsSince(req.WorkspaceID, req.VectorClock)

	// Build response with current server clock
	response := SyncResponse{
		NodeID:      "server",
		Operations:  neededOps,
		VectorClock: s.pulseSync.GetVectorClock(req.NodeID),
	}

	responseBody, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(responseBody)
}

// handlePulseWebSocket handles WebSocket connections for real-time Pulse sync
func (s *Server) handlePulseWebSocket(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		nodeID = r.Header.Get("X-Node-ID")
	}
	if nodeID == "" {
		nodeID = fmt.Sprintf("pulse-%d", time.Now().UnixNano())
	}

	if err := s.pulseSync.Subscribe(nodeID, w, r); err != nil {
		http.Error(w, "failed to subscribe", http.StatusInternalServerError)
		return
	}
}

// handlePulseStatus handles GET /api/pulse/status
func (s *Server) handlePulseStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.pulseSync.mu.RLock()
	status := map[string]interface{}{
		"ws_clients":  len(s.pulseSync.wsClients),
		"workspaces": len(s.pulseSync.workspaceOps),
	}
	s.pulseSync.mu.RUnlock()

	responseBody, err := json.Marshal(status)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(responseBody)
}

// RegisterPulseRoutes registers the Pulse routes on the server
func (s *Server) RegisterPulseRoutes() {
	s.router.HandleFunc("/api/pulse/sync", s.handlePulseSync)
	s.router.HandleFunc("/api/pulse/ws", s.handlePulseWebSocket)
	s.router.HandleFunc("/api/pulse/status", s.handlePulseStatus)
}


// Helper functions

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// wsClient methods

func (c *wsClient) readPump(ps *PulseSync, nodeID string) {
	defer func() {
		ps.Unsubscribe(nodeID)
		c.conn.Close()
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
				// Log error if needed
			}
			break
		}

		var syncMsg SyncRequest
		if err := json.Unmarshal(message, &syncMsg); err != nil {
			continue
		}

		// Handle sync message
		if len(syncMsg.Operations) > 0 {
			for _, op := range syncMsg.Operations {
				ps.RegisterOperation(syncMsg.WorkspaceID, op)
			}
		}
	}
}

func (c *wsClient) writePump(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
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
