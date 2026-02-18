package telemetry

import (
	"database/sql"
	"fmt"
	"time"
)

// Analyzer analyzes telemetry data
type Analyzer struct {
	db *TelemetryDB
}

// NewAnalyzer creates a new analyzer
func NewAnalyzer(db *TelemetryDB) *Analyzer {
	return &Analyzer{db: db}
}

// GetStats returns usage statistics
func (a *Analyzer) GetStats(days int) (Stats, error) {
	return a.db.GetStats(days)
}

// DetectPatterns finds usage patterns
func (a *Analyzer) DetectPatterns() ([]Pattern, error) {
	since := time.Now().AddDate(0, 0, -30)

	var patterns []Pattern

	slowPatterns, err := a.detectSlowCommands(since)
	if err != nil {
		return nil, err
	}
	patterns = append(patterns, slowPatterns...)

	errorPatterns, err := a.detectRecurringErrors(since)
	if err != nil {
		return nil, err
	}
	patterns = append(patterns, errorPatterns...)

	portPatterns, err := a.detectPortConflicts(since)
	if err != nil {
		return nil, err
	}
	patterns = append(patterns, portPatterns...)

	return patterns, nil
}

func (a *Analyzer) detectSlowCommands(since time.Time) ([]Pattern, error) {
	query := `
		SELECT command, COUNT(*) as count, AVG(duration_ms) as avg_dur, MAX(duration_ms) as max_dur
		FROM events WHERE event_type = 'command' AND command != '' AND duration_ms > 5000 AND timestamp >= ?
		GROUP BY command ORDER BY avg_dur DESC
	`

	rows, err := a.db.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var patterns []Pattern
	for rows.Next() {
		var p Pattern
		var count int
		var avgDur, maxDur sql.NullInt64

		err := rows.Scan(&p.PatternType, &count, &avgDur, &maxDur)
		if err != nil {
			return nil, err
		}

		p.Frequency = count
		var avgDurVal int64
		if avgDur.Valid {
			avgDurVal = avgDur.Int64
		}
		p.Description = avgDurationDescription(avgDurVal)
		p.FirstSeen = since
		p.LastSeen = time.Now()
		p.AffectedSessions = []string{}
		p.SuggestedFix = slowCommandFix(p.PatternType)

		patterns = append(patterns, p)
	}

	return patterns, rows.Err()
}

func (a *Analyzer) detectRecurringErrors(since time.Time) ([]Pattern, error) {
	query := `
		SELECT error_type, COUNT(*) as count, MIN(timestamp) as first_seen, MAX(timestamp) as last_seen
		FROM events WHERE event_type = 'command' AND success = 0 AND error_type != '' AND timestamp >= ?
		GROUP BY error_type HAVING count >= 3 ORDER BY count DESC
	`

	rows, err := a.db.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var patterns []Pattern
	for rows.Next() {
		var p Pattern
		var firstSeen, lastSeen sql.NullTime

		err := rows.Scan(&p.PatternType, &p.Frequency, &firstSeen, &lastSeen)
		if err != nil {
			return nil, err
		}

		p.Description = errorDescription(p.PatternType)
		if firstSeen.Valid {
			p.FirstSeen = firstSeen.Time
		}
		if lastSeen.Valid {
			p.LastSeen = lastSeen.Time
		}
		p.AffectedSessions = []string{}
		p.SuggestedFix = errorFix(p.PatternType)

		patterns = append(patterns, p)
	}

	return patterns, rows.Err()
}

func (a *Analyzer) detectPortConflicts(since time.Time) ([]Pattern, error) {
	query := `
		SELECT COUNT(*) as count, MIN(timestamp) as first_seen, MAX(timestamp) as last_seen
		FROM events WHERE event_type = 'command' AND error_type = 'port_conflict' AND timestamp >= ?
	`

	var p Pattern
	var count int
	var firstSeen, lastSeen sql.NullTime

	err := a.db.db.QueryRow(query, since).Scan(&count, &firstSeen, &lastSeen)
	if err != nil {
		return nil, err
	}

	if count >= 3 {
		p.PatternType = "port_conflict"
		p.Description = "Port conflicts occur frequently when starting workspaces"
		p.Frequency = count
		if firstSeen.Valid {
			p.FirstSeen = firstSeen.Time
		}
		if lastSeen.Valid {
			p.LastSeen = lastSeen.Time
		}
		p.SuggestedFix = "Consider using a port range configuration or implement automatic port selection"

		return []Pattern{p}, nil
	}

	return nil, nil
}

// GenerateInsights generates actionable insights
func (a *Analyzer) GenerateInsights() []Insight {
	stats, err := a.db.GetStats(30)
	if err != nil {
		return nil
	}

	var insights []Insight

	if stats.SuccessRate < 80 {
		insights = append(insights, Insight{
			Type:        "usability",
			Title:       "Low Command Success Rate",
			Description: "Only %.1f%% of commands are succeeding. Check common errors for issues.",
			Severity:    "high",
		})
	}

	for _, cmd := range stats.TopCommands {
		if cmd.AvgDuration > 10000 {
			insights = append(insights, Insight{
				Type:        "performance",
				Title:       "Slow Command Detected",
				Description: "'%s' averages %.1fs per execution. Consider optimizing.",
				Severity:    "medium",
			})
			break
		}
	}

	for _, err := range stats.CommonErrors {
		if err.Count > 10 {
			insights = append(insights, Insight{
				Type:        "error",
				Title:       "Frequent Error: " + err.ErrorType,
				Description: "Encountered %d times. See documentation for troubleshooting.",
				Severity:    err.severityFromCount(),
			})
		}
	}

	if stats.WorkspaceStats.TotalCreated > 0 {
		ratio := float64(stats.WorkspaceStats.TotalDestroyed) / float64(stats.WorkspaceStats.TotalCreated)
		if ratio < 0.5 {
			insights = append(insights, Insight{
				Type:        "usability",
				Title:       "Low Workspace Cleanup Rate",
				Description: "Only %.1f%% of created workspaces are destroyed. Consider cleanup reminders.",
				Severity:    "low",
			})
		}
	}

	return insights
}

func avgDurationDescription(ms int64) string {
	if ms > 60000 {
		return "Command frequently takes over 1 minute"
	} else if ms > 30000 {
		return "Command frequently takes 30+ seconds"
	} else if ms > 10000 {
		return "Command frequently takes 10+ seconds"
	}
	return "Command is slower than average"
}

func slowCommandFix(cmd string) string {
	fixes := map[string]string{
		"build":   "Consider using incremental builds or caching Docker layers",
		"start":   "Check if all services are healthy before starting",
		"up":      "Use 'docker compose up -d' for detached mode",
		"create":  "Pre-pull base images to speed up creation",
		"destroy": "This is typically fast; check for hanging processes",
		"logs":    "Use '--tail' flag to limit log output",
		"exec":    "Use '--' to pass commands directly to avoid overhead",
	}

	if fix, ok := fixes[cmd]; ok {
		return fix
	}

	return "Review command execution for optimization opportunities"
}

func errorDescription(errType string) string {
	descriptions := map[string]string{
		"port_conflict":    "Port conflicts prevent workspace startup",
		"docker_error":     "Docker operations are failing",
		"ssh_error":        "SSH connections are timing out",
		"git_error":        "Git operations are encountering issues",
		"permission_error": "Permission denied for required operations",
		"timeout_error":    "Operations are timing out",
		"network_error":    "Network connectivity issues detected",
		"file_not_found":   "Required files are missing",
		"template_error":   "Template rendering failed",
	}

	if desc, ok := descriptions[errType]; ok {
		return desc
	}

	return "Unknown error type occurring repeatedly"
}

func errorFix(errType string) string {
	fixes := map[string]string{
		"port_conflict":    "Configure auto-port selection or free conflicting ports before starting",
		"docker_error":     "Check Docker daemon is running: 'docker ps'",
		"ssh_error":        "Verify SSH keys are loaded: 'ssh-add -l'",
		"git_error":        "Check repository permissions and remote URLs",
		"permission_error": "Run with appropriate permissions or check file ownership",
		"timeout_error":    "Increase timeout settings or check network connectivity",
		"network_error":    "Check internet connection and proxy settings",
		"file_not_found":   "Verify file paths and working directory",
		"template_error":   "Check template syntax and required variables",
	}

	if fix, ok := fixes[errType]; ok {
		return fix
	}

	return "Review error logs for specific error messages"
}

func (e *ErrorStat) severityFromCount() string {
	if e.Count > 50 {
		return "high"
	} else if e.Count > 20 {
		return "medium"
	}
	return "low"
}

// FormatInsight formats an insight for display
func FormatInsight(insight Insight) string {
	return fmt.Sprintf("[%s] %s: %s", insight.Severity, insight.Title, insight.Description)
}

// GetSummary returns a text summary of statistics
func GetSummary(stats Stats) string {
	summary := fmt.Sprintf(`
Telemetry Summary (Last 30 days):
- Total Commands: %d
- Success Rate: %.1f%%
- Average Command Duration: %s

Top Commands:
`, stats.TotalCommands, stats.SuccessRate, stats.AvgCommandDuration)

	for i, cmd := range stats.TopCommands {
		if i >= 5 {
			break
		}
		summary += fmt.Sprintf("  %d. %s: %d runs (%.1f%% success, avg %.1fs)\n",
			i+1, cmd.Command, cmd.Count, cmd.SuccessRate, float64(cmd.AvgDuration)/1000)
	}

	if len(stats.CommonErrors) > 0 {
		summary += "\nCommon Errors:\n"
		for i, err := range stats.CommonErrors {
			if i >= 5 {
				break
			}
			summary += fmt.Sprintf("  %d. %s: %d occurrences\n", i+1, err.ErrorType, err.Count)
		}
	}

	return summary
}
