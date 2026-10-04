package broker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Launcher starts a detached broker for a sandbox. The broker writes its own
// broker.json once it is ready.
type Launcher interface {
	Spawn(ctx context.Context, id string) error
}

// ExecLauncher spawns `<Exe> __sprites-broker <id>` in its own session.
type ExecLauncher struct {
	// Exe defaults to os.Executable().
	Exe      string
	StateDir string
}

// Spawn starts the detached broker process and releases it.
func (l ExecLauncher) Spawn(_ context.Context, id string) error {
	dir, err := Dir(l.StateDir, id)
	if err != nil {
		return err
	}
	exe := l.Exe
	if exe == "" {
		if exe, err = os.Executable(); err != nil {
			return fmt.Errorf("broker: resolve executable: %w", err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(dir+"/broker.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(exe, "__sprites-broker", id)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("broker: spawn: %w", err)
	}
	return cmd.Process.Release()
}

// Manager ensures and stops per-sandbox brokers.
type Manager struct {
	StateDir string
	Launcher Launcher
	// ReadyTimeout bounds the wait for broker.json after a spawn (default 10s).
	ReadyTimeout time.Duration
	// StopGrace bounds the wait after SIGTERM before SIGKILL (default 5s).
	StopGrace time.Duration
}

func (m *Manager) readyTimeout() time.Duration {
	if m.ReadyTimeout > 0 {
		return m.ReadyTimeout
	}
	return 10 * time.Second
}

func (m *Manager) stopGrace() time.Duration {
	if m.StopGrace > 0 {
		return m.StopGrace
	}
	return 5 * time.Second
}

// Alive reports whether the recorded broker is running with its recorded
// identity.
func (m *Manager) Alive(id string) bool {
	st, err := ReadState(m.StateDir, id)
	return err == nil && identityMatches(st)
}

// Ensure is a no-op when the broker is alive; otherwise it spawns one and
// waits for a fresh broker.json.
func (m *Manager) Ensure(ctx context.Context, id string) error {
	if _, err := Dir(m.StateDir, id); err != nil {
		return err
	}
	if m.Alive(id) {
		return nil
	}
	// Stale record: drop it so the wait below sees only the new broker's file.
	if err := RemoveState(m.StateDir, id); err != nil {
		return err
	}
	if err := m.Launcher.Spawn(ctx, id); err != nil {
		return err
	}
	deadline := time.NewTimer(m.readyTimeout())
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if st, err := ReadState(m.StateDir, id); err == nil && identityMatches(st) {
			return nil
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("broker: %s did not write %s within %s", id, stateFile, m.readyTimeout())
		case <-tick.C:
		}
	}
}

// Stop terminates the broker only if the recorded PID still runs the recorded
// identity, then removes broker.json. A recycled PID is never signalled: the
// stale record is removed and Stop returns nil.
func (m *Manager) Stop(id string) error {
	st, err := ReadState(m.StateDir, id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if identityMatches(st) {
		if err := terminate(st, m.stopGrace()); err != nil {
			return err
		}
	}
	return RemoveState(m.StateDir, id)
}

func terminate(st State, grace time.Duration) error {
	if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("broker: signal pid %d: %w", st.PID, err)
	}
	end := time.Now().Add(grace)
	for time.Now().Before(end) {
		if !pidAlive(st.PID) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Re-verify before escalating: the PID may have exited and been reused.
	if identityMatches(st) {
		if err := syscall.Kill(st.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("broker: kill pid %d: %w", st.PID, err)
		}
	}
	return nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// identityMatches reports whether st.PID is alive and its command line equals
// the recorded one.
func identityMatches(st State) bool {
	if !pidAlive(st.PID) || len(st.Cmdline) == 0 {
		return false
	}
	got, err := processCmdline(st.PID)
	if err != nil {
		return false
	}
	return strings.Join(got, "\x00") == strings.Join(st.Cmdline, "\x00")
}

// ProcessCmdline returns the argv of a live process, for recording identity.
func ProcessCmdline(pid int) ([]string, error) { return processCmdline(pid) }
