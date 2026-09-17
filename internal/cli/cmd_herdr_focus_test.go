package cli

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/portfwd"
)

func seedFocusBinding(t *testing.T, storeRoot, workspaceID, sandboxID string) {
	t.Helper()
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus:" + sandboxID,
		HerdrWorkspaceID: workspaceID,
		SandboxHandle:    "test/" + sandboxID,
		SandboxID:        sandboxID,
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, b); err != nil {
		t.Fatalf("seedFocusBinding: %v", err)
	}
}

func TestHerdrFocusChanged_Bound_WritesFocusState(t *testing.T) {
	dir := t.TempDir()
	storeRoot := filepath.Join(dir, "store")
	statePath := filepath.Join(dir, "focus.state")

	seedFocusBinding(t, storeRoot, "wX", "sb1")

	if err := herdrFocusChanged(context.Background(), "wX", false, storeRoot, statePath, t.TempDir(), "sess1", "", io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s, ok, err := portfwd.ReadFocusState(statePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !ok {
		t.Fatal("focus.state not written")
	}
	if s.WorkspaceID != "wX" {
		t.Errorf("workspace_id: got %q, want %q", s.WorkspaceID, "wX")
	}
	if s.SandboxID != "sb1" {
		t.Errorf("sandbox_id: got %q, want %q", s.SandboxID, "sb1")
	}
	if s.Session != "sess1" {
		t.Errorf("session: got %q, want %q", s.Session, "sess1")
	}
	if s.UpdatedAt.IsZero() {
		t.Error("updated_at is zero")
	}
}

func TestHerdrFocusChanged_Unbound_WritesSandboxIDEmpty(t *testing.T) {
	dir := t.TempDir()
	storeRoot := filepath.Join(dir, "store")
	statePath := filepath.Join(dir, "focus.state")

	if err := herdrFocusChanged(context.Background(), "wUnbound", false, storeRoot, statePath, t.TempDir(), "", "", io.Discard); err != nil {
		t.Fatalf("unexpected error on unbound: %v", err)
	}

	s, ok, err := portfwd.ReadFocusState(statePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !ok {
		t.Fatal("focus.state not written for unbound workspace")
	}
	if s.WorkspaceID != "wUnbound" {
		t.Errorf("workspace_id: got %q, want %q", s.WorkspaceID, "wUnbound")
	}
	if s.SandboxID != "" {
		t.Errorf("sandbox_id: got %q, want empty for unbound", s.SandboxID)
	}
}

func TestHerdrFocusChanged_OnlyIfFocused_NoOps(t *testing.T) {
	dir := t.TempDir()
	storeRoot := filepath.Join(dir, "store")
	statePath := filepath.Join(dir, "focus.state")

	initial := portfwd.FocusState{
		WorkspaceID: "wOther",
		SandboxID:   "sb-other",
		Session:     "",
		UpdatedAt:   time.Now().UTC(),
	}
	if err := portfwd.WriteFocusState(statePath, initial); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	seedFocusBinding(t, storeRoot, "wX", "sb1")

	if err := herdrFocusChanged(context.Background(), "wX", true, storeRoot, statePath, t.TempDir(), "", "", io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s, ok, err := portfwd.ReadFocusState(statePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !ok {
		t.Fatal("focus.state disappeared")
	}
	if s.WorkspaceID != "wOther" {
		t.Errorf("workspace_id changed: got %q, want %q", s.WorkspaceID, "wOther")
	}
	if s.SandboxID != "sb-other" {
		t.Errorf("sandbox_id changed: got %q, want %q", s.SandboxID, "sb-other")
	}
}

func TestHerdrFocusChanged_OnlyIfFocused_WritesWhenMatched(t *testing.T) {
	dir := t.TempDir()
	storeRoot := filepath.Join(dir, "store")
	statePath := filepath.Join(dir, "focus.state")

	initial := portfwd.FocusState{WorkspaceID: "wX", SandboxID: "sb-old", UpdatedAt: time.Now().UTC()}
	if err := portfwd.WriteFocusState(statePath, initial); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	seedFocusBinding(t, storeRoot, "wX", "sb1")

	if err := herdrFocusChanged(context.Background(), "wX", true, storeRoot, statePath, t.TempDir(), "", "", io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s, _, err := portfwd.ReadFocusState(statePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if s.SandboxID != "sb1" {
		t.Errorf("sandbox_id: got %q, want %q", s.SandboxID, "sb1")
	}
}
