package main

import (
	"fmt"
	"os"
	"path/filepath"

	"nexus/pkg/telemetry"
)

var telemetryDB *telemetry.TelemetryDB

func initTelemetryDB() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	dbPath := filepath.Join(home, ".nexus", "telemetry.db")
	telemetryDB, err = telemetry.NewTelemetryDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open telemetry database: %w", err)
	}

	return nil
}

type TelemetryConfigCLI struct {
	Enabled       bool
	Anonymize     bool
	RetentionDays int
}

func loadTelemetryConfig() TelemetryConfigCLI {
	config := telemetry.LoadTelemetryConfig()
	return TelemetryConfigCLI{
		Enabled:       config.Enabled,
		Anonymize:     config.Anonymize,
		RetentionDays: config.RetentionDays,
	}
}

func saveTelemetryConfig(config TelemetryConfigCLI) {
	telemetryConfig := telemetry.TelemetryConfig{
		Enabled:       config.Enabled,
		Anonymize:     config.Anonymize,
		RetentionDays: config.RetentionDays,
	}
	telemetry.SaveTelemetryConfig(telemetryConfig)
}

type CLITelemetryStats struct {
	TotalEvents        int
	TotalSessions      int
	TotalCommands      int
	SuccessRate        float64
	AvgCommandDuration string
	WorkspacesCreated  int
	TasksCompleted     int
	TopCommands        []CommandStatInfo
	CommonErrors       []ErrorStatInfo
}

type CommandStatInfo struct {
	Name  string
	Count int
}

type ErrorStatInfo struct {
	Type  string
	Count int
}

func getTelemetryStats(days int) (CLITelemetryStats, error) {
	if telemetryDB == nil {
		if err := initTelemetryDB(); err != nil {
			return CLITelemetryStats{}, err
		}
	}

	stats, err := telemetryDB.GetCLIStats(days)
	if err != nil {
		return CLITelemetryStats{}, err
	}

	topCommands := make([]CommandStatInfo, len(stats.TopCommands))
	for i, cmd := range stats.TopCommands {
		topCommands[i] = CommandStatInfo{
			Name:  cmd.Command,
			Count: cmd.Count,
		}
	}

	commonErrors := make([]ErrorStatInfo, len(stats.CommonErrors))
	for i, err := range stats.CommonErrors {
		commonErrors[i] = ErrorStatInfo{
			Type:  err.ErrorType,
			Count: err.Count,
		}
	}

	return CLITelemetryStats{
		TotalEvents:        stats.TotalEvents,
		TotalSessions:      stats.TotalSessions,
		TotalCommands:      stats.TotalCommands,
		SuccessRate:        stats.SuccessRate * 100,
		AvgCommandDuration: stats.AvgCommandDuration.String(),
		WorkspacesCreated:  stats.WorkspacesCreated,
		TasksCompleted:     stats.TasksCompleted,
		TopCommands:        topCommands,
		CommonErrors:       commonErrors,
	}, nil
}

type telemetryEventWrapper struct {
	ID         string
	Timestamp  string
	SessionID  string
	EventType  string
	Command    string
	Success    bool
	DurationMs int64
	ErrorType  string
}

func getAllTelemetryEvents() ([]telemetryEventWrapper, error) {
	if telemetryDB == nil {
		if err := initTelemetryDB(); err != nil {
			return nil, err
		}
	}

	events, err := telemetryDB.GetAllEvents()
	if err != nil {
		return nil, err
	}

	wrapped := make([]telemetryEventWrapper, len(events))
	for i, e := range events {
		wrapped[i] = telemetryEventWrapper{
			ID:         e.ID,
			Timestamp:  e.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			SessionID:  e.SessionID,
			EventType:  e.EventType,
			Command:    e.Command,
			Success:    e.Success,
			DurationMs: e.Duration.Milliseconds(),
			ErrorType:  e.ErrorType,
		}
	}

	return wrapped, nil
}
