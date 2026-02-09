package metrics

import (
	"fmt"
	"sort"
	"time"
)

type Reporter struct {
	calculator *Calculator
	analyzer   *Analyzer
	benchmark  *BenchmarkCalculator
}

func NewReporter() *Reporter {
	return &Reporter{
		calculator: NewCalculator(),
		analyzer:   NewAnalyzer(),
		benchmark:  NewBenchmarkCalculator(),
	}
}

func (r *Reporter) GenerateDailySummary(logger *Logger, date time.Time) (*DailySummary, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour).Add(-time.Nanosecond)

	logs, err := logger.Query(Filter{
		StartTime: startOfDay,
		EndTime:   endOfDay,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to query logs: %w", err)
	}

	metrics, err := r.calculator.Calculate(logs)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate metrics: %w", err)
	}

	topSkills := r.getTopSkills(logs, 5)

	insights := r.generateInsights(metrics, topSkills)

	return &DailySummary{
		Date:               startOfDay.Format("2006-01-02"),
		TotalInvocations:   metrics.TotalInvocations,
		SkillInvocations:   metrics.SkillInvocations,
		CommandInvocations: metrics.CommandInvocations,
		RuleApplications:   metrics.RuleApplications,
		AverageDuration:    metrics.AverageDuration,
		SuccessRate:        metrics.SuccessRate,
		TopSkills:          topSkills,
		Insights:           insights,
		Metrics:            *metrics,
	}, nil
}

func (r *Reporter) GenerateReport(logger *Logger, days int) (*ProductivityMetrics, *DailySummary, []UsagePattern, error) {
	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -days)

	logs, err := logger.Query(Filter{
		StartTime: startDate,
		EndTime:   endDate,
	})

	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to query logs: %w", err)
	}

	metrics, err := r.calculator.Calculate(logs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to calculate metrics: %w", err)
	}

	summary, err := r.GenerateDailySummary(logger, endDate)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate daily summary: %w", err)
	}

	patterns, err := r.analyzer.AnalyzePatterns(logs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to analyze patterns: %w", err)
	}

	return metrics, summary, patterns, nil
}

func (r *Reporter) GenerateBenchmark(logger *Logger, baselineDays, currentDays int) (*BenchmarkComparison, error) {
	now := time.Now()

	baselineStart := now.AddDate(0, 0, -baselineDays-currentDays)
	baselineEnd := now.AddDate(0, 0, -currentDays)

	baselineLogs, err := logger.Query(Filter{
		StartTime: baselineStart,
		EndTime:   baselineEnd,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to query baseline logs: %w", err)
	}

	currentLogs, err := logger.Query(Filter{
		StartTime: baselineEnd,
		EndTime:   now,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to query current logs: %w", err)
	}

	return r.benchmark.Compare(baselineLogs, currentLogs)
}

func (r *Reporter) getTopSkills(logs []UsageLog, limit int) []SkillCount {
	skillCounts := make(map[string]int)

	for _, log := range logs {
		if log.Invocation.Type == string(InvocationSkill) {
			skill := log.Invocation.Name
			skillCounts[skill]++
		}
	}

	var skills []SkillCount
	for skill, count := range skillCounts {
		skills = append(skills, SkillCount{Skill: skill, Count: count})
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Count > skills[j].Count
	})

	if len(skills) > limit {
		skills = skills[:limit]
	}

	return skills
}

func (r *Reporter) generateInsights(metrics *ProductivityMetrics, topSkills []SkillCount) []string {
	var insights []string

	if metrics.SuccessRate < 80 {
		insights = append(insights, fmt.Sprintf("⚠️  Low success rate: %.1f%%", metrics.SuccessRate))
	} else if metrics.SuccessRate >= 90 {
		insights = append(insights, fmt.Sprintf("✅ Excellent success rate: %.1f%%", metrics.SuccessRate))
	}

	if metrics.AverageDuration > 30000 {
		insights = append(insights, fmt.Sprintf("⚠️  High average duration: %.1fs", metrics.AverageDuration/1000))
	} else if metrics.AverageDuration < 5000 {
		insights = append(insights, fmt.Sprintf("⚡ Fast average duration: %.1fs", metrics.AverageDuration/1000))
	}

	if metrics.InvocationsPerHour < 5 {
		insights = append(insights, fmt.Sprintf("ℹ️  Low invocations per hour: %.1f", metrics.InvocationsPerHour))
	} else if metrics.InvocationsPerHour > 10 {
		insights = append(insights, fmt.Sprintf("🚀 High invocations per hour: %.1f", metrics.InvocationsPerHour))
	}

	if len(topSkills) > 0 {
		topSkill := topSkills[0]
		insights = append(insights, fmt.Sprintf("🏆 Most used skill: %s (%d times)", topSkill.Skill, topSkill.Count))
	}

	if metrics.TotalCost > 0 {
		insights = append(insights, fmt.Sprintf("💰 Total cost: $%.2f", metrics.TotalCost))
	}

	if metrics.TotalTokensUsed > 0 {
		tokensInThousands := metrics.TotalTokensUsed / 1000
		insights = append(insights, fmt.Sprintf("📊 Total tokens used: %dK", tokensInThousands))
	}

	return insights
}
