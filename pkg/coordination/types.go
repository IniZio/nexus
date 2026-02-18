package coordination

import (
	"time"
)

type TaskStatus string

const (
	TaskStatusPending      TaskStatus = "pending"
	TaskStatusAssigned     TaskStatus = "assigned"
	TaskStatusInProgress   TaskStatus = "in_progress"
	TaskStatusVerification TaskStatus = "verification"
	TaskStatusCompleted    TaskStatus = "completed"
	TaskStatusRejected     TaskStatus = "rejected"
	TaskStatusFailed       TaskStatus = "failed"
)

type RejectionRecord struct {
	Reason     string    `json:"reason"`
	RejectedBy string    `json:"rejected_by"`
	RejectedAt time.Time `json:"rejected_at"`
}

type Task struct {
	ID               string                `json:"id"`
	WorkspaceID      string                `json:"workspace_id"`
	Title            string                `json:"title"`
	Description      string                `json:"description,omitempty"`
	Status           TaskStatus            `json:"status"`
	Assignee         string                `json:"assignee,omitempty"`
	ReviewerID       string                `json:"reviewer_id,omitempty"`
	VerifiedBy       string                `json:"verified_by,omitempty"`
	Priority         int                   `json:"priority,omitempty"`
	DependsOn        []string              `json:"depends_on,omitempty"`
	VerificationBy   string                `json:"verification_by,omitempty"`
	VerificationAt   *time.Time            `json:"verification_at,omitempty"`
	RejectionCount   int                   `json:"rejection_count"`
	RejectionHistory []RejectionRecord     `json:"rejection_history,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
	CompletedAt      *time.Time            `json:"completed_at,omitempty"`
	Verification     *VerificationCriteria `json:"verification,omitempty"`
}

type CreateTaskRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

type Agent struct {
	ID           string            `json:"id"`
	WorkspaceID  string            `json:"workspace_id"`
	Name         string            `json:"name"`
	Capabilities []string          `json:"capabilities"`
	CurrentTask  string            `json:"current_task,omitempty"`
	Status       AgentStatus       `json:"status"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	LastSeenAt   time.Time         `json:"last_seen_at"`
}

type AgentStatus string

const (
	AgentStatusIdle    AgentStatus = "idle"
	AgentStatusBusy    AgentStatus = "busy"
	AgentStatusOffline AgentStatus = "offline"
)

type EventType string

const (
	EventTaskCreated   EventType = "task_created"
	EventTaskAssigned  EventType = "task_assigned"
	EventTaskCompleted EventType = "task_completed"
	EventTaskFailed    EventType = "task_failed"
	EventAgentJoined   EventType = "agent_joined"
	EventAgentLeft     EventType = "agent_left"
)

type Event struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Type        EventType `json:"type"`
	TaskID      string    `json:"task_id,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	Data        string    `json:"data,omitempty"`
}

type VerificationCriteria struct {
	TestsPass      bool            `json:"tests_pass"`
	LintPass       bool            `json:"lint_pass"`
	TypeCheckPass  bool            `json:"type_check_pass"`
	ReviewComplete bool            `json:"review_complete"`
	DocsComplete   bool            `json:"docs_complete"`
	CustomChecks   map[string]bool `json:"custom_checks,omitempty"`
}

type ManualChecklistItem struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id"`
	Item        string     `json:"item"`
	Completed   bool       `json:"completed"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}
