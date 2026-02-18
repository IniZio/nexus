package coordination

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type PortMapping struct {
	ID            string
	WorkspaceName string
	ServiceName   string
	ContainerPort int
	HostPort      int
	Protocol      string
	CreatedAt     time.Time
}

type Storage interface {
	CreateTask(ctx context.Context, task *Task) error
	GetTask(ctx context.Context, id string) (*Task, error)
	UpdateTask(ctx context.Context, id string, task *Task) error
	DeleteTask(ctx context.Context, id string) error
	ListTasks(ctx context.Context, workspaceID string, status TaskStatus) ([]*Task, error)
	CompleteTask(ctx context.Context, id string) (*Task, error)

	RegisterAgent(ctx context.Context, agent *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	UpdateAgent(ctx context.Context, id string, agent *Agent) error
	DeleteAgent(ctx context.Context, id string) error
	ListAgents(ctx context.Context, workspaceID string) ([]*Agent, error)

	RecordEvent(ctx context.Context, event *Event) error
	ListEvents(ctx context.Context, workspaceID string, limit int) ([]*Event, error)

	SavePortMapping(ctx context.Context, workspaceName string, mapping PortMapping) error
	GetPortMappings(ctx context.Context, workspaceName string) ([]PortMapping, error)
	DeletePortMappings(ctx context.Context, workspaceName string) error
	ListAllPortMappings(ctx context.Context) (map[string][]PortMapping, error)

	SaveFeedback(ctx context.Context, feedback *SessionFeedback) error
	GetFeedback(ctx context.Context, sessionID string) (*SessionFeedback, error)
	GetFeedbackByTimeRange(ctx context.Context, start, end time.Time) ([]*SessionFeedback, error)
	GetPatterns(ctx context.Context, threshold int) ([]*Pattern, error)

	RunAutomatedChecks(ctx context.Context, taskID, workspaceName string) (VerificationCriteria, error)
	ValidateCriteria(criteria VerificationCriteria) error
	CompleteManualChecklist(ctx context.Context, taskID string, items []string) error
	GetManualChecklist(ctx context.Context, taskID string) ([]ManualChecklistItem, error)
	SetCustomCheck(ctx context.Context, taskID, checkName string, passed bool) error

	Close() error
}

type SQLiteStorage struct {
	db          *sql.DB
	workspaceID string
}

func NewSQLiteStorage(dbPath string, workspaceID string, workspacesRoot string) (*SQLiteStorage, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	storage := &SQLiteStorage{
		db:          db,
		workspaceID: workspaceID,
	}

	if err := storage.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	if err := storage.migrateSchema(); err != nil {
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}

	return storage, nil
}

func (s *SQLiteStorage) initSchema() error {
	query := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		title TEXT NOT NULL,
		description TEXT,
		status TEXT NOT NULL DEFAULT 'pending',
		assignee TEXT,
		priority INTEGER DEFAULT 0,
		depends_on TEXT,
		verification_by TEXT,
		verification_at TEXT,
		verification_criteria TEXT,
		rejection_count INTEGER DEFAULT 0,
		rejection_history TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		completed_at TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_tasks_workspace ON tasks(workspace_id);
	CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
	CREATE INDEX IF NOT EXISTS idx_tasks_assignee ON tasks(assignee);

	CREATE TABLE IF NOT EXISTS agents (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		name TEXT NOT NULL,
		capabilities TEXT,
		current_task TEXT,
		status TEXT NOT NULL DEFAULT 'idle',
		metadata TEXT,
		created_at TEXT NOT NULL,
		last_seen_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_agents_workspace ON agents(workspace_id);
	CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);

	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		type TEXT NOT NULL,
		task_id TEXT,
		agent_id TEXT,
		timestamp TEXT NOT NULL,
		data TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_events_workspace ON events(workspace_id);
	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp DESC);

	CREATE TABLE IF NOT EXISTS port_mappings (
		id TEXT PRIMARY KEY,
		workspace_name TEXT NOT NULL,
		service_name TEXT NOT NULL,
		container_port INTEGER NOT NULL,
		host_port INTEGER NOT NULL,
		protocol TEXT NOT NULL DEFAULT 'tcp',
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_port_mappings_workspace ON port_mappings(workspace_name);
	CREATE INDEX IF NOT EXISTS idx_port_mappings_host_port ON port_mappings(host_port);

	CREATE TABLE IF NOT EXISTS feedback (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		workspace_name TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		tasks_worked TEXT,
		issues TEXT,
		duration REAL NOT NULL,
		success_rate REAL NOT NULL,
		timestamp TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_feedback_session ON feedback(session_id);
	CREATE INDEX IF NOT EXISTS idx_feedback_timestamp ON feedback(timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_feedback_workspace ON feedback(workspace_name);
	`
	_, err := s.db.Exec(query)
	if err != nil {
		return err
	}

	s.db.Exec("PRAGMA journal_mode=WAL")

	return nil
}

func (s *SQLiteStorage) CreateTask(ctx context.Context, task *Task) error {
	task.ID = generateID()
	task.WorkspaceID = s.workspaceID
	task.CreatedAt = time.Now()
	task.UpdatedAt = time.Now()
	if task.Status == "" {
		task.Status = TaskStatusPending
	}

	rejectionHistoryJSON, _ := json.Marshal(task.RejectionHistory)

	query := `
	INSERT INTO tasks (id, workspace_id, title, description, status, assignee, priority, depends_on, verification_by, verification_at, verification_criteria, rejection_count, rejection_history, created_at, updated_at, completed_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		task.ID, task.WorkspaceID, task.Title, task.Description,
		task.Status, task.Assignee, task.Priority, joinStrings(task.DependsOn),
		task.VerificationBy, nil, nil, task.RejectionCount, string(rejectionHistoryJSON),
		task.CreatedAt.Format(time.RFC3339), task.UpdatedAt.Format(time.RFC3339), nil,
	)
	return err
}

func (s *SQLiteStorage) GetTask(ctx context.Context, id string) (*Task, error) {
	query := `
	SELECT id, workspace_id, title, description, status, assignee, priority, depends_on, verification_by, verification_at, verification_criteria, rejection_count, rejection_history, created_at, updated_at, completed_at
	FROM tasks WHERE id = ? AND workspace_id = ?
	`
	var task Task
	var createdAtStr, updatedAtStr string
	var dependsOnStr, verificationByStr, rejectionHistoryStr sql.NullString
	var completedAt, assignee, verificationAt, verificationCriteria sql.NullString

	err := s.db.QueryRowContext(ctx, query, id, s.workspaceID).Scan(
		&task.ID, &task.WorkspaceID, &task.Title, &task.Description,
		&task.Status, &assignee, &task.Priority, &dependsOnStr,
		&verificationByStr, &verificationAt, &verificationCriteria,
		&task.RejectionCount, &rejectionHistoryStr,
		&createdAtStr, &updatedAtStr, &completedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	task.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	task.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAtStr)
	if assignee.Valid {
		task.Assignee = assignee.String
	}
	if dependsOnStr.Valid && dependsOnStr.String != "" {
		task.DependsOn = splitStrings(dependsOnStr.String)
	}
	if verificationByStr.Valid {
		task.VerificationBy = verificationByStr.String
	}
	if verificationAt.Valid {
		t, _ := time.Parse(time.RFC3339, verificationAt.String)
		task.VerificationAt = &t
	}
	if verificationCriteria.Valid && verificationCriteria.String != "" && verificationCriteria.String != "null" {
		var criteria VerificationCriteria
		if err := json.Unmarshal([]byte(verificationCriteria.String), &criteria); err == nil {
			task.Verification = &criteria
		}
	}
	if rejectionHistoryStr.Valid && rejectionHistoryStr.String != "" && rejectionHistoryStr.String != "null" {
		json.Unmarshal([]byte(rejectionHistoryStr.String), &task.RejectionHistory)
	}
	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339, completedAt.String)
		task.CompletedAt = &t
	}

	return &task, nil
}

func (s *SQLiteStorage) UpdateTask(ctx context.Context, id string, task *Task) error {
	task.UpdatedAt = time.Now()
	rejectionHistoryJSON, _ := json.Marshal(task.RejectionHistory)

	var verificationCriteriaStr *string
	if task.Verification != nil {
		v, _ := json.Marshal(task.Verification)
		s := string(v)
		verificationCriteriaStr = &s
	}

	var verificationAtStr *string
	if task.VerificationAt != nil {
		s := task.VerificationAt.Format(time.RFC3339)
		verificationAtStr = &s
	}

	query := `
	UPDATE tasks SET title = ?, description = ?, status = ?, assignee = ?, priority = ?, depends_on = ?, verification_by = ?, verification_at = ?, verification_criteria = ?, rejection_count = ?, rejection_history = ?, updated_at = ?
	WHERE id = ? AND workspace_id = ?
	`
	_, err := s.db.ExecContext(ctx, query,
		task.Title, task.Description, task.Status, task.Assignee, task.Priority,
		joinStrings(task.DependsOn), task.VerificationBy, verificationAtStr, verificationCriteriaStr,
		task.RejectionCount, string(rejectionHistoryJSON), task.UpdatedAt.Format(time.RFC3339),
		id, s.workspaceID,
	)
	return err
}

func (s *SQLiteStorage) DeleteTask(ctx context.Context, id string) error {
	query := "DELETE FROM tasks WHERE id = ? AND workspace_id = ?"
	_, err := s.db.ExecContext(ctx, query, id, s.workspaceID)
	return err
}

func (s *SQLiteStorage) ListTasks(ctx context.Context, workspaceID string, status TaskStatus) ([]*Task, error) {
	query := `
	SELECT id, workspace_id, title, description, status, assignee, priority, depends_on, verification_by, verification_at, verification_criteria, rejection_count, rejection_history, created_at, updated_at, completed_at
	FROM tasks WHERE workspace_id = ?
	`
	args := []interface{}{s.workspaceID}

	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}

	query += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		var task Task
		var createdAtStr, updatedAtStr string
		var dependsOnStr, verificationByStr, rejectionHistoryStr sql.NullString
		var completedAt, assignee, verificationAt, verificationCriteria sql.NullString

		err := rows.Scan(
			&task.ID, &task.WorkspaceID, &task.Title, &task.Description,
			&task.Status, &assignee, &task.Priority, &dependsOnStr,
			&verificationByStr, &verificationAt, &verificationCriteria,
			&task.RejectionCount, &rejectionHistoryStr,
			&createdAtStr, &updatedAtStr, &completedAt,
		)
		if err != nil {
			return nil, err
		}

		task.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		task.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAtStr)
		if assignee.Valid {
			task.Assignee = assignee.String
		}
		if dependsOnStr.Valid && dependsOnStr.String != "" {
			task.DependsOn = splitStrings(dependsOnStr.String)
		}
		if verificationByStr.Valid {
			task.VerificationBy = verificationByStr.String
		}
		if verificationAt.Valid {
			t, _ := time.Parse(time.RFC3339, verificationAt.String)
			task.VerificationAt = &t
		}
		if verificationCriteria.Valid && verificationCriteria.String != "" && verificationCriteria.String != "null" {
			var criteria VerificationCriteria
			if err := json.Unmarshal([]byte(verificationCriteria.String), &criteria); err == nil {
				task.Verification = &criteria
			}
		}
		if rejectionHistoryStr.Valid && rejectionHistoryStr.String != "" && rejectionHistoryStr.String != "null" {
			json.Unmarshal([]byte(rejectionHistoryStr.String), &task.RejectionHistory)
		}
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339, completedAt.String)
			task.CompletedAt = &t
		}

		tasks = append(tasks, &task)
	}

	return tasks, rows.Err()
}

func (s *SQLiteStorage) CompleteTask(ctx context.Context, id string) (*Task, error) {
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", id)
	}

	if err := s.checkDependencies(ctx, task); err != nil {
		return nil, fmt.Errorf("cannot complete task: %w", err)
	}

	now := time.Now()
	task.CompletedAt = &now
	task.Status = TaskStatusVerification
	task.UpdatedAt = now

	query := `
	UPDATE tasks SET status = ?, completed_at = ?, updated_at = ?
	WHERE id = ? AND workspace_id = ?
	`
	_, err = s.db.ExecContext(ctx, query,
		TaskStatusVerification, now.Format(time.RFC3339), now.Format(time.RFC3339),
		id, s.workspaceID,
	)
	return task, err
}

func (s *SQLiteStorage) checkDependencies(ctx context.Context, task *Task) error {
	if len(task.DependsOn) == 0 {
		return nil
	}

	for _, depID := range task.DependsOn {
		dep, err := s.GetTask(ctx, depID)
		if err != nil {
			return err
		}
		if dep == nil {
			return fmt.Errorf("dependency not found: %s", depID)
		}
		if dep.Status != TaskStatusCompleted {
			return fmt.Errorf("dependency not completed: %s (%s)", depID, dep.Status)
		}
	}
	return nil
}

func (s *SQLiteStorage) RegisterAgent(ctx context.Context, agent *Agent) error {
	agent.ID = generateAgentID(agent.Name)
	agent.WorkspaceID = s.workspaceID
	agent.CreatedAt = time.Now()
	agent.LastSeenAt = time.Now()
	if agent.Status == "" {
		agent.Status = AgentStatusIdle
	}

	query := `
	INSERT INTO agents (id, workspace_id, name, capabilities, current_task, status, metadata, created_at, last_seen_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		agent.ID, agent.WorkspaceID, agent.Name, joinStrings(agent.Capabilities),
		agent.CurrentTask, agent.Status, "",
		agent.CreatedAt.Format(time.RFC3339), agent.LastSeenAt.Format(time.RFC3339),
	)
	return err
}

func (s *SQLiteStorage) GetAgent(ctx context.Context, id string) (*Agent, error) {
	query := `
	SELECT id, workspace_id, name, capabilities, current_task, status, created_at, last_seen_at
	FROM agents WHERE id = ? AND workspace_id = ?
	`
	var agent Agent
	var createdAtStr, lastSeenStr, capabilitiesStr string

	err := s.db.QueryRowContext(ctx, query, id, s.workspaceID).Scan(
		&agent.ID, &agent.WorkspaceID, &agent.Name, &capabilitiesStr,
		&agent.CurrentTask, &agent.Status,
		&createdAtStr, &lastSeenStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	agent.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	agent.LastSeenAt, _ = time.Parse(time.RFC3339, lastSeenStr)
	if capabilitiesStr != "" {
		agent.Capabilities = splitStrings(capabilitiesStr)
	}

	return &agent, nil
}

func (s *SQLiteStorage) UpdateAgent(ctx context.Context, id string, agent *Agent) error {
	agent.LastSeenAt = time.Now()
	query := `
	UPDATE agents SET name = ?, capabilities = ?, current_task = ?, status = ?, last_seen_at = ?
	WHERE id = ? AND workspace_id = ?
	`
	_, err := s.db.ExecContext(ctx, query,
		agent.Name, joinStrings(agent.Capabilities), agent.CurrentTask,
		agent.Status, agent.LastSeenAt.Format(time.RFC3339),
		id, s.workspaceID,
	)
	return err
}

func (s *SQLiteStorage) DeleteAgent(ctx context.Context, id string) error {
	query := "DELETE FROM agents WHERE id = ? AND workspace_id = ?"
	_, err := s.db.ExecContext(ctx, query, id, s.workspaceID)
	return err
}

func (s *SQLiteStorage) ListAgents(ctx context.Context, workspaceID string) ([]*Agent, error) {
	query := `
	SELECT id, workspace_id, name, capabilities, current_task, status, created_at, last_seen_at
	FROM agents WHERE workspace_id = ? AND status != 'offline'
	ORDER BY last_seen_at DESC
	`

	rows, err := s.db.QueryContext(ctx, query, s.workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		var agent Agent
		var createdAtStr, lastSeenStr, capabilitiesStr string

		err := rows.Scan(
			&agent.ID, &agent.WorkspaceID, &agent.Name, &capabilitiesStr,
			&agent.CurrentTask, &agent.Status,
			&createdAtStr, &lastSeenStr,
		)
		if err != nil {
			return nil, err
		}

		agent.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		agent.LastSeenAt, _ = time.Parse(time.RFC3339, lastSeenStr)
		if capabilitiesStr != "" {
			agent.Capabilities = splitStrings(capabilitiesStr)
		}

		agents = append(agents, &agent)
	}

	return agents, rows.Err()
}

func (s *SQLiteStorage) RecordEvent(ctx context.Context, event *Event) error {
	event.ID = generateEventID()
	event.Timestamp = time.Now()

	query := `
	INSERT INTO events (id, workspace_id, type, task_id, agent_id, timestamp, data)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		event.ID, s.workspaceID, event.Type, event.TaskID, event.AgentID,
		event.Timestamp.Format(time.RFC3339), event.Data,
	)
	return err
}

func (s *SQLiteStorage) ListEvents(ctx context.Context, workspaceID string, limit int) ([]*Event, error) {
	query := `
	SELECT id, workspace_id, type, task_id, agent_id, timestamp, data
	FROM events WHERE workspace_id = ?
	ORDER BY timestamp DESC
	`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.QueryContext(ctx, query, s.workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		var event Event
		var timestampStr string

		err := rows.Scan(
			&event.ID, &event.WorkspaceID, &event.Type, &event.TaskID,
			&event.AgentID, &timestampStr, &event.Data,
		)
		if err != nil {
			return nil, err
		}

		event.Timestamp, _ = time.Parse(time.RFC3339, timestampStr)
		events = append(events, &event)
	}

	return events, rows.Err()
}

func (s *SQLiteStorage) SavePortMapping(ctx context.Context, workspaceName string, mapping PortMapping) error {
	mapping.ID = generatePortMappingID(workspaceName, mapping.ServiceName)
	mapping.CreatedAt = time.Now()

	query := `
	INSERT OR REPLACE INTO port_mappings (id, workspace_name, service_name, container_port, host_port, protocol, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		mapping.ID, workspaceName, mapping.ServiceName,
		mapping.ContainerPort, mapping.HostPort, mapping.Protocol,
		mapping.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *SQLiteStorage) GetPortMappings(ctx context.Context, workspaceName string) ([]PortMapping, error) {
	query := `
	SELECT id, workspace_name, service_name, container_port, host_port, protocol, created_at
	FROM port_mappings WHERE workspace_name = ?
	ORDER BY service_name
	`

	rows, err := s.db.QueryContext(ctx, query, workspaceName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mappings []PortMapping
	for rows.Next() {
		var mapping PortMapping
		var createdAtStr string

		err := rows.Scan(
			&mapping.ID, &mapping.WorkspaceName, &mapping.ServiceName,
			&mapping.ContainerPort, &mapping.HostPort, &mapping.Protocol, &createdAtStr,
		)
		if err != nil {
			return nil, err
		}

		mapping.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		mappings = append(mappings, mapping)
	}

	return mappings, rows.Err()
}

func (s *SQLiteStorage) DeletePortMappings(ctx context.Context, workspaceName string) error {
	query := "DELETE FROM port_mappings WHERE workspace_name = ?"
	_, err := s.db.ExecContext(ctx, query, workspaceName)
	return err
}

func (s *SQLiteStorage) ListAllPortMappings(ctx context.Context) (map[string][]PortMapping, error) {
	query := `
	SELECT id, workspace_name, service_name, container_port, host_port, protocol, created_at
	FROM port_mappings
	ORDER BY workspace_name, service_name
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]PortMapping)
	for rows.Next() {
		var mapping PortMapping
		var createdAtStr string

		err := rows.Scan(
			&mapping.ID, &mapping.WorkspaceName, &mapping.ServiceName,
			&mapping.ContainerPort, &mapping.HostPort, &mapping.Protocol, &createdAtStr,
		)
		if err != nil {
			return nil, err
		}

		mapping.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		result[mapping.WorkspaceName] = append(result[mapping.WorkspaceName], mapping)
	}

	return result, rows.Err()
}

func (s *SQLiteStorage) migrateSchema() error {
	columns, err := s.getTableColumns("tasks")
	if err != nil {
		return err
	}

	migrations := []struct {
		column string
		def    string
	}{
		{"verification_by", "TEXT"},
		{"verification_at", "TEXT"},
		{"verification_criteria", "TEXT"},
		{"rejection_count", "INTEGER DEFAULT 0"},
		{"rejection_history", "TEXT"},
	}

	for _, m := range migrations {
		if !stringSliceContains(columns, m.column) {
			_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE tasks ADD COLUMN %s %s", m.column, m.def))
			if err != nil {
				return fmt.Errorf("failed to add column %s: %w", m.column, err)
			}
		}
	}

	return nil
}

func (s *SQLiteStorage) getTableColumns(tableName string) ([]string, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", tableName)
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var cid int
		var name string
		var dtype string
		var notnull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &dtype, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func stringSliceContains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

func (s *SQLiteStorage) SaveFeedback(ctx context.Context, feedback *SessionFeedback) error {
	feedback.ID = generateFeedbackID()
	feedback.Timestamp = time.Now()

	tasksJSON, err := json.Marshal(feedback.TasksWorked)
	if err != nil {
		return fmt.Errorf("failed to marshal tasks: %w", err)
	}

	issuesJSON, err := json.Marshal(feedback.Issues)
	if err != nil {
		return fmt.Errorf("failed to marshal issues: %w", err)
	}

	query := `
	INSERT INTO feedback (session_id, workspace_name, agent_id, tasks_worked, issues, duration, success_rate, timestamp)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = s.db.ExecContext(ctx, query,
		feedback.SessionID,
		feedback.WorkspaceName,
		feedback.AgentID,
		string(tasksJSON),
		string(issuesJSON),
		feedback.Duration.Seconds(),
		feedback.SuccessRate,
		feedback.Timestamp.Format(time.RFC3339),
	)
	return err
}

func (s *SQLiteStorage) GetFeedback(ctx context.Context, sessionID string) (*SessionFeedback, error) {
	query := `
	SELECT session_id, workspace_name, agent_id, tasks_worked, issues, duration, success_rate, timestamp
	FROM feedback WHERE session_id = ?
	`
	var feedback SessionFeedback
	var tasksJSON, issuesJSON, timestampStr string
	var durationSecs float64

	err := s.db.QueryRowContext(ctx, query, sessionID).Scan(
		&feedback.SessionID,
		&feedback.WorkspaceName,
		&feedback.AgentID,
		&tasksJSON,
		&issuesJSON,
		&durationSecs,
		&feedback.SuccessRate,
		&timestampStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	feedback.Duration = time.Duration(durationSecs * float64(time.Second))
	feedback.Timestamp, _ = time.Parse(time.RFC3339, timestampStr)
	json.Unmarshal([]byte(tasksJSON), &feedback.TasksWorked)
	json.Unmarshal([]byte(issuesJSON), &feedback.Issues)

	return &feedback, nil
}

func (s *SQLiteStorage) GetFeedbackByTimeRange(ctx context.Context, start, end time.Time) ([]*SessionFeedback, error) {
	query := `
	SELECT session_id, workspace_name, agent_id, tasks_worked, issues, duration, success_rate, timestamp
	FROM feedback WHERE timestamp >= ? AND timestamp <= ?
	ORDER BY timestamp DESC
	`
	rows, err := s.db.QueryContext(ctx, query, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var feedbacks []*SessionFeedback
	for rows.Next() {
		var feedback SessionFeedback
		var tasksJSON, issuesJSON, timestampStr string
		var durationSecs float64

		err := rows.Scan(
			&feedback.SessionID,
			&feedback.WorkspaceName,
			&feedback.AgentID,
			&tasksJSON,
			&issuesJSON,
			&durationSecs,
			&feedback.SuccessRate,
			&timestampStr,
		)
		if err != nil {
			return nil, err
		}

		feedback.Duration = time.Duration(durationSecs * float64(time.Second))
		feedback.Timestamp, _ = time.Parse(time.RFC3339, timestampStr)
		json.Unmarshal([]byte(tasksJSON), &feedback.TasksWorked)
		json.Unmarshal([]byte(issuesJSON), &feedback.Issues)

		feedbacks = append(feedbacks, &feedback)
	}

	return feedbacks, rows.Err()
}

func (s *SQLiteStorage) GetPatterns(ctx context.Context, threshold int) ([]*Pattern, error) {
	query := `
	SELECT session_id, agent_id, tasks_worked, issues, duration, success_rate, timestamp
	FROM feedback
	ORDER BY timestamp DESC
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	issueCounts := make(map[string]*Pattern)

	for rows.Next() {
		var sessionID, agentID, tasksJSON, issuesJSON, timestampStr string
		var durationSecs, successRate float64

		err := rows.Scan(
			&sessionID, &agentID, &tasksJSON, &issuesJSON,
			&durationSecs, &successRate, &timestampStr,
		)
		if err != nil {
			return nil, err
		}

		timestamp, _ := time.Parse(time.RFC3339, timestampStr)

		var issues []Issue
		if err := json.Unmarshal([]byte(issuesJSON), &issues); err != nil {
			continue
		}

		var tasks []string
		json.Unmarshal([]byte(tasksJSON), &tasks)

		for _, issue := range issues {
			key := fmt.Sprintf("%s:%s", issue.Category, issue.Description)

			if pattern, exists := issueCounts[key]; exists {
				pattern.Frequency++
				pattern.LastSeen = timestamp
				pattern.AffectedTasks = appendUnique(pattern.AffectedTasks, tasks...)
			} else {
				issueCounts[key] = &Pattern{
					IssueType:     key,
					Frequency:     1,
					FirstSeen:     timestamp,
					LastSeen:      timestamp,
					AffectedTasks: tasks,
				}
			}
		}
	}

	var patterns []*Pattern
	for _, pattern := range issueCounts {
		if pattern.Frequency >= threshold {
			patterns = append(patterns, pattern)
		}
	}

	return patterns, nil
}

func (s *SQLiteStorage) RunAutomatedChecks(ctx context.Context, taskID, workspaceName string) (VerificationCriteria, error) {
	return VerificationCriteria{}, nil
}

func (s *SQLiteStorage) ValidateCriteria(criteria VerificationCriteria) error {
	return nil
}

func (s *SQLiteStorage) CompleteManualChecklist(ctx context.Context, taskID string, items []string) error {
	return nil
}

func (s *SQLiteStorage) GetManualChecklist(ctx context.Context, taskID string) ([]ManualChecklistItem, error) {
	return nil, nil
}

func (s *SQLiteStorage) SetCustomCheck(ctx context.Context, taskID, checkName string, passed bool) error {
	return nil
}

func appendUnique(slice []string, items ...string) []string {
	seen := make(map[string]bool)
	for _, s := range slice {
		seen[s] = true
	}
	for _, item := range items {
		if !seen[item] {
			slice = append(slice, item)
			seen[item] = true
		}
	}
	return slice
}

func generateFeedbackID() string {
	return fmt.Sprintf("fb-%d", time.Now().UnixNano())
}

func joinStrings(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strings.Join(strs, ",")
	return result
}

func splitStrings(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			result = append(result, p)
		}
	}
	return result
}

func generateID() string {
	return fmt.Sprintf("task-%d", time.Now().UnixNano())
}

func generateAgentID(name string) string {
	return fmt.Sprintf("agent-%s-%d", name, time.Now().UnixNano())
}

func generateEventID() string {
	return fmt.Sprintf("evt-%d", time.Now().UnixNano())
}

func generatePortMappingID(workspaceName, serviceName string) string {
	return fmt.Sprintf("portmap-%s-%s-%d", workspaceName, serviceName, time.Now().UnixNano())
}
