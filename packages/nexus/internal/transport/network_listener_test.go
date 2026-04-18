package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// stubDispatcher is a minimal Dispatcher for testing.
type stubDispatcher struct{}

func (s *stubDispatcher) Dispatch(_ context.Context, method string, _ json.RawMessage) (any, error) {
	switch method {
	case "node.info":
		return map[string]any{
			"node":         map[string]any{"name": "test-node"},
			"capabilities": []map[string]any{{"name": "runtime.process", "available": true}},
		}, nil
	default:
		return nil, fmt.Errorf("method not found: %s", method)
	}
}

// startTestListener starts a NetworkListener on a random port and returns its address + a cancel func.
func startTestListener(t *testing.T, token string) string {
	t.Helper()

	// Pick a random free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cfg := NetworkListenerConfig{
		BindAddress: "127.0.0.1",
		Port:        port,
		TLSMode:     "off",
		Token:       token,
	}
	nl, err := NewNetworkListener(cfg, &stubDispatcher{})
	if err != nil {
		t.Fatalf("NewNetworkListener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() { errCh <- nl.Serve(ctx) }()

	// Wait until the listener is reachable.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Check no immediate startup error.
	select {
	case err := <-errCh:
		t.Fatalf("listener exited early: %v", err)
	default:
	}

	return addr
}

// dialWS dials a WebSocket with an optional bearer token.
func dialWS(t *testing.T, addr, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	return dialer.Dial("ws://"+addr+"/", header)
}

func rpcCall(t *testing.T, conn *websocket.Conn, method string) map[string]any {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	b, _ := json.Marshal(req)
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(msg, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

// TestNetworkListener_NewRejectsEmptyToken ensures construction fails with no token.
func TestNetworkListener_NewRejectsEmptyToken(t *testing.T) {
	_, err := NewNetworkListener(NetworkListenerConfig{
		BindAddress: "127.0.0.1",
		Port:        9999,
		TLSMode:     "off",
		Token:       "",
	}, &stubDispatcher{})
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

// TestNetworkListener_HealthzNoAuth verifies /healthz is accessible without a token.
func TestNetworkListener_HealthzNoAuth(t *testing.T) {
	addr := startTestListener(t, "test-secret")

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz: expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ok") {
		t.Errorf("/healthz body should contain 'ok', got: %s", body)
	}
}

// TestNetworkListener_VersionNoAuth verifies /version is accessible without a token.
func TestNetworkListener_VersionNoAuth(t *testing.T) {
	addr := startTestListener(t, "test-secret")

	resp, err := http.Get("http://" + addr + "/version")
	if err != nil {
		t.Fatalf("GET /version: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/version: expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "version") {
		t.Errorf("/version body should contain 'version', got: %s", body)
	}
}

// TestNetworkListener_WSAuthReject verifies that WS connections with wrong/missing token are rejected.
func TestNetworkListener_WSAuthReject(t *testing.T) {
	addr := startTestListener(t, "correct-token")

	cases := []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"wrong token", "wrong-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, resp, err := dialWS(t, addr, tc.token)
			if conn != nil {
				conn.Close()
				t.Fatalf("expected connection rejection, got open WS conn")
			}
			if err == nil {
				t.Fatal("expected error dialing with bad token")
			}
			if resp != nil && resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", resp.StatusCode)
			}
		})
	}
}

// TestNetworkListener_WSNodeInfo verifies authenticated WS call to node.info succeeds.
func TestNetworkListener_WSNodeInfo(t *testing.T) {
	addr := startTestListener(t, "good-token")

	conn, _, err := dialWS(t, addr, "good-token")
	if err != nil {
		t.Fatalf("dial WS: %v", err)
	}
	defer conn.Close()

	resp := rpcCall(t, conn, "node.info")

	if resp["error"] != nil {
		t.Fatalf("node.info returned error: %v", resp["error"])
	}
	if resp["result"] == nil {
		t.Fatal("node.info: result is nil")
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T", resp["result"])
	}
	node, ok := result["node"].(map[string]any)
	if !ok {
		t.Fatalf("result.node not a map: %T", result["node"])
	}
	if node["name"] == "" || node["name"] == nil {
		t.Error("node.info: node.name is empty")
	}
}

// TestNetworkListener_WSUnknownMethod verifies unknown methods return an RPC error (not connection drop).
func TestNetworkListener_WSUnknownMethod(t *testing.T) {
	addr := startTestListener(t, "tok")

	conn, _, err := dialWS(t, addr, "tok")
	if err != nil {
		t.Fatalf("dial WS: %v", err)
	}
	defer conn.Close()

	resp := rpcCall(t, conn, "no.such.method")

	if resp["error"] == nil {
		t.Fatal("expected RPC error for unknown method, got nil")
	}
}

// TestNetworkListener_StaticTokenStore validates constant-time token comparison.
func TestNetworkListener_StaticTokenStore(t *testing.T) {
	store := NewStaticTokenStore("secret123")

	if !store.Valid("secret123") {
		t.Error("expected valid for correct token")
	}
	if store.Valid("wrong") {
		t.Error("expected invalid for wrong token")
	}
	if store.Valid("") {
		t.Error("expected invalid for empty token")
	}
}
