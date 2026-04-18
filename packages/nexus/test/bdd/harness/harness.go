// Package harness provides BDD test infrastructure for the Nexus daemon.
// It starts a real daemon instance and provides an RPC client for exercising
// daemon behavior in integration tests.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// DaemonHarness manages a daemon instance for BDD tests.
type DaemonHarness struct {
	t      *testing.T
	tmpDir string
	addr   string
	mu     sync.Mutex
}

// New creates a new DaemonHarness. Call h.Start() to bring the daemon up.
// The harness automatically cleans up on test completion.
func New(t *testing.T) *DaemonHarness {
	t.Helper()
	dir, err := os.MkdirTemp("", "nexus-bdd-*")
	if err != nil {
		t.Fatalf("harness: create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return &DaemonHarness{t: t, tmpDir: dir}
}

// TempDir returns the harness temporary directory.
func (h *DaemonHarness) TempDir() string { return h.tmpDir }

// Start brings the daemon up. Returns an error if the daemon fails to start.
// Currently returns an error until the daemon implementation exists.
func (h *DaemonHarness) Start(ctx context.Context) error {
	return fmt.Errorf("harness: daemon not yet implemented")
}

// Stop shuts the daemon down. Safe to call even if Start was not called.
func (h *DaemonHarness) Stop() {}

// RPCClient returns a minimal JSON-RPC client pointed at the running daemon.
func (h *DaemonHarness) RPCClient() *Client {
	return &Client{addr: h.addr, http: &http.Client{Timeout: 10 * time.Second}}
}

// Client is a minimal JSON-RPC 2.0 client for BDD tests.
type Client struct {
	addr string
	http *http.Client
	seq  int
	mu   sync.Mutex
}

// RPCRequest is a JSON-RPC 2.0 request.
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCResponse is a JSON-RPC 2.0 response.
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Call sends a JSON-RPC call and decodes the result into out (if non-nil).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	c.seq++
	c.mu.Unlock()
	// Stub: actual HTTP transport wired when daemon is implemented.
	return fmt.Errorf("client: daemon not yet implemented")
}
