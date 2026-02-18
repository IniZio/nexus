package telemetry

import (
	"os"
	"testing"
	"time"
)

func TestNewTelemetryDB(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	if db == nil {
		t.Error("Database should not be nil")
	}
}

func TestNewTelemetryDB_InvalidPath(t *testing.T) {
	_, err := NewTelemetryDB("/nonexistent/path/db.db")
	if err == nil {
		t.Error("Expected error for invalid path")
	}
}

func TestTelemetryDB_SaveAndQueryEvent(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	event := Event{
		ID:        "test-event-1",
		Timestamp: time.Now(),
		SessionID: "session-1",
		EventType: "command",
		Command:   "build",
		Args:      []string{"--prod"},
		Duration:  5 * time.Second,
		Success:   true,
	}

	err = db.SaveEvent(event)
	if err != nil {
		t.Fatalf("SaveEvent failed: %v", err)
	}

	since := time.Now().Add(-1 * time.Minute)
	events, err := db.QueryEvents(since)
	if err != nil {
		t.Fatalf("QueryEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}

	if events[0].ID != event.ID {
		t.Errorf("Expected event ID %s, got %s", event.ID, events[0].ID)
	}
}

func TestTelemetryDB_SaveAndEndSession(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	session := Session{
		ID:        "test-session-1",
		StartedAt: time.Now(),
	}

	err = db.SaveSession(session)
	if err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	event := Event{
		ID:        "event-1",
		Timestamp: time.Now(),
		SessionID: session.ID,
		EventType: "command",
		Command:   "build",
		Duration:  5 * time.Second,
		Success:   true,
	}
	db.SaveEvent(event)

	event2 := Event{
		ID:        "event-2",
		Timestamp: time.Now(),
		SessionID: session.ID,
		EventType: "command",
		Command:   "test",
		Duration:  3 * time.Second,
		Success:   true,
	}
	db.SaveEvent(event2)

	err = db.EndSession(session.ID, "Good session!")
	if err != nil {
		t.Fatalf("EndSession failed: %v", err)
	}

	activeSessions, err := db.GetActiveSessions()
	if err != nil {
		t.Fatalf("GetActiveSessions failed: %v", err)
	}

	for _, s := range activeSessions {
		if s.ID == session.ID {
			t.Error("Session should not be active after ending")
		}
	}
}

func TestTelemetryDB_GetStats_EmptyDB(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	stats, err := db.GetStats(7)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.TotalCommands != 0 {
		t.Errorf("Expected 0 commands for empty DB, got %d", stats.TotalCommands)
	}

	if stats.AvgCommandDuration != 0 {
		t.Errorf("Expected 0 avg duration for empty DB, got %v", stats.AvgCommandDuration)
	}
}

func TestTelemetryDB_QueryEvents_NullDuration(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	db.SaveEvent(Event{
		ID:        "event-no-duration",
		Timestamp: time.Now(),
		SessionID: "session-1",
		EventType: "command",
		Command:   "build",
		Duration:  0,
		Success:   true,
	})

	since := time.Now().Add(-1 * time.Minute)
	events, err := db.QueryEvents(since)
	if err != nil {
		t.Fatalf("QueryEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(events))
	}

	if events[0].Duration != 0 {
		t.Errorf("Expected 0 duration for event with NULL duration, got %v", events[0].Duration)
	}
}

func TestTelemetryDB_GetStats(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	session := Session{ID: "stats-session", StartedAt: time.Now()}
	db.SaveSession(session)

	db.SaveEvent(Event{
		ID: "cmd1", SessionID: session.ID, EventType: "command", Command: "build",
		Duration: 5000, Success: true, Timestamp: time.Now(),
	})
	db.SaveEvent(Event{
		ID: "cmd2", SessionID: session.ID, EventType: "command", Command: "build",
		Duration: 6000, Success: true, Timestamp: time.Now(),
	})
	db.SaveEvent(Event{
		ID: "cmd3", SessionID: session.ID, EventType: "command", Command: "start",
		Duration: 10000, Success: false, Timestamp: time.Now(),
	})

	db.EndSession(session.ID, "")

	stats, err := db.GetStats(7)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.TotalCommands != 3 {
		t.Errorf("Expected 3 commands, got %d", stats.TotalCommands)
	}

	if len(stats.TopCommands) == 0 {
		t.Error("Expected some top commands")
	}
}

func TestTelemetryDB_GetEventsBySession(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	session1 := Session{ID: "session-1", StartedAt: time.Now()}
	session2 := Session{ID: "session-2", StartedAt: time.Now()}
	db.SaveSession(session1)
	db.SaveSession(session2)

	db.SaveEvent(Event{ID: "e1", SessionID: session1.ID, EventType: "command", Command: "build"})
	db.SaveEvent(Event{ID: "e2", SessionID: session1.ID, EventType: "command", Command: "test"})
	db.SaveEvent(Event{ID: "e3", SessionID: session2.ID, EventType: "command", Command: "deploy"})

	events, err := db.GetEventsBySession(session1.ID)
	if err != nil {
		t.Fatalf("GetEventsBySession failed: %v", err)
	}

	if len(events) != 2 {
		t.Errorf("Expected 2 events for session-1, got %d", len(events))
	}
}

func TestTelemetryDB_DeleteOldEvents(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	db.SaveEvent(Event{
		ID:        "old-event",
		Timestamp: time.Now().Add(-48 * time.Hour),
		EventType: "command",
		Command:   "old",
	})

	db.SaveEvent(Event{
		ID:        "recent-event",
		Timestamp: time.Now(),
		EventType: "command",
		Command:   "recent",
	})

	err = db.DeleteOldEvents(24 * time.Hour)
	if err != nil {
		t.Fatalf("DeleteOldEvents failed: %v", err)
	}

	since := time.Now().Add(-24 * time.Hour)
	events, err := db.QueryEvents(since)
	if err != nil {
		t.Fatalf("QueryEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Errorf("Expected 1 recent event, got %d", len(events))
	}

	if events[0].ID != "recent-event" {
		t.Errorf("Expected recent event, got %s", events[0].ID)
	}
}

func TestTelemetryDB_IndexCreation(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := NewTelemetryDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewTelemetryDB failed: %v", err)
	}
	defer db.Close()

	var count int
	err = db.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE 'idx_%'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count indexes: %v", err)
	}

	if count < 5 {
		t.Errorf("Expected at least 5 indexes, found %d", count)
	}
}
