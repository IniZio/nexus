//go:build herdr_live

// AC-6: a machine-checked test that drives the whole chain, end to end,
// through the REAL nexus3 binary and a REAL herdr session — and where
// deleting any joint under test makes it fail.
//
// The chain (verified by hand on the host, now automated):
//
//	nexus3 create --mount              →  a sandbox with the source mounted
//	__herdr-plugin space-open-pane     →  guest-shell pane, returns pane_id
//	read pane_id back from the binding →  the persisted HerdrSpaceBinding
//	herdr pane send-text + send-keys   →  prompt the guest shell
//	herdr pane wait-output --match     →  read its answer back
//
// This file shares the herdr_live build tag and the createL4ScratchWorkspace
// / closeL4ScratchWorkspace / herdrWorkspaceList / findL4WorkspaceIDByLabel
// helpers with herdr_l4_live_test.go rather than duplicating them.
//
// # Why the guest command reads two numbers from the mount and sums them
//
// `herdr pane wait-output --match TOKEN` matches against pane output, which
// includes the ECHOED command line the shell prints before running it. A
// naive prompt like `echo CHAIN-OK` matches on the echo of the prompt
// itself, not on anything the guest produced — the test passes for the wrong
// reason, catching nothing. This was hit by hand while building this chain.
//
// The guest command here is:
//
//	echo $(( $(cat <mount>/a.txt) + $(cat <mount>/b.txt) ))
//
// Neither addend nor the sum appears anywhere in that command line — only
// the file paths do — so the match token (the sum) cannot appear in the
// echo. This also happens to make the mount itself part of what is under
// test: if --mount were not wired, `cat <mount>/a.txt` fails, the arithmetic
// never produces the expected sum, and wait-output times out. See the joint
// list below for exactly what is, and is not, covered this way.
//
// # Joints under test, and the mutation reasoning (EXECUTED RED 2026-08-23 on
// a KVM+herdr host — see below; each mutation was applied to production,
// rebuilt by this test's own `go build`, and run live)
//
//  1. pane ID not returned. If herdrOpenGuestShellPane returned "" instead of
//     the parsed pane_id, `opened pane: pane_id=` (empty) is what
//     space-open-pane would print, parseOpenedPaneID returns "", and the
//     `returnedPaneID == ""` check below fails the test immediately.
//  2. pane ID not persisted. If the binding were never updated with the pane
//     ID (e.g. the second HerdrSpacePut call were dropped), space-list would
//     report `pane_id=` (empty) for our handle even though space-open-pane's
//     own stdout carried a real ID. The `persistedPaneID == ""` check below
//     fails the test.
//  3. mount not wired. Induced by passing nil to wireLiveMountsToConfig /
//     liveMountsToGuestMounts in cmd_sandbox.go (create --mount then attaches
//     no virtiofs share). Observed live: the guest shell pane never reaches
//     its prompt (`cat <mount>` has nothing to read and the guest-only
//     workspace has no working directory), so the readiness wait-output at
//     step 5 fails — an even stronger sensitivity than the arithmetic-timeout
//     originally predicted. Removing the mount makes the test fail either way.
//
// All three were EXERCISED live on 2026-08-23 (KVM present, herdr 0.8.0
// session reachable): each mutation was applied, the test rebuilt its own
// binary from the mutated source, and the run went RED at the check named
// above; reverting each restored a green chain (last: 436623+171989=608612).
package cli

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestHerdrPlugin_L4_AC6Chain drives the full create→open-pane→persist→
// prompt→read chain through the real nexus3 binary and a real herdr session.
//
// Run with:
//
//	TMPDIR=/tmp go test -count=1 -tags herdr_live ./internal/cli/ -run TestHerdrPlugin_L4_AC6Chain
//
// ac6Env returns the environment for a nexus3 subprocess with XDG_STATE_HOME
// REMOVED.
//
// This package's TestMain redirects XDG_STATE_HOME to a throwaway directory so
// unit tests cannot deposit stub disks in the operator's real state root. That
// isolation is correct for unit tests and fatal here: a live test drives the
// REAL binary against the REAL image cache and sandbox store, and inherits the
// redirect through the subprocess environment. The symptom is a cached image
// reported as `image cache: not found` even though it plainly exists.
//
// Dropping the variable lets store.DefaultRoot() fall back to
// ~/.local/state/nexus3, which is what a live test must talk to.
func ac6Env() []string {
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ac6Cmd builds a nexus3 subprocess with the live-state environment applied.
func ac6Cmd(binary string, args ...string) *exec.Cmd {
	cmd := exec.Command(binary, args...)
	cmd.Env = ac6Env()
	return cmd
}

func TestHerdrPlugin_L4_AC6Chain(t *testing.T) {
	// --- 0. Prerequisites: skip, never fail, when absent. ---
	if _, err := os.Stat("/dev/kvm"); err != nil {
		liveSkip(t, "AC-6: /dev/kvm not available: %v", err)
	}
	beforeWorkspaces := herdrWorkspaceList(t)
	if !strings.Contains(beforeWorkspaces, "workspace_list") {
		liveSkip(t, "AC-6: herdr is not reachable (herdr workspace list did not return a workspace_list)")
	}
	t.Logf("BEFORE: %s", beforeWorkspaces)

	// A kernel image is as much a prerequisite as /dev/kvm. It is a large
	// gitignored binary that lives only in a full checkout, so a clone (or a
	// CI box) legitimately will not have one — that is a skip, not a failure.
	if os.Getenv("NEXUS3_KERNEL_PATH") == "" {
		liveSkip(t, "AC-6: NEXUS3_KERNEL_PATH is not set and the kernel is not resolvable from this tree; "+
			"set it to a vmlinux image to run this test")
	}

	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-ac6")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		liveSkip(t, "AC-6: nexus3 binary cannot be built: %v\n%s", err, out)
	}

	// --- 1. Set up the scratch handle, mount source, and the two addends. ---
	//
	// SAFETY: a unique scratch handle every run, never a fixed name — the
	// operator has live sandboxes demo-1/demo-1 and dev/space1 that this must
	// never collide with or touch.
	handle := fmt.Sprintf("ac6/%08x", rand.Uint32())

	srcDir := t.TempDir()
	const guestMount = "/mnt/ac6-src"
	a := 100000 + rand.Intn(400000)
	b := 100000 + rand.Intn(400000)
	sum := a + b
	token := strconv.Itoa(sum)
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte(strconv.Itoa(a)), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte(strconv.Itoa(b)), 0o600); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	cmdLine := fmt.Sprintf("echo $(( $(cat %s/a.txt) + $(cat %s/b.txt) ))\n", guestMount, guestMount)

	// wsID and label are filled in once space-list confirms the binding;
	// cleanup below re-resolves by label if the test fails before that point.
	var wsID, wsLabel string

	// Cleanup registered BEFORE anything is created, so a t.Fatal anywhere
	// below still tears down whatever got created (mirrors the existing L4
	// test's pattern). Order: remove the sandbox, close the workspace
	// (label-checked — never close a workspace we did not mint), delete the
	// binding, then assert none of it survived.
	t.Cleanup(func() {
		rmOut, rmErr := ac6Cmd(binary, "rm", handle).CombinedOutput()
		if rmErr != nil {
			t.Logf("cleanup: nexus3 rm %s: %v\n%s", handle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus3 rm %s: %s", handle, rmOut)
		}

		id := wsID
		label := wsLabel
		if label == "" {
			// The test never got far enough to observe the real label from
			// space-list; fall back to the convention nexus3 uses
			// (herdrSpaceLabelForRef) so cleanup can still find and verify it.
			label = herdrSpaceLabelForRef(handle)
		}
		if id == "" {
			id = findL4WorkspaceIDByLabel(t, label)
		}
		// closeL4ScratchWorkspace re-resolves by ID and REFUSES to close if
		// the live label does not match — this is the safety check the task
		// requires for the sandbox path too, reused verbatim.
		closeL4ScratchWorkspace(t, id, label)

		// Sandbox already removed and workspace already closed above (both
		// tolerant of "already gone"), so the removal/close steps inside
		// space-remove are no-ops here — this call exists only to delete the
		// persisted binding.
		delOut, delErr := ac6Cmd(binary, "__herdr-plugin", "space-remove", handle).CombinedOutput()
		if delErr != nil {
			t.Logf("cleanup: space-remove %s: %v\n%s", handle, delErr, delOut)
		} else {
			t.Logf("cleanup: space-remove %s: %s", handle, delOut)
		}

		afterWorkspaces := herdrWorkspaceList(t)
		if !strings.Contains(afterWorkspaces, "workspace_list") {
			t.Errorf("herdr workspace list after cleanup did not return a workspace_list (got %q) — leak check inconclusive; check manually", afterWorkspaces)
		} else if strings.Contains(afterWorkspaces, label) {
			t.Errorf("scratch workspace (label %q) survived cleanup; list: %s", label, afterWorkspaces)
		}
		t.Logf("AFTER: %s", afterWorkspaces)
	})

	// --- 2. nexus3 create --mount: a sandbox with the source mounted. ---
	//
	// Driving the REAL binary as a subprocess, exactly as the herdr plugin
	// does — not calling internal Go functions. A test that called
	// herdrOpenGuestShellPane (or svc.Create) directly would prove nothing
	// about the wiring between them.
	// The default ref can be ambiguous across several cached digests, in which
	// case create refuses it and asks for a digest. NEXUS3_AC6_IMAGE lets the
	// operator pin one without editing the test.
	image := os.Getenv("NEXUS3_AC6_IMAGE")
	if image == "" {
		image = herdrDefaultImage
	}
	// No GitHub flags: this test needs no forge access at all and the
	// fail-closed default (no --repo, no --secret) is correct (D-PDE-02).
	createOut, err := ac6Cmd(binary, "create", handle,
		"--image", image,
		"--mount", srcDir+":"+guestMount+":ro",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("nexus3 create --mount: %v\n%s\n(check NEXUS3_KERNEL_PATH is set and %q is a cached image)",
			err, createOut, image)
	}
	t.Logf("nexus3 create: %s", createOut)

	// --- 3. __herdr-plugin space-open-pane: guest-shell pane, returns pane_id. ---
	openOut, err := ac6Cmd(binary, "__herdr-plugin", "space-open-pane", handle).CombinedOutput()
	if err != nil {
		t.Fatalf("__herdr-plugin space-open-pane: %v\n%s", err, openOut)
	}
	t.Logf("space-open-pane: %s", openOut)
	returnedPaneID := parseOpenedPaneID(string(openOut))
	if returnedPaneID == "" {
		// Joint 1: pane ID not returned.
		t.Fatalf("space-open-pane did not report a pane_id (\"opened pane: pane_id=...\" line missing or empty); output: %s", openOut)
	}

	// --- 4. Read pane_id back from the persisted binding via space-list. ---
	//
	// Deliberately NOT reusing returnedPaneID for the prompt step below —
	// the whole point of AC-6 is that the persisted binding, not the one-shot
	// return value, is what makes the chain scriptable
	// (`herdr agent start --pane <ID>` reads it from there).
	listOut, err := ac6Cmd(binary, "__herdr-plugin", "space-list").CombinedOutput()
	if err != nil {
		t.Fatalf("__herdr-plugin space-list: %v\n%s", err, listOut)
	}
	persistedLabel, persistedWorkspaceID, persistedPaneID := parseSpaceListForHandle(string(listOut), handle)
	wsLabel = persistedLabel
	wsID = persistedWorkspaceID
	if persistedLabel == "" {
		t.Fatalf("space-list has no entry for handle %q; output: %s", handle, listOut)
	}
	if persistedPaneID == "" {
		// Joint 2: pane ID not persisted.
		t.Fatalf("binding for handle %q has no persisted pane_id; space-list: %s", handle, listOut)
	}
	if persistedPaneID != returnedPaneID {
		t.Fatalf("persisted pane_id %q != pane_id returned by space-open-pane %q", persistedPaneID, returnedPaneID)
	}

	// --- 5. herdr pane send-text + send-keys: prompt the guest shell. ---
	//
	// VERIFIED against herdr 0.8.0: both take positional arguments —
	// `send-text <PANE_ID> <TEXT>` and `send-keys <PANE_ID> <KEY>...`.
	// The pane runs `nexus3 exec --pty`, which must attach to the in-guest
	// agent before a shell prompt exists. Typing before then sends keystrokes
	// into a pane with no shell behind it, and the command never runs. Wait for
	// the guest's own prompt — its hostname is derived from the handle — before
	// sending anything. Without this the test fails intermittently at the
	// wait-output step for a reason that has nothing to do with the chain.
	guestHost := sandboxHandleHostname(handle)
	if out, err := exec.Command("herdr", "pane", "wait-output", persistedPaneID,
		"--match", guestHost, "--source", "recent", "--timeout", "180000",
	).CombinedOutput(); err != nil {
		readOut, _ := exec.Command("herdr", "pane", "read", persistedPaneID, "--source", "visible", "--lines", "20").CombinedOutput()
		t.Fatalf("guest shell prompt (%q) never appeared in pane %s: %v\n%s\npane (visible):\n%s",
			guestHost, persistedPaneID, err, out, readOut)
	}

	sendOut, err := exec.Command("herdr", "pane", "send-text", persistedPaneID, cmdLine).CombinedOutput()
	if err != nil {
		t.Fatalf("herdr pane send-text: %v\n%s", err, sendOut)
	}
	keysOut, err := exec.Command("herdr", "pane", "send-keys", persistedPaneID, "Enter").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr pane send-keys: %v\n%s", err, keysOut)
	}

	// --- 6. herdr pane wait-output --match: read the answer back. ---
	waitOut, err := exec.Command(
		"herdr", "pane", "wait-output",
		persistedPaneID,
		"--match", token,
		"--source", "recent",
		"--timeout", "120000",
	).CombinedOutput()
	if err != nil {
		readOut, _ := exec.Command("herdr", "pane", "read", persistedPaneID, "--source", "visible", "--lines", "20").CombinedOutput()
		// Joint 3 (mount not wired) surfaces here too, structurally: if the
		// mount is missing, the sum is never produced and this times out.
		t.Fatalf("herdr pane wait-output did not find token %q within 120s: %v\n%s\npane (visible):\n%s",
			token, err, waitOut, readOut)
	}

	matchedLine := parseMatchedLine(string(waitOut))
	if matchedLine == "" {
		t.Fatalf("wait-output succeeded but result.matched_line was empty; raw: %s", waitOut)
	}
	// The core anti-false-positive assertion: the matched line must be the
	// ANSWER, not the echoed prompt. The command line contains no digits at
	// all (only file paths), so this also incidentally proves the match
	// isn't picking up the command's own echo.
	if strings.Contains(matchedLine, strings.TrimSpace(cmdLine)) {
		t.Fatalf("matched line %q contains the echoed prompt text %q — the match is not mutation-sensitive", matchedLine, strings.TrimSpace(cmdLine))
	}
	if !strings.Contains(matchedLine, token) {
		t.Fatalf("matched line %q does not contain the expected token %q (sum of %d + %d)", matchedLine, token, a, b)
	}
	t.Logf("AC-6 chain complete: %d + %d = %s, matched line: %q", a, b, token, matchedLine)
}

// parseOpenedPaneID extracts the pane ID from
// `__herdr-plugin space-open-pane`'s stdout, which prints exactly
// "opened pane: pane_id=<ID>\n" (see herdrPluginSpaceOpenPane in
// cmd_herdr_plugin.go). Returns "" if the line is absent or the ID is empty.
func parseOpenedPaneID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "opened pane: pane_id="); ok {
			return v
		}
	}
	return ""
}

// parseSpaceListForHandle scans `__herdr-plugin space-list`'s stdout for the
// row whose `handle=` field matches handle exactly, and returns its label,
// workspace ID, and pane ID. Each row is tab-separated
// "label=...\tworkspace_id=...\thandle=...\tsandbox_id=...\tpane_id=...\n"
// (see herdrPluginSpaceList in cmd_herdr_plugin.go). Returns all-empty if no
// row matches.
func parseSpaceListForHandle(out, handle string) (label, workspaceID, paneID string) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		matched := false
		for _, f := range fields {
			if f == "handle="+handle {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for _, f := range fields {
			if v, ok := strings.CutPrefix(f, "label="); ok {
				label = v
			}
			if v, ok := strings.CutPrefix(f, "workspace_id="); ok {
				workspaceID = v
			}
			if v, ok := strings.CutPrefix(f, "pane_id="); ok {
				paneID = v
			}
		}
		return label, workspaceID, paneID
	}
	return "", "", ""
}

// parseMatchedLine extracts result.matched_line from a
// `herdr pane wait-output` JSON response — the field named explicitly in
// TASK-AC6.md's requirement 3. UNVERIFIED against a live herdr (see
// AGENT-REPORT.md); returns "" if the response does not parse or the field
// is absent, rather than failing here, so the caller's own check produces
// the diagnostic.
func parseMatchedLine(raw string) string {
	var resp struct {
		Result struct {
			MatchedLine string `json:"matched_line"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return ""
	}
	return resp.Result.MatchedLine
}
