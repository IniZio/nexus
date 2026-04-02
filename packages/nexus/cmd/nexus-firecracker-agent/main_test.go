package main

import (
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