package metrics

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowTracker_NewWorkflowTracker(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	assert.NotNil(t, tracker)
	assert.Contains(t, tracker.sessionsFile, "claude_sessions.json")
	assert.Contains(t, tracker.eventsFile, "claude_events.json")
}

func TestWorkflowTracker_StartSession(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")

	require.NotEmpty(t, sessionID)
	assert.Len(t, sessionID, 16) // 8 bytes hex encoded = 16 chars

	// Verify session was created
	session, err := tracker.GetSession(sessionID)
	require.NoError(t, err)
	assert.Equal(t, "user-123", session.UserID)
	assert.Equal(t, "sonnet", session.Model)
	assert.Empty(t, session.EndTime)
	assert.False(t, session.Outcome.Success)
}

func TestWorkflowTracker_EndSession(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")

	outcome := SessionOutcome{
		Success:    true,
		Duration:   300,
		TokensUsed: 50000,
		Errors:     nil,
	}

	err := tracker.EndSession(sessionID, outcome)
	require.NoError(t, err)

	session, err := tracker.GetSession(sessionID)
	require.NoError(t, err)
	assert.True(t, session.Outcome.Success)
	assert.Equal(t, int64(300), session.Outcome.Duration)
	assert.Equal(t, int64(50000), session.Outcome.TokensUsed)
	assert.NotEmpty(t, session.EndTime)
}

func TestWorkflowTracker_EndSessionWithErrors(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")

	outcome := SessionOutcome{
		Success:  false,
		Duration: 120,
		Errors:   []string{"timeout", "memory limit exceeded"},
	}

	err := tracker.EndSession(sessionID, outcome)
	require.NoError(t, err)

	session, err := tracker.GetSession(sessionID)
	require.NoError(t, err)
	assert.False(t, session.Outcome.Success)
	assert.Len(t, session.Outcome.Errors, 2)
}

func TestWorkflowTracker_RecordEvent(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")

	metadata := map[string]interface{}{
		"files_modified": 5,
		"lines_added":    100,
	}
	tracker.RecordEvent(sessionID, "code_change", WorkflowStageCoding, metadata)

	// Event should be recorded - verify by checking events file exists
	eventsFile := tmpDir + "/.nexus/claude_events.json"
	_, err := os.Stat(eventsFile)
	assert.NoError(t, err)
}

func TestWorkflowTracker_RecordSkillUsage(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")

	tracker.RecordSkillUsage(sessionID, "executor", 1500)
	tracker.RecordSkillUsage(sessionID, "architect", 800)

	session, err := tracker.GetSession(sessionID)
	require.NoError(t, err)
	assert.Contains(t, session.SkillsUsed, "executor")
	assert.Contains(t, session.SkillsUsed, "architect")
	assert.Len(t, session.SkillsUsed, 2)
}

func TestWorkflowTracker_RecordNexusFeature(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")

	tracker.RecordNexusFeature(sessionID, "workspace.create")
	tracker.RecordNexusFeature(sessionID, "transport.ssh")

	session, err := tracker.GetSession(sessionID)
	require.NoError(t, err)
	assert.Contains(t, session.NexusFeatures, "workspace.create")
	assert.Contains(t, session.NexusFeatures, "transport.ssh")
}

func TestWorkflowTracker_SetSessionSatisfaction(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")

	err := tracker.SetSessionSatisfaction(sessionID, SatisfactionVerySatisfied)
	require.NoError(t, err)

	session, err := tracker.GetSession(sessionID)
	require.NoError(t, err)
	assert.Equal(t, SatisfactionVerySatisfied, session.Satisfaction)
}

func TestWorkflowTracker_GetSession_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	_, err := tracker.GetSession("nonexistent")
	assert.Error(t, err)
}

func TestWorkflowTracker_GetWorkflowStats(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	// Create multiple sessions
	sessionIDs := []string{}
	for i := 0; i < 5; i++ {
		sessionID := tracker.StartSession("user-123", "sonnet")
		tracker.RecordSkillUsage(sessionID, "executor", 1000)
		tracker.RecordNexusFeature(sessionID, "workspace.create")

		if i%2 == 0 {
			tracker.SetSessionSatisfaction(sessionID, SatisfactionSatisfied)
		}

		tracker.EndSession(sessionID, SessionOutcome{
			Success:  i != 2, // One failure
			Duration: 200,
		})
		sessionIDs = append(sessionIDs, sessionID)
	}

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	assert.Equal(t, 5, stats.TotalSessions)
	assert.Equal(t, 1, stats.ActiveUsers)
	assert.Equal(t, 4, stats.SessionsByOutcome["success"])
	assert.Equal(t, 1, stats.SessionsByOutcome["failure"])
	assert.Equal(t, 1, stats.NexusFeatureUsage["workspace.create"])
	assert.Contains(t, stats.SkillsFrequency, "executor")
	assert.Len(t, stats.TopSkills, 1)
}

func TestWorkflowTracker_GetWorkflowStats_NoSessions(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	assert.Equal(t, 0, stats.TotalSessions)
	assert.Equal(t, 0, stats.ActiveUsers)
	assert.Equal(t, 0.0, stats.AverageSessionDuration)
}

func TestWorkflowTracker_StatsCalculation(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	// Create sessions with different satisfaction levels
	for i, satisfaction := range []SatisfactionLevel{
		SatisfactionVerySatisfied,
		SatisfactionSatisfied,
		SatisfactionNeutral,
		SatisfactionUnsatisfied,
		SatisfactionVeryUnsatisfied,
	} {
		sessionID := tracker.StartSession("user-123", "sonnet")
		tracker.SetSessionSatisfaction(sessionID, satisfaction)
		tracker.EndSession(sessionID, SessionOutcome{
			Success:  true,
			Duration: 100,
		})
		_ = i // use the loop variable
	}

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	// Average should be 3.0 (1+2+3+4+5) / 5 = 3.0
	assert.Equal(t, 3.0, stats.AverageSatisfaction)
}

func TestWorkflowTracker_AverageSessionDuration(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	// Create sessions with different durations
	durations := []int64{100, 200, 300}
	for _, d := range durations {
		sessionID := tracker.StartSession("user-123", "sonnet")
		tracker.EndSession(sessionID, SessionOutcome{
			Success:  true,
			Duration: d,
		})
	}

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	// Average should be (100 + 200 + 300) / 3 = 200
	assert.Equal(t, 200.0, stats.AverageSessionDuration)
}

func TestWorkflowTracker_LoadCreatesNewStore(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	// Remove files if they exist
	os.Remove(tracker.sessionsFile)
	os.Remove(tracker.eventsFile)

	// Should not error - creates new store
	err := tracker.loadSessions()
	require.NoError(t, err)
	assert.Len(t, tracker.sessions.Sessions, 0)

	err = tracker.loadEvents()
	require.NoError(t, err)
	assert.Len(t, tracker.events.Events, 0)
}

func TestWorkflowTracker_SessionWithWorkspaceID(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")
	require.NotEmpty(t, sessionID)

	// Manually set workspace ID to simulate workspace claim
	if err := tracker.loadSessions(); err != nil {
		t.Fatal(err)
	}
	for i := range tracker.sessions.Sessions {
		if tracker.sessions.Sessions[i].SessionID == sessionID {
			tracker.sessions.Sessions[i].WorkspaceID = "ws-456"
			break
		}
	}
	if err := tracker.saveSessions(); err != nil {
		t.Fatal(err)
	}

	session, err := tracker.GetSession(sessionID)
	require.NoError(t, err)
	assert.Equal(t, "ws-456", session.WorkspaceID)
}

func TestWorkflowTracker_MultipleUsers(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	users := []string{"user-1", "user-2", "user-3"}
	for _, user := range users {
		sessionID := tracker.StartSession(user, "sonnet")
		tracker.EndSession(sessionID, SessionOutcome{
			Success:  true,
			Duration: 100,
		})
	}

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	assert.Equal(t, 3, stats.ActiveUsers)
	assert.Equal(t, 3, stats.TotalSessions)
}

func TestWorkflowTracker_WorkflowStageTimes(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")

	// Record events with different stages and durations
	tracker.RecordEvent(sessionID, "task_created", WorkflowStageTaskCreation, nil)
	tracker.RecordEvent(sessionID, "workspace_claimed", WorkflowStageWorkspaceClaim, nil)
	tracker.RecordEvent(sessionID, "coding_started", WorkflowStageCoding, nil)

	// Manually record durations for stages
	if err := tracker.loadEvents(); err != nil {
		t.Fatal(err)
	}
	for i := range tracker.events.Events {
		if tracker.events.Events[i].SessionID == sessionID {
			if tracker.events.Events[i].Stage == WorkflowStageCoding {
				tracker.events.Events[i].Duration = 5000
			}
		}
	}
	if err := tracker.saveEvents(); err != nil {
		t.Fatal(err)
	}

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	// Verify stage times are tracked
	assert.Contains(t, stats.WorkflowStageTimes, string(WorkflowStageTaskCreation))
	assert.Contains(t, stats.WorkflowStageTimes, string(WorkflowStageCoding))
}

func TestWorkflowTracker_SkillsFrequency(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")
	tracker.RecordSkillUsage(sessionID, "executor", 1000)
	tracker.RecordSkillUsage(sessionID, "executor", 1500)
	tracker.RecordSkillUsage(sessionID, "architect", 800)

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	assert.Equal(t, 2, stats.SkillsFrequency["executor"])
	assert.Equal(t, 1, stats.SkillsFrequency["architect"])
}

func TestWorkflowTracker_TopSkillsSorted(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	sessionID := tracker.StartSession("user-123", "sonnet")
	tracker.RecordSkillUsage(sessionID, "low_skill", 100)
	tracker.RecordSkillUsage(sessionID, "high_skill", 100)
	tracker.RecordSkillUsage(sessionID, "high_skill", 100)
	tracker.RecordSkillUsage(sessionID, "high_skill", 100)

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	require.Len(t, stats.TopSkills, 2)
	assert.Equal(t, "high_skill", stats.TopSkills[0].SkillName)
	assert.Equal(t, 3, stats.TopSkills[0].Count)
	assert.Equal(t, "low_skill", stats.TopSkills[1].SkillName)
	assert.Equal(t, 1, stats.TopSkills[1].Count)
}

func TestWorkflowTracker_RecentSessionsLimit(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	// Create 15 sessions
	for i := 0; i < 15; i++ {
		sessionID := tracker.StartSession("user-123", "sonnet")
		tracker.EndSession(sessionID, SessionOutcome{
			Success:  true,
			Duration: 100,
		})
	}

	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	// Recent sessions should be limited to 10
	assert.Len(t, stats.RecentSessions, 10)
}

func TestWorkflowTracker_DaysFilter(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewWorkflowTracker(tmpDir)

	// Create old session
	oldSessionID := tracker.StartSession("user-old", "sonnet")
	tracker.EndSession(oldSessionID, SessionOutcome{
		Success:  true,
		Duration: 100,
	})

	// Manually set start time to 10 days ago
	if err := tracker.loadSessions(); err != nil {
		t.Fatal(err)
	}
	for i := range tracker.sessions.Sessions {
		if tracker.sessions.Sessions[i].SessionID == oldSessionID {
			tracker.sessions.Sessions[i].StartTime = time.Now().AddDate(0, 0, -10).Format(time.RFC3339)
			break
		}
	}
	if err := tracker.saveSessions(); err != nil {
		t.Fatal(err)
	}

	// Create recent session
	newSessionID := tracker.StartSession("user-new", "sonnet")
	tracker.EndSession(newSessionID, SessionOutcome{
		Success:  true,
		Duration: 100,
	})

	// Query with 7 days filter
	stats, err := tracker.GetWorkflowStats(7)
	require.NoError(t, err)

	// Should only include recent sessions
	assert.Equal(t, 1, stats.TotalSessions)
	assert.Equal(t, "user-new", stats.RecentSessions[0].UserID)
}
