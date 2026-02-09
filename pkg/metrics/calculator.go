package metrics

import (
	"fmt"
	"math"
	"time"
)

type Calculator struct{}

func NewCalculator() *Calculator {
	return &Calculator{}
}

func (c *Calculator) Calculate(logs []UsageLog) (*ProductivityMetrics, error) {
	if len(logs) == 0 {
		return &ProductivityMetrics{}, nil
	}

	var totalDuration int64
	var successCount int
	var totalTokensUsed int64
	var totalCost float64

	skillCount := int64(0)
	commandCount := int64(0)
	ruleCount := int64(0)

	for _, log := range logs {
		totalDuration += log.Outcome.Duration

		if log.Outcome.Success {
			successCount++
		}

		totalTokensUsed += log.Outcome.TokensUsed
		totalCost += log.Outcome.Cost

		switch log.Invocation.Type {
		case string(InvocationSkill):
			skillCount++
		case string(InvocationCommand):
			commandCount++
		case string(InvocationRule):
			ruleCount++
		}
	}

	averageDuration := float64(totalDuration) / float64(len(logs))
	successRate := float64(successCount) / float64(len(logs)) * 100
	failureRate := 100 - successRate

	invocationsPerHour := c.calculateInvocationsPerHour(logs)
	tokensPerTask := float64(totalTokensUsed) / float64(len(logs))

	return &ProductivityMetrics{
		TotalInvocations:   int64(len(logs)),
		SkillInvocations:   skillCount,
		CommandInvocations: commandCount,
		RuleApplications:   ruleCount,
		AverageDuration:    averageDuration,
		TotalDuration:      totalDuration,
		SuccessRate:        successRate,
		FailureRate:        failureRate,
		TotalTokensUsed:    totalTokensUsed,
		TotalCost:          totalCost,
		InvocationsPerHour: invocationsPerHour,
		TokensPerTask:      tokensPerTask,
	}, nil
}

func (c *Calculator) calculateInvocationsPerHour(logs []UsageLog) float64 {
	timeRange := c.getTimeRange(logs)
	duration := timeRange.End.Sub(timeRange.Start)

	if duration <= 0 {
		return 0
	}

	hours := duration.Hours()
	if hours == 0 {
		return float64(len(logs))
	}

	return float64(len(logs)) / hours
}

func (c *Calculator) getTimeRange(logs []UsageLog) struct{ Start, End time.Time } {
	if len(logs) == 0 {
		return struct{ Start, End time.Time }{}
	}

	var earliest, latest time.Time

	for _, log := range logs {
		timestamp, err := time.Parse(time.RFC3339, log.Timestamp)
		if err != nil {
			continue
		}

		if earliest.IsZero() || timestamp.Before(earliest) {
			earliest = timestamp
		}

		if latest.IsZero() || timestamp.After(latest) {
			latest = timestamp
		}
	}

	return struct{ Start, End time.Time }{Start: earliest, End: latest}
}

type BenchmarkCalculator struct {
	calculator *Calculator
}

func NewBenchmarkCalculator() *BenchmarkCalculator {
	return &BenchmarkCalculator{
		calculator: NewCalculator(),
	}
}

func (b *BenchmarkCalculator) Compare(baseline []UsageLog, current []UsageLog) (*BenchmarkComparison, error) {
	baselineMetrics, err := b.calculator.Calculate(baseline)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate baseline metrics: %w", err)
	}

	currentMetrics, err := b.calculator.Calculate(current)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate current metrics: %w", err)
	}

	return &BenchmarkComparison{
		Baseline: *baselineMetrics,
		Current:  *currentMetrics,
		Improvements: ImprovementMetrics{
			Duration:      baselineMetrics.AverageDuration - currentMetrics.AverageDuration,
			SuccessRate:   currentMetrics.SuccessRate - baselineMetrics.SuccessRate,
			TokensPerTask: baselineMetrics.TokensPerTask - currentMetrics.TokensPerTask,
		},
		PercentImprovement: b.calculatePercentImprovement(baselineMetrics, currentMetrics),
	}, nil
}

func (b *BenchmarkCalculator) calculatePercentImprovement(baseline, current *ProductivityMetrics) PercentImprovement {
	durationImprovement := 0.0
	if baseline.AverageDuration > 0 {
		durationImprovement = ((baseline.AverageDuration - current.AverageDuration) / baseline.AverageDuration) * 100
	}

	successRateImprovement := current.SuccessRate - baseline.SuccessRate

	tokensImprovement := 0.0
	if baseline.TokensPerTask > 0 {
		tokensImprovement = ((baseline.TokensPerTask - current.TokensPerTask) / baseline.TokensPerTask) * 100
	}

	return PercentImprovement{
		Duration:      math.Round(durationImprovement*100) / 100,
		SuccessRate:   math.Round(successRateImprovement*100) / 100,
		TokensPerTask: math.Round(tokensImprovement*100) / 100,
	}
}
