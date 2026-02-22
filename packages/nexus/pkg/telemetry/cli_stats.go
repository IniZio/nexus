package telemetry

import (
	"database/sql"
	"time"
)

type CLIStats struct {
	TotalEvents        int           `json:"total_events"`
	TotalSessions      int           `json:"total_sessions"`
	TotalCommands      int           `json:"total_commands"`
	SuccessRate        float64       `json:"success_rate"`
	AvgCommandDuration time.Duration `json:"avg_command_duration"`
	WorkspacesCreated  int           `json:"workspaces_created"`
	TasksCompleted     int           `json:"tasks_completed"`
	TopCommands        []CommandStat `json:"top_commands"`
	CommonErrors       []ErrorStat   `json:"common_errors"`
}

func (t *TelemetryDB) GetCLIStats(days int) (CLIStats, error) {
	since := time.Now().AddDate(0, 0, -days)

	stats := CLIStats{}

	totalEvents := 0
	err := t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE timestamp >= ?
	`, since).Scan(&totalEvents)
	if err != nil {
		return stats, err
	}
	stats.TotalEvents = totalEvents

	totalSessions := 0
	err = t.db.QueryRow(`
		SELECT COUNT(DISTINCT session_id) FROM events WHERE timestamp >= ?
	`, since).Scan(&totalSessions)
	if err != nil {
		return stats, err
	}
	stats.TotalSessions = totalSessions

	totalCmds := 0
	err = t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE event_type = 'command' AND timestamp >= ?
	`, since).Scan(&totalCmds)
	if err != nil {
		return stats, err
	}
	stats.TotalCommands = totalCmds

	successfulCmds := 0
	err = t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE event_type = 'command' AND success = 1 AND timestamp >= ?
	`, since).Scan(&successfulCmds)
	if err != nil {
		return stats, err
	}
	if totalCmds > 0 {
		stats.SuccessRate = float64(successfulCmds) / float64(totalCmds)
	}

	var avgDuration sql.NullFloat64
	err = t.db.QueryRow(`
		SELECT AVG(duration_ms) FROM events WHERE event_type = 'command' AND timestamp >= ?
	`, since).Scan(&avgDuration)
	if err != nil {
		return stats, err
	}
	if avgDuration.Valid {
		stats.AvgCommandDuration = time.Duration(avgDuration.Float64) * time.Millisecond
	}

	stats.TopCommands, err = t.getTopCommands(since)
	if err != nil {
		return stats, err
	}

	stats.CommonErrors, err = t.getCommonErrors(since)
	if err != nil {
		return stats, err
	}

	workspaces := 0
	err = t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE event_type = 'workspace' AND command = 'create' AND timestamp >= ?
	`, since).Scan(&workspaces)
	if err != nil {
		return stats, err
	}
	stats.WorkspacesCreated = workspaces

	tasks := 0
	err = t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE event_type = 'task' AND command = 'complete' AND timestamp >= ?
	`, since).Scan(&tasks)
	if err != nil {
		return stats, err
	}
	stats.TasksCompleted = tasks

	return stats, nil
}

func (t *TelemetryDB) GetAllEvents() ([]Event, error) {
	query := `SELECT * FROM events ORDER BY timestamp DESC`

	rows, err := t.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows)
}
