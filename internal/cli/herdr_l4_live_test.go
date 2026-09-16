//go:build herdr_live

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
//     subprocess and stdout is checked for the ABI string declared in plugins/herdr/abi. This is
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
//	# L4: FAIL — want current ABI from plugins/herdr/abi, binary exited non-zero
func TestHerdrPlugin_L4_BinaryVerb(t *testing.T) {
	probeHome, _ := startIsolatedHerdr(t)
	_ = probeHome

	beforeWorkspaces := herdrWorkspaceList(t)
	t.Logf("BEFORE: %s", beforeWorkspaces)
	if !strings.Contains(beforeWorkspaces, "workspace_list") {
		liveSkip(t, "herdr workspace list did not return a workspace_list (isolated session may not be ready)")
	}

	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-l4")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// never re-introduces a hardcoded copy that silently drifts.
	abiDecl, abiDeclErr := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "abi"))
	if abiDeclErr != nil {
		t.Fatalf("read plugins/herdr/abi: %v", abiDeclErr)
	}
	wantABI := strings.TrimSpace(string(abiDecl))

	// directly. This is both deterministic and mutation-sensitive.
	abiCmd := exec.Command(binary, "__herdr-plugin", "abi")
	abiOut, abiErr := abiCmd.Output()
	if abiErr != nil {
		// On mutation (verb renamed or deleted), the binary exits 2 with
		var stderr []byte
		if ee, ok := abiErr.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("nexus3 __herdr-plugin abi: exit %v\nstderr: %s", abiErr, stderr)
	}
	got := strings.TrimSpace(string(abiOut))
	if got != wantABI {
		t.Errorf("nexus3 __herdr-plugin abi: want %q, got %q", wantABI, got)
	}
	t.Logf("nexus3 __herdr-plugin abi stdout: %q", got)

	// was hardcoded as "1" and silently went stale when the ABI moved to 2.
	label := fmt.Sprintf("nexus3-l4-probe-%d", time.Now().UnixMilli())
	// re-resolves the workspace from the live list; if the workspace was never
	t.Cleanup(func() {
		id := findL4WorkspaceIDByLabel(t, label)
		closeL4ScratchWorkspace(t, id, label)
		afterWorkspaces := herdrWorkspaceList(t)
		if !strings.Contains(afterWorkspaces, "workspace_list") {
			t.Errorf("herdr workspace list after cleanup did not return a workspace_list (got %q) — leak check inconclusive; check manually", afterWorkspaces)
		} else {
			// call to findL4WorkspaceIDByLabel: this assertion must not share a
			// with the wrong JSON field tag both the close AND the leak check
			if strings.Contains(afterWorkspaces, label) {
				t.Errorf("scratch workspace (label %q) survived cleanup; list: %s", label, afterWorkspaces)
			}
		}
		t.Logf("AFTER: %s", afterWorkspaces)
	})
	_, paneID := createL4ScratchWorkspace(t, label)

	runOut, err := herdrExec("pane", "run", paneID, binary, "__herdr-plugin", "abi").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr pane run: %v\n%s", err, runOut)
	}

	waitOut, err := herdrExec(
		"pane", "wait-output",
		paneID,
		"--match", wantABI,
		"--source", "recent",
		"--timeout", "10000",
	).CombinedOutput()
	if err != nil {
		readOut, _ := herdrExec("pane", "read", paneID, "--source", "visible", "--lines", "10").CombinedOutput()
		t.Logf("herdr pane wait-output did not find %q within 10 s: %v\n%s\npane (visible):\n%s",
			wantABI, err, waitOut, readOut)
	} else {
		readOut, _ := herdrExec("pane", "read", paneID, "--source", "visible", "--lines", "5").CombinedOutput()
		t.Logf("pane smoke test passed; visible: %s", strings.TrimSpace(string(readOut)))
	}
}

func herdrWorkspaceList(t *testing.T) string {
	t.Helper()
	out, _ := herdrExec("workspace", "list").CombinedOutput()
	return string(out)
}

// any error. Never fails the test — used by the pre-create cleanup closure so
// workspace by label even though wsID was never returned.
func findL4WorkspaceIDByLabel(t *testing.T, label string) string {
	t.Helper()
	out, err := herdrExec("workspace", "list").CombinedOutput()
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

func createL4ScratchWorkspace(t *testing.T, label string) (wsID, paneID string) {
	t.Helper()
	out, err := herdrExec(
		"workspace", "create",
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

// create. Safe to call multiple times (idempotent).
func closeL4ScratchWorkspace(t *testing.T, wsID, expectedLabel string) {
	t.Helper()
	if wsID == "" {
		return
	}

	getOut, err := herdrExec("workspace", "get", wsID).CombinedOutput()
	if err != nil {
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

	closeOut, err := herdrExec("workspace", "close", wsID).CombinedOutput()
	if err != nil {
		t.Logf("herdr workspace close %s: %v\n%s", wsID, err, closeOut)
		return
	}
	t.Logf("closed scratch workspace %s", wsID)
}

// It exists because `go test` reports a package whose only live test skipped as
func liveSkip(t *testing.T, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if os.Getenv("NEXUS3_LIVE_REQUIRED") == "1" {
		t.Fatalf("%s [NEXUS3_LIVE_REQUIRED=1: refusing to skip]", msg)
	}
	t.Skip(msg)
}
