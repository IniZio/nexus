package feedback

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestCollector(t *testing.T) (*FeedbackCollector, string) {
	tmpDir, err := os.MkdirTemp("", "feedback-test")
	require.NoError(t, err)

	collector := NewCollector(tmpDir)
	return collector, tmpDir
}

func cleanupTestCollector(t *testing.T, tmpDir string) {
	os.RemoveAll(tmpDir)
}

func TestNewCollector(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	assert.NotNil(t, collector)
	assert.Contains(t, collector.filePath, ".nexus")
	assert.Contains(t, collector.filePath, "feedback.json")
}

func TestSubmit(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	tests := []struct {
		name     string
		feedback Feedback
		wantErr  bool
	}{
		{
			name: "valid feedback with all fields",
			feedback: Feedback{
				SessionID:    "session-123",
				UserID:       "user-456",
				FeedbackType: FeedbackBug,
				Satisfaction: SatisfactionHigh,
				Category:     "performance",
				Message:      "Great product!",
				Tags:         []string{"fast", "reliable"},
				Status:       FeedbackStatusNew,
			},
			wantErr: false,
		},
		{
			name: "valid feedback with minimal fields",
			feedback: Feedback{
				SessionID:    "session-789",
				FeedbackType: FeedbackFeature,
				Satisfaction: SatisfactionNeutral,
				Message:      "Would be nice to have dark mode",
			},
			wantErr: false,
		},
		{
			name: "feedback with pre-set ID and timestamp",
			feedback: Feedback{
				ID:           "pre-set-id",
				Timestamp:    "2024-01-01T00:00:00Z",
				SessionID:    "session-abc",
				FeedbackType: FeedbackSuggestion,
				Satisfaction: SatisfactionVeryHigh,
				Message:      "Love the new UI",
				Status:       FeedbackStatusReviewed,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := collector.Submit(tt.feedback)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.feedback.ID == "" {
					assert.NotEmpty(t, tt.feedback.ID)
				}
				if tt.feedback.Timestamp == "" {
					assert.NotEmpty(t, tt.feedback.Timestamp)
				}
				if tt.feedback.Status == "" {
					assert.Equal(t, FeedbackStatusNew, tt.feedback.Status)
				}
			}
		})
	}
}

func TestGet(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	// Submit a feedback first
	original := Feedback{
		SessionID:    "session-get-test",
		FeedbackType: FeedbackPraise,
		Satisfaction: SatisfactionVeryHigh,
		Message:      "Excellent work!",
	}
	err := collector.Submit(original)
	require.NoError(t, err)

	t.Run("get existing feedback", func(t *testing.T) {
		result, err := collector.Get(original.ID)
		require.NoError(t, err)
		assert.Equal(t, original.ID, result.ID)
		assert.Equal(t, original.Message, result.Message)
		assert.Equal(t, original.FeedbackType, result.FeedbackType)
	})

	t.Run("get non-existent feedback", func(t *testing.T) {
		_, err := collector.Get("non-existent-id")
		assert.Error(t, err)
	})
}

func TestList(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	// Submit multiple feedbacks
	feedbacks := []Feedback{
		{SessionID: "s1", FeedbackType: FeedbackBug, Satisfaction: SatisfactionLow, Message: "Bug 1", Category: "crash"},
		{SessionID: "s2", FeedbackType: FeedbackFeature, Satisfaction: SatisfactionHigh, Message: "Feature 1", Category: "ui"},
		{SessionID: "s3", FeedbackType: FeedbackBug, Satisfaction: SatisfactionVeryLow, Message: "Bug 2", Category: "crash"},
		{SessionID: "s4", FeedbackType: FeedbackSuggestion, Satisfaction: SatisfactionNeutral, Message: "Suggestion 1"},
		{SessionID: "s5", FeedbackType: FeedbackPraise, Satisfaction: SatisfactionVeryHigh, Message: "Praise 1"},
	}

	for _, fb := range feedbacks {
		err := collector.Submit(fb)
		require.NoError(t, err)
	}

	t.Run("list all", func(t *testing.T) {
		result, err := collector.List(FeedbackFilter{})
		require.NoError(t, err)
		assert.Len(t, result, 5)
	})

	t.Run("filter by type", func(t *testing.T) {
		result, err := collector.List(FeedbackFilter{Types: []FeedbackType{FeedbackBug}})
		require.NoError(t, err)
		assert.Len(t, result, 2)
		for _, fb := range result {
			assert.Equal(t, FeedbackBug, fb.FeedbackType)
		}
	})

	t.Run("filter by multiple types", func(t *testing.T) {
		result, err := collector.List(FeedbackFilter{Types: []FeedbackType{FeedbackBug, FeedbackFeature}})
		require.NoError(t, err)
		assert.Len(t, result, 3)
	})

	t.Run("filter by satisfaction", func(t *testing.T) {
		result, err := collector.List(FeedbackFilter{Satisfaction: []int{1, 2}}) // Low satisfaction
		require.NoError(t, err)
		assert.Len(t, result, 2)
		for _, fb := range result {
			assert.True(t, fb.Satisfaction <= 2)
		}
	})

	t.Run("filter by category", func(t *testing.T) {
		result, err := collector.List(FeedbackFilter{Categories: []string{"crash"}})
		require.NoError(t, err)
		assert.Len(t, result, 2)
		for _, fb := range result {
			assert.Equal(t, "crash", fb.Category)
		}
	})

	t.Run("filter by status", func(t *testing.T) {
		// Mark one as reviewed
		all, _ := collector.List(FeedbackFilter{})
		collector.UpdateStatus(all[0].ID, FeedbackStatusReviewed)

		result, err := collector.List(FeedbackFilter{Status: FeedbackStatusReviewed})
		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, FeedbackStatusReviewed, result[0].Status)
	})

	t.Run("pagination with limit", func(t *testing.T) {
		result, err := collector.List(FeedbackFilter{Limit: 2})
		require.NoError(t, err)
		assert.Len(t, result, 2)
	})

	t.Run("pagination with offset", func(t *testing.T) {
		all, _ := collector.List(FeedbackFilter{})
		result, err := collector.List(FeedbackFilter{Offset: 2})
		require.NoError(t, err)
		assert.Len(t, result, len(all)-2)
	})

	t.Run("combined filters", func(t *testing.T) {
		result, err := collector.List(FeedbackFilter{
			Types:        []FeedbackType{FeedbackBug},
			Categories:   []string{"crash"},
			Satisfaction: []int{1},
		})
		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "Bug 2", result[0].Message)
	})
}

func TestUpdateStatus(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	// Submit a feedback
	fb := Feedback{
		SessionID:    "session-status-test",
		FeedbackType: FeedbackSuggestion,
		Satisfaction: SatisfactionHigh,
		Message:      "Add dark mode",
	}
	err := collector.Submit(fb)
	require.NoError(t, err)

	t.Run("update status successfully", func(t *testing.T) {
		err := collector.UpdateStatus(fb.ID, FeedbackStatusTriaged)
		require.NoError(t, err)

		result, err := collector.Get(fb.ID)
		require.NoError(t, err)
		assert.Equal(t, FeedbackStatusTriaged, result.Status)
	})

	t.Run("update to resolved", func(t *testing.T) {
		err := collector.UpdateStatus(fb.ID, FeedbackStatusResolved)
		require.NoError(t, err)

		result, err := collector.Get(fb.ID)
		require.NoError(t, err)
		assert.Equal(t, FeedbackStatusResolved, result.Status)
	})

	t.Run("update non-existent feedback", func(t *testing.T) {
		err := collector.UpdateStatus("non-existent", FeedbackStatusReviewed)
		assert.Error(t, err)
	})
}

func TestGetStats(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	// Submit feedbacks with different types and satisfaction levels
	feedbacks := []Feedback{
		{SessionID: "s1", FeedbackType: FeedbackBug, Satisfaction: SatisfactionLow, Message: "Bug 1"},
		{SessionID: "s2", FeedbackType: FeedbackBug, Satisfaction: SatisfactionVeryLow, Message: "Bug 2"},
		{SessionID: "s3", FeedbackType: FeedbackFeature, Satisfaction: SatisfactionHigh, Message: "Feature 1"},
		{SessionID: "s4", FeedbackType: FeedbackPraise, Satisfaction: SatisfactionVeryHigh, Message: "Praise 1"},
		{SessionID: "s5", FeedbackType: FeedbackSuggestion, Satisfaction: SatisfactionNeutral, Message: "Suggestion 1"},
	}

	for _, fb := range feedbacks {
		err := collector.Submit(fb)
		require.NoError(t, err)
	}

	stats, err := collector.GetStats(7)
	require.NoError(t, err)

	assert.Equal(t, 5, stats.TotalFeedback)

	// Check type distribution
	assert.Equal(t, 2, stats.ByType[string(FeedbackBug)])
	assert.Equal(t, 1, stats.ByType[string(FeedbackFeature)])
	assert.Equal(t, 1, stats.ByType[string(FeedbackPraise)])
	assert.Equal(t, 1, stats.ByType[string(FeedbackSuggestion)])

	// Check status distribution (all should be 'new')
	assert.Equal(t, 5, stats.ByStatus[string(FeedbackStatusNew)])

	// Check satisfaction distribution
	assert.Equal(t, 1, stats.SatisfactionDistribution[1]) // VeryLow
	assert.Equal(t, 1, stats.SatisfactionDistribution[2]) // Low
	assert.Equal(t, 1, stats.SatisfactionDistribution[3]) // Neutral
	assert.Equal(t, 1, stats.SatisfactionDistribution[4]) // High
	assert.Equal(t, 1, stats.SatisfactionDistribution[5]) // VeryHigh

	// Check average satisfaction: (1+2+3+4+5)/5 = 3.0
	assert.InDelta(t, 3.0, stats.AverageSatisfaction, 0.01)

	// Check trend has entries for last 7 days
	assert.Len(t, stats.RecentTrend, 7)
	for _, day := range stats.RecentTrend {
		assert.NotEmpty(t, day.Date)
	}
}

func TestGetStatsEmpty(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	stats, err := collector.GetStats(7)
	require.NoError(t, err)

	assert.Equal(t, 0, stats.TotalFeedback)
	assert.Equal(t, 0.0, stats.AverageSatisfaction)
	assert.Len(t, stats.RecentTrend, 7)
}

func TestMatchesTimeFilter(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	// Submit a feedback with current timestamp
	fb := Feedback{
		SessionID:    "s-time",
		FeedbackType: FeedbackFeature,
		Satisfaction: SatisfactionHigh,
		Message:      "Feature request",
	}
	err := collector.Submit(fb)
	require.NoError(t, err)

	t.Run("filter by start time", func(t *testing.T) {
		startTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
		result, err := collector.List(FeedbackFilter{StartTime: startTime})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(result), 1)
	})

	t.Run("filter by end time", func(t *testing.T) {
		endTime := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
		result, err := collector.List(FeedbackFilter{EndTime: endTime})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(result), 1)
	})

	t.Run("filter by time range", func(t *testing.T) {
		startTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
		endTime := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
		result, err := collector.List(FeedbackFilter{StartTime: startTime, EndTime: endTime})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(result), 1)
	})
}

func TestFilePersistence(t *testing.T) {
	// Create first collector and submit feedback
	collector1, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	fb := Feedback{
		SessionID:    "session-persist",
		FeedbackType: FeedbackPraise,
		Satisfaction: SatisfactionVeryHigh,
		Message:      "Should persist!",
	}
	err := collector1.Submit(fb)
	require.NoError(t, err)

	// Create new collector pointing to same path
	collector2 := NewCollector(tmpDir)

	// Should be able to retrieve the feedback
	result, err := collector2.Get(fb.ID)
	require.NoError(t, err)
	assert.Equal(t, fb.Message, result.Message)
	assert.Equal(t, fb.FeedbackType, result.FeedbackType)
}

func TestConcurrentAccess(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	done := make(chan bool)
	errors := make(chan error, 10)

	// Submit feedbacks concurrently
	for i := 0; i < 10; i++ {
		go func(idx int) {
			fb := Feedback{
				SessionID:    "session-concurrent",
				FeedbackType: FeedbackFeature,
				Satisfaction: SatisfactionHigh,
				Message:      "Concurrent feedback",
			}
			errors <- collector.Submit(fb)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Check for errors
	close(errors)
	for err := range errors {
		assert.NoError(t, err)
	}

	// Verify all feedbacks were submitted
	result, err := collector.List(FeedbackFilter{})
	require.NoError(t, err)
	assert.Len(t, result, 10)
}

func TestIDGeneration(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	// Submit multiple feedbacks without ID
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		fb := Feedback{
			SessionID:    "session-gen",
			FeedbackType: FeedbackFeature,
			Satisfaction: SatisfactionHigh,
			Message:      "Test message",
		}
		err := collector.Submit(fb)
		require.NoError(t, err)
		ids[fb.ID] = true
	}

	// All IDs should be unique
	assert.Len(t, ids, 100)

	// IDs should be 16 hex characters (8 bytes)
	for id := range ids {
		assert.Len(t, id, 16)
	}
}

func TestFeedbackWithMetadata(t *testing.T) {
	collector, tmpDir := setupTestCollector(t)
	defer cleanupTestCollector(t, tmpDir)

	fb := Feedback{
		SessionID:    "session-meta",
		FeedbackType: FeedbackBug,
		Satisfaction: SatisfactionLow,
		Message:      "Found a bug",
		Metadata: &FeedbackMetadata{
			Model:           "claude-opus-4",
			NexusVersion:    "1.0.0",
			PulseVersion:   "2.0.0",
			WorkspaceID:     "ws-123",
			TaskID:          "task-456",
			SessionDuration: 3600,
			SkillsUsed:      []string{"executor", "architect"},
		},
	}
	err := collector.Submit(fb)
	require.NoError(t, err)

	result, err := collector.Get(fb.ID)
	require.NoError(t, err)

	assert.NotNil(t, result.Metadata)
	assert.Equal(t, "claude-opus-4", result.Metadata.Model)
	assert.Equal(t, "1.0.0", result.Metadata.NexusVersion)
	assert.Equal(t, "2.0.0", result.Metadata.PulseVersion)
	assert.Equal(t, "ws-123", result.Metadata.WorkspaceID)
	assert.Equal(t, "task-456", result.Metadata.TaskID)
	assert.Equal(t, int64(3600), result.Metadata.SessionDuration)
	assert.Equal(t, []string{"executor", "architect"}, result.Metadata.SkillsUsed)
}

func TestFilePath(t *testing.T) {
	// Test with different base paths
	testCases := []struct {
		basePath string
		expected string
	}{
		{"/home/user", "/home/user/.nexus/feedback.json"},
		{"/var/data", "/var/data/.nexus/feedback.json"},
		{"/tmp", "/tmp/.nexus/feedback.json"},
	}

	for _, tc := range testCases {
		collector := NewCollector(tc.basePath)
		assert.Equal(t, tc.expected, collector.filePath)
	}
}

func TestGenerateID(t *testing.T) {
	// Test that generateID produces unique IDs
	ids := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := generateID()
		assert.False(t, ids[id], "Duplicate ID generated: %s", id)
		ids[id] = true
	}
}

func TestDirectoryCreation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-dir-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	nestedPath := filepath.Join(tmpDir, "nested", "deep", "path")
	collector := NewCollector(nestedPath)

	fb := Feedback{
		SessionID:    "session-dir",
		FeedbackType: FeedbackFeature,
		Satisfaction: SatisfactionHigh,
		Message:      "Test",
	}
	err = collector.Submit(fb)
	require.NoError(t, err)

	// Verify file was created in nested directory
	assert.FileExists(t, collector.filePath)
}
