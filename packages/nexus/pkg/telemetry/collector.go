package telemetry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Collector collects telemetry events
type Collector struct {
	db             *TelemetryDB
	config         Config
	currentSession string
}

// Config for telemetry
type Config struct {
	Enabled             bool
	Anonymize           bool
	RetentionDays       int
	MaxEventsPerSession int
	DBPath              string
}

// NewCollector creates a collector
func NewCollector(dbPath string, config Config) (*Collector, error) {
	db, err := NewTelemetryDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create telemetry database: %w", err)
	}

	if config.RetentionDays <= 0 {
		config.RetentionDays = 30
	}
	if config.MaxEventsPerSession <= 0 {
		config.MaxEventsPerSession = 1000
	}

	return &Collector{
		db:     db,
		config: config,
	}, nil
}

// RecordCommand records a command execution
func (c *Collector) RecordCommand(cmd string, args []string, duration time.Duration, success bool, err error) error {
	if !c.config.Enabled {
		return nil
	}

	event := Event{
		ID:        generateID(),
		Timestamp: time.Now(),
		SessionID: c.currentSession,
		EventType: "command",
		Command:   cmd,
		Duration:  duration,
		Success:   success,
	}

	if err != nil {
		event.ErrorType = classifyError(err)
	}

	if c.config.Anonymize {
		event.Args = anonymizeArgs(args)
	} else {
		event.Args = args
	}

	return c.db.SaveEvent(event)
}

// RecordWorkspace records workspace lifecycle
func (c *Collector) RecordWorkspace(action, workspaceName, template string, ports int) error {
	if !c.config.Enabled {
		return nil
	}

	event := Event{
		ID:           generateID(),
		Timestamp:    time.Now(),
		SessionID:    c.currentSession,
		EventType:    "workspace",
		Command:      action,
		TemplateUsed: template,
		PortsUsed:    ports,
	}

	if c.config.Anonymize && workspaceName != "" {
		event.WorkspaceHash = hashString(workspaceName)
	}

	return c.db.SaveEvent(event)
}

// RecordTask records task lifecycle
func (c *Collector) RecordTask(action, taskID string, duration time.Duration) error {
	if !c.config.Enabled {
		return nil
	}

	event := Event{
		ID:        generateID(),
		Timestamp: time.Now(),
		SessionID: c.currentSession,
		EventType: "task",
		Command:   action,
		Duration:  duration,
	}

	if c.config.Anonymize && taskID != "" {
		event.TaskHash = hashString(taskID)
	}

	if action == "complete" || action == "fail" {
		event.Success = action == "complete"
	}

	return c.db.SaveEvent(event)
}

// RecordSessionStart starts a new session
func (c *Collector) RecordSessionStart() (string, error) {
	session := Session{
		ID:        generateID(),
		StartedAt: time.Now(),
	}

	err := c.db.SaveSession(session)
	if err != nil {
		return "", err
	}

	c.currentSession = session.ID
	return session.ID, nil
}

// RecordSessionEnd ends a session
func (c *Collector) RecordSessionEnd(feedback string) error {
	if c.currentSession == "" {
		return fmt.Errorf("no active session")
	}

	err := c.db.EndSession(c.currentSession, feedback)
	c.currentSession = ""
	return err
}

// GetCurrentSession returns the current session ID
func (c *Collector) GetCurrentSession() string {
	return c.currentSession
}

// GetStats returns usage statistics for the specified number of days
func (c *Collector) GetStats(days int) (Stats, error) {
	return c.db.GetStats(days)
}

// GetEvents returns events since the specified time
func (c *Collector) GetEvents(since time.Time) ([]Event, error) {
	return c.db.QueryEvents(since)
}

// Cleanup removes old events based on retention policy
func (c *Collector) Cleanup() error {
	if c.config.RetentionDays <= 0 {
		return nil
	}

	olderThan := time.Duration(c.config.RetentionDays) * 24 * time.Hour
	return c.db.DeleteOldEvents(olderThan)
}

// Close closes the collector and its database connection
func (c *Collector) Close() error {
	return c.db.Close()
}

// Helper functions
func hashString(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:8]))
}

func anonymizeArgs(args []string) []string {
	var anonymized []string
	for _, arg := range args {
		if looksLikePath(arg) {
			anonymized = append(anonymized, "[path]")
		} else if looksLikeToken(arg) {
			anonymized = append(anonymized, "[token]")
		} else if looksLikeSecret(arg) {
			anonymized = append(anonymized, "[secret]")
		} else if looksLikeEnvVar(arg) {
			anonymized = append(anonymized, "[env]")
		} else {
			anonymized = append(anonymized, arg)
		}
	}
	return anonymized
}

func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.Contains(s, "/home/") ||
		strings.Contains(s, "/Users/")
}

func looksLikeToken(s string) bool {
	if len(s) < 10 {
		return false
	}
	patterns := []string{"ghp_", "github_pat_", "eyJhbGci", "sk-", "token:", "Bearer "}
	for _, p := range patterns {
		if strings.HasPrefix(s, p) || strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func looksLikeSecret(s string) bool {
	if len(s) < 8 {
		return false
	}
	patterns := []string{"secret", "password", "key:", "api_key", "AWS_"}
	for _, p := range patterns {
		if strings.Contains(strings.ToLower(s), p) {
			return true
		}
	}
	return false
}

func looksLikeEnvVar(s string) bool {
	return strings.HasPrefix(s, "$") ||
		strings.HasPrefix(s, "%") ||
		strings.HasPrefix(s, "${")
}

func classifyError(err error) string {
	if err == nil {
		return ""
	}

	errStr := strings.ToLower(err.Error())

	if strings.Contains(errStr, "port") && (strings.Contains(errStr, "conflict") || strings.Contains(errStr, "in use") || strings.Contains(errStr, "bind")) {
		return "port_conflict"
	}
	if strings.Contains(errStr, "docker") {
		return "docker_error"
	}
	if strings.Contains(errStr, "ssh") || strings.Contains(errStr, "connection refused") {
		return "ssh_error"
	}
	if strings.Contains(errStr, "git") || strings.Contains(errStr, "not a git repository") {
		return "git_error"
	}
	if strings.Contains(errStr, "permission") || strings.Contains(errStr, "access denied") {
		return "permission_error"
	}
	if strings.Contains(errStr, "timeout") {
		return "timeout_error"
	}
	if strings.Contains(errStr, "network") || strings.Contains(errStr, "connection") {
		return "network_error"
	}
	if strings.Contains(errStr, "file not found") || strings.Contains(errStr, "no such file") {
		return "file_not_found"
	}
	if strings.Contains(errStr, "template") {
		return "template_error"
	}

	return "unknown"
}
