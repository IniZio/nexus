package portfwd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Every sandbox supervisor used to write the whole forwards.state with only
// its own ports, so with two sandboxes the file flapped between "[5173]" and
// "[]" on alternate ticks (live 2026-09-15, three sandboxes of one downstream repo) and a
// remote client cancelled and re-added the forward every few seconds. Now
// each supervisor owns forwards.d/<sandbox>.state and forwards.state is the
// merge, rewritten under a lock by whoever wrote last.

const (
	perSandboxDirName = "forwards.d"
	lockFileName      = "forwards.lock"
	// StaleAfter drops a per-sandbox file whose supervisor stopped writing
	// (SIGKILL leaves no teardown). Supervisors rewrite on every successful
	// reconcile tick (5s), so a minute of silence means the writer is gone.
	StaleAfter = 60 * time.Second
)

// Entry is one forwarded port as recorded in the state files.
type Entry struct {
	Port        uint16    `json:"port"`
	Sandbox     string    `json:"sandbox"`
	Status      string    `json:"status"`
	ConfirmedAt time.Time `json:"confirmed_at,omitzero"`
	Error       string    `json:"error,omitempty"`
}

// State is the JSON shape of forwards.state and of each per-sandbox file.
type State struct {
	WrittenBy string    `json:"written_by"`
	UpdatedAt time.Time `json:"updated_at"`
	Forwards  []Entry   `json:"forwards"`
}

// WriteSandboxState records sandboxID's forwards and rewrites the merged
// forwards.state. It never returns without attempting the merge.
func WriteSandboxState(dir, sandboxID string, entries []Entry, now time.Time) error {
	if sandboxID == "" {
		return fmt.Errorf("portfwd state: empty sandbox id")
	}
	if err := os.MkdirAll(filepath.Join(dir, perSandboxDirName), 0o750); err != nil {
		return fmt.Errorf("portfwd state dir: %w", err)
	}
	st := State{WrittenBy: "nexus3/" + sandboxID, UpdatedAt: now.UTC(), Forwards: entries}
	if err := writeJSONAtomic(perSandboxFile(dir, sandboxID), st); err != nil {
		return err
	}
	return withLock(dir, func() error { return mergeLocked(dir, now, "") })
}

// RemoveSandboxState deletes sandboxID's file and rewrites the merge; used on
// supervisor teardown so the port disappears immediately rather than after
// StaleAfter.
func RemoveSandboxState(dir, sandboxID string, now time.Time) error {
	if err := os.Remove(perSandboxFile(dir, sandboxID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("portfwd state remove: %w", err)
	}
	return withLock(dir, func() error { return mergeLocked(dir, now, sandboxID) })
}

// Merge unions every fresh per-sandbox file with the previous forwards.state,
// one row per (sandbox, port) from the newest file (updated_at, then
// confirmed_at); live 2026-09-16 a supervisor listed every port twice and
// remote clients doubled every forward.
//
// Rows in forwards.state whose sandbox has no forwards.d file (fresh or stale)
// come from a supervisor that predates forwards.d and writes forwards.state
// directly (HAN-941 F6); they are carried while the sandbox is alive.
// Liveness is the row's confirmed_at: every supervisor stamps it with the
// wall clock on each reconcile tick (5s), so it is a per-row heartbeat that
// a merge copies unchanged. Zero or older than StaleAfter means no live
// writer and the row is dropped, like a stale forwards.d file.
func Merge(dir string, now time.Time) (State, error) {
	return merge(dir, now, "")
}

type mergeCandidate struct {
	entry  Entry
	fileAt time.Time
}

func (c mergeCandidate) newerThan(o mergeCandidate) bool {
	if !c.fileAt.Equal(o.fileAt) {
		return c.fileAt.After(o.fileAt)
	}
	return c.entry.ConfirmedAt.After(o.entry.ConfirmedAt)
}

type mergeKey struct {
	sandbox string
	port    uint16
}

func merge(dir string, now time.Time, excludeSandbox string) (State, error) {
	names, err := filepath.Glob(filepath.Join(dir, perSandboxDirName, "*.state"))
	if err != nil {
		return State{}, fmt.Errorf("portfwd state glob: %w", err)
	}
	best := map[mergeKey]mergeCandidate{}
	consider := func(e Entry, fileAt time.Time) {
		k := mergeKey{sandbox: e.Sandbox, port: e.Port}
		c := mergeCandidate{entry: e, fileAt: fileAt}
		if prev, ok := best[k]; ok && !c.newerThan(prev) {
			return
		}
		best[k] = c
	}
	owned := map[string]bool{}
	for _, name := range names {
		st, ok := readState(name)
		if !ok {
			continue
		}
		if now.Sub(st.UpdatedAt) > StaleAfter {
			continue
		}
		for _, e := range st.Forwards {
			owned[e.Sandbox] = true
			consider(e, st.UpdatedAt)
		}
	}
	if prev, ok := readState(filepath.Join(dir, StateFileName)); ok {
		for _, e := range prev.Forwards {
			if owned[e.Sandbox] || e.Sandbox == excludeSandbox {
				continue
			}
			if _, err := os.Stat(perSandboxFile(dir, e.Sandbox)); err == nil {
				continue
			}
			if e.ConfirmedAt.IsZero() || now.Sub(e.ConfirmedAt) > StaleAfter {
				continue
			}
			consider(e, prev.UpdatedAt)
		}
	}
	merged := State{WrittenBy: "nexus3/merge", UpdatedAt: now.UTC(), Forwards: make([]Entry, 0, len(best))}
	for _, c := range best {
		merged.Forwards = append(merged.Forwards, c.entry)
	}
	sort.Slice(merged.Forwards, func(i, j int) bool {
		if merged.Forwards[i].Port != merged.Forwards[j].Port {
			return merged.Forwards[i].Port < merged.Forwards[j].Port
		}
		return merged.Forwards[i].Sandbox < merged.Forwards[j].Sandbox
	})
	return merged, nil
}

func readState(path string) (State, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, false
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, false
	}
	return st, true
}

func mergeLocked(dir string, now time.Time, excludeSandbox string) error {
	merged, err := merge(dir, now, excludeSandbox)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, StateFileName), merged)
}

func perSandboxFile(dir, sandboxID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, sandboxID)
	return filepath.Join(dir, perSandboxDirName, safe+".state")
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("portfwd state marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return fmt.Errorf("portfwd state write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("portfwd state rename: %w", err)
	}
	return nil
}

func withLock(dir string, fn func() error) error {
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return fmt.Errorf("portfwd state lock: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("portfwd state flock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}
