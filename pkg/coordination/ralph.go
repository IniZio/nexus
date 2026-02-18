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
)

type IssueCategory string

const (
	IssueCategorySyntax      IssueCategory = "syntax"
	IssueCategoryLogic       IssueCategory = "logic"
	IssueCategoryConfig      IssueCategory = "config"
	IssueCategoryDependency  IssueCategory = "dependency"
	IssueCategoryPerformance IssueCategory = "performance"
	IssueCategorySecurity    IssueCategory = "security"
	IssueCategoryUnknown     IssueCategory = "unknown"
)

type Issue struct {
	Category    IssueCategory `json:"category"`
	Description string        `json:"description"`
	Context     string        `json:"context"`
	Frequency   int           `json:"frequency"`
}

type SessionFeedback struct {
	ID            string        `json:"id"`
	SessionID     string        `json:"session_id"`
	WorkspaceName string        `json:"workspace_name"`
	AgentID       string        `json:"agent_id"`
	TasksWorked   []string      `json:"tasks_worked"`
	Issues        []Issue       `json:"issues"`
	Duration      time.Duration `json:"duration"`
	SuccessRate   float64       `json:"success_rate"`
	Timestamp     time.Time     `json:"timestamp"`
}

type Pattern struct {
	IssueType     string    `json:"issue_type"`
	Frequency     int       `json:"frequency"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	AffectedTasks []string  `json:"affected_tasks"`
	SuggestedFix  string    `json:"suggested_fix"`
}

type RalphService struct {
	storage          Storage
	skillsPath       string
	db               *sql.DB
	patternThreshold int
}

func NewRalphService(db *sql.DB, skillsPath string) *RalphService {
	return &RalphService{
		storage:          nil,
		skillsPath:       skillsPath,
		db:               db,
		patternThreshold: 5,
	}
}

func (s *RalphService) CollectFeedback(feedback SessionFeedback) error {
	ctx := context.Background()
	feedback.Timestamp = time.Now()

	issuesJSON, err := json.Marshal(feedback.Issues)
	if err != nil {
		return fmt.Errorf("failed to marshal issues: %w", err)
	}

	tasksJSON, err := json.Marshal(feedback.TasksWorked)
	if err != nil {
		return fmt.Errorf("failed to marshal tasks: %w", err)
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
	if err != nil {
		return fmt.Errorf("failed to insert feedback: %w", err)
	}

	return nil
}

func (s *RalphService) AnalyzePatterns(window time.Duration) ([]Pattern, error) {
	ctx := context.Background()
	cutoff := time.Now().Add(-window)

	query := `
	SELECT session_id, agent_id, tasks_worked, issues, duration, success_rate, timestamp
	FROM feedback
	WHERE timestamp > ?
	ORDER BY timestamp DESC
	`
	rows, err := s.db.QueryContext(ctx, query, cutoff.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("failed to query feedback: %w", err)
	}
	defer rows.Close()

	issueCounts := make(map[string]*Pattern)

	for rows.Next() {
		var sessionID, agentID, tasksJSON, issuesJSON string
		var durationSecs, successRate float64
		var timestampStr string

		err := rows.Scan(&sessionID, &agentID, &tasksJSON, &issuesJSON, &durationSecs, &successRate, &timestampStr)
		if err != nil {
			return nil, fmt.Errorf("failed to scan feedback row: %w", err)
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
					SuggestedFix:  s.generateSuggestedFix(issue),
				}
			}
		}
	}

	var patterns []Pattern
	for _, pattern := range issueCounts {
		if pattern.Frequency >= s.patternThreshold {
			patterns = append(patterns, *pattern)
		}
	}

	return patterns, nil
}

func (s *RalphService) AutoUpdateSkills(patterns []Pattern) error {
	if len(patterns) == 0 {
		return nil
	}

	var updatedSkills []string
	var updateErrors []error

	for _, pattern := range patterns {
		skillName := s.findAffectedSkill(pattern.AffectedTasks)
		if skillName == "" {
			continue
		}

		skillPath := filepath.Join(s.skillsPath, skillName, "SKILL.md")
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			continue
		}

		backupPath := skillPath + ".bak"
		if err := s.backupSkill(skillPath, backupPath); err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("failed to backup %s: %w", skillName, err))
			continue
		}

		if err := s.updateSkill(skillPath, pattern); err != nil {
			if rbErr := s.rollbackSkill(skillPath, backupPath); rbErr != nil {
				updateErrors = append(updateErrors, fmt.Errorf("failed to rollback %s: %w", skillName, rbErr))
			}
			updateErrors = append(updateErrors, fmt.Errorf("failed to update %s: %w", skillName, err))
			continue
		}

		updatedSkills = append(updatedSkills, skillName)
		s.logSkillUpdate(skillName, pattern)
	}

	if len(updateErrors) > 0 {
		return fmt.Errorf("update errors: %v", updateErrors)
	}

	return nil
}

func (s *RalphService) findAffectedSkill(tasks []string) string {
	knownSkills := map[string][]string{
		"creating-nexus-workspaces": {"workspace", "container", "nexus"},
	}

	for _, task := range tasks {
		taskLower := strings.ToLower(task)
		for skillName, keywords := range knownSkills {
			for _, keyword := range keywords {
				if strings.Contains(taskLower, strings.ToLower(keyword)) {
					return skillName
				}
			}
		}
	}

	if len(tasks) > 0 {
		taskBase := filepath.Base(tasks[0])
		if strings.Contains(strings.ToLower(taskBase), "workspace") {
			return "creating-nexus-workspaces"
		}
	}

	return ""
}

func (s *RalphService) backupSkill(skillPath, backupPath string) error {
	content, err := os.ReadFile(skillPath)
	if err != nil {
		return err
	}
	return os.WriteFile(backupPath, content, 0644)
}

func (s *RalphService) rollbackSkill(skillPath, backupPath string) error {
	content, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	return os.WriteFile(skillPath, content, 0644)
}

func (s *RalphService) updateSkill(skillPath string, pattern Pattern) error {
	content, err := os.ReadFile(skillPath)
	if err != nil {
		return err
	}

	if strings.Contains(string(content), fmt.Sprintf("### %s", pattern.IssueType)) {
		return nil
	}

	troubleshootingSection := fmt.Sprintf(`

## Troubleshooting

### %s
- **Frequency:** %d occurrences detected
- **Suggested Fix:** %s
- **Affected Tasks:** %s

`,
		pattern.IssueType,
		pattern.Frequency,
		pattern.SuggestedFix,
		strings.Join(pattern.AffectedTasks, ", "),
	)

	lines := strings.Split(string(content), "\n")
	insertIndex := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "## Quick Reference") || strings.HasPrefix(line, "## Common Mistakes") {
			insertIndex = i
			break
		}
	}

	lines = append(lines[:insertIndex], append([]string{troubleshootingSection}, lines[insertIndex:]...)...)
	updatedContent := strings.Join(lines, "\n")

	return os.WriteFile(skillPath, []byte(updatedContent), 0644)
}

func (s *RalphService) generateSuggestedFix(issue Issue) string {
	switch issue.Category {
	case IssueCategorySyntax:
		return "Review code syntax and ensure proper formatting. Check for missing semicolons, brackets, or parentheses."
	case IssueCategoryLogic:
		return "Review business logic and edge cases. Add validation and error handling."
	case IssueCategoryConfig:
		return "Verify configuration files and environment variables are correctly set."
	case IssueCategoryDependency:
		return "Update dependencies and ensure compatibility between packages."
	case IssueCategoryPerformance:
		return "Optimize code for better performance. Consider caching, batching, or algorithmic improvements."
	case IssueCategorySecurity:
		return "Review security implications. Sanitize inputs and follow security best practices."
	default:
		return "Investigate the issue further and document findings."
	}
}

func (s *RalphService) logSkillUpdate(skillName string, pattern Pattern) {
	fmt.Printf("[RALPH] Skill updated: %s - Pattern: %s (freq: %d)\n", skillName, pattern.IssueType, pattern.Frequency)
}

func (s *RalphService) SetPatternThreshold(threshold int) {
	s.patternThreshold = threshold
}

func (s *RalphService) GetPatternThreshold() int {
	return s.patternThreshold
}
