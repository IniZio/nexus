package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StoreMetadata contains store metadata
type StoreMetadata struct {
	Version     string `json:"version"`
	LastUpdated string `json:"lastUpdated"`
}

// WorkflowStage represents the stage of a Claude Code workflow
type WorkflowStage string

const (
	WorkflowStageTaskCreation   WorkflowStage = "task_creation"
	WorkflowStageWorkspaceClaim WorkflowStage = "workspace_claim"
	WorkflowStageCoding        WorkflowStage = "coding"
	WorkflowStageTesting       WorkflowStage = "testing"
	WorkflowStageCompletion    WorkflowStage = "completion"
)

// SatisfactionLevel represents user satisfaction with a session
type SatisfactionLevel int

const (
	SatisfactionVeryLow  SatisfactionLevel = 1
	SatisfactionLow      SatisfactionLevel = 2
	SatisfactionNeutral  SatisfactionLevel = 3
	SatisfactionHigh     SatisfactionLevel = 4
	SatisfactionVeryHigh SatisfactionLevel = 5
)

// ClaudeSession represents a Claude Code session
type ClaudeSession struct {
	SessionID      string           `json:"sessionId"`
	StartTime      string           `json:"startTime"`
	EndTime        string           `json:"endTime,omitempty"`
	UserID         string           `json:"userId"`
	WorkspaceID    string           `json:"workspaceId,omitempty"`
	Model          string           `json:"model"`
	TasksCreated   int              `json:"tasksCreated"`
	TasksCompleted int              `json:"tasksCompleted"`
	SkillsUsed     []string         `json:"skillsUsed"`
	NexusFeatures  []string         `json:"nexusFeaturesUsed"`
	Satisfaction   SatisfactionLevel `json:"satisfaction,omitempty"`
	Outcome        SessionOutcome   `json:"outcome"`
}

// SessionOutcome represents the outcome of a Claude Code session
type SessionOutcome struct {
	Success    bool     `json:"success"`
	Duration   int64    `json:"durationSeconds"`
	TokensUsed int64    `json:"tokensUsed,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// WorkflowEvent represents an event during a Claude Code workflow
type WorkflowEvent struct {
	EventID      string                 `json:"eventId"`
	Timestamp    string                 `json:"timestamp"`
	SessionID    string                 `json:"sessionId"`
	EventType    string                 `json:"eventType"`
	Stage        WorkflowStage          `json:"stage,omitempty"`
	SkillName    string                 `json:"skillName,omitempty"`
	Duration     int64                  `json:"durationMs,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// SessionStore stores Claude sessions
type SessionStore struct {
	Sessions []ClaudeSession `json:"sessions"`
	Metadata StoreMetadata  `json:"metadata"`
}

// EventStore stores workflow events
type EventStore struct {
	Events   []WorkflowEvent `json:"events"`
	Metadata StoreMetadata  `json:"metadata"`
}

// WorkflowTracker tracks Claude Code workflow metrics
type WorkflowTracker struct {
	mu           sync.RWMutex
	sessionsFile string
	eventsFile   string
	sessions     *SessionStore
	events       *EventStore
}

// WorkflowStats contains aggregated workflow statistics
type WorkflowStats struct {
	TotalSessions          int                   `json:"totalSessions"`
	ActiveUsers            int                   `json:"activeUsers"`
	AverageSessionDuration float64               `json:"averageSessionDurationSeconds"`
	AverageSatisfaction    float64               `json:"averageSatisfaction"`
	SessionsByOutcome      map[string]int        `json:"sessionsByOutcome"`
	SkillsFrequency        map[string]int        `json:"skillsFrequency"`
	NexusFeatureUsage      map[string]int        `json:"nexusFeatureUsage"`
	WorkflowStageTimes     map[string]int64      `json:"workflowStageTimes"`
	TopSkills              []SkillUsageStat      `json:"topSkills"`
	RecentSessions         []ClaudeSession      `json:"recentSessions"`
}

// SkillUsageStat contains statistics about skill usage
type SkillUsageStat struct {
	SkillName   string `json:"skillName"`
	Count       int    `json:"count"`
	AvgDuration int64  `json:"avgDurationMs"`
}

// NewWorkflowTracker creates a new workflow tracker
func NewWorkflowTracker(basePath string) *WorkflowTracker {
	return &WorkflowTracker{
		sessionsFile: filepath.Join(basePath, ".nexus", "claude_sessions.json"),
		eventsFile:   filepath.Join(basePath, ".nexus", "claude_events.json"),
		sessions:     &SessionStore{},
		events:       &EventStore{},
	}
}

// loadSessions loads sessions from disk
func (t *WorkflowTracker) loadSessions() error {
	data, err := os.ReadFile(t.sessionsFile)
	if err != nil {
		if os.IsNotExist(err) {
			t.sessions = &SessionStore{
				Sessions: []ClaudeSession{},
				Metadata: StoreMetadata{Version: "1.0"},
			}
			return nil
		}
		return fmt.Errorf("failed to read sessions: %w", err)
	}
	return json.Unmarshal(data, t.sessions)
}

// saveSessions saves sessions to disk
func (t *WorkflowTracker) saveSessions() error {
	if err := os.MkdirAll(filepath.Dir(t.sessionsFile), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t.sessions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.sessionsFile, data, 0644)
}

// loadEvents loads events from disk
func (t *WorkflowTracker) loadEvents() error {
	data, err := os.ReadFile(t.eventsFile)
	if err != nil {
		if os.IsNotExist(err) {
			t.events = &EventStore{
				Events:   []WorkflowEvent{},
				Metadata: StoreMetadata{Version: "1.0"},
			}
			return nil
		}
		return fmt.Errorf("failed to read events: %w", err)
	}
	return json.Unmarshal(data, t.events)
}

// saveEvents saves events to disk
func (t *WorkflowTracker) saveEvents() error {
	if err := os.MkdirAll(filepath.Dir(t.eventsFile), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t.events, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.eventsFile, data, 0644)
}

// StartSession starts a new Claude session
func (t *WorkflowTracker) StartSession(sessionID, userID, model string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	session := ClaudeSession{
		SessionID:      sessionID,
		StartTime:      time.Now().Format(time.RFC3339),
		UserID:         userID,
		Model:          model,
		TasksCreated:   0,
		TasksCompleted: 0,
		SkillsUsed:     []string{},
		NexusFeatures:  []string{},
		Outcome:        SessionOutcome{Success: false},
	}

	if err := t.loadSessions(); err != nil {
		return err
	}
	t.sessions.Sessions = append(t.sessions.Sessions, session)
	if err := t.saveSessions(); err != nil {
		return err
	}

	event := WorkflowEvent{
		EventID:   generateID(),
		Timestamp: session.StartTime,
		SessionID: sessionID,
		EventType: "session_start",
	}

	if err := t.loadEvents(); err != nil {
		return err
	}
	t.events.Events = append(t.events.Events, event)
	return t.saveEvents()
}

// RecordEvent records a workflow event
func (t *WorkflowTracker) RecordEvent(sessionID, eventType string, metadata map[string]interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.loadEvents(); err != nil {
		return err
	}

	event := WorkflowEvent{
		EventID:   generateID(),
		Timestamp: time.Now().Format(time.RFC3339),
		SessionID: sessionID,
		EventType: eventType,
		Metadata:  metadata,
	}

	t.events.Events = append(t.events.Events, event)
	return t.saveEvents()
}

// RecordSkillUsage records skill usage during a session
func (t *WorkflowTracker) RecordSkillUsage(sessionID, skillName string, durationMs int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.loadSessions(); err != nil {
		return err
	}

	for i := range t.sessions.Sessions {
		if t.sessions.Sessions[i].SessionID == sessionID {
			t.sessions.Sessions[i].SkillsUsed = append(t.sessions.Sessions[i].SkillsUsed, skillName)
			break
		}
	}

	if err := t.saveSessions(); err != nil {
		return err
	}

	event := WorkflowEvent{
		EventID:   generateID(),
		Timestamp: time.Now().Format(time.RFC3339),
		SessionID: sessionID,
		EventType: "skill_usage",
		SkillName: skillName,
		Duration:  durationMs,
	}

	if err := t.loadEvents(); err != nil {
		return err
	}
	t.events.Events = append(t.events.Events, event)
	return t.saveEvents()
}

// CompleteSession marks a session as complete
func (t *WorkflowTracker) CompleteSession(sessionID string, success bool, duration int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.loadSessions(); err != nil {
		return err
	}

	for i := range t.sessions.Sessions {
		if t.sessions.Sessions[i].SessionID == sessionID {
			t.sessions.Sessions[i].EndTime = time.Now().Format(time.RFC3339)
			t.sessions.Sessions[i].Outcome.Success = success
			t.sessions.Sessions[i].Outcome.Duration = duration
			break
		}
	}

	if err := t.saveSessions(); err != nil {
		return err
	}

	event := WorkflowEvent{
		EventID:   generateID(),
		Timestamp: time.Now().Format(time.RFC3339),
		SessionID: sessionID,
		EventType: "session_complete",
	}

	if err := t.loadEvents(); err != nil {
		return err
	}
	t.events.Events = append(t.events.Events, event)
	return t.saveEvents()
}

// GetStats returns workflow statistics
func (t *WorkflowTracker) GetStats() (*WorkflowStats, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if err := t.loadSessions(); err != nil {
		return nil, err
	}

	stats := &WorkflowStats{
		SessionsByOutcome: make(map[string]int),
		SkillsFrequency:   make(map[string]int),
		NexusFeatureUsage: make(map[string]int),
		WorkflowStageTimes: make(map[string]int64),
	}

	var totalDuration int64
	users := make(map[string]bool)

	for _, s := range t.sessions.Sessions {
		stats.TotalSessions++
		if s.UserID != "" {
			users[s.UserID] = true
		}
		if s.Outcome.Success {
			stats.SessionsByOutcome["success"]++
		} else {
			stats.SessionsByOutcome["failure"]++
		}
		totalDuration += s.Outcome.Duration

		for _, skill := range s.SkillsUsed {
			stats.SkillsFrequency[skill]++
		}
	}

	stats.ActiveUsers = len(users)
	if stats.TotalSessions > 0 {
		stats.AverageSessionDuration = float64(totalDuration) / float64(stats.TotalSessions)
	}

	return stats, nil
}
