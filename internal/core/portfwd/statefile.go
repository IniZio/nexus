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
// "[]" on alternate ticks (live 2026-09-15, three example-app sandboxes) and a
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
	return withLock(dir, func() error { return mergeLocked(dir, now) })
}

// RemoveSandboxState deletes sandboxID's file and rewrites the merge; used on
// supervisor teardown so the port disappears immediately rather than after
// StaleAfter.
func RemoveSandboxState(dir, sandboxID string, now time.Time) error {
	if err := os.Remove(perSandboxFile(dir, sandboxID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("portfwd state remove: %w", err)
	}
	return withLock(dir, func() error { return mergeLocked(dir, now) })
}

// Merge reads every fresh per-sandbox file and returns the combined state,
// ports sorted; entries from files older than StaleAfter are dropped.
func Merge(dir string, now time.Time) (State, error) {
	names, err := filepath.Glob(filepath.Join(dir, perSandboxDirName, "*.state"))
	if err != nil {
		return State{}, fmt.Errorf("portfwd state glob: %w", err)
	}
	merged := State{WrittenBy: "nexus3/merge", UpdatedAt: now.UTC(), Forwards: []Entry{}}
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		var st State
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}
		if now.Sub(st.UpdatedAt) > StaleAfter {
			continue
		}
		merged.Forwards = append(merged.Forwards, st.Forwards...)
	}
	sort.Slice(merged.Forwards, func(i, j int) bool {
		if merged.Forwards[i].Port != merged.Forwards[j].Port {
			return merged.Forwards[i].Port < merged.Forwards[j].Port
		}
		return merged.Forwards[i].Sandbox < merged.Forwards[j].Sandbox
	})
	return merged, nil
}

func mergeLocked(dir string, now time.Time) error {
	merged, err := Merge(dir, now)
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
