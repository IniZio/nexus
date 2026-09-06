package repro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeNexus3SandboxList writes a shell script that prints the given JSON
// and returns its path. The script acts as a fake nexus3 binary.
func fakeNexus3SandboxList(t *testing.T, sandboxes []struct {
	Handle string
	State  string
}) string {
	t.Helper()

	type sandboxJSON struct {
		Handle string `json:"handle"`
		State  string `json:"state"`
	}
	type dataJSON struct {
		Sandboxes []sandboxJSON `json:"sandboxes"`
	}
	type rootJSON struct {
		SchemaVersion int      `json:"schema_version"`
		Kind          string   `json:"kind"`
		Data          dataJSON `json:"data"`
	}

	sbJSON := make([]sandboxJSON, len(sandboxes))
	for i, s := range sandboxes {
		sbJSON[i] = sandboxJSON{Handle: s.Handle, State: s.State}
	}
	payload, err := json.Marshal(rootJSON{
		SchemaVersion: 1,
		Kind:          "sandbox.list",
		Data:          dataJSON{Sandboxes: sbJSON},
	})
	if err != nil {
		t.Fatalf("marshal fake payload: %v", err)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "nexus3")
	content := fmt.Sprintf("#!/bin/sh\necho '%s'\n", string(payload))
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatalf("write fake nexus3: %v", err)
	}
	return script
}

// TestCheckBuilderBusy_Empty verifies that an empty sandbox list reports not busy.
func TestCheckBuilderBusy_Empty(t *testing.T) {
	nx := fakeNexus3SandboxList(t, nil)
	busy, reason, err := checkBuilderBusy(nx, map[string]struct{}{"repro/baseline": {}})
	if err != nil {
		t.Fatalf("checkBuilderBusy: %v", err)
	}
	if busy {
		t.Errorf("expected not busy with empty list, got busy (reason=%s)", reason)
	}
}

// TestCheckBuilderBusy_OtherProject verifies non-repro sandboxes are ignored.
func TestCheckBuilderBusy_OtherProject(t *testing.T) {
	nx := fakeNexus3SandboxList(t, []struct{ Handle, State string }{
		{"other-project/sandbox-1", "running"},
		{"example-app/dev", "running"},
	})
	busy, reason, err := checkBuilderBusy(nx, map[string]struct{}{"repro/baseline": {}})
	if err != nil {
		t.Fatalf("checkBuilderBusy: %v", err)
	}
	if busy {
		t.Errorf("non-repro sandboxes should not block (reason=%s)", reason)
	}
}

// TestCheckBuilderBusy_ReprosDetected verifies a repro/* sandbox (not ours) is flagged.
func TestCheckBuilderBusy_ReprosDetected(t *testing.T) {
	nx := fakeNexus3SandboxList(t, []struct{ Handle, State string }{
		{"repro/hostmem-3072-0", "running"},
	})
	busy, reason, err := checkBuilderBusy(nx, map[string]struct{}{"repro/baseline": {}})
	if err != nil {
		t.Fatalf("checkBuilderBusy: %v", err)
	}
	if !busy {
		t.Error("expected busy when another repro/* sandbox is running")
	}
	if reason == "" {
		t.Error("expected non-empty reason when busy")
	}
}

// TestCheckBuilderBusy_OwnHandle verifies our own sandbox handle is not flagged.
func TestCheckBuilderBusy_OwnHandle(t *testing.T) {
	nx := fakeNexus3SandboxList(t, []struct{ Handle, State string }{
		{"repro/baseline", "running"},
	})
	busy, _, err := checkBuilderBusy(nx, map[string]struct{}{"repro/baseline": {}})
	if err != nil {
		t.Fatalf("checkBuilderBusy: %v", err)
	}
	if busy {
		t.Error("own sandbox handle must not block itself")
	}
}

// TestWaitForBuilderFree_InvokedAndReturns verifies that waitForBuilderFree
// is invoked and returns immediately when no other repro/* sandbox exists.
// It uses a fake nexus3 binary with an empty sandbox list so the test is
// hermetic (does not depend on the host's live nexus3 state).
func TestWaitForBuilderFree_InvokedAndReturns(t *testing.T) {
	nx := fakeNexus3SandboxList(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	hif := waitForBuilderFree(ctx, nx, map[string]struct{}{"repro/baseline": {}})
	elapsed := time.Since(start)

	if hif != nil {
		t.Errorf("expected nil HIF (not busy), got: %s %s", hif.Probe, hif.Detail)
	}
	// Should return almost immediately (< 2s), not wait the full 15 min.
	if elapsed > 2*time.Second {
		t.Errorf("waitForBuilderFree took too long: %v", elapsed)
	}
}

// TestWaitForBuilderFree_BlocksAndHIF verifies that when the builder is
// busy, waitForBuilderFree waits and then returns a HIF after the deadline.
// Uses a very short deadline to keep the test fast.
func TestWaitForBuilderFree_BlocksAndHIF(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode (involves a small sleep)")
	}

	// Fake nexus3 that always reports a busy repro/* sandbox.
	nx := fakeNexus3SandboxList(t, []struct{ Handle, State string }{
		{"repro/hostmem-busy", "running"},
	})

	// Context that expires after 2 s — far less than the 15-min harness deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	hif := waitForBuilderFree(ctx, nx, map[string]struct{}{"repro/baseline": {}})
	if hif == nil {
		t.Error("expected HIF when context expires while builder is busy")
	} else if hif.Probe != "precondition.builder_busy" {
		t.Errorf("expected probe precondition.builder_busy, got %s", hif.Probe)
	}
}

// TestCheckBuilderBusy_AllowedSet verifies that handles in the allowed set are
// not flagged as busy, even when they appear in the sandbox list as running.
//
// MUTATION TARGET: if the allowed-set check is removed (reverted to a single
// ownHandle comparison), this test fails because "repro/conc-0" is not the
// ownHandle "repro/conc-1" and would be flagged as busy.
func TestCheckBuilderBusy_AllowedSet(t *testing.T) {
	// Fake nexus3 listing repro/conc-0 as running (a peer in the same wave).
	nx := fakeNexus3SandboxList(t, []struct{ Handle, State string }{
		{"repro/conc-0", "running"},
	})

	// The allowed set contains all three concurrent handles.
	// checkBuilderBusy must NOT flag conc-0 as busy — it's in the set.
	allowed := map[string]struct{}{
		"repro/conc-0": {},
		"repro/conc-1": {},
		"repro/conc-2": {},
	}
	busy, reason, err := checkBuilderBusy(nx, allowed)
	if err != nil {
		t.Fatalf("checkBuilderBusy: %v", err)
	}
	if busy {
		t.Errorf("repro/conc-0 is in the allowed set; must not be flagged as busy (reason=%s)", reason)
	}
}

// TestCheckBuilderBusy_AllowedSet_StillBlocksOthers verifies that the guard is
// not weakened for handles outside the allowed set.
func TestCheckBuilderBusy_AllowedSet_StillBlocksOthers(t *testing.T) {
	// List: conc-0 (allowed) AND hostmem-3072-0 (NOT in allowed set).
	nx := fakeNexus3SandboxList(t, []struct{ Handle, State string }{
		{"repro/conc-0", "running"},
		{"repro/hostmem-3072-0", "running"},
	})

	allowed := map[string]struct{}{
		"repro/conc-0": {},
		"repro/conc-1": {},
		"repro/conc-2": {},
	}
	busy, _, err := checkBuilderBusy(nx, allowed)
	if err != nil {
		t.Fatalf("checkBuilderBusy: %v", err)
	}
	if !busy {
		t.Error("repro/hostmem-3072-0 is NOT in the allowed set; must be flagged as busy")
	}
}
