package cli

// Regression test: parent SIGHUP absorber must NOT propagate SIG_IGN to child.
//
// Contract: when herdr closes a pane it sends SIGHUP to the process group.
// The parent (nexus3-guest-shell) must survive; the child (nexus3 exec /
// guest shell) must die, so cmd.Wait() returns and teardown can run.
//
// Signal.Ignore installs SIG_IGN which is inherited across execve — the child
// would also ignore SIGHUP and never exit (BUG).  Signal.Notify installs a
// Go handler which is reset to SIG_DFL at the child's execve — child dies,
// parent absorbs (FIX).
//
// Mutation proof: replace signal.Notify in installParentSighupAbsorber with
// signal.Ignore and the test HANGS / exceeds the 5 s bound → RED.

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// TestSighupChildMode is the re-exec child entry point.  When the test binary
// is re-exec'd with NEXUS3_TEST_SIGHUP_CHILD=1 and -test.run=TestSighupChildMode
// this function mimics nexus3 exec's signal setup, then blocks until a signal
// kills it (or the parent kills it after 5 s in the error path).
func TestSighupChildMode(t *testing.T) {
	if os.Getenv("NEXUS3_TEST_SIGHUP_CHILD") != "1" {
		t.Skip("not a re-exec child")
	}
	// Mimic nexus3 exec / root.go: Notify SIGINT+SIGTERM only.  SIGHUP
	// disposition is inherited from the parent — SIG_DFL with the fix,
	// SIG_IGN with the bug.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {} // block until killed by SIGHUP (SIG_DFL) or hangs (SIG_IGN)
}

// TestSighupChildDiesOnPaneClose verifies that after installParentSighupAbsorber
// a child process inherits SIG_DFL for SIGHUP and exits within 5 s when the
// process group receives SIGHUP.
func TestSighupChildDiesOnPaneClose(t *testing.T) {
	// Install parent absorber (the production code path).
	stopAbsorber := installParentSighupAbsorber()
	defer stopAbsorber()

	// Re-exec this test binary as the child in TestSighupChildMode.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(self, "-test.run=TestSighupChildMode", "-test.v")
	cmd.Env = append(os.Environ(), "NEXUS3_TEST_SIGHUP_CHILD=1")
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Put child in its own process group so we can send SIGHUP to just it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("child start: %v", err)
	}

	// Give the child a moment to reach its select{} block.
	time.Sleep(200 * time.Millisecond)

	// Send SIGHUP to the child's process group (negative pid = whole pgroup).
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP); err != nil {
		t.Fatalf("kill -SIGHUP pgrp: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case waitErr := <-waitDone:
		// Any exit is fine (signal: hangup, non-zero exit).  The key is
		// cmd.Wait() returned — teardown is now reachable.
		t.Logf("child exited as expected: %v", waitErr)
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not exit within 5 s after SIGHUP — " +
			"SIG_IGN was inherited (BUG: signal.Ignore used instead of signal.Notify)")
	}
}
