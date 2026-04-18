package transport

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// WebSocketConfig holds configuration for the WebSocket transport.
type WebSocketConfig struct {
	Address   string
	TLSConfig *tls.Config
}

// WebSocketTransport implements Transport over HTTP/WebSocket.
// The actual WebSocket upgrade and RPC dispatch logic currently lives in
// pkg/server/server.go and will be migrated here in a future track.
type WebSocketTransport struct {
	cfg    WebSocketConfig
	srv    *http.Server
	mu     sync.Mutex
	closed bool
}

// NewWebSocketTransport creates a new WebSocket transport with the given config.
func NewWebSocketTransport(cfg WebSocketConfig) *WebSocketTransport {
	return &WebSocketTransport{cfg: cfg}
}

func (t *WebSocketTransport) Name() string { return "websocket" }

// Serve starts the HTTP server and handles WebSocket upgrades.
// RPC dispatch is delegated to reg.
func (t *WebSocketTransport) Serve(reg Registry, _ *Deps) error {
	if reg == nil {
		return errors.New("transport: registry must not be nil")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "websocket upgrade required", http.StatusUpgradeRequired)
	})

	t.mu.Lock()
	t.srv = &http.Server{
		Addr:      t.cfg.Address,
		Handler:   mux,
		TLSConfig: t.cfg.TLSConfig,
	}
	srv := t.srv
	t.mu.Unlock()

	var err error
	if t.cfg.TLSConfig != nil {
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("websocket transport: %w", err)
}

func (t *WebSocketTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.srv == nil {
		return nil
	}
	t.closed = true
	return t.srv.Close()
}
