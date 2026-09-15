package portfwd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readMerged(t *testing.T, dir string) State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatalf("read merged: %v", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	return st
}

func ports(st State) []uint16 {
	out := make([]uint16, 0, len(st.Forwards))
	for _, e := range st.Forwards {
		out = append(out, e.Port)
	}
	return out
}

// TestWriteSandboxState_TwoSandboxesDoNotClobber pins the merge: a second
// sandbox writing an empty list must not erase the first sandbox's ports.
/** Live 2026-09-15: forwards.state alternated between [5173,9749] (wG) and
[] (wH) on successive ticks with three sandboxes running. */
func TestWriteSandboxState_TwoSandboxesDoNotClobber(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := WriteSandboxState(dir, "sb-A", []Entry{{Port: 5173, Sandbox: "sb-A", Status: "live"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := WriteSandboxState(dir, "sb-B", nil, now); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 1 || got[0] != 5173 {
		t.Fatalf("sb-B's empty write clobbered sb-A: merged ports = %v", got)
	}
	if err := WriteSandboxState(dir, "sb-B", []Entry{{Port: 8080, Sandbox: "sb-B", Status: "live"}}, now); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 2 || got[0] != 5173 || got[1] != 8080 {
		t.Fatalf("merge must hold both sandboxes' ports sorted, got %v", got)
	}
}

func TestRemoveSandboxState_DropsOnlyThatSandbox(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	_ = WriteSandboxState(dir, "sb-A", []Entry{{Port: 5173, Sandbox: "sb-A"}}, now)
	_ = WriteSandboxState(dir, "sb-B", []Entry{{Port: 8080, Sandbox: "sb-B"}}, now)
	if err := RemoveSandboxState(dir, "sb-A", now); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 1 || got[0] != 8080 {
		t.Fatalf("after removing sb-A want [8080], got %v", got)
	}
	// Removing an unknown sandbox is not an error and leaves the merge intact.
	if err := RemoveSandboxState(dir, "sb-never", now); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 1 {
		t.Fatalf("unknown remove altered merge: %v", got)
	}
}

// TestMerge_DropsStaleWriter covers the SIGKILLed supervisor that never ran
// teardown: its file goes stale and its ports leave the merge.
func TestMerge_DropsStaleWriter(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	_ = WriteSandboxState(dir, "sb-dead", []Entry{{Port: 5173, Sandbox: "sb-dead"}}, now.Add(-StaleAfter-time.Second))
	_ = WriteSandboxState(dir, "sb-live", []Entry{{Port: 8080, Sandbox: "sb-live"}}, now)
	if got := ports(readMerged(t, dir)); len(got) != 1 || got[0] != 8080 {
		t.Fatalf("stale writer must be dropped, got %v", got)
	}
}

func TestWriteSandboxState_EmptyIDRefused(t *testing.T) {
	if err := WriteSandboxState(t.TempDir(), "", nil, time.Now()); err == nil {
		t.Fatal("empty sandbox id must be refused, not written as a shared file")
	}
}
