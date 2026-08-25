package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
