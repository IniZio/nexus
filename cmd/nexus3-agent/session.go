package main

import (
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Session represents one active or completed PTY/pipe session.
type Session struct {
	id  string
	pid int
	cmd *exec.Cmd

	// PTY mode
	ptmx *os.File // PTY master; nil for pipe sessions

	// Pipe mode
	stdinW *os.File // write end of stdin pipe; nil for PTY sessions

	// Output history (shared by all data-plane connections for this session).
	ring *Ring

	// Exit state – written once, read many.
	exitCode atomic.Int32
	exited   atomic.Bool

	// exitCh carries the exit code to the coordinator goroutine.
	// Buffered(1): either cmd.Wait() or the PID-1 reap loop sends exactly once.
	exitCh chan int32
}

// setExited records the exit code, marks the session done, and closes the
// ring so that all blocked WaitNext callers wake and send their Exit frames.
// Must be called exactly once per session (after all ring feeders are done).
func (s *Session) setExited(code int32) {
	s.exitCode.Store(code)
	s.exited.Store(true)
	s.ring.Close()
}

// SessionTable is a concurrent-safe registry of sessions keyed by
// session-ID and by OS PID (for the zombie reaper).
type SessionTable struct {
	mu    sync.RWMutex
	byID  map[string]*Session
	byPID map[int]*Session
}

func newSessionTable() *SessionTable {
	return &SessionTable{
		byID:  make(map[string]*Session),
		byPID: make(map[int]*Session),
	}
}

func (t *SessionTable) add(s *Session) {
	t.mu.Lock()
	t.byID[s.id] = s
	t.byPID[s.pid] = s
	t.mu.Unlock()
}

func (t *SessionTable) get(id string) (*Session, bool) {
	t.mu.RLock()
	s, ok := t.byID[id]
	t.mu.RUnlock()
	return s, ok
}

func (t *SessionTable) list() []*Session {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*Session, 0, len(t.byID))
	for _, s := range t.byID {
		out = append(out, s)
	}
	return out
}

// notifyExit is called by the PID-1 reap loop when it collects a child.
// For our own sessions it delivers the exit code; unknown PIDs are orphans.
func (t *SessionTable) notifyExit(pid int, code int32) {
	t.mu.RLock()
	s, ok := t.byPID[pid]
	t.mu.RUnlock()
	if !ok {
		return // orphan – discard
	}
	select {
	case s.exitCh <- code:
	default: // already delivered (shouldn't happen with buffer 1)
	}
}
