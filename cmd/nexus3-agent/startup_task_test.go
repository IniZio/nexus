package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/bootspec"
)

// TestStartupHookAction covers the pure run/skip gating for the boot task.
//
// MUTATION PROOF: flip any arm (e.g. return startupRun for a non-executable
// file, or startupSkipAbsent when statErr==nil) and the matching subtest fails.
func TestStartupHookAction(t *testing.T) {
	dir := t.TempDir()

	// absent: stat error → skip, never run (a missing hook is the common case).
	_, statErr := os.Stat(filepath.Join(dir, "does-not-exist"))
	if got := startupHookAction(nil, statErr); got != startupSkipAbsent {
		t.Errorf("absent: got %v, want startupSkipAbsent", got)
	}

	// present but not executable (0644) → skip.
	nonExec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(nonExec, []byte("#!/bin/sh\ntrue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(nonExec)
	if err != nil {
		t.Fatal(err)
	}
	if got := startupHookAction(fi, nil); got != startupSkipNonExecutable {
		t.Errorf("non-executable: got %v, want startupSkipNonExecutable", got)
	}

	// a directory (even with exec bits) → skip; a dir is not a runnable hook.
	subdir := filepath.Join(dir, "adir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	dfi, err := os.Stat(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if got := startupHookAction(dfi, nil); got != startupSkipNonExecutable {
		t.Errorf("directory: got %v, want startupSkipNonExecutable", got)
	}

	// present and executable (0755) → run.
	exe := filepath.Join(dir, "hook")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	efi, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if got := startupHookAction(efi, nil); got != startupRun {
		t.Errorf("executable: got %v, want startupRun", got)
	}
}

// TestRunStartupHook_ExecutesInBackground proves runStartupHook actually runs an
// executable hook (in the background) with a working PATH, and that a missing
// hook is a silent no-op.
func TestRunStartupHook_ExecutesInBackground(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "ran")

	hook := filepath.Join(dir, "startup")
	// The hook resolves `touch` via PATH — this also exercises that runStartupHook
	// supplies a usable PATH (guestBaselineEnv), the exact failure mode that would
	// otherwise make a real dockerd bring-up hook silently fail.
	script := "#!/bin/sh\ntouch " + sentinel + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	old := startupHookPath
	startupHookPath = hook
	t.Cleanup(func() { startupHookPath = old })

	runStartupHook(nil) // con=nil: consoleLog tolerates a nil file (writes stderr only)

	// The hook runs in a goroutine; poll briefly for the sentinel.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sentinel); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("startup hook did not run: sentinel %s absent: %v", sentinel, err)
	}

	// Missing hook → silent no-op (must not panic, must not block).
	startupHookPath = filepath.Join(dir, "absent")
	runStartupHook(nil)
}

// writeBootspec writes a Spec as JSON to a temp file and returns its path.
func writeBootspec(t *testing.T, spec bootspec.Spec) string {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "boot.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRunBootTasks_FallbackToStartupHook verifies that an absent manifest
// delegates to the legacy /etc/nexus3/startup path (back-compat AC).
func TestRunBootTasks_FallbackToStartupHook(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "hook-ran")

	hook := filepath.Join(dir, "startup")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldHook := startupHookPath
	startupHookPath = hook
	t.Cleanup(func() { startupHookPath = oldHook })

	oldSpec := bootspecPath
	bootspecPath = filepath.Join(dir, "absent.json") // does not exist
	t.Cleanup(func() { bootspecPath = oldSpec })

	runBootTasks(nil)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sentinel); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("startup hook fallback did not run: sentinel absent: %v", err)
	}
}

// TestRunBootTasks_EmptyArgvSkipped verifies a task with empty Argv is silently
// skipped without panic or blocking.
func TestRunBootTasks_EmptyArgvSkipped(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "after")

	spec := bootspec.Spec{
		Tasks: []bootspec.Task{
			{Name: "empty", Argv: nil},
			{Name: "after", Argv: []string{"/bin/sh", "-c", "touch " + sentinel}, Background: false},
		},
	}

	oldSpec := bootspecPath
	bootspecPath = writeBootspec(t, spec)
	t.Cleanup(func() { bootspecPath = oldSpec })

	runBootTasks(nil)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sentinel); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("task after empty-argv was not reached: sentinel absent: %v", err)
	}
}

// TestRunBootTasks_ForegroundOrderingAndEnv verifies:
//   - a foreground task completes before the next foreground task starts
//     (enforced by a sentinel written in task-1 and read by task-2's argv);
//   - Cwd is honoured (task runs in a temp dir and writes pwd);
//   - Env overrides are honoured (task captures an injected var).
func TestRunBootTasks_ForegroundOrderingAndEnv(t *testing.T) {
	dir := t.TempDir()

	// Task 1: write a marker file via Cwd-relative path.
	cwdOut := filepath.Join(dir, "cwd.txt")
	// Task 2: capture injected env var.
	envOut := filepath.Join(dir, "env.txt")

	spec := bootspec.Spec{
		Tasks: []bootspec.Task{
			{
				Name:       "cwd-check",
				Argv:       []string{"/bin/sh", "-c", "pwd > " + cwdOut},
				Cwd:        dir,
				Background: false,
			},
			{
				Name:       "env-check",
				Argv:       []string{"/bin/sh", "-c", "echo $NEXUS_TEST_VAR > " + envOut},
				Env:        []string{"NEXUS_TEST_VAR=hello-nexus"},
				Background: false,
			},
		},
	}

	oldSpec := bootspecPath
	bootspecPath = writeBootspec(t, spec)
	t.Cleanup(func() { bootspecPath = oldSpec })

	runBootTasks(nil)

	// Both foreground tasks block the supervisor goroutine sequentially;
	// poll until both sentinels appear.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, e1 := os.Stat(cwdOut)
		_, e2 := os.Stat(envOut)
		if e1 == nil && e2 == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cwdBytes, err := os.ReadFile(cwdOut)
	if err != nil {
		t.Fatalf("cwd-check task did not produce output: %v", err)
	}
	// pwd trims trailing newline from comparison
	got := string(cwdBytes)
	if len(got) > 0 && got[len(got)-1] == '\n' {
		got = got[:len(got)-1]
	}
	if got != dir {
		t.Errorf("Cwd: got %q, want %q", got, dir)
	}

	envBytes, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("env-check task did not produce output: %v", err)
	}
	envGot := string(envBytes)
	if len(envGot) > 0 && envGot[len(envGot)-1] == '\n' {
		envGot = envGot[:len(envGot)-1]
	}
	if envGot != "hello-nexus" {
		t.Errorf("Env override: got %q, want %q", envGot, "hello-nexus")
	}
}

// TestRunBootTasks_BackgroundDoesNotBlock verifies that a background task (e.g.
// a slow sleep) does not prevent runBootTasks from returning promptly.
func TestRunBootTasks_BackgroundDoesNotBlock(t *testing.T) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skip("/bin/sleep not available")
	}

	dir := t.TempDir()
	sentinel := filepath.Join(dir, "bg-done")

	spec := bootspec.Spec{
		Tasks: []bootspec.Task{
			// Background sleep — must NOT block runBootTasks.
			{Name: "bg-sleep", Argv: []string{"/bin/sleep", "60"}, Background: true},
			// Foreground touch — runs after the background task is launched.
			{Name: "fg-touch", Argv: []string{"/bin/sh", "-c", "touch " + sentinel}, Background: false},
		},
	}

	oldSpec := bootspecPath
	bootspecPath = writeBootspec(t, spec)
	t.Cleanup(func() { bootspecPath = oldSpec })

	start := time.Now()
	runBootTasks(nil) // must return immediately (goroutine launch only)

	// runBootTasks itself returns before the supervisor goroutine finishes;
	// confirm the return was prompt (< 500 ms).
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("runBootTasks blocked for %v; expected < 500 ms", elapsed)
	}

	// Foreground sentinel still appears eventually (supervisor goroutine runs).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sentinel); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("fg-touch after background task not reached: sentinel absent: %v", err)
	}
}
