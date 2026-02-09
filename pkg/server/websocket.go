package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Upgrader configuration for WebSocket connections
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allow all origins for development; restrict in production
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Event type constants
const (
	EventMetricsUpdate    = "metrics_update"
	EventFeedbackReceived = "feedback_received"
	EventTaskCreated      = "task_created"
	EventAnomalyDetected  = "anomaly_detected"
	EventSatisfactionAlert = "satisfaction_alert"
)

// WebSocketHub manages client connections and message broadcasting
type WebSocketHub struct {
	// Registered clients
	clients map[*Client]bool
	// Register requests from clients
	register chan *Client
	// Unregister requests from clients
	unregister chan *Client
	// Broadcast messages to clients
	broadcast chan []byte
	// Mutex for thread-safe operations
	mu sync.RWMutex
}

// NewWebSocketHub creates a new WebSocket hub
func NewWebSocketHub() *WebSocketHub {
	return &WebSocketHub{
		clients:    make(map[*Client]bool),
		register:    make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte, 256),
	}
}

// Run starts the hub's main event loop
func (h *WebSocketHub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("Client registered. Total clients: %d", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				close(client.send)
				delete(h.clients, client)
			}
			h.mu.Unlock()
			log.Printf("Client unregistered. Total clients: %d", len(h.clients))

		case message := <-h.broadcast:
			h.broadcastMessage(message)
		}
	}
}

// broadcastMessage sends a message to all connected clients
func (h *WebSocketHub) broadcastMessage(message []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		select {
		case client.send <- message:
		default:
			// Client's send buffer is full; close the connection
			close(client.send)
			delete(h.clients, client)
		}
	}
}

// Broadcast sends a message to all connected clients
func (h *WebSocketHub) Broadcast(message []byte) {
	h.broadcast <- message
}

// BroadcastEvent sends a structured event to all connected clients
func (h *WebSocketHub) BroadcastEvent(eventType string, payload interface{}) {
	msg := WSMessage{
		Type:    eventType,
		Payload: payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling WebSocket message: %v", err)
		return
	}

	h.Broadcast(data)
}

// ClientCount returns the number of connected clients
func (h *WebSocketHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Client represents a connected WebSocket client
type Client struct {
	hub *WebSocketHub
	// Buffered channel of outbound messages
	send chan []byte
	// WebSocket connection
	conn *websocket.Conn
	// Optional: client identifier for targeted messages
	ID string
}

// ReadPump handles incoming messages from the client
func (c *Client) ReadPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		// Handle incoming messages from clients (e.g., subscriptions, pings)
		c.handleMessage(message)
	}
}

// handleMessage processes incoming client messages
func (c *Client) handleMessage(message []byte) {
	var msg WSMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		log.Printf("Error unmarshaling client message: %v", err)
		return
	}

	// Handle different message types
	switch msg.Type {
	case "ping":
		// Respond with pong
		response := WSMessage{Type: "pong", Payload: nil}
		data, _ := json.Marshal(response)
		c.send <- data
	}
}

// WritePump handles outgoing messages to the client
func (c *Client) WritePump() {
	defer c.conn.Close()

	for {
		message, ok := <-c.send
		if !ok {
			// Hub closed the channel
			c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}

		w, err := c.conn.NextWriter(websocket.TextMessage)
		if err != nil {
			return
		}
		w.Write(message)

		// Add queued messages to the current WebSocket message
		n := len(c.send)
		for i := 0; i < n; i++ {
			w.Write([]byte{'\n'})
			w.Write(<-c.send)
		}

		if err := w.Close(); err != nil {
			return
		}
	}
}

// WSMessage represents a WebSocket message
type WSMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

// MetricsPayload represents metrics data for WebSocket messages
type MetricsPayload struct {
	Metrics     interface{} `json:"metrics"`
	Timestamp   int64       `json:"timestamp"`
	ServiceName string      `json:"service_name,omitempty"`
}

// FeedbackPayload represents feedback data for WebSocket messages
type FeedbackPayload struct {
	FeedbackID   string      `json:"feedback_id"`
	Content     string      `json:"content"`
	Rating      float64     `json:"rating"`
	ServiceName string      `json:"service_name"`
	Timestamp   int64       `json:"timestamp"`
}

// TaskPayload represents task data for WebSocket messages
type TaskPayload struct {
	TaskID      string `json:"task_id"`
	Title       string `json:"title"`
	Priority    string `json:"priority"`
	Status      string `json:"status"`
	ServiceName string `json:"service_name"`
	Timestamp   int64  `json:"timestamp"`
}

// AlertPayload represents alert data for WebSocket messages
type AlertPayload struct {
	AlertID      string  `json:"alert_id"`
	AlertType    string  `json:"alert_type"` // "anomaly" or "satisfaction"
	Severity     string  `json:"severity"`   // "low", "medium", "high", "critical"
	Message      string  `json:"message"`
	Threshold    float64 `json:"threshold,omitempty"`
	CurrentValue float64 `json:"current_value,omitempty"`
	ServiceName  string  `json:"service_name"`
	Timestamp    int64   `json:"timestamp"`
}

// ServeWS handles WebSocket connections for a specific endpoint
func (h *WebSocketHub) ServeWS(w http.ResponseWriter, r *http.Request, endpoint string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error for %s: %v", endpoint, err)
		return
	}

	client := &Client{
		hub:  h,
		conn: conn,
		send: make(chan []byte, 256),
	}

	h.register <- client

	// Allow collection of memory garbage by starting goroutines
	go client.WritePump()
	go client.ReadPump()
}

// MetricsHandler returns a handler function for the metrics WebSocket endpoint
func (h *WebSocketHub) MetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.ServeWS(w, r, "/ws/metrics")
	}
}

// FeedbackHandler returns a handler function for the feedback WebSocket endpoint
func (h *WebSocketHub) FeedbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.ServeWS(w, r, "/ws/feedback")
	}
}

// AlertsHandler returns a handler function for the alerts WebSocket endpoint
func (h *WebSocketHub) AlertsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.ServeWS(w, r, "/ws/alerts")
	}
}

// BroadcastToEndpoint sends a message to clients connected to a specific endpoint
// This allows for targeted broadcasts based on subscription type
func (h *WebSocketHub) BroadcastToEndpoint(endpoint string, eventType string, payload interface{}) {
	msg := WSMessage{
		Type:    eventType,
		Payload: payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling WebSocket message for %s: %v", endpoint, err)
		return
	}

	h.Broadcast(data)
}
