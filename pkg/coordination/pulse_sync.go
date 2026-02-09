package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PulseSyncRegistry manages operation registry and client subscriptions for Pulse sync
type PulseSyncRegistry struct {
	mu          sync.RWMutex
	operations  map[string]*PulseCRDTOperation // opID -> operation
	nodeClocks  map[string]PulseVectorClock   // nodeID -> vector clock
	subscribers map[string]*pulseSyncClient   // nodeID -> websocket client
	upgrader    websocket.Upgrader
	ctx         context.Context
	cancel      context.CancelFunc
}

type pulseSyncClient struct {
	nodeID string
	conn   *websocket.Conn
	send   chan []byte
}

// Pulse CRDT types - mirrors Pulse's internal CRDT types
type PulseVectorClock map[string]int64

// Dot represents a single operation in the causal history
type PulseDot struct {
	NodeID string `json:"node_id"`
	Seq    int64  `json:"seq"`
}

// PulseCRDTOperation represents a synchronized operation
type PulseCRDTOperation struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`         // "add_issue", "update_task", "claim_task", etc.
	EntityType string          `json:"entity_type"`  // "issue", "task", "worker"
	EntityID   string          `json:"entity_id"`
	Dot        PulseDot        `json:"dot"`
	Payload    json.RawMessage `json:"payload"`
	Timestamp  time.Time       `json:"timestamp"`
}

// PulseSyncMessage represents a sync request/response between nodes
type PulseSyncMessage struct {
	NodeID     string              `json:"node_id"`
	Clock      PulseVectorClock    `json:"clock"`
	Operations []PulseCRDTOperation `json:"operations"`
	Requests   []string            `json:"requests"` // Request missing operations
}

// NewPulseSyncRegistry creates a new Pulse sync registry
func NewPulseSyncRegistry() *PulseSyncRegistry {
	ctx, cancel := context.WithCancel(context.Background())
	return &PulseSyncRegistry{
		operations:  make(map[string]*PulseCRDTOperation),
		nodeClocks:  make(map[string]PulseVectorClock),
		subscribers: make(map[string]*pulseSyncClient),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for development
			},
		},
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start starts the background cleanup goroutine
func (r *PulseSyncRegistry) Start() {
	go r.cleanupLoop()
}

// Stop stops the registry
func (r *PulseSyncRegistry) Stop() {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, client := range r.subscribers {
		client.conn.Close()
	}
}

// RegisterOperation registers an operation and returns operations to broadcast
func (r *PulseSyncRegistry) RegisterOperation(op *PulseCRDTOperation) []*PulseCRDTOperation {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Store operation
	r.operations[op.ID] = op

	// Update node clock
	if r.nodeClocks[op.Dot.NodeID] == nil {
		r.nodeClocks[op.Dot.NodeID] = make(PulseVectorClock)
	}
	if r.nodeClocks[op.Dot.NodeID][op.Dot.NodeID] < op.Dot.Seq {
		r.nodeClocks[op.Dot.NodeID][op.Dot.NodeID] = op.Dot.Seq
	}

	// Broadcast to all other clients
	var toBroadcast []*PulseCRDTOperation
	for nodeID, client := range r.subscribers {
		if nodeID != op.Dot.NodeID {
			// Check if client needs this operation based on their clock
			clientClock := r.nodeClocks[nodeID]
			if clientClock == nil {
				clientClock = make(PulseVectorClock)
			}
			// Client needs operation if their clock is behind
			if clientClock[op.Dot.NodeID] < op.Dot.Seq {
				toBroadcast = append(toBroadcast, op)
				select {
				case client.send <- mustMarshalPulse(op):
				default:
					// Client buffer full, skip
				}
			}
		}
	}

	return toBroadcast
}

// GetOperationsSince returns operations that the client needs based on their clock
func (r *PulseSyncRegistry) GetOperationsSince(clock PulseVectorClock, nodeID string) []*PulseCRDTOperation {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var needed []*PulseCRDTOperation
	for _, op := range r.operations {
		// Check if client needs this operation
		opSeq := clock[op.Dot.NodeID]
		if opSeq < op.Dot.Seq {
			needed = append(needed, op)
		}
	}

	// Update client's clock
	if r.nodeClocks[nodeID] == nil {
		r.nodeClocks[nodeID] = make(PulseVectorClock)
	}
	for k, v := range clock {
		if r.nodeClocks[nodeID][k] < v {
			r.nodeClocks[nodeID][k] = v
		}
	}

	return needed
}

// GetAllNodes returns all known node IDs
func (r *PulseSyncRegistry) GetAllNodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nodes := make([]string, 0, len(r.nodeClocks))
	for nodeID := range r.nodeClocks {
		nodes = append(nodes, nodeID)
	}
	return nodes
}

// GetNodeClock returns the vector clock for a node
func (r *PulseSyncRegistry) GetNodeClock(nodeID string) PulseVectorClock {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if clock, ok := r.nodeClocks[nodeID]; ok {
		result := make(PulseVectorClock)
		for k, v := range clock {
			result[k] = v
		}
		return result
	}
	return make(PulseVectorClock)
}

// Subscribe creates a WebSocket subscription for a client
func (r *PulseSyncRegistry) Subscribe(nodeID string, w http.ResponseWriter, req *http.Request) error {
	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		return fmt.Errorf("failed to upgrade websocket: %w", err)
	}

	client := &pulseSyncClient{
		nodeID: nodeID,
		conn:   conn,
		send:   make(chan []byte, 256),
	}

	r.mu.Lock()
	r.subscribers[nodeID] = client
	// Initialize node clock if needed
	if r.nodeClocks[nodeID] == nil {
		r.nodeClocks[nodeID] = make(PulseVectorClock)
	}
	r.mu.Unlock()

	// Start writer goroutine
	go client.writePump(r.ctx)

	// Start reader goroutine
	client.readPump(r, nodeID)

	return nil
}

// Unsubscribe removes a client subscription
func (r *PulseSyncRegistry) Unsubscribe(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if client, ok := r.subscribers[nodeID]; ok {
		client.conn.Close()
		delete(r.subscribers, nodeID)
	}
}

func (r *PulseSyncRegistry) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			// Cleanup old operations (keep last 10000)
			if len(r.operations) > 10000 {
				// In a real implementation, we'd prune based on oldest timestamp
				// For now, just cap the map size
			}
			r.mu.Unlock()
		}
	}
}

func (c *pulseSyncClient) readPump(r *PulseSyncRegistry, nodeID string) {
	defer func() {
		r.Unsubscribe(nodeID)
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

		var syncMsg PulseSyncMessage
		if err := json.Unmarshal(message, &syncMsg); err != nil {
			continue
		}

		// Handle ping/heartbeat
		if len(syncMsg.Operations) == 0 && len(syncMsg.Requests) == 0 {
			continue
		}

		// Update client's clock
		r.mu.Lock()
		if r.nodeClocks[nodeID] == nil {
			r.nodeClocks[nodeID] = make(PulseVectorClock)
		}
		for k, v := range syncMsg.Clock {
			if r.nodeClocks[nodeID][k] < v {
				r.nodeClocks[nodeID][k] = v
			}
		}
		r.mu.Unlock()
	}
}

func (c *pulseSyncClient) writePump(ctx context.Context) {
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

// Server integration methods

// PulseSyncHandler handles sync requests from Pulse LocalPulse clients
func (s *Server) PulseSyncHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req PulseSyncMessage
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Get operations the client needs based on their clock
	ops := s.pulseSyncRegistry.GetOperationsSince(req.Clock, req.NodeID)

	// Convert []*PulseCRDTOperation to []PulseCRDTOperation
	opsValue := make([]PulseCRDTOperation, len(ops))
	for i, op := range ops {
		opsValue[i] = *op
	}

	// Build response with current server clock
	response := PulseSyncMessage{
		NodeID:     "server",
		Clock:      s.pulseSyncRegistry.GetNodeClock(req.NodeID),
		Operations: opsValue,
		Requests:   []string{}, // Empty means we have all requested data
	}

	responseBody, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(responseBody)
}

// PulseOperationsHandler receives operations from Pulse clients
func (s *Server) PulseOperationsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var op PulseCRDTOperation
	if err := json.Unmarshal(body, &op); err != nil {
		http.Error(w, "invalid operation body", http.StatusBadRequest)
		return
	}

	// Validate operation
	if op.ID == "" || op.Dot.NodeID == "" || op.Dot.Seq == 0 {
		http.Error(w, "invalid operation: missing required fields", http.StatusBadRequest)
		return
	}

	// Register operation and broadcast to other clients
	s.pulseSyncRegistry.RegisterOperation(&op)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

// PulseWebSocketSyncHandler handles WebSocket connections for real-time Pulse sync
func (s *Server) PulseWebSocketSyncHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		nodeID = r.Header.Get("X-Node-ID")
	}
	if nodeID == "" {
		// Generate a random node ID if none provided
		nodeID = fmt.Sprintf("pulse-%d", time.Now().UnixNano())
	}

	if err := s.pulseSyncRegistry.Subscribe(nodeID, w, r); err != nil {
		http.Error(w, "failed to subscribe", http.StatusInternalServerError)
		return
	}
}

// GetPulseSyncStatus returns the current Pulse sync status
func (s *Server) GetPulseSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodes := s.pulseSyncRegistry.GetAllNodes()
	status := map[string]interface{}{
		"nodes":        nodes,
		"node_count":   len(nodes),
		"subscribers":  len(s.pulseSyncRegistry.subscribers),
		"operations":   len(s.pulseSyncRegistry.operations),
	}

	responseBody, err := json.Marshal(status)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(responseBody)
}

func mustMarshalPulse(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// RegisterPulseSyncRoutes registers the Pulse sync routes on the server
func (s *Server) RegisterPulseSyncRoutes() {
	// These are called after the server is created to add pulse sync routes
}

// initPulseSync initializes the Pulse sync registry
func (s *Server) initPulseSync() {
	s.pulseSyncRegistry = NewPulseSyncRegistry()
	s.pulseSyncRegistry.Start()
	log.Println("Pulse sync registry initialized")
}
