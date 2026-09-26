package audit_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/IniZio/nexus/internal/core/audit"
)

func TestWithReasonRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := audit.Reason(ctx); got != "" {
		t.Fatalf("unset reason: want \"\", got %q", got)
	}
	ctx = audit.WithReason(ctx, "test-reason")
	if got := audit.Reason(ctx); got != "test-reason" {
		t.Fatalf("reason round-trip: want %q, got %q", "test-reason", got)
	}
}

func TestRecordAppendsLines(t *testing.T) {
	root := t.TempDir()

	ev1 := audit.Event{Op: "sandbox.remove", SandboxID: "s1", Handle: "h1"}
	if err := audit.Record(context.Background(), root, ev1); err != nil {
		t.Fatalf("Record #1: %v", err)
	}

	ctx2 := audit.WithReason(context.Background(), "prune")
	ev2 := audit.Event{Op: "volume.rm", Volumes: []string{"vol-a", "vol-b"}}
	if err := audit.Record(ctx2, root, ev2); err != nil {
		t.Fatalf("Record #2: %v", err)
	}

	data, err := os.ReadFile(audit.Path(root))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	lines := splitLines(t, data)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}

	var got1 audit.Event
	if err := json.Unmarshal(lines[0], &got1); err != nil {
		t.Fatalf("parse line 1: %v", err)
	}
	checkDefaults(t, "line1", got1)
	if got1.Reason != "unspecified" {
		t.Errorf("line1 reason: want %q, got %q", "unspecified", got1.Reason)
	}
	if got1.Op != "sandbox.remove" {
		t.Errorf("line1 op: want %q, got %q", "sandbox.remove", got1.Op)
	}

	var got2 audit.Event
	if err := json.Unmarshal(lines[1], &got2); err != nil {
		t.Fatalf("parse line 2: %v", err)
	}
	checkDefaults(t, "line2", got2)
	if got2.Reason != "prune" {
		t.Errorf("line2 reason: want %q, got %q", "prune", got2.Reason)
	}
	if diff := cmp.Diff([]string{"vol-a", "vol-b"}, got2.Volumes); diff != "" {
		t.Errorf("line2 volumes mismatch (-want +got):\n%s", diff)
	}
}

func TestRecordFileMode(t *testing.T) {
	root := t.TempDir()
	if err := audit.Record(context.Background(), root, audit.Event{Op: "volume.trash"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	info, err := os.Stat(audit.Path(root))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file mode: want 0600, got %04o", perm)
	}
}

func TestRecordEmptyStateRoot(t *testing.T) {
	if err := audit.Record(context.Background(), "", audit.Event{Op: "sandbox.remove"}); err != nil {
		t.Fatalf("empty stateRoot must return nil, got %v", err)
	}
}

func TestRecordConcurrent(t *testing.T) {
	root := t.TempDir()
	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := audit.Record(context.Background(), root, audit.Event{Op: "volume.rm"}); err != nil {
				t.Errorf("Record: %v", err)
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(audit.Path(root))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := splitLines(t, data)
	if len(lines) != n {
		t.Fatalf("want %d lines, got %d", n, len(lines))
	}
	for i, line := range lines {
		var ev audit.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
		}
	}
}

// splitLines returns non-empty newline-terminated JSON objects.
func splitLines(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			chunk := data[start:i]
			start = i + 1
			if len(chunk) > 0 {
				out = append(out, chunk)
			}
		}
	}
	return out
}

func checkDefaults(t *testing.T, label string, ev audit.Event) {
	t.Helper()
	if ev.TS.IsZero() {
		t.Errorf("%s: TS is zero", label)
	}
	if ev.TS.Location() != time.UTC {
		t.Errorf("%s: TS not UTC: %v", label, ev.TS.Location())
	}
	if ev.PID != os.Getpid() {
		t.Errorf("%s: PID: want %d, got %d", label, os.Getpid(), ev.PID)
	}
	if len(ev.Argv) == 0 {
		t.Errorf("%s: Argv is empty", label)
	}
}
