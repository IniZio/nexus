//go:build linux

package supervisor

// TestWatchdogPipeEOFOnParentSIGKILL exercises the parent-pipe watchdog with
// a REAL process being REALLY SIGKILLed.
//
// # Background
//
// supervisor.go §4b starts a goroutine that blocks on pipeR.Read(). When the
// write end of the pipe closes (because the CLI parent exited for any reason,
// including SIGKILL), the goroutine reads EOF and calls cancel(), which causes
// awaitShutdown to return shutdownBySignal and the graceful-shutdown path to
// execute.
//
// Existing tests in supervisor_test.go cover the mock path: they call
// awaitShutdown directly with synthetic contexts and channels. This file adds a
// REAL-process test: a real child process holds the write end, it is killed with
// SIGKILL, and a real "supervisor" subprocess (also the test binary, re-execed
// in helper mode) verifies that the watchdog goroutine fires.
//
// # Architecture
//
//	[test process]
//	  ├─ holder subprocess   (holds pipeW open; simulates the CLI parent)
//	  └─ supervisor subprocess (holds pipeR; runs watchdog goroutine; exits 0 on fire)
//
// # Coupling
//
// The watchdog goroutine is extracted into the package-level startParentWatchdog
// function in supervisor.go. The supervisor subprocess role calls it directly,
// so the break-and-restore proof is valid against supervisor.go.
//
// A proposed spec change that would close the coupling gap is noted at the
// bottom of this file.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestMain is the package-level entrypoint. In normal mode it delegates to the
// Go test runner. When the binary is re-execed as a helper subprocess it
// dispatches to runWatchdogHelper and exits without running any tests.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		runWatchdogHelper()
		// runWatchdogHelper always calls os.Exit; this is unreachable.
		panic("runWatchdogHelper returned")
	}
	os.Exit(m.Run())
}

// runWatchdogHelper is the subprocess entrypoint. It reads GO_WATCHDOG_HELPER_ROLE
// and dispatches to one of two roles:
//
//   - "holder":     holds the write end of a pipe (fd WATCHDOG_PIPE_W_FD) and
//     blocks until killed. Simulates the CLI parent process.
//
//   - "supervisor": holds the read end of a pipe (fd WATCHDOG_PIPE_R_FD), calls
//     the production startParentWatchdog, and blocks on awaitShutdown. Exits 0
//     if context is cancelled within 10 s (watchdog fired), exits 1 otherwise.
func runWatchdogHelper() {
	switch role := os.Getenv("GO_WATCHDOG_HELPER_ROLE"); role {

	case "holder":
		// Hold pipeW open and sleep until killed.
		// The fd arrives as ExtraFiles[0] → fd 3 in this process.
		fd, err := strconv.Atoi(os.Getenv("WATCHDOG_PIPE_W_FD"))
		if err != nil || fd <= 0 {
			fmt.Fprintln(os.Stderr, "holder: bad WATCHDOG_PIPE_W_FD:", os.Getenv("WATCHDOG_PIPE_W_FD"))
			os.Exit(1)
		}
		f := os.NewFile(uintptr(fd), "pipe-write-end") //nolint:gosec // fd from parent via ExtraFiles
		if f == nil {
			fmt.Fprintln(os.Stderr, "holder: os.NewFile returned nil for fd", fd)
			os.Exit(1)
		}
		// Keep f alive (prevent GC close) and block until SIGKILL.
		_ = f
		select {} // never returns; process exits when test sends SIGKILL

	case "supervisor":
		// Receive the pipe read end and run the watchdog goroutine.
		// The fd arrives as ExtraFiles[0] → fd 3 in this process.
		fd, err := strconv.Atoi(os.Getenv("WATCHDOG_PIPE_R_FD"))
		if err != nil || fd <= 0 {
			fmt.Fprintln(os.Stderr, "supervisor: bad WATCHDOG_PIPE_R_FD:", os.Getenv("WATCHDOG_PIPE_R_FD"))
			os.Exit(1)
		}
		pipeR := os.NewFile(uintptr(fd), "parent-watchdog-pipe") //nolint:gosec // fd from parent via ExtraFiles
		if pipeR == nil {
			fmt.Fprintln(os.Stderr, "supervisor: os.NewFile returned nil for fd", fd)
			os.Exit(1)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Call the production function — this is the coupling that makes the
		// break-and-restore proof valid against supervisor.go, not a copy.
		startParentWatchdog(pipeR, "test-sandbox", cancel)

		// awaitShutdown is the extracted production function called in RunDetached
		// step 7. We block on it here to verify that the goroutine's cancel() call
		// causes it to return shutdownBySignal — the same transition RunDetached
		// depends on before tearing down the VM.
		stopCh := make(chan struct{}) // never closed (no IPC in this helper)
		result := make(chan shutdownCause, 1)
		go func() { result <- awaitShutdown(ctx, stopCh) }()

		select {
		case cause := <-result:
			if cause == shutdownBySignal {
				os.Exit(0) // watchdog fired correctly — test will see exit 0
			}
			fmt.Fprintln(os.Stderr, "supervisor helper: unexpected shutdownCause:", cause)
			os.Exit(1)

		case <-time.After(10 * time.Second):
			fmt.Fprintln(os.Stderr, "supervisor helper: timed out after 10 s — watchdog goroutine did not cancel context")
			os.Exit(1)
		}

	default:
		fmt.Fprintln(os.Stderr, "runWatchdogHelper: unknown role:", role)
		os.Exit(1)
	}
}

// TestWatchdogPipeEOFOnParentSIGKILL verifies that a real SIGKILL of the
// parent process causes the watchdog goroutine to fire and the supervisor to
// initiate a graceful shutdown.
//
// The test creates a real os.Pipe, spawns two subprocesses (holder and
// supervisor), sends SIGKILL to the holder, and asserts the supervisor
// subprocess exits 0 within a 15-second deadline.
func TestWatchdogPipeEOFOnParentSIGKILL(t *testing.T) {
	// ── 1. Create the watchdog pipe ──────────────────────────────────────────
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	// ── 2. Spawn the "holder" — simulates the CLI parent holding pipeW ───────
	holderCmd := exec.Command(os.Args[0])
	holderCmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"GO_WATCHDOG_HELPER_ROLE=holder",
		"WATCHDOG_PIPE_W_FD=3", // ExtraFiles[0] → fd 3 in child
	)
	holderCmd.ExtraFiles = []*os.File{pipeW}
	holderCmd.Stdout = os.Stderr // route helper output to test stderr for visibility
	holderCmd.Stderr = os.Stderr
	if startErr := holderCmd.Start(); startErr != nil {
		pipeR.Close()
		pipeW.Close()
		t.Fatalf("start holder subprocess: %v", startErr)
	}
	// Close the parent's copy of pipeW. Only the holder now holds the write end.
	pipeW.Close()

	// ── 3. Spawn the "supervisor" — holds pipeR and runs the watchdog goroutine ─
	var supStderr bytes.Buffer
	supCmd := exec.Command(os.Args[0])
	supCmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"GO_WATCHDOG_HELPER_ROLE=supervisor",
		"WATCHDOG_PIPE_R_FD=3", // ExtraFiles[0] → fd 3 in child
	)
	supCmd.ExtraFiles = []*os.File{pipeR}
	supCmd.Stdout = os.Stderr
	supCmd.Stderr = &supStderr
	if startErr := supCmd.Start(); startErr != nil {
		pipeR.Close()
		holderCmd.Process.Kill() //nolint:errcheck
		holderCmd.Wait()         //nolint:errcheck
		t.Fatalf("start supervisor subprocess: %v", startErr)
	}
	// Close the parent's copy of pipeR. Only the supervisor now holds the read end.
	pipeR.Close()

	// Ensure all subprocesses are reaped even on test failure.
	t.Cleanup(func() {
		supCmd.Process.Kill() //nolint:errcheck
		supCmd.Wait()         //nolint:errcheck
		holderCmd.Process.Kill() //nolint:errcheck
		holderCmd.Wait()         //nolint:errcheck
	})

	// ── 4. SIGKILL the holder — this is the "CLI dying under SIGKILL" event ──
	if killErr := holderCmd.Process.Signal(syscall.SIGKILL); killErr != nil {
		t.Fatalf("SIGKILL holder: %v", killErr)
	}
	holderCmd.Wait() //nolint:errcheck // reap zombie; ignore SIGKILL exit status

	// ── 5. Assert supervisor exits 0 within a generous deadline ──────────────
	//
	// Successful path (watchdog intact):
	//   SIGKILL holder → OS closes holder's pipeW →
	//   supervisor goroutine reads EOF → cancel() → awaitShutdown returns
	//   shutdownBySignal → supervisor exits 0 (typically < 100 ms)
	//
	// Failure path (watchdog broken — cancel() removed from goroutine):
	//   goroutine blocks waiting for EOF that never triggers cancel() →
	//   10 s internal timeout fires → supervisor exits 1
	const maxWait = 15 * time.Second
	done := make(chan error, 1)
	go func() { done <- supCmd.Wait() }()

	select {
	case waitErr := <-done:
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				t.Errorf(
					"watchdog mechanism FAILED: supervisor subprocess exited %d\n"+
						"  expected: exit 0 — watchdog goroutine received EOF on pipe, called cancel(),\n"+
						"            awaitShutdown returned shutdownBySignal\n"+
						"  got:      exit %d — watchdog did not cancel context within 10 s after parent SIGKILL\n"+
						"  supervisor stderr:\n%s",
					exitErr.ExitCode(), exitErr.ExitCode(), supStderr.String())
			} else {
				t.Errorf("supervisor Wait returned non-ExitError: %v\nsupervisor stderr:\n%s",
					waitErr, supStderr.String())
			}
		}
		// exit 0: watchdog goroutine fired as expected

	case <-time.After(maxWait):
		supCmd.Process.Kill() //nolint:errcheck
		t.Errorf(
			"watchdog mechanism FAILED: supervisor subprocess did not exit within %s\n"+
				"  possible causes: watchdog goroutine not started, pipe EOF did not propagate,\n"+
				"  or supervisor helper blocked in awaitShutdown indefinitely\n"+
				"  supervisor stderr:\n%s",
			maxWait, supStderr.String())
	}
}

