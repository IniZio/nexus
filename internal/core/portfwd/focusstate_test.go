package portfwd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/portfwd"
)

func TestFocusStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "focus.state")

	want := portfwd.FocusState{
		WorkspaceID: "ws-123",
		SandboxID:   "sb-456",
		Session:     "my-session",
		UpdatedAt:   time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
	}
	if err := portfwd.WriteFocusState(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok, err := portfwd.ReadFocusState(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.WorkspaceID != want.WorkspaceID || got.SandboxID != want.SandboxID || got.Session != want.Session {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("UpdatedAt: got %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
}

func TestFocusStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, ok, err := portfwd.ReadFocusState(filepath.Join(dir, "no-such.state"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for missing file")
	}
}

func TestFocusStateAtomicNoLeftoverTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "focus.state")
	s := portfwd.FocusState{WorkspaceID: "w1", SandboxID: "s1", Session: "", UpdatedAt: time.Now().UTC()}
	if err := portfwd.WriteFocusState(path, s); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "focus.state" {
			t.Errorf("unexpected file after write: %s", e.Name())
		}
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		t.Errorf("parent dir mode %o missing 0700 bits", info.Mode().Perm())
	}
}

func TestFocusStateParseBytes(t *testing.T) {
	s := portfwd.FocusState{WorkspaceID: "w2", SandboxID: "s2", Session: "sess", UpdatedAt: time.Now().UTC().Truncate(time.Second)}
	b, _ := json.Marshal(s)
	got, err := portfwd.ParseFocusState(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.WorkspaceID != s.WorkspaceID || got.Session != s.Session {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestFocusStateMalformed(t *testing.T) {
	_, _, err := portfwd.ReadFocusState("/dev/null")
	if err == nil {
		t.Fatal("expected error for empty/malformed file")
	}
}
