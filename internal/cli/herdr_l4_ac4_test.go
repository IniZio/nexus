//go:build herdr_live

// AC-4: operator can take over any agent without killing it or losing orchestrator
// view. Chain: nexus3 create --mount → sandbox with source mounted; nexus3 herdr agent
// → guest agent in herdr pane; ORCHESTRATOR TURN: wait for STEP1= (non-echoable secret
// proves execution); OPERATOR TURN: send text to pane; CONTINUITY: agent recalls token
// (proves survival, not just presence); herdr agent list → orchestrator view intact;
// herdr ready footer → agent UI alive.
//
// Why secret non-echoable: file PATH in brief (echoed), CONTENT not. Agent execution of
// Read/Bash produces secret in transcript. Orchestrator token is number itself, cannot
// fire on echoed brief. Continuity token "<N>" not in operator question ("<number>" is
// template) or orchestrator plain output. So both matches prove execution/survival, not
// stale scrollback.
//
// MUTATIONS: 1. Drop operator text → token never appears → timeout FAIL. 2. Drop
// herdrPaneReportAgent call → herdr agent list missing pane_id FAIL. 3. Kill agent before
// operator turn → cannot answer → continuity timeout FAIL.
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
	liveSkip(t, "AC-4: requires interactive herdr session with KVM; isolated mode cannot satisfy this")
	if _, err := os.Stat("/dev/kvm"); err != nil {
		liveSkip(t, "AC-4: /dev/kvm not available: %v", err)
	}
	beforeWorkspaces := herdrWorkspaceList(t)
	if !strings.Contains(beforeWorkspaces, "workspace_list") {
		liveSkip(t, "AC-4: herdr is not reachable (herdr workspace list did not return a workspace_list)")
	}
	t.Logf("BEFORE: %s", beforeWorkspaces)

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

	// SAFETY: unique handle every run; secret is non-echoable (file content,
	// not visible in brief), so agent must genuinely execute to produce it.
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

	// nexus3 create --mount --agent claude-code: --agent seeds the Anthropic
	// OAuth token via MITM broker (D-PDE-02: fail-closed, no GitHub flags).
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

	// PRODUCTION path: starts sandbox, opens workspace/pane, launches claude,
	// delivers brief (registers agent in herdr's tracker).
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

	// Read pane ID from persisted binding (not from space-agent's stdout).
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

	// ORCHESTRATOR TURN: wait for secret number (non-echoable, proves execution).
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

	// OPERATOR TURN: operator's question does NOT contain secret number.
	// Continuity token "<N>" proves recall (not in question or orchestrator output).
	operatorQuestion := "Please output the integer you just read, wrapped in angle brackets like this: <number>. Use the actual number, not the word 'number'."
	continuityToken := "<" + secretStr + ">"
	sendOut, err := exec.Command("herdr", "pane", "send-text", persistedPaneID, operatorQuestion).CombinedOutput()
	if err != nil {
		t.Fatalf("herdr pane send-text (operator): %v\n%s", err, sendOut)
	}
	time.Sleep(briefSettleDelay)
	keysOut, err := exec.Command("herdr", "pane", "send-keys", persistedPaneID, "Enter").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr pane send-keys (operator): %v\n%s", err, keysOut)
	}

	// CONTINUITY ASSERTION: surviving agent recalls "<N>", proves original
	// exec. Token never appears in brief, operator question, or orchestrator
	// plain output. Protection against stale match is ENTIRELY token design.
	// Mutation 1: drop operator text → token never appears → times out.
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
	if strings.Contains(contLine, operatorQuestion) {
		t.Fatalf("continuity: matched line %q equals the echoed operator prompt — match is not mutation-sensitive", contLine)
	}

	// ORCHESTRATOR VIEW: herdr agent list still reports the agent (Mutation 2).
	agentListOut, err := exec.Command("herdr", "agent", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr agent list: %v\n%s", err, agentListOut)
	}
	t.Logf("herdr agent list: %s", agentListOut)
	if !strings.Contains(string(agentListOut), persistedPaneID) {
		t.Fatalf("herdr agent list does not contain pane %q; orchestrator view did not survive the operator takeover\nagent list: %s",
			persistedPaneID, agentListOut)
	}

	// ORCHESTRATOR VIEW: claude's ready footer visible after operator takeover.
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
