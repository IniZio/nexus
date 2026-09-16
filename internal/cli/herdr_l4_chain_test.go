//go:build herdr_live

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

func ac6Cmd(binary string, args ...string) *exec.Cmd {
	cmd := exec.Command(binary, args...)
	cmd.Env = ac6Env()
	return cmd
}

func TestHerdrPlugin_L4_AC6Chain(t *testing.T) {
	liveSkip(t, "AC-6: requires interactive herdr session with KVM; isolated mode cannot satisfy this")
	if _, err := os.Stat("/dev/kvm"); err != nil {
		liveSkip(t, "AC-6: /dev/kvm not available: %v", err)
	}
	beforeWorkspaces := herdrWorkspaceList(t)
	if !strings.Contains(beforeWorkspaces, "workspace_list") {
		liveSkip(t, "AC-6: herdr is not reachable (herdr workspace list did not return a workspace_list)")
	}
	t.Logf("BEFORE: %s", beforeWorkspaces)

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

	var wsID, wsLabel string

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
			t.Errorf("herdr workspace list after cleanup did not return a workspace_list (got %q) — leak check inconclusive; check manually", afterWorkspaces)
		} else if strings.Contains(afterWorkspaces, label) {
			t.Errorf("scratch workspace (label %q) survived cleanup; list: %s", label, afterWorkspaces)
		}
		t.Logf("AFTER: %s", afterWorkspaces)
	})

	image := os.Getenv("NEXUS3_AC6_IMAGE")
	if image == "" {
		image = herdrDefaultImage
	}
	createOut, err := ac6Cmd(binary, "create", handle,
		"--image", image,
		"--mount", srcDir+":"+guestMount+":ro",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("nexus3 create --mount: %v\n%s\n(check NEXUS3_KERNEL_PATH is set and %q is a cached image)",
			err, createOut, image)
	}
	t.Logf("nexus3 create: %s", createOut)

	openOut, err := ac6Cmd(binary, "__herdr-plugin", "space-open-pane", handle).CombinedOutput()
	if err != nil {
		t.Fatalf("__herdr-plugin space-open-pane: %v\n%s", err, openOut)
	}
	t.Logf("space-open-pane: %s", openOut)
	returnedPaneID := parseOpenedPaneID(string(openOut))
	if returnedPaneID == "" {
		t.Fatalf("space-open-pane did not report a pane_id (\"opened pane: pane_id=...\" line missing or empty); output: %s", openOut)
	}

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
	if persistedPaneID != returnedPaneID {
		t.Fatalf("persisted pane_id %q != pane_id returned by space-open-pane %q", persistedPaneID, returnedPaneID)
	}

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

	waitOut, err := exec.Command(
		"herdr", "pane", "wait-output",
		persistedPaneID,
		"--match", token,
		"--source", "recent",
		"--timeout", "120000",
	).CombinedOutput()
	if err != nil {
		readOut, _ := exec.Command("herdr", "pane", "read", persistedPaneID, "--source", "visible", "--lines", "20").CombinedOutput()
		t.Fatalf("herdr pane wait-output did not find token %q within 120s: %v\n%s\npane (visible):\n%s",
			token, err, waitOut, readOut)
	}

	matchedLine := parseMatchedLine(string(waitOut))
	if matchedLine == "" {
		t.Fatalf("wait-output succeeded but result.matched_line was empty; raw: %s", waitOut)
	}
	if strings.Contains(matchedLine, strings.TrimSpace(cmdLine)) {
		t.Fatalf("matched line %q contains the echoed prompt text %q — the match is not mutation-sensitive", matchedLine, strings.TrimSpace(cmdLine))
	}
	if !strings.Contains(matchedLine, token) {
		t.Fatalf("matched line %q does not contain the expected token %q (sum of %d + %d)", matchedLine, token, a, b)
	}
	t.Logf("AC-6 chain complete: %d + %d = %s, matched line: %q", a, b, token, matchedLine)
}

func parseOpenedPaneID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "opened pane: pane_id="); ok {
			return v
		}
	}
	return ""
}

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
