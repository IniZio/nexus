package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// waitZombie blocks until pid has exited but not yet been collected.
func waitZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil {
			rest := b[strings.LastIndexByte(string(b), ')')+2:]
			if len(rest) > 0 && rest[0] == 'Z' {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pid %d did not become a zombie in time", pid)
}

// Interleaving behind the idle-guest RSS leak: child exits and drainChildren
// runs after cmd.Start but before add. The exit must still reach exitCh.
func TestSpawn_ReaperCannotOrphanChildBeforeRegistration(t *testing.T) {
	st := newSessionTable()
	a := &Agent{sessions: st}
	sess := &Session{id: "spawn-vs-reaper", exitCh: make(chan int32, 1)}

	drained := make(chan struct{})
	var pid int
	err := st.spawn(sess, func() (int, error) {
		cmd := exec.Command("/bin/true")
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		sess.cmd = cmd
		pid = cmd.Process.Pid
		waitZombie(t, pid)
		go func() { a.drainChildren(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(300 * time.Millisecond):
		}
		return pid, nil
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("drainChildren did not complete after spawn returned")
	}

	select {
	case code := <-sess.exitCh:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("exit of pid %d was discarded as an orphan: session %q exited=%v, byPID has pid=%v",
			pid, sess.id, sess.exited.Load(), func() bool { _, ok := st.byPID[pid]; return ok }())
	}
}
