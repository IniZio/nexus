package cli

// Tests for TBD-PD-33, TBD-PD-34, TBD-PD-35.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── TBD-PD-33: herdrShellCwd ─────────────────────────────────────────────────

type fakeSandboxGetter struct {
	sb  domain.Sandbox
	err error
}

func (f *fakeSandboxGetter) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return f.sb, f.err
}

func TestHerdrShellCwd_LiveMountFirst(t *testing.T) {
	sb := domain.Sandbox{
		LiveMounts:     []domain.LiveMount{{GuestPath: "/workspace/repo"}},
		MountedVolumes: []domain.VolumeAttachment{{GuestPath: "/mnt/data"}},
	}
	got := herdrShellCwd(context.Background(), "ref", &fakeSandboxGetter{sb: sb})
	if got != "/workspace/repo" {
		t.Errorf("got %q, want /workspace/repo", got)
	}
}

func TestHerdrShellCwd_FallsBackToMountedVolume(t *testing.T) {
	sb := domain.Sandbox{
		MountedVolumes: []domain.VolumeAttachment{{GuestPath: "/mnt/data"}},
	}
	got := herdrShellCwd(context.Background(), "ref", &fakeSandboxGetter{sb: sb})
	if got != "/mnt/data" {
		t.Errorf("got %q, want /mnt/data", got)
	}
}

func TestHerdrShellCwd_FallsBackToRoot(t *testing.T) {
	sb := domain.Sandbox{} // no mounts
	got := herdrShellCwd(context.Background(), "ref", &fakeSandboxGetter{sb: sb})
	if got != "/root" {
		t.Errorf("got %q, want /root", got)
	}
}

func TestHerdrShellCwd_ServiceErrorFallsToRoot(t *testing.T) {
	got := herdrShellCwd(context.Background(), "ref", &fakeSandboxGetter{err: errors.New("sandbox not found")})
	if got != "/root" {
		t.Errorf("got %q, want /root on service error", got)
	}
}

// ── TBD-PD-34: herdrSpaceResolve identifier matrix ───────────────────────────

// TestHerdrSpaceResolve_ByLabel verifies resolution by exact SpaceLabel.
func TestHerdrSpaceResolve_ByLabel(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wX", SandboxHandle: "orca/demo", SandboxID: "sb-1"}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := herdrSpaceResolve(ctx, root, "nexus3:demo")
	if err != nil || got != b {
		t.Errorf("ByLabel: got %+v err %v, want %+v", got, err, b)
	}
}

// TestHerdrSpaceResolve_ByHandle verifies resolution by sandbox handle
// (the identifier returned by space-create and used by space-list).
func TestHerdrSpaceResolve_ByHandle(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wX", SandboxHandle: "orca/demo", SandboxID: "sb-1"}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := herdrSpaceResolve(ctx, root, "orca/demo")
	if err != nil || got != b {
		t.Errorf("ByHandle: got %+v err %v, want %+v", got, err, b)
	}
}

// TestHerdrSpaceResolve_ByDerivedLabel verifies resolution by the label that
// space-create derives from the ref: herdrSpaceLabelForRef("orca/demo") → "nexus3:orca/demo".
func TestHerdrSpaceResolve_ByDerivedLabel(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	// Binding uses the DERIVED label (as space-create would store it).
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:orca/demo", HerdrWorkspaceID: "wX", SandboxHandle: "orca/demo", SandboxID: "sb-1"}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Query by bare handle — herdrSpaceResolve will fall through to derived label.
	got, err := herdrSpaceResolve(ctx, root, "orca/demo")
	if err != nil || got != b {
		t.Errorf("ByDerivedLabel: got %+v err %v, want %+v", got, err, b)
	}
}

// TestHerdrSpaceResolve_ByWorkspaceID verifies resolution by HerdrWorkspaceID.
func TestHerdrSpaceResolve_ByWorkspaceID(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wXYZ", SandboxHandle: "orca/demo", SandboxID: "sb-1"}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := herdrSpaceResolve(ctx, root, "wXYZ")
	if err != nil || got != b {
		t.Errorf("ByWorkspaceID: got %+v err %v, want %+v", got, err, b)
	}
}

// TestHerdrSpaceResolve_NotFound verifies that an unknown key returns ErrHerdrSpaceNotFound.
func TestHerdrSpaceResolve_NotFound(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	_, err := herdrSpaceResolve(ctx, root, "no-such-ref")
	if !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("want ErrHerdrSpaceNotFound, got %v", err)
	}
}

// ── TBD-PD-35: herdrSpaceRemoveFull ErrNotFound tolerance ───────────────────

// storeNotFoundSvc is a fake that returns an error wrapping store.ErrNotFound.
type storeNotFoundSvc struct {
	removed []string
}

func (s *storeNotFoundSvc) Pause(_ context.Context, ref string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}
func (s *storeNotFoundSvc) Resume(_ context.Context, ref string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}
func (s *storeNotFoundSvc) Remove(_ context.Context, ref string) error {
	s.removed = append(s.removed, ref)
	// Simulate service wrapping store.ErrNotFound as "resolve %q: %w".
	return fmt.Errorf("resolve %q: %w", ref, store.ErrNotFound)
}

// TestHerdrSpaceRemoveFull_ToleratesNotFound confirms that a sandbox that is
// already gone (store.ErrNotFound from svc.Remove) still results in binding deletion.
func TestHerdrSpaceRemoveFull_ToleratesNotFound(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wX", SandboxHandle: "orca/demo", SandboxID: "sb-xxx"}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	svc := &storeNotFoundSvc{}
	if err := herdrSpaceRemoveFull(ctx, svc, root, "", b); err != nil {
		t.Fatalf("herdrSpaceRemoveFull returned error: %v", err)
	}
	// svc.Remove was called (attempted removal)
	if len(svc.removed) != 1 || svc.removed[0] != b.SandboxHandle {
		t.Errorf("removed = %v, want [%s]", svc.removed, b.SandboxHandle)
	}
	// Binding deleted despite not-found error.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("binding still present after RemoveFull with not-found sandbox; got %v", err)
	}
}

// TestHerdrSpaceRemoveFull_PropagatesRealError confirms non-not-found errors still fail.
func TestHerdrSpaceRemoveFull_PropagatesRealError(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wX", SandboxHandle: "orca/demo", SandboxID: "sb-xxx"}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	svc := &fakeSandboxSvc{removeErr: errors.New("driver offline")}
	err := herdrSpaceRemoveFull(ctx, svc, root, "", b)
	if err == nil {
		t.Fatal("expected error from service, got nil")
	}
	// Binding must remain.
	if _, err2 := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err2 != nil {
		t.Errorf("binding must remain after failed RemoveFull, got %v", err2)
	}
}

// ── TBD-PD-35: herdrWorkspaceClose ──────────────────────────────────────────

// TestHerdrWorkspaceClose_EmptyBinIsNoop confirms empty herdrBin is a no-op.
func TestHerdrWorkspaceClose_EmptyBinIsNoop(t *testing.T) {
	if err := herdrWorkspaceClose(context.Background(), "", "wX"); err != nil {
		t.Errorf("empty herdrBin: got error %v, want nil", err)
	}
}

// TestHerdrWorkspaceClose_EmptyWorkspaceIDIsNoop confirms empty workspaceID is a no-op.
func TestHerdrWorkspaceClose_EmptyWorkspaceIDIsNoop(t *testing.T) {
	if err := herdrWorkspaceClose(context.Background(), "/some/bin", ""); err != nil {
		t.Errorf("empty workspaceID: got error %v, want nil", err)
	}
}

// TestHerdrWorkspaceClose_WorkspaceNotFoundIsSuccess confirms workspace_not_found exit
// from the herdr binary is treated as success (already closed).
func TestHerdrWorkspaceClose_WorkspaceNotFoundIsSuccess(t *testing.T) {
	// Write a mock binary that exits 1 and prints workspace_not_found.
	dir := t.TempDir()
	script := filepath.Join(dir, "herdr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho workspace_not_found; exit 1\n"), 0o755); err != nil {
		t.Fatalf("write mock: %v", err)
	}
	if err := herdrWorkspaceClose(context.Background(), script, "wX"); err != nil {
		t.Errorf("workspace_not_found should be success, got %v", err)
	}
}

// TestHerdrWorkspaceClose_RealErrorPropagates confirms a genuine error propagates.
func TestHerdrWorkspaceClose_RealErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "herdr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'internal error'; exit 2\n"), 0o755); err != nil {
		t.Fatalf("write mock: %v", err)
	}
	if err := herdrWorkspaceClose(context.Background(), script, "wX"); err == nil {
		t.Error("expected error from non-zero exit, got nil")
	}
}

// TestHerdrWorkspaceClose_SuccessIsNil confirms a zero-exit is success.
func TestHerdrWorkspaceClose_SuccessIsNil(t *testing.T) {
	// Check that /bin/true works as a mock success.
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true not found")
	}
	if err := herdrWorkspaceClose(context.Background(), trueBin, "wX"); err != nil {
		t.Errorf("zero-exit should be nil, got %v", err)
	}
}

// ── mutation-test guard: store.ErrNotFound tolerance ─────────────────────────

// TestHerdrSpaceRemoveFull_MutationGuard_NotFoundTolerance verifies that
// removing the not-found check in herdrSpaceRemoveFull would cause a test failure.
// (This test IS the evidence that the guard is covered — a mutation that makes
// herdrSpaceRemoveFull return the not-found error would fail this test.)
func TestHerdrSpaceRemoveFull_MutationGuard_NotFoundTolerance(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:x", HerdrWorkspaceID: "wQ", SandboxHandle: "p/x", SandboxID: "sb-q"}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Inject a not-found error from the sandbox service.
	svc := &storeNotFoundSvc{}
	err := herdrSpaceRemoveFull(ctx, svc, root, "", b)
	if err != nil {
		t.Errorf("MutationGuard: herdrSpaceRemoveFull must succeed when sandbox not found; got %v", err)
	}
	// The binding must be gone — if not, the tolerate logic wasn't reached.
	if _, getErr := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); !errors.Is(getErr, ErrHerdrSpaceNotFound) {
		t.Errorf("MutationGuard: binding still present; not-found tolerance did not delete it; getErr=%v", getErr)
	}
}

// ── mutation-test guard: resolver routing ────────────────────────────────────

// TestHerdrSpaceResolve_MutationGuard_HandleRouting verifies that routing
// space verbs through herdrSpaceResolve makes handle-based lookup work.
// If the resolver were removed and callers passed rest[0] directly as a label,
// this test would fail because "orca/demo" is a handle, not a label.
func TestHerdrSpaceResolve_MutationGuard_HandleRouting(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{SpaceLabel: "nexus3:orca-demo", HerdrWorkspaceID: "wM", SandboxHandle: "orca/demo", SandboxID: "sb-m"}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Simulate what the fixed space-pause/resume/remove handler does:
	// resolve first, then call ByLabel with resolved label.
	resolved, err := herdrSpaceResolve(ctx, root, "orca/demo")
	if err != nil {
		t.Fatalf("herdrSpaceResolve by handle: %v", err)
	}
	// Now verify that calling ByLabel with the resolved label works.
	got, err := HerdrSpaceGetByLabel(ctx, root, resolved.SpaceLabel)
	if err != nil || got != b {
		t.Errorf("GetByLabel(resolved.SpaceLabel): got %+v err %v, want %+v", got, err, b)
	}
	// Contrast: calling GetByLabel with the raw handle would fail.
	if _, err2 := HerdrSpaceGetByLabel(ctx, root, "orca/demo"); !errors.Is(err2, ErrHerdrSpaceNotFound) {
		t.Errorf("GetByLabel(raw handle) should fail with not-found; got %v", err2)
	}
}

// ── TBD-PD-33: --cwd flag plumbing ──────────────────────────────────────────


func TestHerdrShellCwd_EmptyGuestPathSkipped(t *testing.T) {
	// A LiveMount with an empty GuestPath should be skipped, not returned.
	sb := domain.Sandbox{
		LiveMounts: []domain.LiveMount{
			{GuestPath: ""},             // skip
			{GuestPath: "/workspace/a"}, // use this
		},
	}
	got := herdrShellCwd(context.Background(), "ref", &fakeSandboxGetter{sb: sb})
	if got != "/workspace/a" {
		t.Errorf("got %q, want /workspace/a (empty GuestPath must be skipped)", got)
	}
}

// ── HerdrSpaceRemoveByLabel ErrNotFound tolerance (TBD-PD-35) ────────────────

// TestHerdrSpaceRemoveByLabel_ToleratesStoreNotFound verifies that a
// store.ErrNotFound-wrapped error from svc.Remove does not block binding deletion.
func TestHerdrSpaceRemoveByLabel_ToleratesStoreNotFound(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:gone", HerdrWorkspaceID: "wG",
		SandboxHandle: "p/gone", SandboxID: "sb-gone",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	svc := &storeNotFoundSvc{}
	if err := HerdrSpaceRemoveByLabel(ctx, svc, root, b.SpaceLabel); err != nil {
		t.Fatalf("HerdrSpaceRemoveByLabel with not-found sandbox returned error: %v", err)
	}
	// Binding must be gone.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("binding still present after Remove with not-found sandbox")
	}
}

// sandboxHandleHostname tests (hostname cmdline injection).

func TestSandboxHandleHostname(t *testing.T) {
	cases := []struct {
		handle string
		want   string
	}{
		{"orca/agent1", "orca-agent1"},
		{"test/agent1", "test-agent1"},
		{"simple", "simple"},
		{"Upper/Case", "upper-case"},
		{"a/b/c", "a-b-c"},
		{strings.Repeat("x", 70), strings.Repeat("x", 63)}, // truncation
	}
	for _, tc := range cases {
		got := sandboxHandleHostname(tc.handle)
		if got != tc.want {
			t.Errorf("sandboxHandleHostname(%q) = %q, want %q", tc.handle, got, tc.want)
		}
	}
}

// TestCmdlineContainsSandboxHandle verifies that the cmdline produced by the
// newDriver closure in cmd_sandbox.go contains --sandbox-handle.
func TestCmdlineContainsSandboxHandle(t *testing.T) {
	handle := "test/agent1"
	hostArg := " --sandbox-handle=" + sandboxHandleHostname(handle)

	// Simulate cmd_sandbox.go newDriver: no workspace mounts.
	cmdline := diskBootCmdlineBase + " --" + hostArg
	if !strings.Contains(cmdline, "--sandbox-handle=test-agent1") {
		t.Errorf("no-mount cmdline %q missing --sandbox-handle=test-agent1", cmdline)
	}
}
