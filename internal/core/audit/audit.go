// Package audit provides an append-only audit log for destructive operations
// (sandbox removal, volume deletion, etc.). Each call to Record writes one
// JSON line to <stateRoot>/audit/destructive.jsonl and emits an slog.Info line
// unconditionally — even when the file write fails or stateRoot is empty.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

type ctxKey struct{}

// WithReason attaches a human-readable reason string to ctx for later retrieval
// via Reason. Callers such as CLI verbs set this before delegating to service
// layer functions that call Record.
func WithReason(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, ctxKey{}, reason)
}

// Reason returns the reason string previously stored by WithReason, or "" if
// none was set.
func Reason(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}

// Event describes a single destructive operation.
type Event struct {
	TS              time.Time `json:"ts"`
	Op              string    `json:"op"`
	SandboxID       string    `json:"sandbox_id,omitempty"`
	Handle          string    `json:"handle,omitempty"`
	Volumes         []string  `json:"volumes,omitempty"`
	Reason          string    `json:"reason"`
	Argv            []string  `json:"argv"`
	PID             int       `json:"pid"`
	PPID            int       `json:"ppid"`
	Cwd             string    `json:"cwd,omitempty"`
	HerdrSocketPath string    `json:"herdr_socket_path,omitempty"`
	Err             string    `json:"err,omitempty"`
}

// Path returns the canonical path of the audit log file for the given stateRoot.
func Path(stateRoot string) string {
	return filepath.Join(stateRoot, "audit", "destructive.jsonl")
}

// now is a seam for tests to override time.
var now = time.Now

// Record writes ev to the audit log and emits an slog.Info line.
//
// Zero-valued fields are filled automatically:
//   - TS: current UTC time
//   - Reason: Reason(ctx), falling back to "unspecified"
//   - Argv: os.Args
//   - PID, PPID: current process IDs
//   - Cwd: current working directory (best-effort)
//   - HerdrSocketPath: $HERDR_SOCKET_PATH
//
// When stateRoot is empty the file write is skipped but the slog line is still
// emitted. Record always returns nil when stateRoot is empty. File write errors
// are returned but do not suppress the slog line.
func Record(ctx context.Context, stateRoot string, ev Event) error {
	// Fill zero fields.
	if ev.TS.IsZero() {
		ev.TS = now().UTC()
	}
	if ev.Reason == "" {
		if r := Reason(ctx); r != "" {
			ev.Reason = r
		} else {
			ev.Reason = "unspecified"
		}
	}
	if len(ev.Argv) == 0 {
		ev.Argv = os.Args
	}
	if ev.PID == 0 {
		ev.PID = os.Getpid()
	}
	if ev.PPID == 0 {
		ev.PPID = os.Getppid()
	}
	if ev.Cwd == "" {
		if cwd, err := os.Getwd(); err == nil {
			ev.Cwd = cwd
		}
	}
	if ev.HerdrSocketPath == "" {
		ev.HerdrSocketPath = os.Getenv("HERDR_SOCKET_PATH")
	}

	// Always emit slog line.
	slog.Info("destructive op",
		"op", ev.Op,
		"sandbox_id", ev.SandboxID,
		"handle", ev.Handle,
		"volumes", ev.Volumes,
		"reason", ev.Reason,
		"pid", ev.PID,
		"ppid", ev.PPID,
		"herdr_socket_path", ev.HerdrSocketPath,
	)

	if stateRoot == "" {
		return nil
	}

	// Encode to JSON.
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	// Ensure directory exists.
	dir := filepath.Join(stateRoot, "audit")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// Open and write atomically with a single Write call.
	f, err := os.OpenFile(Path(stateRoot), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(line)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
