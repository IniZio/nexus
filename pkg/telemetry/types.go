package telemetry

import "time"

// Event represents a telemetry event
type Event struct {
	ID            string        `json:"id"`
	Timestamp     time.Time     `json:"timestamp"`
	SessionID     string        `json:"session_id"`
	EventType     string        `json:"event_type"`
	WorkspaceHash string        `json:"workspace_hash,omitempty"`
	TaskHash      string        `json:"task_hash,omitempty"`
	Command       string        `json:"command,omitempty"`
	Args          []string      `json:"args,omitempty"`
	Duration      time.Duration `json:"duration_ms"`
	Success       bool          `json:"success"`
	ErrorType     string        `json:"error_type,omitempty"`
	TemplateUsed  string        `json:"template_used,omitempty"`
	ServicesCount int           `json:"services_count,omitempty"`
	PortsUsed     int           `json:"ports_used,omitempty"`
}

// Session represents a user session
type Session struct {
	ID                string        `json:"id"`
	StartedAt         time.Time     `json:"started_at"`
	EndedAt           *time.Time    `json:"ended_at,omitempty"`
	Duration          time.Duration `json:"duration"`
	CommandsExecuted  int           `json:"commands_executed"`
	WorkspacesCreated int           `json:"workspaces_created"`
	TasksCompleted    int           `json:"tasks_completed"`
	ErrorsEncountered int           `json:"errors_encountered"`
	UserFeedback      string        `json:"user_feedback,omitempty"`
}

// Pattern represents detected usage patterns
type Pattern struct {
	PatternType      string    `json:"pattern_type"`
	Description      string    `json:"description"`
	Frequency        int       `json:"frequency"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
	AffectedSessions []string  `json:"affected_sessions"`
	SuggestedFix     string    `json:"suggested_fix,omitempty"`
}

// Stats represents usage statistics
type Stats struct {
	TotalCommands      int            `json:"total_commands"`
	SuccessRate        float64        `json:"success_rate"`
	AvgCommandDuration time.Duration  `json:"avg_command_duration"`
	TopCommands        []CommandStat  `json:"top_commands"`
	CommonErrors       []ErrorStat    `json:"common_errors"`
	WorkspaceStats     WorkspaceStats `json:"workspace_stats"`
	TaskStats          TaskStats      `json:"task_stats"`
}

// CommandStat represents statistics for a command
type CommandStat struct {
	Command     string  `json:"command"`
	Count       int     `json:"count"`
	AvgDuration int64   `json:"avg_duration_ms"`
	SuccessRate float64 `json:"success_rate"`
}

// ErrorStat represents error statistics
type ErrorStat struct {
	ErrorType string    `json:"error_type"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// WorkspaceStats represents workspace usage statistics
type WorkspaceStats struct {
	TotalCreated   int            `json:"total_created"`
	TotalDestroyed int            `json:"total_destroyed"`
	ActiveCount    int            `json:"active_count"`
	AvgLifetime    time.Duration  `json:"avg_lifetime"`
	TopTemplates   []TemplateStat `json:"top_templates"`
}

// TemplateStat represents template usage statistics
type TemplateStat struct {
	TemplateName string `json:"template_name"`
	Count        int    `json:"count"`
}

// TaskStats represents task usage statistics
type TaskStats struct {
	TotalCreated   int           `json:"total_created"`
	TotalCompleted int           `json:"total_completed"`
	CompletionRate float64       `json:"completion_rate"`
	AvgDuration    time.Duration `json:"avg_duration"`
}

// Insight represents an actionable insight
type Insight struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}
