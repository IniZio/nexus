package gitssh_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/gitssh"
)

func TestZ6_RunRelayExitsCleanlyWithNoSSHExec(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := gitssh.RelayConfig{
		SandboxID:    "sb-test",
		VsockUDSPath: filepath.Join(t.TempDir(), "relay.sock"),
	}

	err := gitssh.RunRelay(ctx, cfg)
	if err != nil {
		t.Errorf("RunRelay returned error with cancelled ctx and no connections: %v", err)
	}
}
