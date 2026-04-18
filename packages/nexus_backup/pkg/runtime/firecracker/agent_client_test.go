package firecracker

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestAgentClientExecRoundTrip(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		defer server.Close()
		var req ExecRequest
		if err := json.NewDecoder(server).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			return
		}
		resp := ExecResult{
			ID:       req.ID,
			ExitCode: 0,
			Stdout:   "ok",
			Stderr:   "",
		}
		if err := json.NewEncoder(server).Encode(resp); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}()

	c := NewAgentClient(client)
	res, err := c.Exec(context.Background(), ExecRequest{ID: "1", Command: "echo", Args: []string{"ok"}})
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if res.Stdout != "ok" {
		t.Fatalf("unexpected stdout: %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if res.ID != "1" {
		t.Fatalf("expected ID \"1\", got %s", res.ID)
	}
}

func TestAgentClientExecWithErrorResult(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		defer server.Close()
		var req ExecRequest
		if err := json.NewDecoder(server).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			return
		}
		resp := ExecResult{
			ID:       req.ID,
			ExitCode: 1,
			Stdout:   "",
			Stderr:   "command failed",
		}
		if err := json.NewEncoder(server).Encode(resp); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}()

	c := NewAgentClient(client)
	res, err := c.Exec(context.Background(), ExecRequest{ID: "2", Command: "false"})
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if res.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", res.ExitCode)
	}
	if res.Stderr != "command failed" {
		t.Fatalf("unexpected stderr: %q", res.Stderr)
	}
}

func TestAgentClientContextCancellation(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	// Slow server that never responds
	go func() {
		defer server.Close()
		// Read request but don't respond
		var req ExecRequest
		_ = json.NewDecoder(server).Decode(&req)
		// Block indefinitely
		select {}
	}()

	c := NewAgentClient(client)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Exec(ctx, ExecRequest{ID: "3", Command: "sleep", Args: []string{"10"}})
	if err == nil {
		t.Fatal("expected error due to context cancellation")
	}
}

func TestAgentClientRequestFraming(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	receivedReq := make(chan ExecRequest, 1)
	go func() {
		defer server.Close()
		var req ExecRequest
		if err := json.NewDecoder(server).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			return
		}
		receivedReq <- req
		resp := ExecResult{ID: req.ID, ExitCode: 0}
		_ = json.NewEncoder(server).Encode(resp)
	}()

	c := NewAgentClient(client)
	_, err := c.Exec(context.Background(), ExecRequest{
		ID:      "4",
		Command: "test-cmd",
		Args:    []string{"arg1", "arg2"},
		WorkDir: "/tmp",
	})
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}

	req := <-receivedReq
	if req.ID != "4" {
		t.Fatalf("expected ID \"4\", got %s", req.ID)
	}
	if req.Command != "test-cmd" {
		t.Fatalf("expected command \"test-cmd\", got %s", req.Command)
	}
	if len(req.Args) != 2 || req.Args[0] != "arg1" || req.Args[1] != "arg2" {
		t.Fatalf("unexpected args: %v", req.Args)
	}
	if req.WorkDir != "/tmp" {
		t.Fatalf("expected workdir \"/tmp\", got %s", req.WorkDir)
	}
}

func TestAgentClientNilConnection(t *testing.T) {
	c := NewAgentClient(nil)
	_, err := c.Exec(context.Background(), ExecRequest{ID: "5", Command: "echo"})
	if err == nil {
		t.Fatal("expected error for nil connection")
	}
}
