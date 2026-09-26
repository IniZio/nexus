package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// withFakeHerdrEmpty overrides herdrExecCommandContext for the duration of t
// and returns an empty workspace list (no workspaces).
func withFakeHerdrEmpty(t *testing.T) {
	t.Helper()
	orig := herdrExecCommandContext
	t.Cleanup(func() { herdrExecCommandContext = orig })
	herdrExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		payload := `{"result":{"workspaces":[{"workspace_id":"wX"}]}}`
		cmd := exec.CommandContext(ctx, "cat")
		cmd.Stdin = strings.NewReader(payload)
		return cmd
	}
}

// TestHerdrSpacePruneWorkspaceExistsFn_ForeignSession: a binding owned by a
// different herdr session must survive even when absent from the listing.
func TestHerdrSpacePruneWorkspaceExistsFn_ForeignSession(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-session-B.sock")
	withFakeHerdrEmpty(t)

	pred := herdrSpacePruneWorkspaceExistsFn(context.Background(), "fake-herdr")

	b := HerdrSpaceBinding{
		HerdrWorkspaceID: "wGONE",
		HerdrSession:     "/tmp/herdr-session-A.sock",
	}
	if got := pred(b); got != true {
		t.Errorf("foreign-session binding: predicate = %v, want true (must not be pruned by another session)", got)
	}
}

// TestHerdrSpacePruneWorkspaceExistsFn_LegacyNoSession: a legacy binding with
// no HerdrSession must survive regardless of the listing.
func TestHerdrSpacePruneWorkspaceExistsFn_LegacyNoSession(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-session-cur.sock")
	withFakeHerdrEmpty(t)

	pred := herdrSpacePruneWorkspaceExistsFn(context.Background(), "fake-herdr")

	b := HerdrSpaceBinding{
		HerdrWorkspaceID: "wLEGACY",
		HerdrSession:     "",
	}
	if got := pred(b); got != true {
		t.Errorf("legacy binding (empty HerdrSession): predicate = %v, want true (legacy bindings must not be pruned)", got)
	}
}

// TestHerdrSpacePruneWorkspaceExistsFn_SameSessionAbsent: a binding owned by
// the current session that is absent from the listing must be pruned.
func TestHerdrSpacePruneWorkspaceExistsFn_SameSessionAbsent(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-session-cur.sock")

	orig := herdrExecCommandContext
	t.Cleanup(func() { herdrExecCommandContext = orig })
	herdrExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		payload := `{"result":{"workspaces":[{"workspace_id":"wOTHER"}]}}`
		cmd := exec.CommandContext(ctx, "cat")
		cmd.Stdin = strings.NewReader(payload)
		return cmd
	}

	pred := herdrSpacePruneWorkspaceExistsFn(context.Background(), "fake-herdr")

	b := HerdrSpaceBinding{
		HerdrWorkspaceID: "wGONE",
		HerdrSession:     "/tmp/herdr-session-cur.sock",
	}
	if got := pred(b); got != false {
		t.Errorf("same-session absent binding: predicate = %v, want false (stale workspace must be prunable)", got)
	}
}

// TestHerdrCurrentSession covers the three resolution paths.
func TestHerdrCurrentSession(t *testing.T) {
	t.Run("HERDR_SOCKET_PATH set", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-dbg.sock")
		t.Setenv("HERDR_SESSION", "")
		got := herdrCurrentSession()
		if got != "/tmp/herdr-dbg.sock" {
			t.Errorf("got %q, want /tmp/herdr-dbg.sock", got)
		}
	})

	t.Run("HERDR_SESSION set", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "")
		t.Setenv("HERDR_SESSION", "mysession")
		got := herdrCurrentSession()
		want := herdrSocketPath("mysession")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("neither set", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "")
		t.Setenv("HERDR_SESSION", "")
		got := herdrCurrentSession()
		want := herdrDefaultSession()
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}
