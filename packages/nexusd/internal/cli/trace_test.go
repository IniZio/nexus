package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/telemetry"
)

func TestTraceIDMatchesPrefixForLongIDs(t *testing.T) {
	eventID := "1234567890abcdefAAAAAAAAAAAAAAAA"
	queryID := "1234567890abcdef"

	if !traceIDMatches(eventID, queryID) {
		t.Fatal("expected 16-character trace ID prefix match to work")
	}
}

func TestTraceIDMatchesDoesNotPanicOnShortIDs(t *testing.T) {
	tests := []struct {
		name    string
		eventID string
		queryID string
		want    bool
	}{
		{name: "exact short ID", eventID: "abc", queryID: "abc", want: true},
		{name: "different short IDs", eventID: "abc", queryID: "abd", want: false},
		{name: "short query against long event", eventID: "1234567890abcdefAAAA", queryID: "1234567", want: false},
		{name: "short event against long query", eventID: "1234567", queryID: "1234567890abcdef", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := traceIDMatches(tc.eventID, tc.queryID); got != tc.want {
				t.Fatalf("traceIDMatches(%q, %q) = %t, want %t", tc.eventID, tc.queryID, got, tc.want)
			}
		})
	}
}

func TestSafeTruncate(t *testing.T) {
	if got := safeTruncate("abc", 8); got != "abc" {
		t.Fatalf("safeTruncate short value = %q, want %q", got, "abc")
	}

	if got := safeTruncate("1234567890", 8); got != "12345678" {
		t.Fatalf("safeTruncate long value = %q, want %q", got, "12345678")
	}
}

func TestTraceListHandlesShortIDsWithoutPanic(t *testing.T) {
	setUpTelemetryDB(t, []telemetry.Event{
		{
			ID:        "abc",
			Timestamp: time.Now(),
			SessionID: "xy",
			EventType: "command",
			Command:   "status",
			Duration:  1250 * time.Millisecond,
			Success:   true,
		},
	})

	output := captureStdout(t, func() {
		if err := traceListCmd.RunE(traceListCmd, nil); err != nil {
			t.Fatalf("trace list should succeed: %v", err)
		}
	})

	if !strings.Contains(output, "abc") {
		t.Fatalf("expected output to include short trace ID, got %q", output)
	}
	if !strings.Contains(output, "xy") {
		t.Fatalf("expected output to include short session ID, got %q", output)
	}
}

func TestTraceShowMatchesShortAndPrefixedTraceIDs(t *testing.T) {
	now := time.Now()
	setUpTelemetryDB(t, []telemetry.Event{
		{
			ID:        "short-id",
			Timestamp: now,
			SessionID: "session-a",
			EventType: "command",
			Command:   "doctor",
			Duration:  2 * time.Second,
			Success:   true,
		},
		{
			ID:        "1234567890abcdef-full-event-id",
			Timestamp: now.Add(time.Second),
			SessionID: "session-b",
			EventType: "command",
			Command:   "version",
			Duration:  500 * time.Millisecond,
			Success:   true,
		},
	})

	t.Run("exact short ID", func(t *testing.T) {
		output := captureStdout(t, func() {
			if err := traceShowCmd.RunE(traceShowCmd, []string{"short-id"}); err != nil {
				t.Fatalf("trace show should find short ID: %v", err)
			}
		})

		if !strings.Contains(output, "Trace ID: short-id") {
			t.Fatalf("expected short trace to be shown, got %q", output)
		}
	})

	t.Run("16-char prefix", func(t *testing.T) {
		output := captureStdout(t, func() {
			if err := traceShowCmd.RunE(traceShowCmd, []string{"1234567890abcdef"}); err != nil {
				t.Fatalf("trace show should match by 16-char prefix: %v", err)
			}
		})

		if !strings.Contains(output, "Trace ID: 1234567890abcdef-full-event-id") {
			t.Fatalf("expected prefixed trace to be shown, got %q", output)
		}
	})
}

func TestTraceStatsHandlesShortStatSlicesWithoutPanic(t *testing.T) {
	now := time.Now()
	setUpTelemetryDB(t, []telemetry.Event{
		{
			ID:        "evt-1",
			Timestamp: now,
			SessionID: "session-a",
			EventType: "command",
			Command:   "status",
			Duration:  700 * time.Millisecond,
			Success:   true,
		},
		{
			ID:        "evt-2",
			Timestamp: now.Add(time.Second),
			SessionID: "session-a",
			EventType: "command",
			Command:   "deploy",
			Duration:  1200 * time.Millisecond,
			Success:   false,
			ErrorType: "timeout",
		},
	})

	originalJSONOutput := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = originalJSONOutput })

	output := captureStdout(t, func() {
		if err := traceStatsCmd.RunE(traceStatsCmd, nil); err != nil {
			t.Fatalf("trace stats should succeed: %v", err)
		}
	})

	if !strings.Contains(output, "Top Commands:") {
		t.Fatalf("expected top commands section in output, got %q", output)
	}
	if !strings.Contains(output, "Common Errors:") {
		t.Fatalf("expected common errors section in output, got %q", output)
	}
}

func setUpTelemetryDB(t *testing.T, events []telemetry.Event) {
	t.Helper()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	nexusDir := filepath.Join(homeDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("failed to create nexus dir: %v", err)
	}

	dbPath := filepath.Join(nexusDir, "telemetry.db")
	db, err := telemetry.NewTelemetryDB(dbPath)
	if err != nil {
		t.Fatalf("failed to create telemetry db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	for _, event := range events {
		if err := db.SaveEvent(event); err != nil {
			t.Fatalf("failed to save event %q: %v", event.ID, err)
		}
	}
}
