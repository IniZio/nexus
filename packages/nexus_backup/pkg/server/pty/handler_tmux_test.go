package pty

import (
	"strings"
	"testing"
)

func TestBuildTmuxAttachCommand_UsesWorkspaceDirAndStaleSessionGuard(t *testing.T) {
	cmd := buildTmuxAttachCommand("nexus_ws_session", "/workspace/ws-demo")

	if !strings.Contains(cmd, "tmux has-session -t") {
		t.Fatalf("expected tmux session existence check, got %q", cmd)
	}
	if !strings.Contains(cmd, "tmux display-message -p -t") {
		t.Fatalf("expected tmux pane cwd probe, got %q", cmd)
	}
	if !strings.Contains(cmd, "tmux kill-session -t") {
		t.Fatalf("expected stale tmux session cleanup, got %q", cmd)
	}
	if !strings.Contains(cmd, "tmux new-session -A -c '/workspace/ws-demo' -s") {
		t.Fatalf("expected tmux to attach/create in quoted guest cwd, got %q", cmd)
	}
}

