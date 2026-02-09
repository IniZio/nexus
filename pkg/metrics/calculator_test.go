package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculator_Calculate(t *testing.T) {
	calculator := NewCalculator()

	logs := []UsageLog{
		{
			Agent: "sisyphus",
			Invocation: Invocation{
				Type:     "skill",
				Name:     "analyze-codebase",
				Category: "code-analysis",
			},
			Timestamp: time.Now().Format(time.RFC3339),
			Outcome: Outcome{
				Success:    true,
				Duration:   1000,
				TokensUsed: 500,
				Cost:       0.001,
			},
		},
		{
			Agent: "sisyphus",
			Invocation: Invocation{
				Type:     "skill",
				Name:     "refactor-code",
				Category: "code-analysis",
			},
			Timestamp: time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			Outcome: Outcome{
				Success:    false,
				Duration:   2000,
				TokensUsed: 1000,
				Cost:       0.002,
			},
		},
	}

	metrics, err := calculator.Calculate(logs)
	require.NoError(t, err)
	assert.Equal(t, int64(2), metrics.TotalInvocations)
	assert.Equal(t, int64(2), metrics.SkillInvocations)
	assert.Equal(t, 1500.0, metrics.AverageDuration)
	assert.Equal(t, 50.0, metrics.SuccessRate)
	assert.Equal(t, 50.0, metrics.FailureRate)
	assert.Equal(t, int64(1500), metrics.TotalTokensUsed)
	assert.Equal(t, 0.003, metrics.TotalCost)
	assert.Equal(t, 750.0, metrics.TokensPerTask)
}

func TestCalculator_CalculateEmpty(t *testing.T) {
	calculator := NewCalculator()

	metrics, err := calculator.Calculate([]UsageLog{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), metrics.TotalInvocations)
	assert.Equal(t, 0.0, metrics.SuccessRate)
}

func TestCalculator_AllInvocationTypes(t *testing.T) {
	calculator := NewCalculator()

	logs := []UsageLog{
		{
			Invocation: Invocation{Type: "skill", Name: "test-skill"},
			Outcome:    Outcome{Success: true, Duration: 100},
		},
		{
			Invocation: Invocation{Type: "command", Name: "test-cmd"},
			Outcome:    Outcome{Success: true, Duration: 100},
		},
		{
			Invocation: Invocation{Type: "rule", Name: "test-rule"},
			Outcome:    Outcome{Success: true, Duration: 100},
		},
	}

	metrics, err := calculator.Calculate(logs)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metrics.SkillInvocations)
	assert.Equal(t, int64(1), metrics.CommandInvocations)
	assert.Equal(t, int64(1), metrics.RuleApplications)
}

func TestBenchmarkCalculator_Compare(t *testing.T) {
	calculator := NewBenchmarkCalculator()

	baseline := []UsageLog{
		{
			Invocation: Invocation{Type: "skill", Name: "test"},
			Outcome:    Outcome{Success: true, Duration: 10000, TokensUsed: 1000},
		},
	}

	current := []UsageLog{
		{
			Invocation: Invocation{Type: "skill", Name: "test"},
			Outcome:    Outcome{Success: true, Duration: 8000, TokensUsed: 800},
		},
	}

	comparison, err := calculator.Compare(baseline, current)
	require.NoError(t, err)
	assert.Equal(t, 10000.0, comparison.Baseline.AverageDuration)
	assert.Equal(t, 8000.0, comparison.Current.AverageDuration)
	assert.Equal(t, 2000.0, comparison.Improvements.Duration)
	assert.Equal(t, 20.0, comparison.PercentImprovement.Duration)
	assert.Equal(t, 200.0, comparison.Improvements.TokensPerTask)
	assert.Equal(t, 20.0, comparison.PercentImprovement.TokensPerTask)
}

func TestBenchmarkCalculator_EmptyLogs(t *testing.T) {
	calculator := NewBenchmarkCalculator()

	comparison, err := calculator.Compare([]UsageLog{}, []UsageLog{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), comparison.Baseline.TotalInvocations)
	assert.Equal(t, int64(0), comparison.Current.TotalInvocations)
}
