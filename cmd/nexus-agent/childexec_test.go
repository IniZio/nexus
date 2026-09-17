package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestResizeExecFunc_SurvivesPid1Reaper hammers the production reaper (startReaper →
// reapLoop → Wait4(-1)) while the default resizeExecFunc seam runs short-lived children;
// this reproduces the live "blkid /dev/vdd: waitid: no child processes" failure.
func TestResizeExecFunc_SurvivesPid1Reaper(t *testing.T) {
	st := newSessionTable()
	sigCh := make(chan os.Signal)
	a := &Agent{sessions: st, isPid1: true, reapSigCh: sigCh}
	ctx, cancel := context.WithCancel(context.Background())
	a.startReaper(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sigCh <- syscall.SIGCHLD:
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		time.Sleep(100 * time.Millisecond)
		oneShotReaper.Store(nil)
	})

	const n = 200
	var echild int
	for i := 0; i < n; i++ {
		out, err := resizeExecFunc("/bin/sh", "-c", "echo hello; exit 3")
		if err != nil && strings.Contains(err.Error(), "no child processes") {
			echild++
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "exit status 3") {
			t.Fatalf("iteration %d: err = %v, want exit status 3", i, err)
		}
		if got := strings.TrimSpace(string(out)); got != "hello" {
			t.Fatalf("iteration %d: out = %q, want %q", i, got, "hello")
		}
	}
	if echild > 0 {
		t.Fatalf("%d/%d calls lost their exit status to the reaper (waitid: no child processes)", echild, n)
	}

	st.mu.Lock()
	leaked := len(st.oneShot)
	st.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("oneShot registry leaked %d entries", leaked)
	}
}

func TestExecCollectCmd_WithEnvAndDir(t *testing.T) {
	st := newSessionTable()
	sigCh := make(chan os.Signal)
	a := &Agent{sessions: st, isPid1: true, reapSigCh: sigCh}
	ctx, cancel := context.WithCancel(context.Background())
	a.startReaper(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sigCh <- syscall.SIGCHLD:
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		time.Sleep(50 * time.Millisecond)
		oneShotReaper.Store(nil)
	})

	cmd := exec.Command("/bin/sh", "-c", "echo $MY_VAR")
	cmd.Env = []string{"MY_VAR=hello"}
	out, err := execCollectCmd(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "hello" {
		t.Fatalf("output = %q, want %q", got, "hello")
	}
}

func TestExecCollectCmd_NonZeroExit(t *testing.T) {
	st := newSessionTable()
	sigCh := make(chan os.Signal)
	a := &Agent{sessions: st, isPid1: true, reapSigCh: sigCh}
	ctx, cancel := context.WithCancel(context.Background())
	a.startReaper(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sigCh <- syscall.SIGCHLD:
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		time.Sleep(50 * time.Millisecond)
		oneShotReaper.Store(nil)
	})

	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	_, err := execCollectCmd(cmd)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("err = %v, want exit status 7", err)
	}
}

func TestExecCollectCmd_BareBypassRace(t *testing.T) {
	st := newSessionTable()
	sigCh := make(chan os.Signal)
	a := &Agent{sessions: st, isPid1: true, reapSigCh: sigCh}
	ctx, cancel := context.WithCancel(context.Background())
	a.startReaper(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sigCh <- syscall.SIGCHLD:
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		time.Sleep(50 * time.Millisecond)
		oneShotReaper.Store(nil)
	})

	cmd := exec.Command("/bin/true")
	exitCh := make(chan int32, 1)

	st.spawnMu.Lock()
	if err := cmd.Start(); err != nil {
		st.spawnMu.Unlock()
		t.Fatal(err)
	}
	st.mu.Lock()
	st.oneShot[cmd.Process.Pid] = exitCh
	st.mu.Unlock()
	st.spawnMu.Unlock()

	<-exitCh // reaper has Wait4'd this PID; zombie is gone

	err := cmd.Wait()
	if err == nil || !strings.Contains(err.Error(), "no child processes") {
		t.Fatalf("expected ECHILD after reaper consumed zombie, got %v", err)
	}
}
