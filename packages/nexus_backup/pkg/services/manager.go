package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const maxLogBytes = 64 * 1024
const defaultStopTimeout = 1 * time.Second

type StartOptions struct {
	StopTimeout  time.Duration
	AutoRestart  bool
	MaxRestarts  int
	RestartDelay time.Duration
}

type StopResult struct {
	Stopped bool
	Forced  bool
}

type cappedBuffer struct {
	buf   bytes.Buffer
	max   int
	trunc bool
}

func newCappedBuffer(max int) *cappedBuffer {
	return &cappedBuffer{max: max}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	remaining := c.max - c.buf.Len()
	if remaining <= 0 {
		c.trunc = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = c.buf.Write(p[:remaining])
		c.trunc = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string {
	return c.buf.String()
}

type ServiceProcess struct {
	WorkspaceID string
	Name        string
	Command     string
	Args        []string
	StartedAt   time.Time
	PID         int
	Options     StartOptions
	Restarted   int
	stdout      bytes.Buffer
	stderr      bytes.Buffer
	stdoutCap   *cappedBuffer
	stderrCap   *cappedBuffer
	cmd         *exec.Cmd
	stopping    bool
}

type Manager struct {
	mu    sync.RWMutex
	procs map[string]*ServiceProcess
}

func NewManager() *Manager {
	return &Manager{procs: make(map[string]*ServiceProcess)}
}

func normalizeOptions(opts StartOptions) StartOptions {
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = defaultStopTimeout
	}
	if opts.AutoRestart {
		if opts.MaxRestarts <= 0 {
			opts.MaxRestarts = 1
		}
		if opts.RestartDelay <= 0 {
			opts.RestartDelay = 100 * time.Millisecond
		}
	}
	return opts
}

func (m *Manager) Start(ctx context.Context, workspaceID, name, workDir, command string, args []string, opts StartOptions) (*ServiceProcess, error) {
	if workspaceID == "" || name == "" || command == "" {
		return nil, fmt.Errorf("workspaceID, name, and command are required")
	}
	opts = normalizeOptions(opts)
	key := workspaceID + ":" + name

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.procs[key]; ok {
		return nil, fmt.Errorf("service already running: %s", key)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workDir
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	sp := &ServiceProcess{
		WorkspaceID: workspaceID,
		Name:        name,
		Command:     command,
		Args:        args,
		StartedAt:   time.Now().UTC(),
		Options:     opts,
		stdoutCap:   newCappedBuffer(maxLogBytes),
		stderrCap:   newCappedBuffer(maxLogBytes),
		cmd:         cmd,
	}
	cmd.Stdout = io.MultiWriter(&sp.stdout, sp.stdoutCap)
	cmd.Stderr = io.MultiWriter(&sp.stderr, sp.stderrCap)

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sp.PID = cmd.Process.Pid
	m.procs[key] = sp

	go m.monitor(key, workDir, sp)

	copy := *sp
	return &copy, nil
}

func (m *Manager) monitor(key string, workDir string, sp *ServiceProcess) {
	_ = sp.cmd.Wait()

	m.mu.Lock()
	current, ok := m.procs[key]
	if !ok || current != sp {
		m.mu.Unlock()
		return
	}

	if sp.stopping {
		delete(m.procs, key)
		m.mu.Unlock()
		return
	}

	if !sp.Options.AutoRestart || sp.Restarted >= sp.Options.MaxRestarts {
		delete(m.procs, key)
		m.mu.Unlock()
		return
	}

	nextRestart := sp.Restarted + 1
	command := sp.Command
	args := append([]string(nil), sp.Args...)
	opts := sp.Options
	m.mu.Unlock()

	time.Sleep(opts.RestartDelay)

	m.mu.Lock()
	current, ok = m.procs[key]
	if !ok || current != sp || current.stopping {
		m.mu.Unlock()
		return
	}

	cmd := exec.Command(command, args...)
	cmd.Dir = workDir
	cmd.Stdout = io.MultiWriter(&sp.stdout, sp.stdoutCap)
	cmd.Stderr = io.MultiWriter(&sp.stderr, sp.stderrCap)
	if err := cmd.Start(); err != nil {
		delete(m.procs, key)
		m.mu.Unlock()
		return
	}

	sp.cmd = cmd
	sp.PID = cmd.Process.Pid
	sp.Restarted = nextRestart
	m.mu.Unlock()

	go m.monitor(key, workDir, sp)
}

func (m *Manager) Stop(workspaceID, name string) bool {
	res := m.StopWithTimeout(workspaceID, name, defaultStopTimeout)
	return res.Stopped
}

func (m *Manager) StopWithTimeout(workspaceID, name string, timeout time.Duration) StopResult {
	if timeout <= 0 {
		timeout = defaultStopTimeout
	}

	key := workspaceID + ":" + name
	m.mu.Lock()
	sp, ok := m.procs[key]
	if ok {
		sp.stopping = true
	}
	m.mu.Unlock()
	if !ok {
		return StopResult{Stopped: false, Forced: false}
	}

	if sp.cmd != nil && sp.cmd.Process != nil {
		_ = sp.cmd.Process.Signal(syscall.SIGTERM)
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if !isProcessRunning(sp.cmd.Process) {
				m.mu.Lock()
				delete(m.procs, key)
				m.mu.Unlock()
				return StopResult{Stopped: true, Forced: false}
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = sp.cmd.Process.Signal(syscall.SIGKILL)
		m.mu.Lock()
		delete(m.procs, key)
		m.mu.Unlock()
		return StopResult{Stopped: true, Forced: true}
	}

	m.mu.Lock()
	delete(m.procs, key)
	m.mu.Unlock()
	return StopResult{Stopped: true, Forced: false}
}

func isProcessRunning(proc *os.Process) bool {
	if proc == nil {
		return false
	}
	err := proc.Signal(syscall.Signal(0))
	return err == nil
}

func (m *Manager) Restart(ctx context.Context, workspaceID, name, workDir, command string, args []string, opts StartOptions) (*ServiceProcess, error) {
	_ = m.StopWithTimeout(workspaceID, name, opts.StopTimeout)
	return m.Start(ctx, workspaceID, name, workDir, command, args, opts)
}

func (m *Manager) Status(workspaceID, name string) map[string]interface{} {
	key := workspaceID + ":" + name
	m.mu.RLock()
	sp, ok := m.procs[key]
	m.mu.RUnlock()
	if !ok {
		return map[string]interface{}{"running": false}
	}
	return map[string]interface{}{
		"running":     true,
		"workspaceId": workspaceID,
		"name":        name,
		"pid":         sp.PID,
		"startedAt":   sp.StartedAt,
	}
}

func (m *Manager) Logs(workspaceID, name string) map[string]interface{} {
	key := workspaceID + ":" + name
	m.mu.RLock()
	sp, ok := m.procs[key]
	m.mu.RUnlock()
	if !ok {
		return map[string]interface{}{"stdout": "", "stderr": "", "running": false}
	}
	return map[string]interface{}{
		"stdout":  sp.stdoutCap.String(),
		"stderr":  sp.stderrCap.String(),
		"running": true,
	}
}
