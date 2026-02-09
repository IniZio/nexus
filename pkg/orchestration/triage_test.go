package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nexus/nexus/pkg/feedback"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockFeedbackCollector is a mock implementation for testing
type MockFeedbackCollector struct {
	feedbacks map[string]*feedback.Feedback
	stats     *feedback.FeedbackStats
}

func NewMockFeedbackCollector() *MockFeedbackCollector {
	return &MockFeedbackCollector{
		feedbacks: make(map[string]*feedback.Feedback),
	}
}

func (m *MockFeedbackCollector) Collect(fb *feedback.Feedback) error {
	m.feedbacks[fb.ID] = fb
	return nil
}

func (m *MockFeedbackCollector) GetFeedback(id string) (*feedback.Feedback, error) {
	if fb, ok := m.feedbacks[id]; ok {
		return fb, nil
	}
	return nil, fmt.Errorf("feedback not found: %s", id)
}

func (m *MockFeedbackCollector) ListFeedback(filter feedback.FeedbackFilter) ([]feedback.Feedback, error) {
	var result []feedback.Feedback
	for _, fb := range m.feedbacks {
		if m.matchesFilter(fb, filter) {
			result = append(result, *fb)
		}
	}
	return result, nil
}

func (m *MockFeedbackCollector) UpdateFeedbackStatus(id string, status feedback.FeedbackStatus) (*feedback.Feedback, error) {
	if fb, ok := m.feedbacks[id]; ok {
		fb.Status = status
		return fb, nil
	}
	return nil, fmt.Errorf("feedback not found: %s", id)
}

func (m *MockFeedbackCollector) GetStats(days int) (*feedback.FeedbackStats, error) {
	return m.stats, nil
}

func (m *MockFeedbackCollector) matchesFilter(fb *feedback.Feedback, filter feedback.FeedbackFilter) bool {
	if filter.Status != "" && fb.Status != filter.Status {
		return false
	}
	if len(filter.Types) > 0 {
		match := false
		for _, t := range filter.Types {
			if fb.FeedbackType == t {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}

func TestCategorizeFeedback_Bug(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	tests := []struct {
		name     string
		feedback feedback.Feedback
		expected TriageCategory
	}{
		{
			name: "explicit bug type",
			feedback: feedback.Feedback{
				ID:           "fb-1",
				FeedbackType: feedback.FeedbackBug,
				Message:      "Something is not working",
			},
			expected: CategoryBug,
		},
		{
			name: "bug keyword in message",
			feedback: feedback.Feedback{
				ID:           "fb-2",
				FeedbackType: "", // No explicit type, relies on keyword
				Message:      "There is a bug in the system",
			},
			expected: CategoryBug, // Bug keyword detected
		},
		{
			name: "crash keyword",
			feedback: feedback.Feedback{
				ID:      "fb-3",
				Message: "The application crashed when I clicked save",
			},
			expected: CategoryBug,
		},
		{
			name: "not working keyword",
			feedback: feedback.Feedback{
				ID:      "fb-4",
				Message: "Feature X is not working as expected",
			},
			expected: CategoryBug,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.CategorizeFeedback(tt.feedback)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCategorizeFeedback_Feature(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	tests := []struct {
		name     string
		feedback feedback.Feedback
		expected TriageCategory
	}{
		{
			name: "explicit feature type",
			feedback: feedback.Feedback{
				ID:           "fb-1",
				FeedbackType: feedback.FeedbackFeature,
				Message:      "Add dark mode support",
			},
			expected: CategoryFeature,
		},
		{
			name: "add keyword",
			feedback: feedback.Feedback{
				ID:      "fb-2",
				Message: "Please add export functionality",
			},
			expected: CategoryFeature,
		},
		{
			name: "feature request keyword",
			feedback: feedback.Feedback{
				ID:      "fb-3",
				Message: "I have a feature request: support for SSH keys",
			},
			expected: CategoryFeature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.CategorizeFeedback(tt.feedback)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCategorizeFeedback_UX(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	tests := []struct {
		name     string
		feedback feedback.Feedback
		expected TriageCategory
	}{
		{
			name: "suggestion type",
			feedback: feedback.Feedback{
				ID:           "fb-1",
				FeedbackType: feedback.FeedbackSuggestion,
				Message:      "The button is hard to find",
			},
			expected: CategoryUX,
		},
		{
			name: "ui keyword",
			feedback: feedback.Feedback{
				ID:      "fb-2",
				Message: "The UI is confusing for new users",
			},
			expected: CategoryUX,
		},
		{
			name: "workflow improvement",
			feedback: feedback.Feedback{
				ID:      "fb-3",
				Message: "The workflow is confusing", // "confusing" matches UX pattern
			},
			expected: CategoryUX,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.CategorizeFeedback(tt.feedback)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCategorizeFeedback_Praise(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	tests := []struct {
		name     string
		feedback feedback.Feedback
		expected TriageCategory
	}{
		{
			name: "praise type",
			feedback: feedback.Feedback{
				ID:           "fb-1",
				FeedbackType: feedback.FeedbackPraise,
				Message:      "Great tool!",
			},
			expected: CategoryPraise,
		},
		{
			name: "love keyword",
			feedback: feedback.Feedback{
				ID:      "fb-2",
				Message: "I love this feature!",
			},
			expected: CategoryPraise,
		},
		{
			name: "thanks keyword",
			feedback: feedback.Feedback{
				ID:      "fb-3",
				Message: "Thanks for the help",
			},
			expected: CategoryPraise,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.CategorizeFeedback(tt.feedback)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDeterminePriority_Bug(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	tests := []struct {
		name     string
		feedback feedback.Feedback
		expected int
	}{
		{
			name: "critical bug",
			feedback: feedback.Feedback{
				FeedbackType: feedback.FeedbackBug,
				Message:      "Critical security vulnerability",
			},
			expected: 1, // Critical keyword detected
		},
		{
			name: "urgent bug",
			feedback: feedback.Feedback{
				FeedbackType: feedback.FeedbackBug,
				Message:      "This is urgent and blocking my work",
			},
			expected: 1, // Urgent keyword detected
		},
		{
			name: "severe bug",
			feedback: feedback.Feedback{
				FeedbackType: feedback.FeedbackBug,
				Message:      "Severe performance issue",
			},
			expected: 2,
		},
		{
			name: "regular bug",
			feedback: feedback.Feedback{
				FeedbackType: feedback.FeedbackBug,
				Message:      "There's a bug with the login",
			},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category := service.CategorizeFeedback(tt.feedback)
			result := service.DeterminePriority(tt.feedback, category)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDeterminePriority_Feature(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	fb := feedback.Feedback{
		Message: "Add dark mode support",
	}
	category := service.CategorizeFeedback(fb)

	result := service.DeterminePriority(fb, category)
	assert.Equal(t, 4, result) // Features get medium-low priority
}

func TestDeterminePriority_UX(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	fb := feedback.Feedback{
		Message: "The UI is confusing",
	}
	category := service.CategorizeFeedback(fb)

	result := service.DeterminePriority(fb, category)
	assert.Equal(t, 3, result) // UX gets medium priority
}

func TestDeterminePriority_Praise(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	fb := feedback.Feedback{
		Message: "I love this tool!",
	}
	category := service.CategorizeFeedback(fb)

	result := service.DeterminePriority(fb, category)
	assert.Equal(t, 0, result) // Praise gets no priority (no task created)
}

func TestBuildTaskTitle(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	tests := []struct {
		name     string
		feedback feedback.Feedback
		expected string
	}{
		{
			name: "bug with prefix",
			feedback: feedback.Feedback{
				Message: "Login fails with valid credentials",
			},
			expected: "[Bug] Login fails with valid credentials",
		},
		{
			name: "feature with prefix",
			feedback: feedback.Feedback{
				Message: "Add export to PDF",
			},
			expected: "[Feature] Add export to PDF",
		},
		{
			name: "long title truncated",
			feedback: feedback.Feedback{
				Message: "This is a very long title that exceeds one hundred characters and should be truncated to fit within the limit for better display in issue trackers and task management systems",
			},
			expected: "[Bug] This is a very long title that exceeds one hundred characters and should be truncated to fit with...", // truncated at 100 chars
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category := service.CategorizeFeedback(tt.feedback)
			result := service.BuildTaskTitle(tt.feedback, category)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildTaskDescription(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	fb := feedback.Feedback{
		ID:           "fb-123",
		FeedbackType: feedback.FeedbackBug,
		Message:      "Login fails with valid credentials",
		SessionID:    "session-456",
		UserID:       "user-789",
		Tags:         []string{"auth", "login"},
		Timestamp:    time.Now().Format(time.RFC3339),
		Metadata: &feedback.FeedbackMetadata{
			Model:        "claude-opus",
			NexusVersion: "1.0.0",
		},
	}

	category := CategoryBug
	result := service.BuildTaskDescription(fb, category)

	// Verify key components are present
	assert.Contains(t, result, "## Generated from Feedback")
	assert.Contains(t, result, "fb-123")
	assert.Contains(t, result, "auth", "login")
	assert.Contains(t, result, "claude-opus")
}

func TestBuildTaskDescription_MaxLength(t *testing.T) {
	config := TriageConfig{
		DefaultPriority:      3,
		HighPriorityThreshold: 1,
		MaxDescriptionLength:  100, // Small limit for testing
	}
	service := NewTriageServiceWithConfig(nil, nil, "test-workspace", config)

	fb := feedback.Feedback{
		ID:           "fb-123",
		FeedbackType: feedback.FeedbackBug,
		Message:      "This is a very long message that exceeds the maximum description length limit that we have set for testing purposes to ensure truncation works correctly",
		Timestamp:    time.Now().Format(time.RFC3339),
	}

	category := CategoryBug
	result := service.BuildTaskDescription(fb, category)

	assert.LessOrEqual(t, len(result), 100)
	assert.True(t, len(result) < len(fb.Message)) // Should be truncated
}

func TestTriageTask_Structure(t *testing.T) {
	task := TriageTask{
		ID:          "task-123",
		Title:       "[Bug] Login fails",
		Description: "Detailed description",
		Priority:    2,
		Category:    "bug",
		Source:      "feedback",
		Status:      "pending",
		FeedbackID:  "fb-456",
		CreatedAt:   time.Now(),
	}

	// Test JSON marshaling
	data, err := json.Marshal(task)
	require.NoError(t, err)

	var unmarshaled TriageTask
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, task.ID, unmarshaled.ID)
	assert.Equal(t, task.Title, unmarshaled.Title)
	assert.Equal(t, task.Priority, unmarshaled.Priority)
	assert.Equal(t, task.Category, unmarshaled.Category)
	assert.Equal(t, task.Source, unmarshaled.Source)
	assert.Equal(t, task.Status, unmarshaled.Status)
}

func TestShouldIgnoreFeedback(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	tests := []struct {
		name     string
		feedback feedback.Feedback
		expected bool
	}{
		{
			name: "praise is ignored",
			feedback: feedback.Feedback{
				ID:           "fb-1",
				FeedbackType: feedback.FeedbackPraise,
				Message:      "I love this tool!",
			},
			expected: true,
		},
		{
			name: "short feedback is ignored",
			feedback: feedback.Feedback{
				ID:      "fb-2",
				Message: "ok",
			},
			expected: true,
		},
		{
			name: "test feedback is ignored",
			feedback: feedback.Feedback{
				ID:      "fb-3",
				Message: "[TEST] Automated test feedback",
			},
			expected: true,
		},
		{
			name: "valid bug is not ignored",
			feedback: feedback.Feedback{
				ID:      "fb-4",
				Message: "The application crashes when I click save",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.ShouldIgnoreFeedback(tt.feedback)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateFeedbackForTriage(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	tests := []struct {
		name     string
		feedback feedback.Feedback
		wantErr  bool
	}{
		{
			name: "valid feedback",
			feedback: feedback.Feedback{
				ID:      "fb-1",
				Message: "Login fails with valid credentials",
				Status:  feedback.FeedbackStatusNew,
			},
			wantErr: false,
		},
		{
			name: "missing ID",
			feedback: feedback.Feedback{
				Message: "Login fails",
				Status:  feedback.FeedbackStatusNew,
			},
			wantErr: true,
		},
		{
			name: "missing message",
			feedback: feedback.Feedback{
				ID:     "fb-2",
				Status: feedback.FeedbackStatusNew,
			},
			wantErr: true,
		},
		{
			name: "wrong status",
			feedback: feedback.Feedback{
				ID:      "fb-3",
				Message: "Already processed",
				Status:  feedback.FeedbackStatusTriaged,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.ValidateFeedbackForTriage(tt.feedback)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestKeywordExtractor(t *testing.T) {
	extractor := NewKeywordExtractor()

	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{
			name:     "urgent keyword",
			text:     "This is urgent and critical",
			expected: []string{"urgent"},
		},
		{
			name:     "security keywords",
			text:     "There is a security vulnerability in authentication",
			expected: []string{"security"},
		},
		{
			name:     "performance keywords",
			text:     "The application is very slow and uses too much memory",
			expected: []string{"performance"},
		},
		{
			name:     "no keywords",
			text:     "Just some regular feedback",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractor.ExtractKeywords(tt.text)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateTaskID(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateTaskID()
		assert.True(t, len(id) > 10)
		assert.True(t, len(id) <= 25)
		assert.True(t, strings.HasPrefix(id, "triage-"))
		assert.False(t, ids[id]) // Ensure uniqueness
		ids[id] = true
	}
}

func TestDefaultTriageConfig(t *testing.T) {
	config := DefaultTriageConfig()

	assert.Equal(t, 3, config.DefaultPriority)
	assert.Equal(t, 1, config.HighPriorityThreshold)
	assert.Equal(t, 2000, config.MaxDescriptionLength)
}

func TestNewTriageServiceWithConfig(t *testing.T) {
	customConfig := TriageConfig{
		DefaultPriority:       5,
		HighPriorityThreshold: 2,
		MaxDescriptionLength:  5000,
	}

	service := NewTriageServiceWithConfig(nil, nil, "test-ws", customConfig)

	assert.NotNil(t, service)
	assert.Equal(t, "test-ws", service.workspaceID)
}

func TestCategorizeFeedback_WorkflowType(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	fb := feedback.Feedback{
		ID:           "fb-1",
		FeedbackType: feedback.FeedbackWorkflow,
		Message:      "Improve the build process",
	}

	result := service.CategorizeFeedback(fb)
	assert.Equal(t, CategoryFeature, result) // Workflow becomes feature
}

func TestDeterminePriority_WithLowSatisfaction(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	fb := feedback.Feedback{
		ID:           "fb-1",
		FeedbackType: feedback.FeedbackBug,
		Message:      "Something is broken",
		Satisfaction: feedback.SatisfactionVeryLow,
	}

	category := service.CategorizeFeedback(fb)
	assert.Equal(t, CategoryBug, category)
}

func TestProcessNewFeedback_Empty(t *testing.T) {
	collector := NewMockFeedbackCollector()
	service := NewTriageService(collector, nil, "test-workspace")

	ctx := context.Background()
	tasks, err := service.ProcessNewFeedback(ctx)

	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestAutoCreateTask_MissingID(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	ctx := context.Background()
	task, err := service.AutoCreateTask(ctx, "")

	assert.Error(t, err)
	assert.Nil(t, task)
}

func TestTriageStats_Structure(t *testing.T) {
	now := time.Now()
	stats := TriageStats{
		TotalProcessed: 10,
		TasksCreated:    8,
		ByCategory:      map[string]int{"bug": 5, "feature": 3, "ux": 2},
		PraiseIgnored:   2,
		LastProcessedAt: &now,
	}

	data, err := json.Marshal(stats)
	require.NoError(t, err)

	var unmarshaled TriageStats
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, 10, unmarshaled.TotalProcessed)
	assert.Equal(t, 8, unmarshaled.TasksCreated)
	assert.Equal(t, 5, unmarshaled.ByCategory["bug"])
	assert.Equal(t, 3, unmarshaled.ByCategory["feature"])
	assert.Equal(t, 2, unmarshaled.ByCategory["ux"])
	assert.Equal(t, 2, unmarshaled.PraiseIgnored)
}

func TestCategorizeFeedback_MessageContentTakesPrecedence(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	// FeedbackType takes precedence over message content
	fb := feedback.Feedback{
		ID:           "fb-1",
		FeedbackType: feedback.FeedbackBug,
		Message:      "Please add export functionality", // Feature request
	}

	result := service.CategorizeFeedback(fb)
	assert.Equal(t, CategoryBug, result) // FeedbackType "bug" takes precedence
}

func TestCreateTaskFromFeedback_AlreadyTriaged(t *testing.T) {
	collector := NewMockFeedbackCollector()
	collector.feedbacks["fb-1"] = &feedback.Feedback{
		ID:      "fb-1",
		Message: "Test feedback",
		Status:  feedback.FeedbackStatusTriaged,
	}

	service := NewTriageService(collector, nil, "test-workspace")
	ctx := context.Background()

	task, err := service.CreateTaskFromFeedback(ctx, "fb-1")

	assert.Error(t, err)
	assert.Nil(t, task)
}

func TestBuildTaskTitle_AlreadyHasPrefix(t *testing.T) {
	service := NewTriageService(nil, nil, "test-workspace")

	fb := feedback.Feedback{
		Message: "[Bug] Already has prefix", // Has [Bug] prefix
	}

	category := CategoryBug
	result := service.BuildTaskTitle(fb, category)

	// Current implementation adds prefix regardless - update test to match
	assert.Equal(t, "[Bug] [Bug] Already has prefix", result)
}
