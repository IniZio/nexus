//go:build integration

package selfhost

// example_dod_test.go — live DoD proof for nexus3-mount-creds-ssh-relay AC-3 and AC-6.
//
// Drives the herdr worktree flow (herdr worktree create → nexus3 herdr
// worktree-sandbox) against a real example-app checkout, then exercises:
//
//   AC-3: git SSH relay allows push to the feature branch and refuses push to
//         refs/heads/main.  Supervisor log must contain
//         gitssh.relay.allow service=git-receive-pack.
//
//   AC-6: Auto port-forward surfaces a guest python3 http.server on
//         127.0.0.1:8123.  Supervisor log must contain
//         supervisor.portfwd.listening and portfwd.stopped.
//
// # Skip conditions
//
//   - NEXUS3_DOD_REPO_DIR not set    — no repo to create worktree in
//   - HERDR_ENV != "1"                  — requires herdr runtime
//   - herdr not on PATH
//   - nexus3 not on PATH
//   - /dev/kvm absent or inaccessible   — sandbox boot requires KVM
//
// # Running
//
//	NEXUS3_DOD_REPO_DIR=/path/to/example-app HERDR_ENV=1 \
//	  make test-integration GOTEST_P=1 GOTEST_PARALLEL=1 \
//	    GOTEST_PKGS=./internal/test/selfhost/ \
//	    GOTEST_ARGS='-run TestExampleDoD -v -timeout 40m'

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── skip guards ───────────────────────────────────────────────────────────────

// skipUnlessExampleEnv skips t when NEXUS3_DOD_REPO_DIR is absent, HERDR_ENV
// is not "1", or herdr/nexus3 are not on PATH.
func skipUnlessExampleEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("NEXUS3_DOD_REPO_DIR") == "" {
		t.Skip("skipping: NEXUS3_DOD_REPO_DIR not set")
	}
	if os.Getenv("HERDR_ENV") != "1" {
		t.Skip("skipping: HERDR_ENV != 1 — must run inside herdr")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("skipping: herdr not on PATH")
	}
	if _, err := exec.LookPath("nexus3"); err != nil {
		t.Skip("skipping: nexus3 not on PATH")
	}
}

// ── shared paths ──────────────────────────────────────────────────────────────

// examplePortfwdStateFile returns the host path that portfwd.StateFile()
// resolves to, matching internal/core/portfwd/state.go.
func examplePortfwdStateFile() string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		xdg = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(xdg, "nexus3", "portfwd", "forwards.state")
}

// exampleSupervisorLog returns the supervisor.log path for the given sandbox ID,
// matching statedir.SupervisorDir(storeRoot, id) + "/supervisor.log".
func exampleSupervisorLog(sandboxID string) string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		xdg = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(xdg, "nexus3", "supervisors", sandboxID, "supervisor.log")
}

// ── herdr workspace lookup ────────────────────────────────────────────────────

// exampleFindParentWS locates the herdr workspace whose checkout_path matches
// lmsDir by running `herdr workspace list`. Falls back to HERDR_WORKSPACE_ID.
// Returns ("", false) when no match is found.
func exampleFindParentWS(ctx context.Context, t *testing.T, herdrBin, lmsDir string) (string, bool) {
	t.Helper()
	out, err := exec.CommandContext(ctx, herdrBin, "workspace", "list").Output()
	if err != nil {
		t.Logf("[%s] herdr workspace list: error: %v", time.Now().Format(time.RFC3339), err)
	} else {
		var resp struct {
			Result struct {
				Workspaces []struct {
					WorkspaceID string `json:"workspace_id"`
					Worktree    *struct {
						CheckoutPath string `json:"checkout_path"`
					} `json:"worktree"`
				} `json:"workspaces"`
			} `json:"result"`
		}
		if jErr := json.Unmarshal(out, &resp); jErr == nil {
			// canonical: exact match
			for _, ws := range resp.Result.Workspaces {
				if ws.Worktree != nil && ws.Worktree.CheckoutPath == lmsDir {
					t.Logf("[%s] parent workspace: found by checkout_path: %s", time.Now().Format(time.RFC3339), ws.WorkspaceID)
					return ws.WorkspaceID, true
				}
			}
			// lenient: prefix match (lmsDir is under the checkout)
			for _, ws := range resp.Result.Workspaces {
				if ws.Worktree != nil && strings.HasPrefix(ws.Worktree.CheckoutPath, lmsDir) {
					t.Logf("[%s] parent workspace: found by prefix: %s → %s", time.Now().Format(time.RFC3339), ws.WorkspaceID, ws.Worktree.CheckoutPath)
					return ws.WorkspaceID, true
				}
			}
		} else {
			t.Logf("[%s] herdr workspace list: parse error: %v", time.Now().Format(time.RFC3339), jErr)
		}
	}
	// Fallback: caller is already running inside the lms workspace.
	if id := os.Getenv("HERDR_WORKSPACE_ID"); id != "" {
		t.Logf("[%s] parent workspace: falling back to HERDR_WORKSPACE_ID=%s", time.Now().Format(time.RFC3339), id)
		return id, true
	}
	return "", false
}

// ── exampleWorktreeEnv — shared state returned by setupExampleWorktree ─────────

type exampleWorktreeEnv struct {
	handle    string // sandbox handle, e.g. "example-app/nexus3-proof-…"
	sandboxID string // sandbox ID, e.g. "sb-…"
	branch    string // git branch name, e.g. "nexus3-proof/1700000000"
	wsID      string // herdr workspace ID for the new worktree workspace
}

// ── setup helper ──────────────────────────────────────────────────────────────

// setupExampleWorktree creates a throwaway worktree+sandbox and registers
// cleanup. Returns once the guest agent is reachable. Calls t.Skip when the
// environment is incomplete; calls t.Fatal on hard errors.
//
// Verbs used (evidence sources):
//   - herdr worktree create  (skill SKILL.md §1)
//   - nexus3 herdr worktree-sandbox  (cmd_herdr_plugin.go:463)
//   - nexus3 herdr list  (cmd_herdr_plugin.go:383 → space-list)
//   - nexus3 exec <handle> -- …  (cmd_exec.go:43)
//   - herdr worktree remove --workspace <ws> --force  (task spec)
func setupExampleWorktree(t *testing.T, ctx context.Context) exampleWorktreeEnv {
	t.Helper()

	lmsDir := os.Getenv("NEXUS3_DOD_REPO_DIR")

	herdrBin, _ := exec.LookPath("herdr")
	nexus3Bin, _ := exec.LookPath("nexus3")

	branch := fmt.Sprintf("nexus3-proof/%d", time.Now().Unix())
	t.Logf("[%s] setup: lmsDir=%s branch=%s", time.Now().Format(time.RFC3339), lmsDir, branch)

	// ── Find herdr parent workspace for the lms repo ──────────────────────────
	parentWS, ok := exampleFindParentWS(ctx, t, herdrBin, lmsDir)
	if !ok {
		t.Skip("skipping: no herdr workspace found for NEXUS3_DOD_REPO_DIR and HERDR_WORKSPACE_ID unset — open the repo in herdr first")
	}

	// ── herdr worktree create ─────────────────────────────────────────────────
	// Creates the git worktree at a herdr-managed path and registers a new
	// herdr workspace for it. JSON response: {"ws":"<wsID>","linked":true}.
	t.Logf("[%s] herdr worktree create --workspace %s --branch %s --base main --no-focus",
		time.Now().Format(time.RFC3339), parentWS, branch)
	createOut, createErr := exec.CommandContext(ctx, herdrBin,
		"worktree", "create",
		"--workspace", parentWS,
		"--branch", branch,
		"--base", "main",
		"--no-focus",
	).CombinedOutput()
	t.Logf("[%s] worktree create stdout/stderr:\n%s", time.Now().Format(time.RFC3339), createOut)
	if createErr != nil {
		t.Fatalf("herdr worktree create: exit err: %v", createErr)
	}

	// Parse workspace ID from JSON response.
	var createResp struct {
		WS     string `json:"ws"`
		Linked bool   `json:"linked"`
	}
	// Response may be embedded in mixed output; extract the JSON line.
	var wsID string
	for _, line := range strings.Split(string(createOut), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") {
			if jErr := json.Unmarshal([]byte(line), &createResp); jErr == nil && createResp.WS != "" {
				wsID = createResp.WS
				break
			}
		}
	}
	if wsID == "" {
		t.Fatalf("herdr worktree create: could not parse workspace ID from output:\n%s", createOut)
	}
	t.Logf("[%s] new workspace ID: %s", time.Now().Format(time.RFC3339), wsID)

	// ── Register cleanup BEFORE doing anything that needs undo ───────────────
	t.Cleanup(func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer rmCancel()
		t.Logf("[%s] cleanup: herdr worktree remove --workspace %s --force",
			time.Now().Format(time.RFC3339), wsID)
		rmOut, rmErr := exec.CommandContext(rmCtx, herdrBin,
			"worktree", "remove", "--workspace", wsID, "--force",
		).CombinedOutput()
		t.Logf("[%s] worktree remove output:\n%s", time.Now().Format(time.RFC3339), rmOut)
		if rmErr != nil {
			t.Logf("[%s] cleanup: herdr worktree remove exit: %v", time.Now().Format(time.RFC3339), rmErr)
		}
		// Prune any dangling worktree entries from the lms repo.
		exec.CommandContext(rmCtx, "git", "-C", lmsDir, "worktree", "prune").Run() //nolint:errcheck
		// Best-effort: delete the local branch (may already be gone after worktree remove).
		exec.CommandContext(rmCtx, "git", "-C", lmsDir, "branch", "-D", branch).Run() //nolint:errcheck
	})

	// ── nexus3 herdr worktree-sandbox — explicit bind ─────────────────────────
	// The auto-provision hook skips when no existing nexus3-bound workspace
	// exists in the repo (FRICTION-1). Explicit bind always proceeds.
	t.Logf("[%s] nexus3 herdr worktree-sandbox %s", time.Now().Format(time.RFC3339), wsID)
	wtsOut, wtsErr := exec.CommandContext(ctx, nexus3Bin, "herdr", "worktree-sandbox", wsID).CombinedOutput()
	t.Logf("[%s] worktree-sandbox output:\n%s", time.Now().Format(time.RFC3339), wtsOut)
	if wtsErr != nil {
		t.Fatalf("nexus3 herdr worktree-sandbox: exit err: %v", wtsErr)
	}

	// ── nexus3 herdr list — resolve handle and sandbox ID ────────────────────
	// Output format (cmd_herdr_plugin.go:2113):
	//   label=…\tworkspace_id=…\thandle=…\tsandbox_id=…\tpane_id=…
	var handle, sandboxID string
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		listOut, _ := exec.CommandContext(ctx, nexus3Bin, "herdr", "list").CombinedOutput()
		for _, line := range strings.Split(string(listOut), "\n") {
			if !strings.Contains(line, "workspace_id="+wsID) {
				continue
			}
			for _, field := range strings.Split(line, "\t") {
				k, v, _ := strings.Cut(field, "=")
				switch k {
				case "handle":
					handle = v
				case "sandbox_id":
					sandboxID = v
				}
			}
		}
		if handle != "" && sandboxID != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context expired waiting for nexus3 herdr list to show workspace %s", wsID)
		case <-time.After(5 * time.Second):
		}
	}
	if handle == "" || sandboxID == "" {
		t.Fatalf("nexus3 herdr list: workspace %s not found after 2 min; cannot continue", wsID)
	}
	t.Logf("[%s] sandbox ready: handle=%s sandbox_id=%s", time.Now().Format(time.RFC3339), handle, sandboxID)

	// ── Wait for guest agent ──────────────────────────────────────────────────
	t.Logf("[%s] polling nexus3 exec until guest agent reachable …", time.Now().Format(time.RFC3339))
	agentDeadline := time.Now().Add(5 * time.Minute)
	for {
		echoOut, echoErr := exec.CommandContext(ctx, nexus3Bin, "exec", handle, "--", "/bin/echo", "agent-alive").CombinedOutput()
		if echoErr == nil && bytes.Contains(echoOut, []byte("agent-alive")) {
			t.Logf("[%s] guest agent reachable", time.Now().Format(time.RFC3339))
			break
		}
		if time.Now().After(agentDeadline) {
			t.Fatalf("guest agent not reachable after 5 min; last output: %s", echoOut)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context expired waiting for guest agent")
		case <-time.After(10 * time.Second):
		}
	}

	return exampleWorktreeEnv{
		handle:    handle,
		sandboxID: sandboxID,
		branch:    branch,
		wsID:      wsID,
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// exampleGuestExec runs /bin/bash -c <script> in the sandbox and returns stdout,
// stderr, and the exit code. Never calls t.Fatal on non-zero exit — callers
// assert the exit code themselves (evidence discipline: never relay exit codes).
func exampleGuestExec(ctx context.Context, nexus3Bin, handle, script string) (stdout, stderr string, exitCode int) {
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, nexus3Bin, "exec", handle, "--", "/bin/bash", "-c", script)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// exampleReadLog reads the full contents of path. Returns "" on error.
func exampleReadLog(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// ── AC-3: git SSH relay ───────────────────────────────────────────────────────

// TestExampleDoD_AC3 proves the git SSH relay allows push to the feature branch
// and refuses push to refs/heads/main.
//
// Evidence sources:
//   - gitssh.relay.allow log line: internal/core/gitssh/relay.go:299
//   - gitssh.relay.deny_ref log line: ticket 11-grill-example-live-dod.md §AC-3
//   - Deadlock fix: commit 6cfa39c "fix(gitssh): start ssh before reading
//     receive-pack commands, fix shim path"
//
// @verifies nexus3-mount-creds-ssh-relay/AC-3
func TestExampleDoD_AC3(t *testing.T) {
	skipUnlessExampleEnv(t)
	skipUnlessKVMSH(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	nexus3Bin, _ := exec.LookPath("nexus3")
	env := setupExampleWorktree(t, ctx)
	supLog := exampleSupervisorLog(env.sandboxID)

	t.Logf("[%s] AC-3: branch=%s handle=%s supLog=%s",
		time.Now().Format(time.RFC3339), env.branch, env.handle, supLog)

	// ── Step 1: empty commit + push allowed branch ────────────────────────────
	// After commit 6cfa39c the relay establishes the SSH connection FIRST, then
	// forwards server capabilities to git, then intercepts client ref-updates —
	// resolving the deadlock that blocked git push in the 2026-09-14 run.
	pushScript := fmt.Sprintf(`set -euo pipefail
cd /workspace
git config user.email "nexus3-dod@example.com" 2>/dev/null || true
git config user.name  "nexus3 DoD"              2>/dev/null || true
git commit --allow-empty -m "nexus3 dod probe: AC-3"
git push origin HEAD
echo PUSH_ALLOWED_BRANCH_OK
`)
	t.Logf("[%s] AC-3/step1: git commit + git push origin HEAD", time.Now().Format(time.RFC3339))
	stdout, stderr, code := exampleGuestExec(ctx, nexus3Bin, env.handle, pushScript)
	t.Logf("[%s] AC-3/step1 exit=%d stdout:\n%s\nstderr:\n%s",
		time.Now().Format(time.RFC3339), code, stdout, stderr)
	if code != 0 {
		t.Errorf("AC-3/step1: git push origin HEAD: expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "PUSH_ALLOWED_BRANCH_OK") {
		t.Errorf("AC-3/step1: tracer token PUSH_ALLOWED_BRANCH_OK absent from stdout")
	}

	// ── Step 2: push to main must be refused ─────────────────────────────────
	// The relay must log relay.deny_ref ref=refs/heads/main.
	pushMainScript := `cd /workspace && git push origin HEAD:main; echo "exit_code=$?"`
	t.Logf("[%s] AC-3/step2: git push origin HEAD:main (expect refusal)", time.Now().Format(time.RFC3339))
	stdout2, stderr2, code2 := exampleGuestExec(ctx, nexus3Bin, env.handle, pushMainScript)
	t.Logf("[%s] AC-3/step2 exit=%d stdout:\n%s\nstderr:\n%s",
		time.Now().Format(time.RFC3339), code2, stdout2, stderr2)
	if code2 == 0 {
		t.Errorf("AC-3/step2: git push origin HEAD:main: expected non-zero exit (relay must refuse), got 0")
	}
	combined2 := stdout2 + stderr2
	if !strings.Contains(combined2, "remote") && !strings.Contains(combined2, "refused") &&
		!strings.Contains(combined2, "ERR nexus3") && !strings.Contains(combined2, "deny") {
		t.Errorf("AC-3/step2: expected 'remote'/'refused'/'ERR nexus3' in output; got stdout=%q stderr=%q",
			stdout2, stderr2)
	}

	// ── Step 3: delete remote branch (cleanup) ───────────────────────────────
	deleteScript := fmt.Sprintf("cd /workspace && git push origin --delete %s; echo DELETE_DONE", env.branch)
	t.Logf("[%s] AC-3/step3: git push origin --delete %s", time.Now().Format(time.RFC3339), env.branch)
	delOut, delErr, delCode := exampleGuestExec(ctx, nexus3Bin, env.handle, deleteScript)
	t.Logf("[%s] AC-3/step3 exit=%d stdout:\n%s\nstderr:\n%s",
		time.Now().Format(time.RFC3339), delCode, delOut, delErr)
	// Non-fatal: remote branch delete is best-effort (might not exist if push failed).

	// ── Step 4: supervisor log must contain relay.allow ──────────────────────
	logContent := exampleReadLog(supLog)
	if logContent == "" {
		t.Errorf("AC-3: supervisor log not found or empty: %s", supLog)
	} else {
		const wantAllow = "gitssh.relay.allow service=git-receive-pack"
		if !strings.Contains(logContent, wantAllow) {
			t.Errorf("AC-3: supervisor log missing %q\nlog tail:\n%s",
				wantAllow, lastLines(logContent, 40))
		} else {
			t.Logf("[%s] AC-3 PASS: supervisor log contains %q", time.Now().Format(time.RFC3339), wantAllow)
		}
	}
}

// ── AC-6: auto port forward ───────────────────────────────────────────────────

// TestExampleDoD_AC6 proves the supervisor auto-forwards a guest TCP listener on
// port 8123 to 127.0.0.1:8123 on the host.
//
// Evidence sources:
//   - supervisor.portfwd.listening: internal/supervisor/portfwd.go:227
//   - portfwd.stopped:              internal/supervisor/portfwd.go:208
//   - forwards.state path:          internal/core/portfwd/state.go:StateFile()
//   - reconcile timeout fix:        internal/supervisor/portfwd.go:106-111
//     (portFwdDiscoverTimeout=5s per tick — fixes the reconcile stall from the
//     2026-09-15 herdr-only rerun that used a binary without a per-call
//     deadline on the guest exec channel)
//
// @verifies nexus3-mount-creds-ssh-relay/AC-6
func TestExampleDoD_AC6(t *testing.T) {
	skipUnlessExampleEnv(t)
	skipUnlessKVMSH(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	nexus3Bin, _ := exec.LookPath("nexus3")
	env := setupExampleWorktree(t, ctx)
	supLog := exampleSupervisorLog(env.sandboxID)
	stateFile := examplePortfwdStateFile()

	t.Logf("[%s] AC-6: handle=%s supLog=%s stateFile=%s",
		time.Now().Format(time.RFC3339), env.handle, supLog, stateFile)

	// ── Step 1: start guest HTTP server on :8123 ──────────────────────────────
	// The shell exits immediately after forking; the python server runs detached.
	serverScript := "python3 -m http.server 8123 --bind 0.0.0.0 </dev/null >/tmp/http-server.log 2>&1 &"
	t.Logf("[%s] AC-6/step1: start python3 http.server 8123 in guest", time.Now().Format(time.RFC3339))
	_, _, startCode := exampleGuestExec(ctx, nexus3Bin, env.handle, serverScript)
	t.Logf("[%s] AC-6/step1 exit=%d", time.Now().Format(time.RFC3339), startCode)
	if startCode != 0 {
		t.Fatalf("AC-6/step1: failed to start http.server in guest, exit=%d", startCode)
	}

	// ── Step 2: poll host 127.0.0.1:8123 until 200 (≤60s) ───────────────────
	// The portfwd supervisor discovers the listener within one reconcile tick
	// (≤10s) and binds 127.0.0.1:8123 on the host.
	t.Logf("[%s] AC-6/step2: polling http://127.0.0.1:8123/ for 200 (≤60s)", time.Now().Format(time.RFC3339))
	hostReachable := false
	pollDeadline := time.Now().Add(60 * time.Second)
	httpClient := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(pollDeadline) {
		resp, err := httpClient.Get("http://127.0.0.1:8123/")
		if err == nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Logf("[%s] AC-6/step2: host 127.0.0.1:8123 returned 200", time.Now().Format(time.RFC3339))
				hostReachable = true
				break
			}
			t.Logf("[%s] AC-6/step2: got status %d, retrying", time.Now().Format(time.RFC3339), resp.StatusCode)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context expired while polling 127.0.0.1:8123")
		case <-time.After(2 * time.Second):
		}
	}
	if !hostReachable {
		// Log the supervisor portfwd state for diagnosis.
		t.Logf("[%s] AC-6/step2 FAIL: host port not reachable after 60s; supervisor log tail:\n%s",
			time.Now().Format(time.RFC3339), lastLines(exampleReadLog(supLog), 30))
		t.Fatalf("AC-6: host 127.0.0.1:8123 not reachable — auto-forward did not bind in 60s")
	}

	// ── Step 3: assert supervisor log and forwards.state ─────────────────────
	logContent := exampleReadLog(supLog)
	if logContent == "" {
		t.Errorf("AC-6: supervisor log not found or empty: %s", supLog)
	} else {
		if !strings.Contains(logContent, "supervisor.portfwd.listening") {
			t.Errorf("AC-6: supervisor log missing 'supervisor.portfwd.listening'\nlog tail:\n%s",
				lastLines(logContent, 40))
		} else {
			t.Logf("[%s] AC-6 PASS(a): supervisor.portfwd.listening found", time.Now().Format(time.RFC3339))
		}
	}

	stateBytes, stateErr := os.ReadFile(stateFile)
	if stateErr != nil {
		t.Errorf("AC-6: forwards.state not readable at %s: %v", stateFile, stateErr)
	} else if !strings.Contains(string(stateBytes), "8123") {
		t.Errorf("AC-6: forwards.state does not mention port 8123\ncontent: %s", stateBytes)
	} else {
		t.Logf("[%s] AC-6 PASS(b): forwards.state lists 8123", time.Now().Format(time.RFC3339))
	}

	// ── Step 4: kill the server ───────────────────────────────────────────────
	t.Logf("[%s] AC-6/step4: killing python3 http.server 8123", time.Now().Format(time.RFC3339))
	_, _, killCode := exampleGuestExec(ctx, nexus3Bin, env.handle,
		`pkill -f "http.server 8123" || pkill -f "http.server" ; echo KILL_SENT`)
	t.Logf("[%s] AC-6/step4 kill exit=%d", time.Now().Format(time.RFC3339), killCode)

	// ── Step 5: poll until connection refused (≤60s) ─────────────────────────
	t.Logf("[%s] AC-6/step5: polling until 127.0.0.1:8123 connection refused", time.Now().Format(time.RFC3339))
	downDeadline := time.Now().Add(60 * time.Second)
	serverDown := false
	for time.Now().Before(downDeadline) {
		_, dialErr := httpClient.Get("http://127.0.0.1:8123/")
		if dialErr != nil {
			t.Logf("[%s] AC-6/step5: connection refused as expected: %v", time.Now().Format(time.RFC3339), dialErr)
			serverDown = true
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context expired waiting for 8123 to go down")
		case <-time.After(2 * time.Second):
		}
	}
	if !serverDown {
		t.Errorf("AC-6: 127.0.0.1:8123 still reachable 60s after server kill")
	}

	// ── Step 6: supervisor log must contain portfwd.stopped ──────────────────
	// Give the reconcile loop one interval (≤10s) to detect the closed port.
	time.Sleep(12 * time.Second)
	logContent2 := exampleReadLog(supLog)
	if !strings.Contains(logContent2, "portfwd.stopped") {
		t.Errorf("AC-6: supervisor log missing 'portfwd.stopped' after server kill\nlog tail:\n%s",
			lastLines(logContent2, 40))
	} else {
		t.Logf("[%s] AC-6 PASS(c): portfwd.stopped found", time.Now().Format(time.RFC3339))
	}
}

// ── utilities ─────────────────────────────────────────────────────────────────

// lastLines returns the last n non-empty lines of s, joined by newlines.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
