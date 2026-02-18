package telemetry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TelemetryDB handles database operations
type TelemetryDB struct {
	db *sql.DB
}

// NewTelemetryDB creates/opens telemetry database
func NewTelemetryDB(path string) (*TelemetryDB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	tdb := &TelemetryDB{db: db}
	if err := tdb.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to init schema: %w", err)
	}

	return tdb, nil
}

func (t *TelemetryDB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		session_id TEXT,
		event_type TEXT NOT NULL,
		workspace_hash TEXT,
		task_hash TEXT,
		command TEXT,
		args TEXT,
		duration_ms INTEGER,
		success BOOLEAN,
		error_type TEXT,
		template_used TEXT,
		services_count INTEGER,
		ports_used INTEGER
	);
	
	CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type);
	CREATE INDEX IF NOT EXISTS idx_events_time ON events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
	CREATE INDEX IF NOT EXISTS idx_events_command ON events(command);
	
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		started_at DATETIME,
		ended_at DATETIME,
		duration_ms INTEGER,
		commands_executed INTEGER DEFAULT 0,
		workspaces_created INTEGER DEFAULT 0,
		tasks_completed INTEGER DEFAULT 0,
		errors_encountered INTEGER DEFAULT 0,
		user_feedback TEXT
	);
	
	CREATE TABLE IF NOT EXISTS patterns (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		pattern_type TEXT,
		description TEXT,
		frequency INTEGER DEFAULT 1,
		first_seen DATETIME,
		last_seen DATETIME,
		affected_sessions TEXT,
		suggested_fix TEXT
	);
	
	CREATE INDEX IF NOT EXISTS idx_patterns_type ON patterns(pattern_type);
	CREATE INDEX IF NOT EXISTS idx_patterns_last_seen ON patterns(last_seen);
	`

	_, err := t.db.Exec(schema)
	return err
}

// SaveEvent saves a telemetry event
func (t *TelemetryDB) SaveEvent(e Event) error {
	argsJSON, _ := json.Marshal(e.Args)

	query := `
	INSERT OR REPLACE INTO events (
		id, timestamp, session_id, event_type, workspace_hash, task_hash,
		command, args, duration_ms, success, error_type, template_used,
		services_count, ports_used
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := t.db.Exec(query,
		e.ID, e.Timestamp, e.SessionID, e.EventType, e.WorkspaceHash,
		e.TaskHash, e.Command, string(argsJSON), int64(e.Duration.Milliseconds()),
		e.Success, e.ErrorType, e.TemplateUsed, e.ServicesCount, e.PortsUsed,
	)
	return err
}

// SaveSession saves a new session
func (t *TelemetryDB) SaveSession(s Session) error {
	query := `
	INSERT INTO sessions (
		id, started_at, duration_ms, commands_executed, workspaces_created,
		tasks_completed, errors_encountered, user_feedback
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := t.db.Exec(query,
		s.ID, s.StartedAt, int64(s.Duration.Milliseconds()), s.CommandsExecuted,
		s.WorkspacesCreated, s.TasksCompleted, s.ErrorsEncountered, s.UserFeedback,
	)
	return err
}

// EndSession ends a session and updates statistics
func (t *TelemetryDB) EndSession(sessionID, feedback string) error {
	now := time.Now()

	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var duration int64
	err = tx.QueryRow(`
		SELECT COALESCE(SUM(duration_ms), 0) FROM events WHERE session_id = ?
	`, sessionID).Scan(&duration)
	if err != nil {
		return err
	}

	commands := 0
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM events WHERE session_id = ? AND event_type = 'command'
	`, sessionID).Scan(&commands)
	if err != nil {
		return err
	}

	workspaces := 0
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM events WHERE session_id = ? AND event_type = 'workspace' AND command = 'create'
	`, sessionID).Scan(&workspaces)
	if err != nil {
		return err
	}

	tasks := 0
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM events WHERE session_id = ? AND event_type = 'task' AND command = 'complete'
	`, sessionID).Scan(&tasks)
	if err != nil {
		return err
	}

	errors := 0
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM events WHERE session_id = ? AND success = 0
	`, sessionID).Scan(&errors)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		UPDATE sessions SET ended_at = ?, duration_ms = ?, commands_executed = ?,
		workspaces_created = ?, tasks_completed = ?, errors_encountered = ?,
		user_feedback = ? WHERE id = ?
	`, now, duration, commands, workspaces, tasks, errors, feedback, sessionID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// QueryEvents returns events within a time range
func (t *TelemetryDB) QueryEvents(since time.Time) ([]Event, error) {
	query := `SELECT * FROM events WHERE timestamp >= ? ORDER BY timestamp`

	rows, err := t.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var argsJSON string
		var durationMs sql.NullInt64

		err := rows.Scan(
			&e.ID, &e.Timestamp, &e.SessionID, &e.EventType,
			&e.WorkspaceHash, &e.TaskHash, &e.Command, &argsJSON,
			&durationMs, &e.Success, &e.ErrorType, &e.TemplateUsed,
			&e.ServicesCount, &e.PortsUsed,
		)
		if err != nil {
			return nil, err
		}

		if durationMs.Valid {
			e.Duration = time.Duration(durationMs.Int64) * time.Millisecond
		}

		if argsJSON != "" {
			json.Unmarshal([]byte(argsJSON), &e.Args)
		}

		events = append(events, e)
	}

	return events, rows.Err()
}

// GetEventsBySession returns all events for a session
func (t *TelemetryDB) GetEventsBySession(sessionID string) ([]Event, error) {
	query := `SELECT * FROM events WHERE session_id = ? ORDER BY timestamp`

	rows, err := t.db.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows)
}

// GetStats returns usage statistics
func (t *TelemetryDB) GetStats(days int) (Stats, error) {
	since := time.Now().AddDate(0, 0, -days)

	stats := Stats{}

	totalCmds := 0
	err := t.db.QueryRow(`
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
		stats.SuccessRate = float64(successfulCmds) / float64(totalCmds) * 100
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

	stats.WorkspaceStats, err = t.getWorkspaceStats(since)
	if err != nil {
		return stats, err
	}

	stats.TaskStats, err = t.getTaskStats(since)
	if err != nil {
		return stats, err
	}

	return stats, nil
}

func (t *TelemetryDB) getTopCommands(since time.Time) ([]CommandStat, error) {
	query := `
		SELECT command, COUNT(*) as count, AVG(duration_ms) as avg_dur,
			(COUNT(CASE WHEN success = 1 THEN 1 END) * 100.0 / COUNT(*)) as success_rate
		FROM events WHERE event_type = 'command' AND command != '' AND timestamp >= ?
		GROUP BY command ORDER BY count DESC LIMIT 10
	`

	rows, err := t.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commands []CommandStat
	for rows.Next() {
		var cs CommandStat
		var avgDur sql.NullFloat64
		var successRate sql.NullFloat64

		err := rows.Scan(&cs.Command, &cs.Count, &avgDur, &successRate)
		if err != nil {
			return nil, err
		}

		if avgDur.Valid {
			cs.AvgDuration = int64(avgDur.Float64)
		}
		if successRate.Valid {
			cs.SuccessRate = successRate.Float64
		}

		commands = append(commands, cs)
	}

	return commands, rows.Err()
}

func (t *TelemetryDB) getCommonErrors(since time.Time) ([]ErrorStat, error) {
	query := `
		SELECT error_type, COUNT(*) as count, MIN(timestamp) as first_seen, MAX(timestamp) as last_seen
		FROM events WHERE event_type = 'command' AND success = 0 AND error_type != '' AND timestamp >= ?
		GROUP BY error_type ORDER BY count DESC LIMIT 10
	`

	rows, err := t.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var errors []ErrorStat
	for rows.Next() {
		var es ErrorStat
		var firstSeenStr, lastSeenStr string
		err := rows.Scan(&es.ErrorType, &es.Count, &firstSeenStr, &lastSeenStr)
		if err != nil {
			return nil, err
		}
		if firstSeenStr != "" {
			es.FirstSeen, _ = time.Parse("2006-01-02 15:04:05.999999999-07:00", firstSeenStr)
		}
		if lastSeenStr != "" {
			es.LastSeen, _ = time.Parse("2006-01-02 15:04:05.999999999-07:00", lastSeenStr)
		}
		errors = append(errors, es)
	}

	return errors, rows.Err()
}

func (t *TelemetryDB) getWorkspaceStats(since time.Time) (WorkspaceStats, error) {
	ws := WorkspaceStats{}

	err := t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE event_type = 'workspace' AND command = 'create' AND timestamp >= ?
	`, since).Scan(&ws.TotalCreated)
	if err != nil {
		return ws, err
	}

	err = t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE event_type = 'workspace' AND command = 'destroy' AND timestamp >= ?
	`, since).Scan(&ws.TotalDestroyed)
	if err != nil {
		return ws, err
	}

	ws.ActiveCount = ws.TotalCreated - ws.TotalDestroyed

	ws.TopTemplates, err = t.getTopTemplates(since)
	if err != nil {
		return ws, err
	}

	return ws, nil
}

func (t *TelemetryDB) getTopTemplates(since time.Time) ([]TemplateStat, error) {
	query := `
		SELECT template_used, COUNT(*) as count
		FROM events WHERE event_type = 'workspace' AND template_used != '' AND timestamp >= ?
		GROUP BY template_used ORDER BY count DESC LIMIT 5
	`

	rows, err := t.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []TemplateStat
	for rows.Next() {
		var ts TemplateStat
		err := rows.Scan(&ts.TemplateName, &ts.Count)
		if err != nil {
			return nil, err
		}
		templates = append(templates, ts)
	}

	return templates, rows.Err()
}

func (t *TelemetryDB) getTaskStats(since time.Time) (TaskStats, error) {
	ts := TaskStats{}

	err := t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE event_type = 'task' AND command = 'create' AND timestamp >= ?
	`, since).Scan(&ts.TotalCreated)
	if err != nil {
		return ts, err
	}

	err = t.db.QueryRow(`
		SELECT COUNT(*) FROM events WHERE event_type = 'task' AND command = 'complete' AND timestamp >= ?
	`, since).Scan(&ts.TotalCompleted)
	if err != nil {
		return ts, err
	}

	if ts.TotalCreated > 0 {
		ts.CompletionRate = float64(ts.TotalCompleted) / float64(ts.TotalCreated) * 100
	}

	var avgDuration sql.NullFloat64
	err = t.db.QueryRow(`
		SELECT AVG(duration_ms) FROM events WHERE event_type = 'task' AND timestamp >= ?
	`, since).Scan(&avgDuration)
	if err != nil {
		return ts, err
	}
	if avgDuration.Valid {
		ts.AvgDuration = time.Duration(avgDuration.Float64) * time.Millisecond
	}

	return ts, nil
}

// GetActiveSessions returns currently active sessions
func (t *TelemetryDB) GetActiveSessions() ([]Session, error) {
	query := `SELECT * FROM sessions WHERE ended_at IS NULL ORDER BY started_at`

	rows, err := t.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSessions(rows)
}

// DeleteOldEvents removes events older than the specified duration
func (t *TelemetryDB) DeleteOldEvents(olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	_, err := t.db.Exec(`DELETE FROM events WHERE timestamp < ?`, cutoff)
	return err
}

// Close closes the database connection
func (t *TelemetryDB) Close() error {
	return t.db.Close()
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var e Event
		var argsJSON string
		var durationMs sql.NullInt64

		err := rows.Scan(
			&e.ID, &e.Timestamp, &e.SessionID, &e.EventType,
			&e.WorkspaceHash, &e.TaskHash, &e.Command, &argsJSON,
			&durationMs, &e.Success, &e.ErrorType, &e.TemplateUsed,
			&e.ServicesCount, &e.PortsUsed,
		)
		if err != nil {
			return nil, err
		}

		if durationMs.Valid {
			e.Duration = time.Duration(durationMs.Int64) * time.Millisecond
		}

		if argsJSON != "" && argsJSON != "null" {
			json.Unmarshal([]byte(argsJSON), &e.Args)
		}

		events = append(events, e)
	}
	return events, rows.Err()
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	var sessions []Session
	for rows.Next() {
		var s Session
		var durationMs int64
		var endedAt sql.NullTime

		err := rows.Scan(
			&s.ID, &s.StartedAt, &endedAt, &durationMs,
			&s.CommandsExecuted, &s.WorkspacesCreated, &s.TasksCompleted,
			&s.ErrorsEncountered, &s.UserFeedback,
		)
		if err != nil {
			return nil, err
		}

		s.Duration = time.Duration(durationMs) * time.Millisecond
		if endedAt.Valid {
			s.EndedAt = &endedAt.Time
		}

		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
