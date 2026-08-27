//go:build herdr_live

// AC-4: a machine-checked test that the operator can take over any agent
// the orchestrator started — typing into its pane — without killing it or
// losing the orchestrator's view of it.
//
// The chain (verified by hand, now automated):
//
//	nexus3 create --mount              →  sandbox with source mounted
//	nexus3 herdr agent           →  guest agent running in a herdr pane
//	ORCHESTRATOR TURN: wait for STEP1= →  agent read the secret and reported it
//	OPERATOR TURN: send text to pane   →  same pane, as if a human typed
//	continuity assertion               →  agent recalled the orchestrator's token
//	herdr agent list                   →  orchestrator's view still present
//	herdr pane wait-output footer      →  agent UI still alive
//
// This file lives in the herdr_live build tag and reuses the ac6Cmd/ac6Env,
// herdrWorkspaceList, findL4WorkspaceIDByLabel, createL4ScratchWorkspace,
// closeL4ScratchWorkspace, parseSpaceListForHandle, and parseMatchedLine
// helpers defined in herdr_l4_live_test.go and herdr_l4_chain_test.go.
//
// # Why the secret number is non-echoable
//
// The brief tells the agent to read a file whose PATH appears in the brief
// (and is therefore echoed into the terminal as the user message), but whose
// CONTENT does not. Only the agent's execution of the Read/Bash tool produces
// the secret number in the transcript. The orchestrator match token is the
// number itself, so it cannot fire on the echoed brief — the agent must have
// genuinely run.
//
// For the continuity match the token is the number wrapped in angle brackets
// ("<N>"). The operator's question asks for the bracket format but does not
// contain the bracketed number. The orchestrator's plain-number output does
// not match the bracketed form. So the continuity match is non-echoable from
// both the brief and the operator prompt, and is not stale-matchable from the
// orchestrator turn.
//
// # Why the continuity match distinguishes "agent survived" from "agent present"
//
// The operator asks: "What was the number in your STEP1 output?" — a question
// that does NOT contain the secret number. The only source of the number in
// fresh (--source recent) pane output is the agent recalling it from its own
// conversation context. A freshly started or replaced agent has no such
// context: it was not there for the orchestrator's turn, does not know the
// file content, and cannot answer. So recall = survival. This is the load-
// bearing assertion of the whole test.
//
// # Mutations and their expected failures
//
//  1. Operator text not sent: the agent is idle after STEP1=. No fresh output
//     containing the secret number appears. --source recent times out. FAIL.
//
//  2. herdrPaneReportAgent not called inside space-agent: herdr agent list
//     does not carry our pane_id. FAIL.
//
//  3. Agent killed before operator turn (ctrl+c twice): a killed agent cannot
//     answer the operator's question. The continuity wait times out. FAIL.
package cli

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHerdrPlugin_L4_AC4Takeover(t *testing.T) {
	// --- 0. Prerequisites: skip, never fail, when absent. ---
	if _, err := os.Stat("/dev/kvm"); err != nil {
		liveSkip(t, "AC-4: /dev/kvm not available: %v", err)
	}
	beforeWorkspaces := herdrWorkspaceList(t)
	if !strings.Contains(beforeWorkspaces, "workspace_list") {
		liveSkip(t, "AC-4: herdr is not reachable (herdr workspace list did not return a workspace_list)")
	}
	t.Logf("BEFORE: %s", beforeWorkspaces)

	// A kernel image is a prerequisite as much as /dev/kvm — large binary,
	// legitimately absent in CI or a fresh clone.
	if os.Getenv("NEXUS3_KERNEL_PATH") == "" {
		liveSkip(t, "AC-4: NEXUS3_KERNEL_PATH is not set; set it to a vmlinux image to run this test")
	}

	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-ac4")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		liveSkip(t, "AC-4: nexus3 binary cannot be built: %v\n%s", err, out)
	}

	// --- 1. Scratch handle, mount source, secret number. ---
	//
	// SAFETY: unique handle every run — never collide with demo-1/demo-1,
	// dev/space1, or ac1/setup. The secret is a 6-digit integer written to
	// a file the agent will read. The number does NOT appear in the brief
	// text or in any command the test sends, so it cannot match on a terminal
	// echo — the agent must genuinely execute to produce it.
	handle := fmt.Sprintf("ac4/%08x", rand.Uint32())

	srcDir := t.TempDir()
	const guestMount = "/mnt/ac4-src"
	secret := 100000 + rand.Intn(900000) // 6-digit, well outside typical prompt line numbers
	secretStr := strconv.Itoa(secret)
	if err := os.WriteFile(filepath.Join(srcDir, "secret.txt"), []byte(secretStr), 0o600); err != nil {
		t.Fatalf("write secret.txt: %v", err)
	}

	var wsID, wsLabel string

	// Cleanup registered BEFORE anything is created so a t.Fatal anywhere
	// below still tears down whatever was created (mirrors the AC-6 pattern).
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
			label = herdrSpaceLabelForRef(handle)
		}
		if id == "" {
			id = findL4WorkspaceIDByLabel(t, label)
		}
		closeL4ScratchWorkspace(t, id, label)

		delOut, delErr := ac6Cmd(binary, "__herdr-plugin", "space-remove", handle).CombinedOutput()
		if delErr != nil {
			t.Logf("cleanup: space-remove %s: %v\n%s", handle, delErr, delOut)
		} else {
			t.Logf("cleanup: space-remove %s: %s", handle, delOut)
		}

		afterWorkspaces := herdrWorkspaceList(t)
		if !strings.Contains(afterWorkspaces, "workspace_list") {
			t.Errorf("herdr workspace list after cleanup did not return a workspace_list — leak check inconclusive; check manually")
		} else if strings.Contains(afterWorkspaces, label) {
			t.Errorf("scratch workspace (label %q) survived cleanup; list: %s", label, afterWorkspaces)
		}
		t.Logf("AFTER: %s", afterWorkspaces)
	})

	// --- 2. nexus3 create --mount --agent claude-code: sandbox with source mounted. ---
	//
	// --agent claude-code seeds the Anthropic OAuth token via the MITM broker
	// so the in-guest claude can call the API. Without it the agent says "Not
	// logged in" and cannot process any brief.
	//
	// No GitHub flags: the claude-code profile only needs Anthropic egress
	// (api.anthropic.com + platform.claude.com); no GitHub access is required.
	// Fail-closed default (no --repo, no --secret) is correct (D-PDE-02).
	//
	// Reuses NEXUS3_AC6_IMAGE so the operator pins the same image for both
	// AC-6 and AC-4.
	image := os.Getenv("NEXUS3_AC6_IMAGE")
	if image == "" {
		image = herdrDefaultImage
	}
	createOut, err := ac6Cmd(binary, "create", handle,
		"--image", image,
		"--agent", "claude-code",
		"--mount", srcDir+":"+guestMount+":ro",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("nexus3 create: %v\n%s\n(check NEXUS3_KERNEL_PATH is set and %q is a cached image)",
			err, createOut, image)
	}
	t.Logf("nexus3 create: %s", createOut)

	// --- 3. space-agent: launch the agent and deliver the brief. ---
	//
	// This is the PRODUCTION path — the same command the orchestrator would
	// run. It: starts the sandbox, opens/reuses the herdr workspace, opens
	// the guest shell pane, launches claude, waits for its ready prompt,
	// delivers the brief, and registers the agent in herdr's tracker.
	//
	// The brief tells the agent to read secret.txt. The PATH appears in the
	// brief (and is echoed as the user message in claude's TUI transcript),
	// but the CONTENT (the secret number) does not. The orchestrator match
	// token is the number itself, so it cannot fire on the echoed brief text.
	brief := fmt.Sprintf(
		"Read the file at %s/secret.txt. It contains exactly one integer. "+
			"Output that integer on its own line, with no other text. "+
			"Then wait quietly for further instructions.",
		guestMount,
	)
	agentOut, err := ac6Cmd(binary, "__herdr-plugin", "space-agent", "--autonomous", handle, brief).CombinedOutput()
	if err != nil {
		t.Fatalf("__herdr-plugin space-agent: %v\n%s", err, agentOut)
	}
	t.Logf("space-agent: %s", agentOut)

	// --- 4. Read pane ID from the persisted binding. ---
	//
	// Not reusing any value from space-agent's stdout: the binding is what
	// lets the orchestrator (or the operator) reach the pane later without
	// the original invocation's return value.
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
		t.Fatalf("binding for handle %q has no persisted pane_id; space-list: %s", handle, listOut)
	}
	t.Logf("pane=%s workspace=%s label=%q", persistedPaneID, persistedWorkspaceID, persistedLabel)

	// --- 5. ORCHESTRATOR TURN: wait for the agent to output the secret number. ---
	//
	// The agent is processing the brief. It reads secret.txt and outputs the
	// integer on its own line. The match token is the secret number itself —
	// it appears nowhere in the brief text (only the file path does), so this
	// cannot fire on the echoed user message. The agent must have genuinely
	// executed to produce it.
	orchWait, err := exec.Command(
		"herdr", "pane", "wait-output",
		persistedPaneID,
		"--match", secretStr,
		"--source", "recent",
		"--timeout", "120000",
	).CombinedOutput()
	if err != nil {
		readOut, _ := exec.Command("herdr", "pane", "read", persistedPaneID, "--source", "visible", "--lines", "20").CombinedOutput()
		t.Fatalf("orchestrator: agent did not output secret %q within 120s: %v\n%s\npane (visible):\n%s",
			secretStr, err, orchWait, readOut)
	}
	orchLine := parseMatchedLine(string(orchWait))
	t.Logf("orchestrator turn complete; matched line: %q (secret=%s)", orchLine, secretStr)
	if !strings.Contains(orchLine, secretStr) {
		t.Fatalf("orchestrator: matched line %q does not contain secret %s", orchLine, secretStr)
	}

	// --- 6. OPERATOR TURN: type into the same pane as a human would. ---
	//
	// The operator's question does NOT contain the secret number. The
	// continuity match token is the number wrapped in angle brackets
	// ("<N>"), which also does not appear in this question (the question
	// asks for the bracket format but uses the literal text "<number>",
	// not the actual value). A freshly spawned or replaced agent cannot
	// produce the correct "<N>" response.
	operatorQuestion := "Please output the integer you just read, wrapped in angle brackets like this: <number>. Use the actual number, not the word 'number'."
	// continuityToken is the token the agent must produce — "<secretStr>".
	// It does not appear in the operator question (which says "<number>") or
	// in the orchestrator output (which is the plain integer, not bracketed).
	continuityToken := "<" + secretStr + ">"
	sendOut, err := exec.Command("herdr", "pane", "send-text", persistedPaneID, operatorQuestion).CombinedOutput()
	if err != nil {
		t.Fatalf("herdr pane send-text (operator): %v\n%s", err, sendOut)
	}
	// Settle delay matches herdrPaneSubmitToAgent so the text is placed in
	// claude's input box before Enter is sent.
	time.Sleep(briefSettleDelay)
	keysOut, err := exec.Command("herdr", "pane", "send-keys", persistedPaneID, "Enter").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr pane send-keys (operator): %v\n%s", err, keysOut)
	}

	// --- 7. CONTINUITY ASSERTION: the surviving agent recalls the token. ---
	//
	// This is the load-bearing assertion of the test. A restarted or replaced
	// agent cannot recall the orchestrator's earlier turn — the secret file
	// content was never in its context. Only the ORIGINAL, SURVIVING agent
	// can answer from memory. Recall = survival.
	//
	// Match token: "<secretStr>" (the number in angle brackets). It does not
	// appear in the operator's question ("<number>" is the template, not the
	// value) and does not appear in the orchestrator's plain-number output.
	// So this cannot fire on any echoed text or stale scrollback.
	//
	// NOTE on --source recent: it does NOT scope the search to output
	// produced after this call. `herdr pane wait-output --help` is explicit:
	// "The selected snapshot is searched immediately, including existing
	// output, then polled." So the protection against a stale match comes
	// ENTIRELY from the token design above — "<N>" appears nowhere in the
	// brief, the operator question, or the orchestrator's plain-number reply.
	//
	// This matters if anyone changes those strings: make the orchestrator turn
	// emit the bracketed form too and this test passes without any takeover
	// happening, with nothing in the diff to suggest it. Mutation 1 (operator
	// text never sent) is what pins that, and it must be re-run after any edit
	// to the brief or the operator question.
	contWait, err := exec.Command(
		"herdr", "pane", "wait-output",
		persistedPaneID,
		"--match", continuityToken,
		"--source", "recent",
		"--timeout", "90000",
	).CombinedOutput()
	if err != nil {
		readOut, _ := exec.Command("herdr", "pane", "read", persistedPaneID, "--source", "visible", "--lines", "30").CombinedOutput()
		t.Fatalf("continuity: agent did not output %q within 90s: %v\n%s\npane (visible):\n%s",
			continuityToken, err, contWait, readOut)
	}
	contLine := parseMatchedLine(string(contWait))
	t.Logf("continuity assertion passed; matched line: %q (token=%s)", contLine, continuityToken)
	if !strings.Contains(contLine, continuityToken) {
		t.Fatalf("continuity: matched line %q does not contain token %s", contLine, continuityToken)
	}
	// Sanity: the matched line must not be the echoed operator prompt.
	// The operator's text does not contain the continuity token, so this
	// would indicate a herdr matching bug rather than a test design flaw.
	if strings.Contains(contLine, operatorQuestion) {
		t.Fatalf("continuity: matched line %q equals the echoed operator prompt — match is not mutation-sensitive", contLine)
	}

	// --- 8. ORCHESTRATOR VIEW: herdr agent list still reports the agent. ---
	//
	// herdrPaneReportAgent is called inside herdrPluginSpaceAgent (step 3).
	// If that call were deleted, the agent would not appear here even though
	// it is running, and this assertion would fail. Mutation 2.
	agentListOut, err := exec.Command("herdr", "agent", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr agent list: %v\n%s", err, agentListOut)
	}
	t.Logf("herdr agent list: %s", agentListOut)
	if !strings.Contains(string(agentListOut), persistedPaneID) {
		t.Fatalf("herdr agent list does not contain pane %q; orchestrator view did not survive the operator takeover\nagent list: %s",
			persistedPaneID, agentListOut)
	}

	// --- 9. ORCHESTRATOR VIEW: the pane still matches the claude ready footer. ---
	//
	// After the agent responded to the operator and returned to its prompt,
	// claude's ready footer ("shift+tab to cycle" for autonomous mode) should
	// be visible. A dead agent would not produce this. This confirms that the
	// agent's UI is intact and the orchestrator could continue driving it.
	footerMatch := claudeReadyMatch(true /* autonomous */)
	footerWait, err := exec.Command(
		"herdr", "pane", "wait-output",
		persistedPaneID,
		"--match", footerMatch,
		"--source", "recent",
		"--timeout", "60000",
	).CombinedOutput()
	if err != nil {
		readOut, _ := exec.Command("herdr", "pane", "read", persistedPaneID, "--source", "visible", "--lines", "10").CombinedOutput()
		t.Fatalf("orchestrator view: claude ready footer %q not seen within 60s after operator takeover: %v\n%s\npane:\n%s",
			footerMatch, err, footerWait, readOut)
	}
	t.Logf("AC-4 complete: agent survived operator takeover; pane=%s secret=%s recalled; herdr view intact",
		persistedPaneID, secretStr)
}
