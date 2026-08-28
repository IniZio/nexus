package supervisor

// T5-AC1 / T5-AC2 / T5-AC3: IPC egress-allow handler end-to-end tests over a
// test Unix socket, mirroring the stop-handler test pattern.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serveTestIPC starts an IPC server on a temporary Unix socket with the given
// allowEgress callback and returns the socket path plus a cleanup function.
func serveTestIPC(t *testing.T, allowEgress allowEgressFunc) string {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, err := serveIPC(ctx, sockPath, nil, "test-sandbox", allowEgress)
	if err != nil {
		t.Fatalf("serveIPC: %v", err)
	}
	// Give the server a moment to accept connections.
	time.Sleep(10 * time.Millisecond)
	return sockPath
}

// httpOverUnix returns an http.Client that dials the given Unix socket.
func httpOverUnix(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

// TestIPCEgressAllow_Success verifies that a well-formed request to a working
// allowEgress callback returns HTTP 200 with OK:true. (T5-AC1 handler path)
func TestIPCEgressAllow_Success(t *testing.T) {
	var received string
	allowEgress := allowEgressFunc(func(host string) error {
		received = host
		return nil
	})

	sockPath := serveTestIPC(t, allowEgress)

	err := RequestEgressAllow(context.Background(), sockPath, "registry.npmjs.org")
	if err != nil {
		t.Fatalf("RequestEgressAllow: unexpected error: %v", err)
	}
	if received != "registry.npmjs.org" {
		t.Errorf("allowEgress received host %q, want %q", received, "registry.npmjs.org")
	}
}

// TestIPCEgressAllow_CallbackError verifies that when the allowEgress callback
// returns an error, the handler responds with OK:false and a non-200 status,
// and RequestEgressAllow surfaces the error. (T5-AC2 partial)
func TestIPCEgressAllow_CallbackError(t *testing.T) {
	allowEgress := allowEgressFunc(func(host string) error {
		return fmt.Errorf("perimeter rejected host %q", host)
	})

	sockPath := serveTestIPC(t, allowEgress)

	err := RequestEgressAllow(context.Background(), sockPath, "evil.example.com")
	if err == nil {
		t.Fatal("expected error from failing callback, got nil")
	}
	if !strings.Contains(err.Error(), "server error") {
		t.Errorf("error should mention server error; got: %v", err)
	}
}

// TestIPCEgressAllow_MalformedBody verifies that a malformed JSON body returns
// 400 and does not invoke the allowEgress callback. (T5-AC2)
func TestIPCEgressAllow_MalformedBody(t *testing.T) {
	called := false
	allowEgress := allowEgressFunc(func(host string) error {
		called = true
		return nil
	})

	sockPath := serveTestIPC(t, allowEgress)
	client := httpOverUnix(sockPath)

	for _, badBody := range []string{"not-json", `{"host":""}`, ``} {
		resp, err := client.Post(
			"http://localhost"+ipcEgressAllowPath,
			"application/json",
			strings.NewReader(badBody),
		)
		if err != nil {
			t.Fatalf("POST with bad body %q: %v", badBody, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("bad body %q: status = %d, want %d", badBody, resp.StatusCode, http.StatusBadRequest)
		}
		if called {
			t.Errorf("bad body %q: allowEgress callback was invoked — must not mutate on bad request", badBody)
		}
	}
}

// TestIPCEgressAllow_NilCallback verifies that a nil allowEgress callback
// (perimeter not yet ready) returns 503. (edge case: IPC started before perimeter)
func TestIPCEgressAllow_NilCallback(t *testing.T) {
	sockPath := serveTestIPC(t, nil) // nil callback
	client := httpOverUnix(sockPath)

	resp, err := client.Post(
		"http://localhost"+ipcEgressAllowPath,
		"application/json",
		strings.NewReader(`{"host":"api.example.com"}`),
	)
	if err != nil {
		t.Fatalf("POST with nil callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("nil callback: status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

// TestRequestEgressAllow_DeadSock verifies that RequestEgressAllow against a
// non-existent socket path returns a clear, identifiable error. (T5-AC3)
func TestRequestEgressAllow_DeadSock(t *testing.T) {
	deadPath := filepath.Join(os.TempDir(), "nexus3-test-nonexistent-supervisor.sock")
	// Ensure it doesn't accidentally exist.
	_ = os.Remove(deadPath)

	err := RequestEgressAllow(context.Background(), deadPath, "api.example.com")
	if err == nil {
		t.Fatal("expected error for non-existent socket, got nil")
	}
	// The error must mention the socket path so the CLI can surface it.
	if !strings.Contains(err.Error(), deadPath) {
		t.Errorf("error should mention socket path %q; got: %v", deadPath, err)
	}
}
