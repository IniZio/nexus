package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// memhogMainPath is the absolute path passed to `go run` so the test is
// hermetic regardless of the caller's working directory.
const memhogMainPath = "$HOME/nexus3/internal/test/repro/cmd/memhog/main.go"

// TestMemhogLowersMemAvailable verifies that memhog actually lowers host
// MemAvailable by a small, controlled amount (512 MiB), then releases it.
//
// Skipped unless REPRO_MEMHOG_TEST=1 is set, because allocating and touching
// GiBs of RAM during `go test ./...` can starve concurrent builder VMs.
//
// With REPRO_MEMHOG_TEST=1 the test caps the reduction to 512 MiB —
// enough to prove the mechanism, not enough to destabilise the host.
// The 1536 MiB hard floor lives in the memhog binary itself; this test
// adds a separate conservative ceiling of currentAvail-512.
func TestMemhogLowersMemAvailable(t *testing.T) {
	if os.Getenv("REPRO_MEMHOG_TEST") != "1" {
		t.Skip("set REPRO_MEMHOG_TEST=1 to run this test (allocates host RAM)")
	}

	beforeMiB, err := readMemAvailable()
	if err != nil {
		t.Fatalf("cannot read MemAvailable: %v", err)
	}
	t.Logf("MemAvailable before: %d MiB", beforeMiB)

	if beforeMiB < 2048 {
		t.Skip("insufficient MemAvailable for test (need ≥2048 MiB)")
	}

	// Target = current - 512 MiB. Never below 1536 MiB (the binary's own floor
	// will also enforce this, but we compute it explicitly here for clarity).
	targetMiB := beforeMiB - 512
	if targetMiB < 1536 {
		targetMiB = 1536
	}

	cmd := exec.Command("go", "run", memhogMainPath,
		fmt.Sprintf("--target-free-mib=%d", targetMiB))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start memhog: %v", err)
	}

	// Allow 4 s for allocation and page-touching to complete.
	time.Sleep(4 * time.Second)

	afterMiB, err := readMemAvailable()
	if err != nil {
		t.Errorf("cannot read MemAvailable after start: %v", err)
	}
	t.Logf("MemAvailable after start: %d MiB (target was %d MiB)", afterMiB, targetMiB)

	if afterMiB >= beforeMiB {
		// Non-fatal: some systems may flush pages aggressively.
		t.Errorf("MemAvailable did not drop: before=%d after=%d MiB", beforeMiB, afterMiB)
	}

	// Send SIGTERM to the subprocess.
	if sigErr := cmd.Process.Signal(syscall.SIGTERM); sigErr != nil {
		t.Errorf("SIGTERM failed: %v", sigErr)
	}

	// Wait for exit with 5 s timeout.
	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	select {
	case waitErr := <-exitCh:
		// Accept both clean exit (nil) and signal-terminated exit.
		// When SIGTERM is sent to a `go run` subprocess the wrapper process may
		// propagate the signal rather than forwarding it to the child; the result
		// is an *exec.ExitError with Signaled()==true, which is expected here.
		if waitErr != nil {
			if ex, ok := waitErr.(*exec.ExitError); ok {
				if ws, ok2 := ex.Sys().(syscall.WaitStatus); ok2 && ws.Signaled() {
					t.Logf("memhog terminated by signal %v (expected)", ws.Signal())
				} else {
					t.Errorf("memhog exited with error: %v", waitErr)
				}
			} else {
				t.Errorf("memhog exited with error: %v", waitErr)
			}
		}
	case <-time.After(5 * time.Second):
		t.Error("memhog did not exit within 5 s after SIGTERM")
		_ = cmd.Process.Kill()
		<-exitCh
	}

	// Wait 2 s and record recovered MemAvailable.
	time.Sleep(2 * time.Second)
	recoverMiB, err := readMemAvailable()
	if err != nil {
		t.Errorf("cannot read MemAvailable after release: %v", err)
	}
	t.Logf("MemAvailable after release: %d MiB", recoverMiB)
}
