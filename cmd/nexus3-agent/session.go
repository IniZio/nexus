package main

import (
	"cmp"
	"os"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// sessionRetentionTTL is how long a completed session is kept in the table
// after it exits. Five minutes gives ample time for a host to poll
// SessionStatus or reconnect a dropped data-plane connection. Declared as a
// var so tests can override it.
var sessionRetentionTTL = 5 * time.Minute

// sessionRetentionBudget caps the total ring-buffer bytes retained by completed
// sessions. Each ring is eagerly allocated at defaultRingCap (16 MiB), so the
// effective session count is budget / ringCap. At 64 MiB that is 4 sessions,
// which consumes ≈12% of a 512 MiB guest — a fraction that does not perturb
// the memory governor. Bounding on bytes rather than count makes the cap
// correct even if defaultRingCap changes. Declared as a var so tests can
// override it.
var sessionRetentionBudget = 64 * 1024 * 1024 // 64 MiB

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

	// exitedAt is the time.Now().UnixNano() recorded by setExited, written
	// before exited is set to true so that any goroutine observing exited==true
	// via an atomic load is guaranteed to see exitedAt already set (Go memory
	// model: atomic store of exited happens-after exitedAt store).
	exitedAt atomic.Int64

	// exitCh carries the exit code to the coordinator goroutine.
	// Buffered(1): either cmd.Wait() or the PID-1 reap loop sends exactly once.
	exitCh chan int32
}

// setExited records the exit code, marks the session done, and closes the
// ring so that all blocked WaitNext callers wake and send their Exit frames.
// Must be called exactly once per session (after all ring feeders are done).
func (s *Session) setExited(code int32) {
	s.exitCode.Store(code)
	s.exitedAt.Store(time.Now().UnixNano()) // written before exited=true (publish fence)
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

// add registers a new session and then sweeps expired or excess completed
// sessions so the table stays bounded. Every exec goes through add, making it
// the natural trigger for reclamation without requiring a background goroutine.
func (t *SessionTable) add(s *Session) {
	t.mu.Lock()
	t.byID[s.id] = s
	t.byPID[s.pid] = s
	t.mu.Unlock()
	t.sweepExited()
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

// sweepExited evicts completed sessions that have either passed the retention
// TTL or caused the total retained ring bytes to exceed sessionRetentionBudget.
// It is called both from add (on every new exec) and from the session-exit
// goroutine in control.go (via setExited), so reclamation fires whether or not
// a new exec ever arrives after an idle guest's last session ends.
//
// Running sessions (exited==false) are never touched. Deletion happens under
// the table lock; no blocking I/O is performed inside the lock.
func (t *SessionTable) sweepExited() {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()

	// Pass 1: TTL eviction — remove sessions that have been complete long
	// enough that no client can plausibly still need them.
	var retained []*Session
	for _, s := range t.byID {
		if !s.exited.Load() {
			continue // running sessions are never evicted
		}
		exitedAt := time.Unix(0, s.exitedAt.Load())
		if now.Sub(exitedAt) >= sessionRetentionTTL {
			delete(t.byID, s.id)
			delete(t.byPID, s.pid)
			continue
		}
		retained = append(retained, s)
	}

	// Pass 2: byte-budget eviction — if retained ring bytes still exceed the
	// budget (e.g. a burst of many execs inside the TTL window), evict the
	// oldest-exited sessions until we are at or under the budget. Bounding on
	// bytes rather than session count is correct even if defaultRingCap changes.
	var totalBytes int
	for _, s := range retained {
		totalBytes += s.ring.cap
	}
	if totalBytes <= sessionRetentionBudget {
		return
	}
	slices.SortFunc(retained, func(a, b *Session) int {
		return cmp.Compare(a.exitedAt.Load(), b.exitedAt.Load())
	})
	for _, s := range retained {
		if totalBytes <= sessionRetentionBudget {
			break
		}
		delete(t.byID, s.id)
		delete(t.byPID, s.pid)
		totalBytes -= s.ring.cap
	}
}
