package orchestration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nexus/nexus/pkg/feedback"
	"github.com/nexus/nexus/pkg/pulse"
)

// FeedbackCollector interface from coordination package
type FeedbackCollector interface {
	Collect(fb *feedback.Feedback) error
	GetFeedback(id string) (*feedback.Feedback, error)
	ListFeedback(filter feedback.FeedbackFilter) ([]feedback.Feedback, error)
	UpdateFeedbackStatus(id string, status feedback.FeedbackStatus) (*feedback.Feedback, error)
	GetStats(days int) (*feedback.FeedbackStats, error)
}

// TriageCategory represents the category of feedback for triage
type TriageCategory string

const (
	CategoryBug        TriageCategory = "bug"
	CategoryFeature    TriageCategory = "feature"
	CategoryUX         TriageCategory = "ux"
	CategoryPraise     TriageCategory = "praise"
	CategoryUnknown    TriageCategory = "unknown"
)

// TriageTask represents a task created from feedback
type TriageTask struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    int       `json:"priority"`
	Category    string    `json:"category"`
	Source      string    `json:"source"`
	Status      string    `json:"status"`
	FeedbackID  string    `json:"feedback_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// TriageConfig holds configuration for the triage service
type TriageConfig struct {
	DefaultPriority      int
	HighPriorityThreshold int
	MaxDescriptionLength  int
}

// DefaultTriageConfig returns the default triage configuration
func DefaultTriageConfig() TriageConfig {
	return TriageConfig{
		DefaultPriority:       3,
		HighPriorityThreshold: 1,
		MaxDescriptionLength:  2000,
	}
}

// TriageService handles automatic conversion of feedback to Pulse tasks
type TriageService struct {
	feedbackCollector FeedbackCollector
	pulseClient      *pulse.Client
	config           TriageConfig
	workspaceID      string
}

// NewTriageService creates a new triage service
func NewTriageService(collector FeedbackCollector, pulseClient *pulse.Client, workspaceID string) *TriageService {
	return &TriageService{
		feedbackCollector: collector,
		pulseClient:      pulseClient,
		workspaceID:      workspaceID,
		config:           DefaultTriageConfig(),
	}
}

// NewTriageServiceWithConfig creates a new triage service with custom config
func NewTriageServiceWithConfig(collector FeedbackCollector, pulseClient *pulse.Client, workspaceID string, config TriageConfig) *TriageService {
	return &TriageService{
		feedbackCollector: collector,
		pulseClient:      pulseClient,
		workspaceID:      workspaceID,
		config:           config,
	}
}

// CategorizeFeedback determines the category of a feedback entry
func (s *TriageService) CategorizeFeedback(fb feedback.Feedback) TriageCategory {
	msg := strings.ToLower(fb.Message)
	fbType := strings.ToLower(string(fb.FeedbackType))

	// Explicit type from feedback
	if fbType == "bug" {
		return CategoryBug
	}
	if fbType == "feature" {
		return CategoryFeature
	}
	if fbType == "suggestion" {
		return CategoryUX
	}
	if fbType == "praise" {
		return CategoryPraise
	}
	if fbType == "workflow" {
		return CategoryFeature
	}

	// Keyword-based categorization for unknown types
	bugPatterns := []string{
		"bug", "crash", "error", "broken", "fail", "issue", "problem",
		"not working", "doesn't work", "doesnt work", "fix",
	}

	uxPatterns := []string{
		"ui", "ux", "interface", "design", "confusing", "hard to",
		"difficult", "unclear", "confusing", "suggestion", "improve",
		"would be nice", "could be better", "workflow",
	}

	featurePatterns := []string{
		"add", "new feature", "support for", "implement", "would like",
		"request", "feature request", "want to see", "please add",
	}

	praisePatterns := []string{
		"great", "awesome", "amazing", "love", "thanks", "helpful",
		"fantastic", "wonderful", "best", "excellent",
	}

	for _, pattern := range bugPatterns {
		if strings.Contains(msg, pattern) {
			return CategoryBug
		}
	}

	for _, pattern := range featurePatterns {
		if strings.Contains(msg, pattern) {
			return CategoryFeature
		}
	}

	for _, pattern := range uxPatterns {
		if strings.Contains(msg, pattern) {
			return CategoryUX
		}
	}

	for _, pattern := range praisePatterns {
		if strings.Contains(msg, pattern) {
			return CategoryPraise
		}
	}

	return CategoryUnknown
}

// DeterminePriority returns the priority based on category and content
func (s *TriageService) DeterminePriority(fb feedback.Feedback, category TriageCategory) int {
	switch category {
	case CategoryBug:
		// High priority for bugs (1-2)
		return s.determineBugPriority(fb)
	case CategoryFeature:
		// Medium-low priority for features (4-5)
		return 4
	case CategoryUX:
		// Medium priority for UX (3)
		return 3
	case CategoryPraise:
		// No task created for praise (priority 0)
		return 0
	default:
		return s.config.DefaultPriority
	}
}

// determineBugPriority determines bug priority based on severity keywords
func (s *TriageService) determineBugPriority(fb feedback.Feedback) int {
	msg := strings.ToLower(fb.Message)

	criticalPatterns := []string{
		"critical", "urgent", "crash", "data loss", "security",
		"vulnerability", "blocking", "production", "security flaw",
	}

	highPatterns := []string{
		"severe", "major", "frequently", "always", "completely broken",
	}

	for _, pattern := range criticalPatterns {
		if strings.Contains(msg, pattern) {
			return 1 // Critical priority
		}
	}

	for _, pattern := range highPatterns {
		if strings.Contains(msg, pattern) {
			return 2 // High priority
		}
	}

	return 2 // Default high priority for bugs
}

// BuildTaskTitle creates a task title from feedback
func (s *TriageService) BuildTaskTitle(fb feedback.Feedback, category TriageCategory) string {
	title := fb.Message
	if len(title) > 100 {
		title = title[:97] + "..."
	}

	prefix := map[TriageCategory]string{
		CategoryBug:     "[Bug] ",
		CategoryFeature: "[Feature] ",
		CategoryUX:      "[UX] ",
	}

	if prefix, ok := prefix[category]; ok {
		if !strings.HasPrefix(strings.ToLower(title), prefix) {
			title = prefix + title
		}
	}

	return title
}

// BuildTaskDescription creates a detailed task description from feedback
func (s *TriageService) BuildTaskDescription(fb feedback.Feedback, category TriageCategory) string {
	var sb strings.Builder

	sb.WriteString("## Generated from Feedback\n\n")
	sb.WriteString(fmt.Sprintf("**Feedback ID:** %s\n", fb.ID))
	sb.WriteString(fmt.Sprintf("**Feedback Type:** %s\n", fb.FeedbackType))
	sb.WriteString(fmt.Sprintf("**Category:** %s\n", category))
	sb.WriteString(fmt.Sprintf("**Submitted:** %s\n", fb.Timestamp))
	if fb.SessionID != "" {
		sb.WriteString(fmt.Sprintf("**Session:** %s\n", fb.SessionID))
	}
	if fb.UserID != "" {
		sb.WriteString(fmt.Sprintf("**User:** %s\n", fb.UserID))
	}

	sb.WriteString("\n---\n\n")
	sb.WriteString("### Original Feedback\n\n")
	sb.WriteString(fb.Message)

	if len(fb.Tags) > 0 {
		sb.WriteString("\n\n### Tags\n\n")
		sb.WriteString(fmt.Sprintf("`%s`", strings.Join(fb.Tags, "`, `")))
	}

	if fb.Metadata != nil {
		sb.WriteString("\n\n### Metadata\n\n")
		if fb.Metadata.Model != "" {
			sb.WriteString(fmt.Sprintf("- **Model:** %s\n", fb.Metadata.Model))
		}
		if fb.Metadata.NexusVersion != "" {
			sb.WriteString(fmt.Sprintf("- **Nexus Version:** %s\n", fb.Metadata.NexusVersion))
		}
		if fb.Metadata.WorkspaceID != "" {
			sb.WriteString(fmt.Sprintf("- **Workspace:** %s\n", fb.Metadata.WorkspaceID))
		}
		if fb.Metadata.TaskID != "" {
			sb.WriteString(fmt.Sprintf("- **Task:** %s\n", fb.Metadata.TaskID))
		}
	}

	desc := sb.String()
	if len(desc) > s.config.MaxDescriptionLength {
		desc = desc[:s.config.MaxDescriptionLength-3] + "..."
	}

	return desc
}

// CreateTaskFromFeedback converts a feedback entry to a Pulse task
func (s *TriageService) CreateTaskFromFeedback(ctx context.Context, fbID string) (*TriageTask, error) {
	fb, err := s.feedbackCollector.GetFeedback(fbID)
	if err != nil {
		return nil, fmt.Errorf("failed to get feedback: %w", err)
	}

	// Check if already triaged
	if fb.Status == feedback.FeedbackStatusTriaged || fb.Status == feedback.FeedbackStatusResolved {
		return nil, fmt.Errorf("feedback already processed")
	}

	category := s.CategorizeFeedback(*fb)
	priority := s.DeterminePriority(*fb, category)

	// Skip praise - no task needed
	if category == CategoryPraise {
		return nil, nil
	}

	title := s.BuildTaskTitle(*fb, category)
	description := s.BuildTaskDescription(*fb, category)

	// Create Pulse issue if client is available
	var pulseIssue *pulse.Issue
	if s.pulseClient != nil {
		labels := []string{"feedback", string(category)}
		if fb.Satisfaction <= 2 {
			labels = append(labels, "low-satisfaction")
		}

		issue, err := s.pulseClient.CreateIssue(
			s.workspaceID,
			title,
			description,
			priority,
			0,
			labels,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create pulse issue: %w", err)
		}
		pulseIssue = issue
	}

	// Generate task ID
	taskID := generateTaskID()

	task := &TriageTask{
		ID:          taskID,
		Title:       title,
		Description: description,
		Priority:    priority,
		Category:    string(category),
		Source:      "feedback",
		Status:      "pending",
		FeedbackID:  fb.ID,
		CreatedAt:   time.Now(),
	}

	// Update feedback status
	_, err = s.feedbackCollector.UpdateFeedbackStatus(fbID, feedback.FeedbackStatusTriaged)
	if err != nil {
		return nil, fmt.Errorf("failed to update feedback status: %w", err)
	}

	if pulseIssue != nil {
		task.ID = pulseIssue.ID
	}

	return task, nil
}

// ProcessNewFeedback triages all new feedback entries
func (s *TriageService) ProcessNewFeedback(ctx context.Context) ([]*TriageTask, error) {
	filter := feedback.FeedbackFilter{
		Status: feedback.FeedbackStatusNew,
	}

	newFeedback, err := s.feedbackCollector.ListFeedback(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list feedback: %w", err)
	}

	var tasks []*TriageTask
	for _, fb := range newFeedback {
		task, err := s.CreateTaskFromFeedback(ctx, fb.ID)
		if err != nil {
			continue // Skip problematic feedback
		}
		if task != nil {
			tasks = append(tasks, task)
		}
	}

	return tasks, nil
}

// AutoCreateTask handles the API endpoint logic for auto-creating tasks
func (s *TriageService) AutoCreateTask(ctx context.Context, feedbackID string) (*TriageTask, error) {
	if feedbackID == "" {
		return nil, fmt.Errorf("feedback ID is required")
	}

	return s.CreateTaskFromFeedback(ctx, feedbackID)
}

// TriageStats contains statistics about triage operations
type TriageStats struct {
	TotalProcessed   int            `json:"total_processed"`
	TasksCreated     int            `json:"tasks_created"`
	ByCategory       map[string]int `json:"by_category"`
	PraiseIgnored    int            `json:"praise_ignored"`
	Errors           int            `json:"errors"`
	LastProcessedAt  *time.Time     `json:"last_processed_at,omitempty"`
}

// GetStats returns triage statistics
func (s *TriageService) GetStats(ctx context.Context) (*TriageStats, error) {
	filter := feedback.FeedbackFilter{
		Status: feedback.FeedbackStatusTriaged,
	}

	triaged, err := s.feedbackCollector.ListFeedback(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list triaged feedback: %w", err)
	}

	praiseFilter := feedback.FeedbackFilter{
		Types: []feedback.FeedbackType{feedback.FeedbackPraise},
	}
	praise, err := s.feedbackCollector.ListFeedback(praiseFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to list praise feedback: %w", err)
	}

	stats := &TriageStats{
		TotalProcessed:  len(triaged),
		TasksCreated:    len(triaged),
		ByCategory:      make(map[string]int),
		PraiseIgnored:   len(praise),
	}

	for _, fb := range triaged {
		stats.ByCategory[string(s.CategorizeFeedback(fb))]++
	}

	now := time.Now()
	stats.LastProcessedAt = &now

	return stats, nil
}

// generateTaskID generates a unique task ID
func generateTaskID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("triage-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("triage-%s", hex.EncodeToString(b))
}

// KeywordExtractor extracts key terms from feedback for categorization
type KeywordExtractor struct {
	patterns map[string][]*regexp.Regexp
}

// NewKeywordExtractor creates a new keyword extractor
func NewKeywordExtractor() *KeywordExtractor {
	return &KeywordExtractor{
		patterns: map[string][]*regexp.Regexp{
			"urgent": {
				regexp.MustCompile(`(?i)urgent|critical|asap|immediately`),
				regexp.MustCompile(`(?i)blocking|showstopper`),
			},
			"security": {
				regexp.MustCompile(`(?i)security|vulnerability|exploit|cve`),
				regexp.MustCompile(`(?i)authentication|authorization|auth`),
			},
			"performance": {
				regexp.MustCompile(`(?i)slow|performance|latency`),
				regexp.MustCompile(`(?i)memory|cpu|resource`),
			},
		},
	}
}

// ExtractKeywords finds relevant keywords in feedback
func (e *KeywordExtractor) ExtractKeywords(text string) []string {
	var keywords []string

	for category, patterns := range e.patterns {
		for _, pattern := range patterns {
			if pattern.MatchString(text) {
				keywords = append(keywords, category)
				break
			}
		}
	}

	return keywords
}

// ShouldIgnoreFeedback determines if feedback should be ignored during triage
func (s *TriageService) ShouldIgnoreFeedback(fb feedback.Feedback) bool {
	category := s.CategorizeFeedback(fb)
	if category == CategoryPraise {
		return true
	}

	// Ignore very short or unclear feedback
	if len(fb.Message) < 10 {
		return true
	}

	// Ignore test/automated feedback
	if strings.HasPrefix(fb.Message, "[TEST]") || strings.HasPrefix(fb.Message, "[AUTOMATED]") {
		return true
	}

	return false
}

// ValidateFeedbackForTriage checks if feedback is valid for triage
func (s *TriageService) ValidateFeedbackForTriage(fb feedback.Feedback) error {
	if fb.ID == "" {
		return fmt.Errorf("feedback ID is required")
	}
	if fb.Message == "" {
		return fmt.Errorf("feedback message is required")
	}
	if fb.Status != feedback.FeedbackStatusNew {
		return fmt.Errorf("feedback must be in 'new' status for triage")
	}
	return nil
}
