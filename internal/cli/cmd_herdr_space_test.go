package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

func TestHerdrSpacePutGetListDelete(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b1 := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:demo-orca-01",
		HerdrWorkspaceID: "wB",
		SandboxHandle:    "orca/demo-orca-01",
		SandboxID:        "sb-aaa",
	}
	b2 := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:demo-orca-02",
		HerdrWorkspaceID: "wC",
		SandboxHandle:    "orca/demo-orca-02",
		SandboxID:        "sb-bbb",
	}

	// Put two bindings.
	if err := HerdrSpacePut(ctx, root, b1); err != nil {
		t.Fatalf("Put b1: %v", err)
	}
	if err := HerdrSpacePut(ctx, root, b2); err != nil {
		t.Fatalf("Put b2: %v", err)
	}

	// GetByLabel.
	got, err := HerdrSpaceGetByLabel(ctx, root, b1.SpaceLabel)
	if err != nil {
		t.Fatalf("GetByLabel: %v", err)
	}
	if got != b1 {
		t.Errorf("GetByLabel got %+v, want %+v", got, b1)
	}

	// GetByHandle.
	got2, err := HerdrSpaceGetByHandle(ctx, root, b2.SandboxHandle)
	if err != nil {
		t.Fatalf("GetByHandle: %v", err)
	}
	if got2 != b2 {
		t.Errorf("GetByHandle got %+v, want %+v", got2, b2)
	}

	// List.
	all, err := HerdrSpaceList(ctx, root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List len=%d, want 2", len(all))
	}

	// Delete b1.
	if err := HerdrSpaceDelete(ctx, root, b1.SpaceLabel); err != nil {
		t.Fatalf("Delete b1: %v", err)
	}
	all, err = HerdrSpaceList(ctx, root)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List after delete len=%d, want 1", len(all))
	}

	// b1 not found; b2 still there.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b1.SpaceLabel); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("expected ErrHerdrSpaceNotFound, got %v", err)
	}
	if _, err := HerdrSpaceGetByLabel(ctx, root, b2.SpaceLabel); err != nil {
		t.Errorf("b2 not found after delete: %v", err)
	}
}

func TestHerdrSpacePutNotFoundErrors(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	if _, err := HerdrSpaceGetByLabel(ctx, root, "nexus3:nope"); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("GetByLabel on empty: want ErrHerdrSpaceNotFound, got %v", err)
	}
	if _, err := HerdrSpaceGetByHandle(ctx, root, "orca/nope"); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("GetByHandle on empty: want ErrHerdrSpaceNotFound, got %v", err)
	}
	if err := HerdrSpaceDelete(ctx, root, "nexus3:nope"); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("Delete on empty: want ErrHerdrSpaceNotFound, got %v", err)
	}
}

func TestHerdrSpacePut1to1InvariantLabel(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b1 := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:alpha",
		HerdrWorkspaceID: "wA",
		SandboxHandle:    "orca/alpha",
		SandboxID:        "sb-111",
	}
	if err := HerdrSpacePut(ctx, root, b1); err != nil {
		t.Fatalf("Put b1: %v", err)
	}

	// Re-bind the same label to a different sandbox — old entry must be replaced.
	b2 := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:alpha",
		HerdrWorkspaceID: "wB",
		SandboxHandle:    "orca/beta",
		SandboxID:        "sb-222",
	}
	if err := HerdrSpacePut(ctx, root, b2); err != nil {
		t.Fatalf("Put b2 (same label): %v", err)
	}

	all, _ := HerdrSpaceList(ctx, root)
	if len(all) != 1 {
		t.Fatalf("1:1 label invariant: len=%d, want 1", len(all))
	}
	if all[0] != b2 {
		t.Errorf("1:1 label: got %+v, want %+v", all[0], b2)
	}
}

func TestHerdrSpacePut1to1InvariantHandle(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b1 := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:alpha",
		HerdrWorkspaceID: "wA",
		SandboxHandle:    "orca/alpha",
		SandboxID:        "sb-111",
	}
	if err := HerdrSpacePut(ctx, root, b1); err != nil {
		t.Fatalf("Put b1: %v", err)
	}

	// Re-bind the same sandbox handle under a different label.
	b2 := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:gamma",
		HerdrWorkspaceID: "wC",
		SandboxHandle:    "orca/alpha", // same handle
		SandboxID:        "sb-111",
	}
	if err := HerdrSpacePut(ctx, root, b2); err != nil {
		t.Fatalf("Put b2 (same handle): %v", err)
	}

	all, _ := HerdrSpaceList(ctx, root)
	if len(all) != 1 {
		t.Fatalf("1:1 handle invariant: len=%d, want 1", len(all))
	}
	if all[0] != b2 {
		t.Errorf("1:1 handle: got %+v, want %+v", all[0], b2)
	}
}

// ── lifecycle fake ────────────────────────────────────────────────────────────

// fakeSandboxSvc is a test double for HerdrSpaceSandboxService.
type fakeSandboxSvc struct {
	paused  []string
	resumed []string
	removed []string
	pauseErr  error
	resumeErr error
	removeErr error
}

func (f *fakeSandboxSvc) Pause(_ context.Context, ref string) (domain.Sandbox, error) {
	if f.pauseErr != nil {
		return domain.Sandbox{}, f.pauseErr
	}
	f.paused = append(f.paused, ref)
	return domain.Sandbox{}, nil
}

func (f *fakeSandboxSvc) Resume(_ context.Context, ref string) (domain.Sandbox, error) {
	if f.resumeErr != nil {
		return domain.Sandbox{}, f.resumeErr
	}
	f.resumed = append(f.resumed, ref)
	return domain.Sandbox{}, nil
}

func (f *fakeSandboxSvc) Remove(_ context.Context, ref string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, ref)
	return nil
}

// ── lifecycle tests ───────────────────────────────────────────────────────────

func TestHerdrSpacePauseByLabel(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wX",
		SandboxHandle: "orca/demo", SandboxID: "sb-xxx",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	svc := &fakeSandboxSvc{}
	if err := HerdrSpacePauseByLabel(ctx, svc, root, b.SpaceLabel); err != nil {
		t.Fatalf("PauseByLabel: %v", err)
	}
	if len(svc.paused) != 1 || svc.paused[0] != b.SandboxHandle {
		t.Errorf("paused refs = %v, want [%s]", svc.paused, b.SandboxHandle)
	}
}

func TestHerdrSpacePauseByLabel_NotFound(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	svc := &fakeSandboxSvc{}
	err := HerdrSpacePauseByLabel(ctx, svc, root, "nexus3:nope")
	if !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("want ErrHerdrSpaceNotFound, got %v", err)
	}
	if len(svc.paused) != 0 {
		t.Errorf("service must not be called for missing label")
	}
}

func TestHerdrSpaceResumeByLabel(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wX",
		SandboxHandle: "orca/demo", SandboxID: "sb-xxx",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	svc := &fakeSandboxSvc{}
	if err := HerdrSpaceResumeByLabel(ctx, svc, root, b.SpaceLabel); err != nil {
		t.Fatalf("ResumeByLabel: %v", err)
	}
	if len(svc.resumed) != 1 || svc.resumed[0] != b.SandboxHandle {
		t.Errorf("resumed refs = %v, want [%s]", svc.resumed, b.SandboxHandle)
	}
}

func TestHerdrSpaceResumeByLabel_NotFound(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	svc := &fakeSandboxSvc{}
	err := HerdrSpaceResumeByLabel(ctx, svc, root, "nexus3:nope")
	if !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("want ErrHerdrSpaceNotFound, got %v", err)
	}
	if len(svc.resumed) != 0 {
		t.Errorf("service must not be called for missing label")
	}
}

func TestHerdrSpaceRemoveByLabel(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wX",
		SandboxHandle: "orca/demo", SandboxID: "sb-xxx",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	svc := &fakeSandboxSvc{}
	if err := HerdrSpaceRemoveByLabel(ctx, svc, root, b.SpaceLabel); err != nil {
		t.Fatalf("RemoveByLabel: %v", err)
	}
	// Sandbox removed with the correct handle.
	if len(svc.removed) != 1 || svc.removed[0] != b.SandboxHandle {
		t.Errorf("removed refs = %v, want [%s]", svc.removed, b.SandboxHandle)
	}
	// Binding deleted — subsequent lookup must return ErrHerdrSpaceNotFound.
	if _, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("binding still present after Remove; want ErrHerdrSpaceNotFound, got %v", err)
	}
}

func TestHerdrSpaceRemoveByLabel_NotFound(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	svc := &fakeSandboxSvc{}
	err := HerdrSpaceRemoveByLabel(ctx, svc, root, "nexus3:nope")
	if !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("want ErrHerdrSpaceNotFound, got %v", err)
	}
	if len(svc.removed) != 0 {
		t.Errorf("service must not be called for missing label")
	}
}

func TestHerdrSpaceRemoveByLabel_ServiceError(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:demo", HerdrWorkspaceID: "wX",
		SandboxHandle: "orca/demo", SandboxID: "sb-xxx",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	svcErr := fmt.Errorf("driver offline")
	svc := &fakeSandboxSvc{removeErr: svcErr}
	err := HerdrSpaceRemoveByLabel(ctx, svc, root, b.SpaceLabel)
	if err == nil {
		t.Fatal("expected error from service, got nil")
	}
	// Binding must still exist when the service call fails.
	if _, err2 := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); err2 != nil {
		t.Errorf("binding must remain after failed Remove, got %v", err2)
	}
}
