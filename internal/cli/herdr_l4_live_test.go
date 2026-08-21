//go:build herdr_live

// Package cli — Layer 4 of the herdr+nexus3 test strategy.
//
// This file requires a live herdr session and a nexus3 binary build.
// It is excluded from the default test run and from CI; it must be
// explicitly tagged to execute.
//
// Run with:
//
//	TMPDIR=/tmp go test -count=1 -tags herdr_live ./internal/cli/ -run TestHerdrPlugin_L4
//
// Prerequisites:
//   - herdr must be running (herdr workspace list must succeed)
//   - The test creates and closes its own scratch workspace.
//     It never touches the operator's existing workspaces.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHerdrPlugin_L4_BinaryVerb is Layer 4 of the herdr+nexus3 test strategy.
//
// It builds the nexus3 binary, then asserts in two ways:
//
//  1. Direct exec — the binary is called with `__herdr-plugin abi` as a
//     subprocess and stdout is checked for the exact ABI string "1". This is
//     the mutation-sensitive primary assertion: layers 1–3 all call
//     runHerdrPlugin in-process; none would catch a deleted or renamed
//     `init()`-level Command{Name: "__herdr-plugin", ...} registration.
//
//  2. herdr pane smoke test — the same binary is run inside a real herdr pane
//     via `herdr pane run` so the operator can see the round-trip works end-
//     to-end. pane output is logged but not used as an assertion (pane noise
//     makes substring matching unreliable).
//
// # Mutation guide
//
// To verify Layer 4 is the only layer that catches a missing verb:
//
//	sed -i 's/__herdr-plugin"/__herdr-plugin-MUTATED"/' internal/cli/cmd_herdr_plugin.go
//	TMPDIR=/tmp go build -o /tmp/nexus3-mutated ./cmd/nexus3
//	/tmp/nexus3-mutated __herdr-plugin abi  # exits 2: "unknown command"
//	go test ./internal/cli/ -count=1        # L1/L2/L3: all green
//	TMPDIR=/tmp go test -count=1 -tags herdr_live ./internal/cli/ -run TestHerdrPlugin_L4
//	# L4: FAIL — want "1", binary exited non-zero
func TestHerdrPlugin_L4_BinaryVerb(t *testing.T) {
	// Safety: record before-state so the operator can confirm their
	// workspaces are untouched after the run.
	beforeWorkspaces := herdrWorkspaceList(t)
	t.Logf("BEFORE: %s", beforeWorkspaces)
	if !strings.Contains(beforeWorkspaces, "workspace_list") {
		t.Fatalf("herdr workspace list did not return a workspace_list — is herdr running?")
	}

	// --- 1. Build the binary. ---
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-l4")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// --- 2. Primary assertion: exec the binary directly. ---
	//
	// `herdr pane wait-output` cannot distinguish "1" from shell-prompt
	// noise, so we exec the binary as a subprocess and check stdout
	// directly. This is both deterministic and mutation-sensitive.
	abiCmd := exec.Command(binary, "__herdr-plugin", "abi")
	abiOut, abiErr := abiCmd.Output()
	if abiErr != nil {
		// On mutation (verb renamed or deleted), the binary exits 2 with
		// "error: unknown command: __herdr-plugin". Report the stderr so
		// the operator can tell whether the verb registration was lost.
		var stderr []byte
		if ee, ok := abiErr.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("nexus3 __herdr-plugin abi: exit %v\nstderr: %s", abiErr, stderr)
	}
	got := strings.TrimSpace(string(abiOut))
	if got != "1" {
		t.Errorf("nexus3 __herdr-plugin abi: want %q, got %q", "1", got)
	}
	t.Logf("nexus3 __herdr-plugin abi stdout: %q", got)

	// --- 3. herdr pane smoke test. ---
	//
	// Run the same binary inside a real herdr workspace pane. This confirms
	// the binary works in herdr's execution context. The pane output is
	// logged; the assertion is wait-output success, which confirms "1"
	// appeared (even if other content also appeared before the shell reset).
	label := fmt.Sprintf("nexus3-l4-probe-%d", time.Now().UnixMilli())
	// Register cleanup by label BEFORE createL4ScratchWorkspace so that a
	// t.Fatal inside the helper (e.g. at JSON parse, after herdr has already
	// created the workspace) still triggers cleanup. findL4WorkspaceIDByLabel
	// re-resolves the workspace from the live list; if the workspace was never
	// created or is already gone, it returns "" and closeL4ScratchWorkspace
	// returns early without touching anything.
	t.Cleanup(func() {
		id := findL4WorkspaceIDByLabel(t, label)
		closeL4ScratchWorkspace(t, id, label)
		// Assert the cleanup actually happened.  Without this the closure can
		// silently no-op (as it did when the list response was decoded with the
		// wrong JSON field tag) and the test still passes while leaking a live
		// workspace into the operator's session.
		afterWorkspaces := herdrWorkspaceList(t)
		// Guard: if the list command itself failed (herdr died, empty output),
		// the absence of the label is meaningless — the workspace may be
		// stranded and invisible. Fail loudly so the operator knows to check.
		if !strings.Contains(afterWorkspaces, "workspace_list") {
			t.Errorf("herdr workspace list after cleanup did not return a workspace_list (got %q) — leak check inconclusive; check manually", afterWorkspaces)
		} else {
			// Deliberately a raw-substring check on the list output rather than a
			// call to findL4WorkspaceIDByLabel: this assertion must not share a
			// decoding mechanism with the resolver it is checking.  An earlier
			// version used the resolver, and when the resolver decoded the list
			// with the wrong JSON field tag both the close AND the leak check
			// silently no-opped, so the test passed while leaking a workspace.
			if strings.Contains(afterWorkspaces, label) {
				t.Errorf("scratch workspace (label %q) survived cleanup; list: %s", label, afterWorkspaces)
			}
		}
		t.Logf("AFTER: %s", afterWorkspaces)
	})
	_, paneID := createL4ScratchWorkspace(t, label)

	runOut, err := exec.Command("herdr", "pane", "run", paneID, binary, "__herdr-plugin", "abi").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr pane run: %v\n%s", err, runOut)
	}

	// wait-output confirms the pane produced "1" before the subprocess
	// exited. Shell-prompt noise can also contain "1" (e.g. prompt with
	// line numbers), but here we are using this only as a smoke test that
	// herdr's pane machinery ran the binary — the direct-exec assertion
	// above is the real gate.
	waitOut, err := exec.Command(
		"herdr", "pane", "wait-output",
		paneID,
		"--match", "1",
		"--source", "recent",
		"--timeout", "10000",
	).CombinedOutput()
	if err != nil {
		readOut, _ := exec.Command("herdr", "pane", "read", paneID, "--source", "visible", "--lines", "10").CombinedOutput()
		t.Logf("herdr pane wait-output did not find %q within 10 s: %v\n%s\npane (visible):\n%s",
			"1", err, waitOut, readOut)
		// Non-fatal: the primary assertion (direct exec) already passed.
	} else {
		readOut, _ := exec.Command("herdr", "pane", "read", paneID, "--source", "visible", "--lines", "5").CombinedOutput()
		t.Logf("pane smoke test passed; visible: %s", strings.TrimSpace(string(readOut)))
	}
}

// herdrWorkspaceList runs `herdr workspace list` and returns the raw JSON.
// Never fails the test — used for before/after safety logging only.
func herdrWorkspaceList(t *testing.T) string {
	t.Helper()
	out, _ := exec.Command("herdr", "workspace", "list").CombinedOutput()
	return string(out)
}

// findL4WorkspaceIDByLabel scans `herdr workspace list` for a workspace whose
// label matches exactly and returns its workspace_id, or "" if not found or on
// any error. Never fails the test — used by the pre-create cleanup closure so
// that a fatal inside createL4ScratchWorkspace still lets cleanup close the
// workspace by label even though wsID was never returned.
func findL4WorkspaceIDByLabel(t *testing.T, label string) string {
	t.Helper()
	out, err := exec.Command("herdr", "workspace", "list").CombinedOutput()
	if err != nil {
		t.Logf("findL4WorkspaceIDByLabel: workspace list: %v", err)
		return ""
	}
	var resp struct {
		Result struct {
			WorkspaceList []struct {
				WorkspaceID string `json:"workspace_id"`
				Label       string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Logf("findL4WorkspaceIDByLabel: parse: %v", err)
		return ""
	}
	for _, ws := range resp.Result.WorkspaceList {
		if ws.Label == label {
			return ws.WorkspaceID
		}
	}
	return ""
}

// createL4ScratchWorkspace creates a herdr workspace (--no-focus) and returns
// the workspace ID and root pane ID from the JSON response.
func createL4ScratchWorkspace(t *testing.T, label string) (wsID, paneID string) {
	t.Helper()
	out, err := exec.Command(
		"herdr", "workspace", "create",
		"--label", label,
		"--no-focus",
		"--cwd", os.TempDir(),
	).CombinedOutput()
	if err != nil {
		t.Fatalf("herdr workspace create: %v\n%s", err, out)
	}

	var resp struct {
		Result struct {
			Workspace struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspace"`
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("parse workspace create response: %v\nraw: %s", err, out)
	}
	wsID = resp.Result.Workspace.WorkspaceID
	paneID = resp.Result.RootPane.PaneID
	if wsID == "" || paneID == "" {
		t.Fatalf("workspace create returned empty ids; raw: %s", out)
	}
	t.Logf("created scratch workspace %s (pane %s) label=%q", wsID, paneID, label)
	return wsID, paneID
}

// closeL4ScratchWorkspace re-resolves the workspace by ID, asserts the label
// matches what we created, then closes it. Refuses to close and fails the
// test if the label does not match — prevents closing a workspace we did not
// create. Safe to call multiple times (idempotent).
func closeL4ScratchWorkspace(t *testing.T, wsID, expectedLabel string) {
	t.Helper()
	if wsID == "" {
		return
	}

	getOut, err := exec.Command("herdr", "workspace", "get", wsID).CombinedOutput()
	if err != nil {
		// Workspace may already be gone — log and return.
		t.Logf("workspace get %s: %v (may already be closed)", wsID, err)
		return
	}

	var resp struct {
		Result struct {
			Workspace struct {
				Label string `json:"label"`
			} `json:"workspace"`
		} `json:"result"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(getOut, &resp); err != nil {
		t.Logf("parse workspace get response: %v\nraw: %s — skipping close", err, getOut)
		return
	}
	if resp.Error != nil && resp.Error.Code == "workspace_not_found" {
		return // already gone
	}
	actualLabel := resp.Result.Workspace.Label
	if actualLabel != expectedLabel {
		t.Errorf("SAFETY ABORT: workspace %s has label %q, expected %q — refusing to close",
			wsID, actualLabel, expectedLabel)
		return
	}

	closeOut, err := exec.Command("herdr", "workspace", "close", wsID).CombinedOutput()
	if err != nil {
		t.Logf("herdr workspace close %s: %v\n%s", wsID, err, closeOut)
		return
	}
	t.Logf("closed scratch workspace %s", wsID)
}
