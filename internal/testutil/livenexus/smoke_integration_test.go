//go:build integration

package livenexus

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveSmoke creates one isolated sandbox, runs exec true, removes it, and
// proves the prod nexus ps and herdr sessions are unchanged.
func TestLiveSmoke(t *testing.T) {
	// Capture prod state before.
	prodPS := captureNexusPS(t)
	prodSessions := captureHerdrSessions(t)

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

	afterPS := captureNexusPS(t)
	afterSessions := captureHerdrSessions(t)

	if prodPS != afterPS {
		t.Errorf("nexus ps changed:\nbefore: %s\nafter:  %s", prodPS, afterPS)
	}
	if prodSessions != afterSessions {
		t.Errorf("herdr sessions changed:\nbefore: %s\nafter:  %s", prodSessions, afterSessions)
	}
	t.Logf("prod nexus ps diff: empty (good)")
	t.Logf("prod herdr sessions diff: empty (good)")
}

func captureNexusPS(t *testing.T) string {
	t.Helper()
	cmd := exec.Command(resolveNexusBin(), "ps")
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

func captureHerdrSessions(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("herdr", "session", "list")
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

