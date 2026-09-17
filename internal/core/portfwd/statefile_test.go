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
// Live 2026-09-15: forwards.state alternated between [5173,9749] (wG) and
// [] (wH) on successive ticks with three sandboxes running.
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

// TestMerge_DedupesSandboxPort pins the (sandbox, port) union.
func TestMerge_DedupesSandboxPort(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	entries := make([]Entry, 0, 30)
	for port := uint16(3000); port < 3015; port++ {
		entries = append(entries,
			Entry{Port: port, Sandbox: "sb-A", Status: "live", ConfirmedAt: now},
			Entry{Port: port, Sandbox: "sb-A", Status: "live", ConfirmedAt: now})
	}
	if err := WriteSandboxState(dir, "sb-A", entries, now); err != nil {
		t.Fatal(err)
	}
	got := readMerged(t, dir)
	if len(got.Forwards) != 15 {
		t.Fatalf("30 duplicate rows must collapse to 15, got %d: %v", len(got.Forwards), ports(got))
	}
	for i, e := range got.Forwards {
		if e.Port != uint16(3000+i) || e.Sandbox != "sb-A" {
			t.Fatalf("row %d = %+v, want port %d sb-A", i, e, 3000+i)
		}
	}
}

func TestMerge_DedupeKeepsNewestFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := WriteSandboxState(dir, "sb-A", []Entry{{Port: 5173, Sandbox: "sb-A", Status: "pending", ConfirmedAt: now.Add(-10 * time.Second)}}, now.Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := WriteSandboxState(dir, "sb-A-dup", []Entry{{Port: 5173, Sandbox: "sb-A", Status: "error", ConfirmedAt: now, Error: "bind: address in use"}}, now); err != nil {
		t.Fatal(err)
	}
	got := readMerged(t, dir)
	if len(got.Forwards) != 1 || got.Forwards[0].Status != "error" {
		t.Fatalf("want the newest file's row for (sb-A,5173), got %+v", got.Forwards)
	}
	if got.Forwards[0].Error != "bind: address in use" {
		t.Fatalf("Error must round-trip through the merge, got %q", got.Forwards[0].Error)
	}
}

func writeLegacyState(t *testing.T, dir string, st State) {
	t.Helper()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateFileName), data, 0o640); err != nil {
		t.Fatal(err)
	}
}

// TestMerge_CarriesLiveLegacyWriter covers HAN-941 F6: a supervisor that
// predates forwards.d writes forwards.state directly, so its rows exist
// nowhere else and a merge that ignores forwards.state erases them.
func TestMerge_CarriesLiveLegacyWriter(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLegacyState(t, dir, State{WrittenBy: "nexus/sb-old", UpdatedAt: now.Add(-3 * time.Second), Forwards: []Entry{
		{Port: 9749, Sandbox: "sb-old", Status: "live", ConfirmedAt: now.Add(-3 * time.Second)},
	}})
	if err := WriteSandboxState(dir, "sb-new", []Entry{{Port: 5173, Sandbox: "sb-new", Status: "live", ConfirmedAt: now}}, now); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 2 || got[0] != 5173 || got[1] != 9749 {
		t.Fatalf("live legacy writer's port must survive the merge, got %v", got)
	}
	// A second merge reads the merged file (written_by merge) and must still
	// carry the row while its heartbeat is fresh.
	if err := WriteSandboxState(dir, "sb-new", []Entry{{Port: 5173, Sandbox: "sb-new", Status: "live", ConfirmedAt: now}}, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 2 || got[1] != 9749 {
		t.Fatalf("legacy row must survive a re-merge, got %v", got)
	}
}

func TestMerge_DropsDeadLegacyWriter(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLegacyState(t, dir, State{WrittenBy: "nexus/sb-old", UpdatedAt: now.Add(-StaleAfter - time.Second), Forwards: []Entry{
		{Port: 9749, Sandbox: "sb-old", Status: "live", ConfirmedAt: now.Add(-StaleAfter - time.Second)},
		{Port: 9750, Sandbox: "sb-noheartbeat", Status: "live"},
	}})
	if err := WriteSandboxState(dir, "sb-new", []Entry{{Port: 5173, Sandbox: "sb-new", Status: "live", ConfirmedAt: now}}, now); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 1 || got[0] != 5173 {
		t.Fatalf("dead legacy writer's rows must be dropped, got %v", got)
	}
}

// A sandbox that owns a forwards.d file is never resurrected from
// forwards.state: graceful teardown must remove its port immediately even
// though the previous merge holds a fresh heartbeat for it.
func TestRemoveSandboxState_NotResurrectedFromMerged(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	_ = WriteSandboxState(dir, "sb-A", []Entry{{Port: 5173, Sandbox: "sb-A", Status: "live", ConfirmedAt: now}}, now)
	_ = WriteSandboxState(dir, "sb-B", []Entry{{Port: 8080, Sandbox: "sb-B", Status: "live", ConfirmedAt: now}}, now)
	if err := RemoveSandboxState(dir, "sb-A", now); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 1 || got[0] != 8080 {
		t.Fatalf("removed sandbox resurrected from forwards.state: %v", got)
	}
	if err := WriteSandboxState(dir, "sb-B", []Entry{{Port: 8080, Sandbox: "sb-B", Status: "live", ConfirmedAt: now}}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := ports(readMerged(t, dir)); len(got) != 1 || got[0] != 8080 {
		t.Fatalf("removed sandbox resurrected on the following merge: %v", got)
	}
}

func TestWriteSandboxState_EmptyIDRefused(t *testing.T) {
	if err := WriteSandboxState(t.TempDir(), "", nil, time.Now()); err == nil {
		t.Fatal("empty sandbox id must be refused, not written as a shared file")
	}
}
