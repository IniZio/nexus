package cli

// Tests for the herdrGlobalPruneGuard wiring inside herdrPluginSpacePrune.
// All seams are injected; no VMs or herdr servers are touched.
//
// Cases:
//  1. Non-default HERDR_SOCKET_PATH + --apply → *UsageError mentioning allow-foreign-sessions; nothing removed.
//  2. Non-default HERDR_SOCKET_PATH + dry-run → proceeds; no error.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

// noopListSvc is a minimal herdrSpacePruneLister with no sandboxes.
type noopListSvc struct{}

func (noopListSvc) List(_ context.Context) ([]domain.Sandbox, error) {
	return nil, nil
}

// TestHerdrPruneGuardWiring_NonDefaultSession_Apply verifies that --apply is
// refused when HERDR_SOCKET_PATH points at a non-default socket.
func TestHerdrPruneGuardWiring_NonDefaultSession_Apply(t *testing.T) {
	stubWorktreeAbsent(t)
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-debug-session.sock")
	ctx := context.Background()
	root := t.TempDir()

	var buf bytes.Buffer
	err := herdrPluginSpacePrune(ctx, []string{"--apply"}, &buf, noopListSvc{}, root, "/bin/true")
	if err == nil {
		t.Fatal("want error from guard; got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want *UsageError; got %T: %v", err, err)
	}
	if !strings.Contains(ue.Msg, "allow-foreign-sessions") {
		t.Errorf("error must mention --allow-foreign-sessions; got: %s", ue.Msg)
	}
}

// TestHerdrPruneGuardWiring_NonDefaultSession_DryRun verifies that dry-run is
// never refused by the guard, even with a non-default socket.
func TestHerdrPruneGuardWiring_NonDefaultSession_DryRun(t *testing.T) {
	stubWorktreeAbsent(t)
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-debug-session.sock")
	ctx := context.Background()
	root := t.TempDir()

	var buf bytes.Buffer
	if err := herdrPluginSpacePrune(ctx, []string{}, &buf, noopListSvc{}, root, ""); err != nil {
		t.Errorf("dry-run with non-default session: want nil; got %v", err)
	}
}

// TestHerdrPruneGuardWiring_AllowForeign_Proceeds verifies that
// --allow-foreign-sessions passes the guard even with a non-default socket.
func TestHerdrPruneGuardWiring_AllowForeign_Proceeds(t *testing.T) {
	stubWorktreeAbsent(t)
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-debug-session.sock")
	ctx := context.Background()
	root := t.TempDir()

	var buf bytes.Buffer
	err := herdrPluginSpacePrune(ctx, []string{"--apply", "--allow-foreign-sessions"}, &buf, noopListSvc{}, root, "/bin/true")
	if err != nil {
		var ue *UsageError
		if errors.As(err, &ue) && strings.Contains(ue.Msg, "allow-foreign-sessions") {
			t.Fatalf("guard must not fire with --allow-foreign-sessions; got: %v", err)
		}
	}
}

// TestHerdrPruneGuardWiring_ForeignBinding_DefaultSession verifies that a
// binding owned by a foreign herdr session triggers the guard even when the
// current socket is the default.
func TestHerdrPruneGuardWiring_ForeignBinding_DefaultSession(t *testing.T) {
	stubWorktreeAbsent(t)
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_SESSION", "")
	ctx := context.Background()
	root := t.TempDir()

	foreign := HerdrSpaceBinding{
		SpaceLabel:       "nexus:proj/foreign",
		HerdrWorkspaceID: "wFOREIGN",
		SandboxHandle:    "proj/foreign",
		SandboxID:        "sb-foreign",
		HerdrSession:     "/tmp/herdr-other-session.sock",
	}
	if err := HerdrSpacePut(ctx, root, foreign); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	svc := &fakePruneSvc{sbs: []domain.Sandbox{{Project: "proj", Name: "foreign"}}}
	var buf bytes.Buffer
	err := herdrPluginSpacePrune(ctx, []string{"--apply"}, &buf, svc, root, "/bin/true")
	if err == nil {
		t.Fatal("want error from guard (foreign binding); got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want *UsageError; got %T: %v", err, err)
	}
	if !strings.Contains(ue.Msg, "allow-foreign-sessions") {
		t.Errorf("error must mention --allow-foreign-sessions; got: %s", ue.Msg)
	}

	if _, err := HerdrSpaceGetByLabel(ctx, root, foreign.SpaceLabel); err != nil {
		t.Errorf("foreign binding must be preserved; got: %v", err)
	}
	if len(svc.removed) != 0 {
		t.Errorf("no sandbox must be removed; got %v", svc.removed)
	}
}
