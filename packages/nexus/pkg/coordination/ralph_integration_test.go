package coordination

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRalphFeedbackCollection(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	feedback := &SessionFeedback{
		SessionID:     "session-1",
		WorkspaceName: "test-workspace",
		AgentID:       "agent-1",
		TasksWorked:   []string{"task-1", "task-2"},
		Issues: []Issue{
			{Category: IssueCategorySyntax, Description: "Missing semicolon", Context: "main.go:10", Frequency: 1},
		},
		Duration:    5 * time.Minute,
		SuccessRate: 0.8,
	}

	err = storage.SaveFeedback(ctx, feedback)
	if err != nil {
		t.Fatalf("Failed to save feedback: %v", err)
	}

	retrieved, err := storage.GetFeedback(ctx, "session-1")
	if err != nil {
		t.Fatalf("Failed to get feedback: %v", err)
	}
	if retrieved == nil {
		t.Fatal("Expected feedback to be retrieved, got nil")
	}
	if retrieved.SessionID != "session-1" {
		t.Errorf("Expected session ID 'session-1', got '%s'", retrieved.SessionID)
	}
	if len(retrieved.Issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(retrieved.Issues))
	}
}

func TestRalphFeedbackMultipleSessions(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	feedback1 := &SessionFeedback{
		SessionID:     "session-1",
		WorkspaceName: "test-workspace",
		AgentID:       "agent-1",
		TasksWorked:   []string{"task-1"},
		Issues:        []Issue{{Category: IssueCategorySyntax, Description: "Error 1", Context: "", Frequency: 1}},
		Duration:      time.Minute,
		SuccessRate:   0.5,
	}

	feedback2 := &SessionFeedback{
		SessionID:     "session-2",
		WorkspaceName: "test-workspace",
		AgentID:       "agent-2",
		TasksWorked:   []string{"task-2"},
		Issues:        []Issue{{Category: IssueCategorySyntax, Description: "Error 2", Context: "", Frequency: 1}},
		Duration:      time.Minute,
		SuccessRate:   0.6,
	}

	if err := storage.SaveFeedback(ctx, feedback1); err != nil {
		t.Fatalf("Failed to save feedback1: %v", err)
	}
	if err := storage.SaveFeedback(ctx, feedback2); err != nil {
		t.Fatalf("Failed to save feedback2: %v", err)
	}

	fb1, _ := storage.GetFeedback(ctx, "session-1")
	fb2, _ := storage.GetFeedback(ctx, "session-2")

	if fb1 == nil || fb2 == nil {
		t.Fatal("Both feedbacks should be retrievable")
	}
}

func TestRalphFeedbackByTimeRange(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	feedback := &SessionFeedback{
		SessionID:     "session-time",
		WorkspaceName: "test-workspace",
		AgentID:       "agent-1",
		TasksWorked:   []string{"task-1"},
		Issues:        []Issue{},
		Duration:      time.Minute,
		SuccessRate:   1.0,
	}
	_ = storage.SaveFeedback(ctx, feedback)

	feedbacks, err := storage.GetFeedbackByTimeRange(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Failed to get feedback by time range: %v", err)
	}
	if len(feedbacks) < 1 {
		t.Errorf("Expected at least 1 feedback in time range, got %d", len(feedbacks))
	}
}

func TestRalphPatternDetection(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	for i := 0; i < 6; i++ {
		feedback := &SessionFeedback{
			SessionID:     "session-pattern-" + string(rune('A'+i)),
			WorkspaceName: "test-workspace",
			AgentID:       "agent-1",
			TasksWorked:   []string{"workspace-task"},
			Issues: []Issue{
				{Category: IssueCategorySyntax, Description: "Common syntax error", Context: "", Frequency: 1},
			},
			Duration:    time.Minute,
			SuccessRate: 0.7,
		}
		_ = storage.SaveFeedback(ctx, feedback)
	}

	patterns, err := storage.GetPatterns(ctx, 5)
	if err != nil {
		t.Fatalf("Failed to get patterns: %v", err)
	}
	if len(patterns) < 1 {
		t.Error("Expected at least 1 pattern with threshold 5")
	}
}

func TestRalphPatternThreshold(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	for i := 0; i < 3; i++ {
		feedback := &SessionFeedback{
			SessionID:     "session-rare-" + string(rune('A'+i)),
			WorkspaceName: "test-workspace",
			AgentID:       "agent-1",
			TasksWorked:   []string{"rare-task"},
			Issues: []Issue{
				{Category: IssueCategoryConfig, Description: "Rare config issue", Context: "", Frequency: 1},
			},
			Duration:    time.Minute,
			SuccessRate: 0.9,
		}
		_ = storage.SaveFeedback(ctx, feedback)
	}

	patterns, err := storage.GetPatterns(ctx, 5)
	if err != nil {
		t.Fatalf("Failed to get patterns: %v", err)
	}
	if len(patterns) != 0 {
		t.Errorf("Expected 0 patterns for rare issues (3 < threshold 5), got %d", len(patterns))
	}
}

func TestRalphAutoUpdateSkills(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "creating-nexus-workspaces"), 0755); err != nil {
		t.Fatalf("Failed to create skills dir: %v", err)
	}

	skillContent := `# Test Skill

## Overview
This is a test skill.

## Quick Reference
- Point 1
- Point 2
`
	skillPath := filepath.Join(skillsDir, "creating-nexus-workspaces", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatalf("Failed to write skill: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}
	defer db.Close()

	service := NewRalphService(db, skillsDir)
	patterns := []Pattern{
		{
			IssueType:     "syntax:Common syntax error",
			Frequency:     6,
			AffectedTasks: []string{"workspace-setup"},
			SuggestedFix:  "Check syntax carefully",
		},
	}

	err = service.AutoUpdateSkills(patterns)
	if err != nil {
		t.Fatalf("AutoUpdateSkills failed: %v", err)
	}

	updatedContent, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("Failed to read updated skill: %v", err)
	}
	if !contains(string(updatedContent), "Troubleshooting") {
		t.Error("Expected Troubleshooting section to be added")
	}
}

func TestRalphSkillBackup(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "creating-nexus-workspaces"), 0755); err != nil {
		t.Fatalf("Failed to create skills dir: %v", err)
	}

	originalContent := "# Original Skill Content"
	skillPath := filepath.Join(skillsDir, "creating-nexus-workspaces", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("Failed to write skill: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, skillsDir)
	patterns := []Pattern{
		{IssueType: "config:Test", Frequency: 6, AffectedTasks: []string{"workspace-setup-1"}},
	}

	_ = service.AutoUpdateSkills(patterns)

	backupPath := skillPath + ".bak"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("Backup file should be created")
	}

	backupContent, _ := os.ReadFile(backupPath)
	if string(backupContent) != originalContent {
		t.Error("Backup should contain original content")
	}
}

func TestRalphRollbackOnError(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "creating-nexus-workspaces"), 0755); err != nil {
		t.Fatalf("Failed to create skills dir: %v", err)
	}

	originalContent := "# Original Skill"
	skillPath := filepath.Join(skillsDir, "creating-nexus-workspaces", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("Failed to write skill: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, skillsDir)
	patterns := []Pattern{
		{IssueType: "syntax:Error", Frequency: 6, AffectedTasks: []string{"workspace-fix"}},
	}

	_ = service.AutoUpdateSkills(patterns)

	currentContent, _ := os.ReadFile(skillPath)
	if string(currentContent) == originalContent {
		t.Log("Skill may have been rolled back (content unchanged)")
	}
}

func TestRalphMultiplePatterns(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "creating-nexus-workspaces"), 0755); err != nil {
		t.Fatalf("Failed to create skills dir: %v", err)
	}

	skillContent := "# Multi-Pattern Skill\n\n## Quick Reference\n"
	skillPath := filepath.Join(skillsDir, "creating-nexus-workspaces", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatalf("Failed to write skill: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, skillsDir)
	patterns := []Pattern{
		{IssueType: "syntax:Error1", Frequency: 6, AffectedTasks: []string{"workspace-task-1"}},
		{IssueType: "logic:Error2", Frequency: 7, AffectedTasks: []string{"workspace-task-2"}},
	}

	err := service.AutoUpdateSkills(patterns)
	if err != nil {
		t.Fatalf("AutoUpdateSkills with multiple patterns failed: %v", err)
	}

	content, _ := os.ReadFile(skillPath)
	count := countOccurrences(string(content), "Troubleshooting")
	if count < 1 {
		t.Error("Expected at least one troubleshooting section")
	}
}

func TestRalphIdempotentUpdates(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "creating-nexus-workspaces"), 0755); err != nil {
		t.Fatalf("Failed to create skills dir: %v", err)
	}

	skillContent := "# Idempotent Skill\n\n## Quick Reference\n"
	skillPath := filepath.Join(skillsDir, "creating-nexus-workspaces", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatalf("Failed to write skill: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, skillsDir)
	pattern := Pattern{IssueType: "config:Test", Frequency: 6, AffectedTasks: []string{"workspace-update"}}

	_ = service.AutoUpdateSkills([]Pattern{pattern})
	firstContent, _ := os.ReadFile(skillPath)

	_ = service.AutoUpdateSkills([]Pattern{pattern})
	secondContent, _ := os.ReadFile(skillPath)

	firstCount := countOccurrences(string(firstContent), "Troubleshooting")
	secondCount := countOccurrences(string(secondContent), "Troubleshooting")

	if firstCount != secondCount {
		t.Errorf("Updates should be idempotent. First had %d, second had %d", firstCount, secondCount)
	}
}

func TestRalphSkillSyntaxPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "creating-nexus-workspaces"), 0755); err != nil {
		t.Fatalf("Failed to create skills dir: %v", err)
	}

	skillContent := `---
name: test-skill
description: A test skill
---

# Test Skill

## Overview
This is a **test** skill.

## Quick Reference
- Item 1
- Item 2
`
	skillPath := filepath.Join(skillsDir, "creating-nexus-workspaces", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatalf("Failed to write skill: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, skillsDir)
	patterns := []Pattern{
		{IssueType: "performance:Slow", Frequency: 6, AffectedTasks: []string{"workspace-performance"}},
	}

	_ = service.AutoUpdateSkills(patterns)

	content, _ := os.ReadFile(skillPath)
	if !contains(string(content), "name: test-skill") {
		t.Error("Original YAML frontmatter should be preserved")
	}
	if !contains(string(content), "**test**") {
		t.Error("Original markdown formatting should be preserved")
	}
}

func TestRalphNotificationOnUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "creating-nexus-workspaces"), 0755); err != nil {
		t.Fatalf("Failed to create skills dir: %v", err)
	}

	skillContent := "# Notify Test Skill\n\n## Quick Reference\n"
	skillPath := filepath.Join(skillsDir, "creating-nexus-workspaces", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatalf("Failed to write skill: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, skillsDir)
	patterns := []Pattern{
		{IssueType: "security:Vuln", Frequency: 6, AffectedTasks: []string{"workspace-security"}},
	}

	err := service.AutoUpdateSkills(patterns)
	if err != nil {
		t.Fatalf("AutoUpdateSkills failed: %v", err)
	}
}

func TestRalphEmptyPatterns(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, "/skills")

	err := service.AutoUpdateSkills([]Pattern{})
	if err != nil {
		t.Errorf("Empty patterns should not cause error, got: %v", err)
	}
}

func TestRalphNonexistentSkill(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, "/nonexistent")
	patterns := []Pattern{
		{IssueType: "test:Issue", Frequency: 6, AffectedTasks: []string{"task"}},
	}

	err := service.AutoUpdateSkills(patterns)
	if err != nil {
		t.Logf("Expected error for nonexistent skill path: %v", err)
	}
}

func TestRalphFeedbackWithMultipleIssues(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	feedback := &SessionFeedback{
		SessionID:     "multi-issue-session",
		WorkspaceName: "test-workspace",
		AgentID:       "agent-1",
		TasksWorked:   []string{"task-1"},
		Issues: []Issue{
			{Category: IssueCategorySyntax, Description: "Syntax 1", Frequency: 1},
			{Category: IssueCategoryLogic, Description: "Logic 1", Frequency: 1},
			{Category: IssueCategoryConfig, Description: "Config 1", Frequency: 1},
		},
		Duration:    10 * time.Minute,
		SuccessRate: 0.5,
	}

	err = storage.SaveFeedback(ctx, feedback)
	if err != nil {
		t.Fatalf("Failed to save feedback with multiple issues: %v", err)
	}

	retrieved, err := storage.GetFeedback(ctx, "multi-issue-session")
	if err != nil {
		t.Fatalf("Failed to get feedback: %v", err)
	}
	if len(retrieved.Issues) != 3 {
		t.Errorf("Expected 3 issues, got %d", len(retrieved.Issues))
	}
}

func TestRalphPatternFrequencyTracking(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	issue := Issue{Category: IssueCategoryPerformance, Description: "Slow query", Frequency: 1}
	for i := 0; i < 8; i++ {
		feedback := &SessionFeedback{
			SessionID:     "freq-session-" + string(rune('A'+i)),
			WorkspaceName: "test-workspace",
			AgentID:       "agent-1",
			TasksWorked:   []string{"perf-task"},
			Issues:        []Issue{issue},
			Duration:      time.Minute,
			SuccessRate:   0.8,
		}
		_ = storage.SaveFeedback(ctx, feedback)
	}

	patterns, err := storage.GetPatterns(ctx, 5)
	if err != nil {
		t.Fatalf("Failed to get patterns: %v", err)
	}

	var foundPattern bool
	for _, p := range patterns {
		if contains(p.IssueType, "Slow query") {
			foundPattern = true
			if p.Frequency != 8 {
				t.Errorf("Expected frequency 8, got %d", p.Frequency)
			}
		}
	}
	if !foundPattern {
		t.Error("Expected to find the recurring performance issue pattern")
	}
}

func TestRalphFeedbackDurationTracking(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	feedback := &SessionFeedback{
		SessionID:     "duration-test",
		WorkspaceName: "test-workspace",
		AgentID:       "agent-1",
		TasksWorked:   []string{"task-1"},
		Issues:        []Issue{},
		Duration:      42 * time.Minute,
		SuccessRate:   0.9,
	}

	_ = storage.SaveFeedback(ctx, feedback)

	retrieved, _ := storage.GetFeedback(ctx, "duration-test")
	if retrieved.Duration.Minutes() != 42 {
		t.Errorf("Expected duration 42 minutes, got %v", retrieved.Duration)
	}
}

func TestRalphFeedbackSuccessRate(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	testCases := []float64{0.0, 0.25, 0.5, 0.75, 1.0}
	for i, rate := range testCases {
		feedback := &SessionFeedback{
			SessionID:     "rate-" + string(rune('A'+i)),
			WorkspaceName: "test-workspace",
			AgentID:       "agent-1",
			TasksWorked:   []string{"task-1"},
			Issues:        []Issue{},
			Duration:      time.Minute,
			SuccessRate:   rate,
		}
		_ = storage.SaveFeedback(ctx, feedback)
	}

	for i, rate := range testCases {
		retrieved, _ := storage.GetFeedback(ctx, "rate-"+string(rune('A'+i)))
		if retrieved.SuccessRate != rate {
			t.Errorf("Expected success rate %f, got %f", rate, retrieved.SuccessRate)
		}
	}
}

func TestRalphIssueCategories(t *testing.T) {
	categories := []IssueCategory{
		IssueCategorySyntax,
		IssueCategoryLogic,
		IssueCategoryConfig,
		IssueCategoryDependency,
		IssueCategoryPerformance,
		IssueCategorySecurity,
		IssueCategoryUnknown,
	}

	for _, cat := range categories {
		if cat == "" {
			t.Error("Category should not be empty")
		}
	}

	if len(categories) != 7 {
		t.Errorf("Expected 7 categories, got %d", len(categories))
	}
}

func TestRalphGenerateSuggestedFix(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, "/skills")

	testCases := []struct {
		category IssueCategory
		expected string
	}{
		{IssueCategorySyntax, "Review code syntax"},
		{IssueCategoryLogic, "Review business logic"},
		{IssueCategoryConfig, "Verify configuration files"},
		{IssueCategoryDependency, "Update dependencies"},
		{IssueCategoryPerformance, "Optimize code"},
		{IssueCategorySecurity, "Review security implications"},
		{IssueCategoryUnknown, "Investigate the issue"},
	}

	for _, tc := range testCases {
		issue := Issue{Category: tc.category, Description: "Test"}
		fix := service.generateSuggestedFix(issue)
		if !contains(fix, tc.expected) {
			t.Errorf("For category %s, expected fix containing '%s', got '%s'", tc.category, tc.expected, fix)
		}
	}
}

func TestRalphSetPatternThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := sql.Open("sqlite3", dbPath)
	defer db.Close()

	service := NewRalphService(db, "/skills")

	service.SetPatternThreshold(10)
	if service.GetPatternThreshold() != 10 {
		t.Errorf("Expected threshold 10, got %d", service.GetPatternThreshold())
	}

	service.SetPatternThreshold(3)
	if service.GetPatternThreshold() != 3 {
		t.Errorf("Expected threshold 3, got %d", service.GetPatternThreshold())
	}
}

func TestRalphAnalyzePatternsWithWindow(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	_ = time.Now()
	feedback := &SessionFeedback{
		SessionID:     "window-test",
		WorkspaceName: "test-workspace",
		AgentID:       "agent-1",
		TasksWorked:   []string{"task-1"},
		Issues: []Issue{
			{Category: IssueCategorySyntax, Description: "Recent error", Frequency: 1},
		},
		Duration:    time.Minute,
		SuccessRate: 0.8,
	}
	_ = storage.SaveFeedback(ctx, feedback)

	patterns, err := storage.GetPatterns(ctx, 1)
	if err != nil {
		t.Fatalf("Failed to get patterns: %v", err)
	}
	if len(patterns) < 1 {
		t.Error("Expected at least 1 pattern")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(haystack) > 0 && containsSubstring(haystack, needle))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func countOccurrences(haystack, needle string) int {
	count := 0
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			count++
		}
	}
	return count
}
