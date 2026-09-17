package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
)

// oneShotReaper is set by Agent.startReaper. While set, cmd.Wait races drainChildren
// for the exit status and loses with ECHILD; execCollect registers the child instead.
var oneShotReaper atomic.Pointer[SessionTable]

func execCollect(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	t := oneShotReaper.Load()
	if t == nil {
		return cmd.CombinedOutput()
	}
	return t.runCollect(cmd)
}

func (t *SessionTable) runCollect(cmd *exec.Cmd) ([]byte, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	exitCh := make(chan int32, 1)
	if err := t.spawnOneShot(cmd, exitCh); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close()
	out, readErr := io.ReadAll(pr)
	pr.Close()
	code := <-exitCh
	if readErr != nil {
		return out, readErr
	}
	if code != 0 {
		return out, fmt.Errorf("exit status %d", code)
	}
	return out, nil
}
