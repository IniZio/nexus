package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleExecRunsCommandAndReturnsExitCode(t *testing.T) {
	resp := handleExec(execRequest{Command: "bash", Args: []string{"-lc", "echo hi"}})
	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", resp.ExitCode)
	}
	if strings.TrimSpace(resp.Stdout) != "hi" {
		t.Fatalf("unexpected stdout: %q", resp.Stdout)
	}
}

func TestHandleExecReturnsNonZeroExitCodeOnFailure(t *testing.T) {
	resp := handleExec(execRequest{Command: "bash", Args: []string{"-lc", "exit 42"}})
	if resp.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", resp.ExitCode)
	}
}

func TestHandleExecCapturesStderr(t *testing.T) {
	resp := handleExec(execRequest{Command: "bash", Args: []string{"-lc", "echo error >&2"}})
	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Stderr, "error") {
		t.Fatalf("expected stderr to contain 'error', got: %q", resp.Stderr)
	}
}

func TestHandleExecIncludesStartErrorInStderr(t *testing.T) {
	resp := handleExec(execRequest{Command: "command-that-does-not-exist-for-test"})
	if resp.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code")
	}
	if !strings.Contains(resp.Stderr, "executable file not found") {
		t.Fatalf("expected stderr to include start error, got: %q", resp.Stderr)
	}
}

func TestServeConnSendsErrorOnDecodeFailure(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(server)
	}()

	// Send invalid JSON
	client.Write([]byte("not valid json\n"))

	// Read response
	decoder := json.NewDecoder(client)
	var resp execResponse
	if err := decoder.Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if resp.ExitCode != 1 {
		t.Fatalf("expected exit code 1 on decode error, got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Stderr, "decode error") {
		t.Fatalf("expected stderr to contain 'decode error', got: %q", resp.Stderr)
	}

	// Connection should close after error
	client.Close()
	<-done
}

func TestServeConnRejectsMissingRequestID(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(server)
	}()

	// Send request without ID
	encoder := json.NewEncoder(client)
	encoder.Encode(execRequest{ID: "", Command: "echo", Args: []string{"test"}})

	// Read error response
	decoder := json.NewDecoder(client)
	var resp execResponse
	if err := decoder.Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if resp.ExitCode != 1 {
		t.Fatalf("expected exit code 1 for missing ID, got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Stderr, "request ID is required") {
		t.Fatalf("expected stderr to contain 'request ID is required', got: %q", resp.Stderr)
	}

	// Server should still be listening for more requests
	encoder.Encode(execRequest{ID: "req-2", Command: "echo", Args: []string{"success"}})

	var successResp execResponse
	if err := decoder.Decode(&successResp); err != nil {
		t.Fatalf("failed to decode success response: %v", err)
	}

	if successResp.ID != "req-2" {
		t.Fatalf("expected ID 'req-2', got %q", successResp.ID)
	}
	if strings.TrimSpace(successResp.Stdout) != "success" {
		t.Fatalf("unexpected stdout: %q", successResp.Stdout)
	}

	client.Close()
	<-done
}

func TestServeConnHonorsWorkDirField(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(server)
	}()

	workDir := filepath.Join(t.TempDir(), "subdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	encoder := json.NewEncoder(client)
	request := map[string]any{
		"id":      "req-workdir",
		"command": "bash",
		"args":    []string{"-lc", "pwd"},
		"workdir": workDir,
	}
	if err := encoder.Encode(request); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	decoder := json.NewDecoder(client)
	var resp execResponse
	if err := decoder.Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with stderr %q", resp.ExitCode, resp.Stderr)
	}
	if strings.TrimSpace(resp.Stdout) != workDir {
		t.Fatalf("expected stdout %q, got %q", workDir, strings.TrimSpace(resp.Stdout))
	}

	client.Close()
	<-done
}
