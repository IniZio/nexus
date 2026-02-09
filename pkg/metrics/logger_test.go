package metrics

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogger_Log(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewLogger(tmpDir)

	entry := UsageLog{
		ID:        "test-123",
		Timestamp: time.Now().Format(time.RFC3339),
		Agent:     "sisyphus",
		Invocation: Invocation{
			Type:     "skill",
			Name:     "analyze-codebase",
			Category: "code-analysis",
		},
		Context: Context{
			Task:    "Analyze codebase",
			Project: "test-project",
			Files:   []string{"main.go", "utils.go"},
		},
		Outcome: Outcome{
			Success:  true,
			Duration: 1500,
		},
	}

	err := logger.Log(entry)
	require.NoError(t, err)

	stored, err := logger.Query(Filter{})
	require.NoError(t, err)
	assert.Len(t, stored, 1)
	assert.Equal(t, entry.ID, stored[0].ID)
	assert.Equal(t, entry.Agent, stored[0].Agent)
}

func TestLogger_QueryByAgent(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewLogger(tmpDir)

	_ = logger.Log(UsageLog{
		Agent:      "sisyphus",
		Invocation: Invocation{Type: "skill", Name: "test"},
		Timestamp:  time.Now().Format(time.RFC3339),
		Outcome:    Outcome{Success: true, Duration: 1000},
	})

	_ = logger.Log(UsageLog{
		Agent:      "oracle",
		Invocation: Invocation{Type: "skill", Name: "test2"},
		Timestamp:  time.Now().Format(time.RFC3339),
		Outcome:    Outcome{Success: true, Duration: 1000},
	})

	sisyphusLogs, err := logger.Query(Filter{Agent: "sisyphus"})
	require.NoError(t, err)
	assert.Len(t, sisyphusLogs, 1)
	assert.Equal(t, "sisyphus", sisyphusLogs[0].Agent)
}

func TestLogger_QueryByTimeRange(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewLogger(tmpDir)

	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	tomorrow := now.Add(24 * time.Hour)

	_ = logger.Log(UsageLog{
		Timestamp:  yesterday.Format(time.RFC3339),
		Invocation: Invocation{Type: "skill", Name: "test"},
		Outcome:    Outcome{Success: true, Duration: 1000},
	})

	_ = logger.Log(UsageLog{
		Timestamp:  now.Format(time.RFC3339),
		Invocation: Invocation{Type: "skill", Name: "test2"},
		Outcome:    Outcome{Success: true, Duration: 1000},
	})

	_ = logger.Log(UsageLog{
		Timestamp:  tomorrow.Format(time.RFC3339),
		Invocation: Invocation{Type: "skill", Name: "test3"},
		Outcome:    Outcome{Success: true, Duration: 1000},
	})

	todayLogs, err := logger.Query(Filter{
		StartTime: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()),
		EndTime:   time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location()),
	})
	require.NoError(t, err)
	assert.Len(t, todayLogs, 1)
}

func TestLogger_LoadCreatesNewStore(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := loggerFilePath(tmpDir)
	logger := NewLogger(tmpDir)

	_ = os.Remove(logPath)

	err := logger.Load()
	require.NoError(t, err)
	assert.Len(t, logger.store.Entries, 0)
}

func TestLogger_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewLogger(tmpDir)

	entry := UsageLog{
		Agent:      "sisyphus",
		Invocation: Invocation{Type: "skill", Name: "test"},
		Timestamp:  time.Now().Format(time.RFC3339),
		Outcome:    Outcome{Success: true, Duration: 1000},
	}

	err := logger.Log(entry)
	require.NoError(t, err)

	newLogger := NewLogger(tmpDir)
	err = newLogger.Load()
	require.NoError(t, err)
	assert.Len(t, newLogger.store.Entries, 1)
}

func loggerFilePath(basePath string) string {
	return basePath + "/.nexus/logs/usage.json"
}
