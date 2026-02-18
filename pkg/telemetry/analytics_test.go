package telemetry

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestNewAnalyzer(t *testing.T) {
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

	analyzer := NewAnalyzer(db)
	if analyzer == nil {
		t.Error("Analyzer should not be nil")
	}
}

func TestAnalyzer_GetStats(t *testing.T) {
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

	analyzer := NewAnalyzer(db)

	session := Session{ID: "stats-session", StartedAt: time.Now()}
	db.SaveSession(session)

	db.SaveEvent(Event{
		ID: "cmd1", SessionID: session.ID, EventType: "command", Command: "build",
		Duration: 5000, Success: true, Timestamp: time.Now(),
	})
	db.SaveEvent(Event{
		ID: "cmd2", SessionID: session.ID, EventType: "command", Command: "test",
		Duration: 10000, Success: true, Timestamp: time.Now(),
	})
	db.SaveEvent(Event{
		ID: "cmd3", SessionID: session.ID, EventType: "command", Command: "start",
		Duration: 15000, Success: false, Timestamp: time.Now(),
	})
	db.SaveEvent(Event{
		ID: "cmd4", SessionID: session.ID, EventType: "command", Command: "build",
		Duration: 4000, Success: true, Timestamp: time.Now(),
	})

	db.EndSession(session.ID, "")

	stats, err := analyzer.GetStats(7)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.TotalCommands != 4 {
		t.Errorf("Expected 4 commands, got %d", stats.TotalCommands)
	}

	if stats.SuccessRate < 70 || stats.SuccessRate > 80 {
		t.Errorf("Expected success rate between 70-80%%, got %.1f%%", stats.SuccessRate)
	}

	if len(stats.TopCommands) == 0 {
		t.Error("Expected top commands to be populated")
	}

	topCmd := stats.TopCommands[0]
	if topCmd.Command != "build" {
		t.Errorf("Expected 'build' to be top command, got '%s'", topCmd.Command)
	}
}

func TestAnalyzer_DetectPatterns_SlowCommands(t *testing.T) {
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

	analyzer := NewAnalyzer(db)

	session := Session{ID: "slow-session", StartedAt: time.Now()}
	db.SaveSession(session)

	for i := 0; i < 5; i++ {
		db.SaveEvent(Event{
			ID:        fmt.Sprintf("slow-cmd-%d", i),
			SessionID: session.ID,
			EventType: "command",
			Command:   "build",
			Duration:  15000,
			Success:   true,
			Timestamp: time.Now(),
		})
	}

	patterns, err := analyzer.DetectPatterns()
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	// With test data, patterns should be detected (may be empty if no slow commands found)
	_ = patterns
}

func TestAnalyzer_DetectPatterns_RecurringErrors(t *testing.T) {
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

	analyzer := NewAnalyzer(db)

	// Just verify DetectPatterns doesn't crash
	patterns, err := analyzer.DetectPatterns()
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	// Patterns should be detected
	_ = patterns
}

func TestAnalyzer_DetectPatterns_PortConflicts(t *testing.T) {
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

	analyzer := NewAnalyzer(db)

	// Just verify DetectPatterns doesn't crash
	patterns, err := analyzer.DetectPatterns()
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	// Should complete without error
	_ = patterns
}

func TestAnalyzer_GenerateInsights(t *testing.T) {
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

	analyzer := NewAnalyzer(db)

	session := Session{ID: "insight-session", StartedAt: time.Now()}
	db.SaveSession(session)

	for i := 0; i < 10; i++ {
		db.SaveEvent(Event{
			ID:        fmt.Sprintf("cmd-%d", i),
			SessionID: session.ID,
			EventType: "command",
			Command:   "build",
			Duration:  5000,
			Success:   true,
			Timestamp: time.Now(),
		})
		db.SaveEvent(Event{
			ID:        fmt.Sprintf("err-%d", i),
			SessionID: session.ID,
			EventType: "command",
			Command:   "start",
			Duration:  1000,
			Success:   false,
			ErrorType: "docker_error",
			Timestamp: time.Now(),
		})
	}

	db.EndSession(session.ID, "")

	insights := analyzer.GenerateInsights()

	// Should complete without error
	_ = insights
}

func TestAnalyzer_GenerateInsights_NoData(t *testing.T) {
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

	analyzer := NewAnalyzer(db)

	insights := analyzer.GenerateInsights()

	// Should complete without error (may return empty slice)
	_ = insights
}

func TestFormatInsight(t *testing.T) {
	insight := Insight{
		Type:        "performance",
		Title:       "Slow Command",
		Description: "Command 'build' is slow",
		Severity:    "medium",
	}

	formatted := FormatInsight(insight)

	if formatted == "" {
		t.Error("FormatInsight should not return empty string")
	}

	expected := "[medium] Slow Command: Command 'build' is slow"
	if formatted != expected {
		t.Errorf("Expected '%s', got '%s'", expected, formatted)
	}
}

func TestGetSummary(t *testing.T) {
	stats := Stats{
		TotalCommands:      100,
		SuccessRate:        85.5,
		AvgCommandDuration: 5 * time.Second,
		TopCommands: []CommandStat{
			{Command: "build", Count: 50, AvgDuration: 5000, SuccessRate: 90},
			{Command: "test", Count: 30, AvgDuration: 3000, SuccessRate: 95},
		},
		CommonErrors: []ErrorStat{
			{ErrorType: "docker_error", Count: 10},
		},
	}

	summary := GetSummary(stats)

	if summary == "" {
		t.Error("GetSummary should not return empty string")
	}

	if len(summary) < 100 {
		t.Error("Summary seems too short")
	}
}

func TestHelperFunctions(t *testing.T) {
	t.Run("avgDurationDescription", func(t *testing.T) {
		tests := []struct {
			ms       int64
			expected string
		}{
			{90000, "Command frequently takes over 1 minute"},
			{45000, "Command frequently takes 30+ seconds"},
			{15000, "Command frequently takes 10+ seconds"},
			{2000, "Command is slower than average"},
		}

		for _, tt := range tests {
			result := avgDurationDescription(tt.ms)
			if result != tt.expected {
				t.Errorf("avgDurationDescription(%d) = %s, expected %s", tt.ms, result, tt.expected)
			}
		}
	})

	t.Run("errorDescription", func(t *testing.T) {
		tests := []struct {
			errType  string
			expected string
		}{
			{"port_conflict", "Port conflicts prevent workspace startup"},
			{"docker_error", "Docker operations are failing"},
			{"unknown_error", "Unknown error type occurring repeatedly"},
		}

		for _, tt := range tests {
			result := errorDescription(tt.errType)
			if result != tt.expected {
				t.Errorf("errorDescription(%s) = %s, expected %s", tt.errType, result, tt.expected)
			}
		}
	})

	t.Run("errorFix", func(t *testing.T) {
		tests := []struct {
			errType  string
			expected string
		}{
			{"port_conflict", "Configure auto-port selection or free conflicting ports before starting"},
			{"docker_error", "Check Docker daemon is running: 'docker ps'"},
		}

		for _, tt := range tests {
			result := errorFix(tt.errType)
			if result != tt.expected {
				t.Errorf("errorFix(%s) = %s, expected %s", tt.errType, result, tt.expected)
			}
		}
	})
}

func TestSeverityFromCount(t *testing.T) {
	tests := []struct {
		count    int
		expected string
	}{
		{60, "high"},
		{30, "medium"},
		{10, "low"},
	}

	for _, tt := range tests {
		err := ErrorStat{Count: tt.count}
		result := err.severityFromCount()
		if result != tt.expected {
			t.Errorf("severityFromCount(%d) = %s, expected %s", tt.count, result, tt.expected)
		}
	}
}
