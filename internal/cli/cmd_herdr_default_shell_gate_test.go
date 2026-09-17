package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

// markerWriter records everything written and creates markerPath the first
// time trigger appears in the stream. The fake winner blocks on that marker,
// so the test passes only when the log reaches the pane WHILE the subprocess
// is still running — a tail that merely drains at exit times the winner out.
type markerWriter struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	trigger    string
	markerPath string
	fired      bool
}

func (m *markerWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf.Write(p)
	if !m.fired && strings.Contains(m.buf.String(), m.trigger) {
		m.fired = true
		_ = os.WriteFile(m.markerPath, nil, 0o644)
	}
	return len(p), nil
}

func (m *markerWriter) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}

// stubWinnerCreate replaces the auto-create subprocess with a shell script that
// plays the concurrent hook pane: it appends build lines to the provisioning
// log at logPath, waits for markerPath (written by the pane once it has SEEN
// the first line), then writes the binding for wsID into storeRoot and exits.
func stubWinnerCreate(t *testing.T, storeRoot, wsID, logPath, markerPath string) {
	t.Helper()
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus:repo/gate",
		HerdrWorkspaceID: wsID,
		SandboxHandle:    "repo/gate",
		SandboxID:        "sb-GATE",
		WorktreeManaged:  true,
	}
	data, err := json.Marshal([]HerdrSpaceBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	bindingsPath := filepath.Join(storeRoot, "herdr-space-bindings.json")
	script := fmt.Sprintf(`
printf 'worktree-sandbox: build source: --file /wt\n' >> %q
i=0
while [ ! -e %q ]; do
  i=$((i+1)); if [ "$i" -gt 60 ]; then printf 'WINNER-TIMEOUT: pane never saw the log\n' >> %q; break; fi
  sleep 0.05
done
printf 'worktree-sandbox: bound workspace %s\n' >> %q
printf '%%s' %q > %q
`, logPath, markerPath, logPath, wsID, logPath, string(data), bindingsPath)
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, _ string, args ...string) *osexec.Cmd {
		if len(args) < 4 || args[0] != "herdr" || args[1] != "worktree-sandbox" || args[2] != "--auto" || args[3] != wsID {
			t.Errorf("auto-create argv = %q, want herdr worktree-sandbox --auto %s", args, wsID)
		}
		return osexec.CommandContext(ctx, "sh", "-c", script)
	}
	t.Cleanup(func() { herdrExecCommandContext = old })
}

func fastCreateLogPoll(t *testing.T) {
	t.Helper()
	old := herdrWtCreateLogPollInterval
	herdrWtCreateLogPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { herdrWtCreateLogPollInterval = old })
}

func TestHerdrDefaultShell_FirstTabStreamsProvisioningLogThenEntersGuest(t *testing.T) {
	fastCreateLogPoll(t)
	root := t.TempDir()
	const wsID = "w1M"
	logPath := herdrWtCreateLogPath(root, wsID)
	marker := filepath.Join(root, "pane-saw-log")
	stubWinnerCreate(t, root, wsID, logPath, marker)

	oldPred := herdrAutoCreatePredicateFn
	herdrAutoCreatePredicateFn = func([]HerdrSpaceBinding) bool { return true }
	t.Cleanup(func() { herdrAutoCreatePredicateFn = oldPred })
	childArgv := stubWtSeams(t)

	pane := &markerWriter{trigger: "build source", markerPath: marker}
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				_, _ = pane.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()

	svc := &fakeDialableGetter{fakeDefaultShellGetter: fakeDefaultShellGetter{sb: domain.Sandbox{
		State:      domain.Running,
		LiveMounts: []domain.LiveMount{{GuestPath: "/workspace"}},
	}}}
	cap := &capturedExec{}
	coreErr := runCore(context.Background(), wsGetenv(wsID, nil), root, svc, cap.fn)
	os.Stderr = oldStderr
	w.Close()
	<-copied
	if coreErr != nil {
		t.Fatalf("core: %v", coreErr)
	}

	out := pane.String()
	if strings.Contains(out, "WINNER-TIMEOUT") {
		t.Fatalf("provisioning log was not streamed into the pane while the winner ran:\n%s", out)
	}
	for _, want := range []string{"worktree-sandbox: build source: --file /wt", "worktree-sandbox: bound workspace " + wsID} {
		if !strings.Contains(out, want) {
			t.Errorf("pane output missing %q:\n%s", want, out)
		}
	}
	if cap.calls != 0 {
		t.Fatalf("host shell exec'd (argv0=%q) — a pre-ready pane must never get a host prompt", cap.argv0)
	}
	got := strings.Join(*childArgv, " ")
	if !strings.Contains(got, "exec --pty --cwd /workspace repo/gate /bin/bash --login") {
		t.Errorf("guest exec argv = %q, want nexus exec --pty --cwd /workspace repo/gate", got)
	}
}

func TestHerdrTailFileUntil_SkipsStaleContentAndFollowsTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "create.log")
	if err := os.WriteFile(path, []byte("STALE line from a previous workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		herdrTailFileUntil(path, &out, done, 10*time.Millisecond, 10*time.Minute)
	}()
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(path, []byte("fresh 1\n"), 0o644); err != nil { // truncate + rewrite, as pane.sh does
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("fresh 2\n")
	f.Close()
	close(done)
	<-finished
	got := out.String()
	if strings.Contains(got, "STALE") {
		t.Errorf("stale content replayed: %q", got)
	}
	if got != "fresh 1\nfresh 2\n" {
		t.Errorf("tail output = %q, want fresh 1 + fresh 2", got)
	}
}

func TestHerdrDefaultShell_PaneScriptWritesCreateLogAtGoPath(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "bin", "pane.sh"))
	if err != nil {
		t.Skipf("pane.sh not readable from this cwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "pane.sh"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\necho \"shim: $*\"\necho \"worktree-sandbox: build step from shim\"\nexit ${SHIM_EXIT:-0}\n"
	if err := os.WriteFile(filepath.Join(dir, "nexus-shim.sh"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	stateHome := filepath.Join(dir, "state")
	const wsID = "w9"
	run := func(shimExit string) (string, int) {
		cmd := osexec.Command("sh", filepath.Join(dir, "bin", "pane.sh"), "worktree-sandbox")
		cmd.Env = append(os.Environ(), "HERDR_WORKSPACE_ID="+wsID, "XDG_STATE_HOME="+stateHome, "NEXUS_WORKTREE_AUTO=1", "SHIM_EXIT="+shimExit)
		cmd.Stdin = strings.NewReader("\n")
		out, runErr := cmd.CombinedOutput()
		code := 0
		if ee, ok := runErr.(*osexec.ExitError); ok {
			code = ee.ExitCode()
		} else if runErr != nil {
			t.Fatalf("run pane.sh: %v\n%s", runErr, out)
		}
		return string(out), code
	}

	out, code := run("0")
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	logPath := herdrWtCreateLogPath(filepath.Join(stateHome, "nexus"), wsID)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("provisioning log not at the Go-side path %s: %v\npane output:\n%s", logPath, err, out)
	}
	for _, want := range []string{"shim: herdr worktree-sandbox --auto " + wsID, "worktree-sandbox: build step from shim"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log missing %q:\n%s", want, data)
		}
	}

	out, code = run("3")
	if code != 3 {
		t.Errorf("exit %d, want 3 (shim status must cross the tee pipe):\n%s", code, out)
	}
	data, _ = os.ReadFile(logPath)
	if n := strings.Count(string(data), "build step from shim"); n != 1 {
		t.Errorf("log has %d build lines after second run, want 1 (per-run truncate)", n)
	}
}
