//go:build integration

package livenexus

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestLiveSmoke creates one isolated sandbox, runs exec true, removes it, and
// proves no prod state (nexus ps, herdr sessions, worktrees, git branches,
// systemd units, live procs) was modified — verified by the harness t.Cleanup.
func TestLiveSmoke(t *testing.T) {
	// New() captures a before-snapshot; t.Cleanup compares the after-snapshot.
	h := New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Logf("isolated state root: %s", h.StateRoot())
	t.Logf("herdr session: %s  socket: %s", h.SessionName(), h.SocketPath())

	out, err := h.Run(ctx, "run", "--project", "livenexus-smoke", "ghcr.io/inizio/nexus-base:latest", "--", "true")
	if err != nil {
		t.Fatalf("nexus run: %v\n%s", err, out)
	}
	t.Logf("nexus run output: %s", strings.TrimSpace(out))
	// Prod isolation is verified exhaustively in t.Cleanup by the harness
	// (nexus ps, herdr sessions, herdr worktrees/nexus, git branches,
}
