package telemetry

import (
	"time"
)

type InsightsAnalyzer struct {
	db *TelemetryDB
}

func NewInsightsAnalyzer(db *TelemetryDB) *InsightsAnalyzer {
	return &InsightsAnalyzer{db: db}
}

func (a *InsightsAnalyzer) GenerateInsights() ([]Insight, error) {
	insights := []Insight{}

	stats, err := a.db.GetStats(7)
	if err != nil {
		return nil, err
	}

	if stats.SuccessRate < 80 {
		insights = append(insights, Insight{
			Type:        "success_rate",
			Title:       "Low Success Rate",
			Description: "Your command success rate is below 80%. Check for common errors below.",
			Severity:    "high",
		})
	}

	if stats.AvgCommandDuration > 30*time.Second {
		insights = append(insights, Insight{
			Type:        "performance",
			Title:       "Slow Commands",
			Description: "Average command duration is over 30 seconds. Consider using simpler commands.",
			Severity:    "medium",
		})
	}

	if len(stats.CommonErrors) > 0 {
		insights = append(insights, Insight{
			Type:        "errors",
			Title:       "Frequent Errors Detected",
			Description: "Multiple error patterns detected. Review common errors for solutions.",
			Severity:    "medium",
		})
	}

	if stats.TaskStats.CompletionRate < 50 && stats.TaskStats.TotalCreated > 5 {
		insights = append(insights, Insight{
			Type:        "tasks",
			Title:       "Low Task Completion",
			Description: "Less than 50% of tasks are being completed. Consider breaking tasks into smaller steps.",
			Severity:    "high",
		})
	}

	if stats.SuccessRate >= 80 && stats.SuccessRate < 90 {
		insights = append(insights, Insight{
			Type:        "improvement",
			Title:       "Good Progress",
			Description: "80-90% success rate. Review common errors to improve further.",
			Severity:    "low",
		})
	}

	if stats.SuccessRate >= 90 && len(stats.CommonErrors) == 0 {
		insights = append(insights, Insight{
			Type:        "success",
			Title:       "Excellent Performance",
			Description: "Over 90% success rate with no errors. Keep up the great work!",
			Severity:    "low",
		})
	}

	return insights, nil
}
