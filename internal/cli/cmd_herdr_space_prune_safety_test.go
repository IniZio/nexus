package cli

// Tests for the reversible-first prune safety rules and scoped-prune session
// filtering added by fix/prune-cross-session. All seams are injected; no VMs
// or herdr servers are touched.

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/audit"
	"github.com/IniZio/nexus/internal/core/domain"
)

// restoreWorktreeAbsentFn saves/restores herdrPruneWorktreeAbsentFn around t.
func withWorktreeAbsentFn(t *testing.T, fn func(string) bool) {
	t.Helper()
	old := herdrPruneWorktreeAbsentFn
	herdrPruneWorktreeAbsentFn = fn
	t.Cleanup(func() { herdrPruneWorktreeAbsentFn = old })
}

// TestHerdrPruneWorktreeAbsentFn verifies the production seam implementation.
func TestHerdrPruneWorktreeAbsentFn(t *testing.T) {
	presentDir := t.TempDir()
	absentPath := filepath.Join(t.TempDir(), "gone")

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"absent abs path", absentPath, true},
		{"present abs path", presentDir, false},
		{"empty path", "", false},
		{"relative path", "relative/gone", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := herdrPruneWorktreeAbsentFn(c.path)
			if got != c.want {
				t.Errorf("herdrPruneWorktreeAbsentFn(%q) = %v; want %v", c.path, got, c.want)
			}
		})
	}
}

// TestHerdrSpacePruneSafety_ForeignSession_NoReap is the regression test for
// the 2026-09-26 incident: a prune run with HERDR_SOCKET_PATH pointing to
// a debug session must NOT reap sandboxes that belong to another session,
// even when the workspace ID is absent from the debug session's listing.
func TestHerdrSpacePruneSafety_ForeignSession_NoReap(t *testing.T) {
	reapCalled := false
	volumesCalled := false

	old := herdrWtRemoveVolumesFn
	herdrWtRemoveVolumesFn = func(_ context.Context, _ string, _ string) []string {
		volumesCalled = true
		return nil
	}
	t.Cleanup(func() { herdrWtRemoveVolumesFn = old })

	// WorktreePath points to an existing dir; worktree present → no reap.
	withWorktreeAbsentFn(t, func(path string) bool { return false })

	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-session-B.sock")
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus:proj/prod",
		HerdrWorkspaceID: "wPROD",
		SandboxHandle:    "proj/prod",
		SandboxID:        "sb-prod",
		WorktreeManaged:  true,
		HerdrSession:     "/tmp/herdr-session-A.sock",
		WorktreePath:     t.TempDir(),
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists := func(b HerdrSpaceBinding) bool { return true }
	workspaceExists := func(b HerdrSpaceBinding) bool { return false }
	closerErr := errors.New("workspace_not_found")
	closer := func(_ context.Context, _ string) error { return closerErr }
	removeSandbox := func(_ context.Context, handle string) error {
		reapCalled = true
		return nil
	}

	var buf bytes.Buffer
	if err := herdrSpacePruneBindings(ctx, &buf, root, "", []HerdrSpaceBinding{b},
		sandboxExists, workspaceExists, closer, removeSandbox, true, false); err != nil {
		t.Fatalf("herdrSpacePruneBindings: %v", err)
	}

	if reapCalled {
		t.Error("removeSandbox must NOT be called for foreign-session binding")
	}
	if volumesCalled {
		t.Error("herdrWtRemoveVolumesFn must NOT be called for foreign-session binding")
	}
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err != nil {
		t.Errorf("binding must be retained (closer failed); got %v", err)
	}
}

// TestHerdrSpacePruneSafety_SameSession_WorktreePresent_NoReap: when the
// worktree checkout exists the sandbox must not be reaped.
func TestHerdrSpacePruneSafety_SameSession_WorktreePresent_NoReap(t *testing.T) {
	reapCalled := false
	volumesCalled := false

	oldVol := herdrWtRemoveVolumesFn
	herdrWtRemoveVolumesFn = func(_ context.Context, _ string, _ string) []string {
		volumesCalled = true
		return nil
	}
	t.Cleanup(func() { herdrWtRemoveVolumesFn = oldVol })

	withWorktreeAbsentFn(t, func(path string) bool { return false }) // checkout present

	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-session-cur.sock")
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus:proj/live",
		HerdrWorkspaceID: "wLIVE",
		SandboxHandle:    "proj/live",
		SandboxID:        "sb-live",
		WorktreeManaged:  true,
		HerdrSession:     "/tmp/herdr-session-cur.sock",
		WorktreePath:     t.TempDir(),
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists := func(b HerdrSpaceBinding) bool { return true }
	workspaceExists := func(b HerdrSpaceBinding) bool { return false }
	closer := func(_ context.Context, _ string) error { return nil }
	removeSandbox := func(_ context.Context, _ string) error {
		reapCalled = true
		return nil
	}

	var buf bytes.Buffer
	if err := herdrSpacePruneBindings(ctx, &buf, root, "", []HerdrSpaceBinding{b},
		sandboxExists, workspaceExists, closer, removeSandbox, true, false); err != nil {
		t.Fatalf("herdrSpacePruneBindings: %v", err)
	}

	if reapCalled {
		t.Error("removeSandbox must NOT be called when worktree checkout is present")
	}
	if volumesCalled {
		t.Error("herdrWtRemoveVolumesFn must NOT be called when worktree checkout is present")
	}
	out := buf.String()
	if !strings.Contains(out, "KEPT sandbox=proj/live") {
		t.Errorf("output must contain KEPT line; got:\n%s", out)
	}
}

// TestHerdrSpacePruneSafety_SameSession_EmptyWorktreePath_NoReap: legacy
// bindings with empty WorktreePath are treated as unverifiable → no reap.
func TestHerdrSpacePruneSafety_SameSession_EmptyWorktreePath_NoReap(t *testing.T) {
	reapCalled := false
	volumesCalled := false

	oldVol := herdrWtRemoveVolumesFn
	herdrWtRemoveVolumesFn = func(_ context.Context, _ string, _ string) []string {
		volumesCalled = true
		return nil
	}
	t.Cleanup(func() { herdrWtRemoveVolumesFn = oldVol })

	// empty WorktreePath → herdrPruneWorktreeAbsentFn("") = false → no reap
	// (use real fn; it already handles empty correctly)

	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-session-cur.sock")
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus:proj/legacy",
		HerdrWorkspaceID: "wLEG",
		SandboxHandle:    "proj/legacy",
		SandboxID:        "sb-leg",
		WorktreeManaged:  true,
		HerdrSession:     "/tmp/herdr-session-cur.sock",
		WorktreePath:     "",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists := func(b HerdrSpaceBinding) bool { return true }
	workspaceExists := func(b HerdrSpaceBinding) bool { return false }
	closer := func(_ context.Context, _ string) error { return nil }
	removeSandbox := func(_ context.Context, _ string) error {
		reapCalled = true
		return nil
	}

	var buf bytes.Buffer
	if err := herdrSpacePruneBindings(ctx, &buf, root, "", []HerdrSpaceBinding{b},
		sandboxExists, workspaceExists, closer, removeSandbox, true, false); err != nil {
		t.Fatalf("herdrSpacePruneBindings: %v", err)
	}

	if reapCalled {
		t.Error("removeSandbox must NOT be called when WorktreePath is empty")
	}
	if volumesCalled {
		t.Error("herdrWtRemoveVolumesFn must NOT be called when WorktreePath is empty")
	}
}

// TestHerdrSpacePruneSafety_SameSession_WorktreeAbsent_Reaps: when the
// worktree checkout is confirmed absent, the sandbox is reaped and the audit
// context reason is non-empty.
func TestHerdrSpacePruneSafety_SameSession_WorktreeAbsent_Reaps(t *testing.T) {
	volumesCalled := false
	var auditReasonSeen string

	oldVol := herdrWtRemoveVolumesFn
	herdrWtRemoveVolumesFn = func(_ context.Context, _ string, _ string) []string {
		volumesCalled = true
		return nil
	}
	t.Cleanup(func() { herdrWtRemoveVolumesFn = oldVol })

	withWorktreeAbsentFn(t, func(path string) bool { return path != "" })

	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-session-cur.sock")
	ctx := context.Background()
	root := t.TempDir()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus:proj/done",
		HerdrWorkspaceID: "wDONE",
		SandboxHandle:    "proj/done",
		SandboxID:        "sb-done",
		WorktreeManaged:  true,
		HerdrSession:     "/tmp/herdr-session-cur.sock",
		WorktreePath:     filepath.Join(t.TempDir(), "gone"),
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	sandboxExists := func(b HerdrSpaceBinding) bool { return true }
	workspaceExists := func(b HerdrSpaceBinding) bool { return false }
	closer := func(_ context.Context, _ string) error { return nil }
	removeSandbox := func(ctx context.Context, _ string) error {
		auditReasonSeen = audit.Reason(ctx)
		return nil
	}

	var buf bytes.Buffer
	if err := herdrSpacePruneBindings(ctx, &buf, root, "", []HerdrSpaceBinding{b},
		sandboxExists, workspaceExists, closer, removeSandbox, true, false); err != nil {
		t.Fatalf("herdrSpacePruneBindings: %v", err)
	}

	if auditReasonSeen == "" {
		t.Error("audit.Reason must be non-empty when reaping a confirmed-absent worktree")
	}
	if !volumesCalled {
		t.Error("herdrWtRemoveVolumesFn must be called when worktree is absent")
	}
	out := buf.String()
	if !strings.Contains(out, "REAPED sandbox=proj/done") {
		t.Errorf("output must contain REAPED line; got:\n%s", out)
	}
}

// TestHerdrSpacePrune_ScopedPrune_ForeignSession_NotCandidate verifies the
// scoped --workspace filter skips bindings from other sessions.
func TestHerdrSpacePrune_ScopedPrune_ForeignSession_NotCandidate(t *testing.T) {
	swept := stubWtRemoveVolumes(t)
	withWorktreeAbsentFn(t, func(path string) bool { return true })
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-session-B.sock")
	ctx := context.Background()
	root := t.TempDir()

	foreign := HerdrSpaceBinding{
		SpaceLabel:       "nexus:proj/foreign",
		HerdrWorkspaceID: "wFOREIGN",
		SandboxHandle:    "proj/foreign",
		SandboxID:        "sb-foreign",
		WorktreeManaged:  true,
		HerdrSession:     "/tmp/herdr-session-A.sock",
		WorktreePath:     filepath.Join(t.TempDir(), "gone"),
	}
	if err := HerdrSpacePut(ctx, root, foreign); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	svc := &fakePruneSvc{sbs: []domain.Sandbox{{Project: "proj", Name: "foreign"}}}

	var buf bytes.Buffer
	if err := herdrPluginSpacePrune(ctx, []string{"--apply", "--workspace", "wFOREIGN"}, &buf, svc, root, "/bin/true"); err != nil {
		t.Fatalf("herdrPluginSpacePrune: %v", err)
	}

	if len(svc.removed) != 0 {
		t.Errorf("foreign-session binding must not be reaped; removed=%v", svc.removed)
	}
	if len(*swept) != 0 {
		t.Errorf("foreign-session volumes must not be swept; swept=%v", *swept)
	}
	if _, err := HerdrSpaceGetByLabel(ctx, root, foreign.SpaceLabel); err != nil {
		t.Errorf("foreign-session binding must survive; got %v", err)
	}
}
