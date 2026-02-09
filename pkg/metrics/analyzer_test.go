package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnalyzer_AnalyzePatterns(t *testing.T) {
	analyzer := NewAnalyzer()

	now := time.Now()
	morning := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
	afternoon := time.Date(now.Year(), now.Month(), now.Day(), 14, 0, 0, 0, now.Location())

	logs := []UsageLog{
		{
			Agent: "sisyphus",
			Invocation: Invocation{
				Type:     "skill",
				Name:     "analyze-codebase",
				Category: "code-analysis",
			},
			Timestamp: morning.Format(time.RFC3339),
			Outcome: Outcome{
				Success:  true,
				Duration: 1500,
			},
		},
		{
			Agent: "sisyphus",
			Invocation: Invocation{
				Type:     "skill",
				Name:     "analyze-codebase",
				Category: "code-analysis",
			},
			Timestamp: afternoon.Format(time.RFC3339),
			Outcome: Outcome{
				Success:  false,
				Duration: 2500,
			},
		},
		{
			Agent: "sisyphus",
			Invocation: Invocation{
				Type:     "skill",
				Name:     "refactor-code",
				Category: "code-analysis",
			},
			Timestamp: morning.Format(time.RFC3339),
			Outcome: Outcome{
				Success:  true,
				Duration: 3000,
			},
		},
	}

	patterns, err := analyzer.AnalyzePatterns(logs)
	require.NoError(t, err)
	assert.Len(t, patterns, 2)

	analyzePattern := patterns[0]
	assert.Equal(t, "analyze-codebase", analyzePattern.Skill)
	assert.Equal(t, 2, analyzePattern.Frequency)
	assert.Equal(t, 2000.0, analyzePattern.AverageDuration)
	assert.Equal(t, 50.0, analyzePattern.SuccessRate)
}

func TestAnalyzer_GroupBySkill(t *testing.T) {
	analyzer := NewAnalyzer()

	logs := []UsageLog{
		{
			Invocation: Invocation{Type: "skill", Name: "skill-a"},
			Outcome:    Outcome{Success: true, Duration: 100},
		},
		{
			Invocation: Invocation{Type: "skill", Name: "skill-a"},
			Outcome:    Outcome{Success: true, Duration: 100},
		},
		{
			Invocation: Invocation{Type: "skill", Name: "skill-b"},
			Outcome:    Outcome{Success: true, Duration: 100},
		},
		{
			Invocation: Invocation{Type: "command", Name: "cmd-a"},
			Outcome:    Outcome{Success: true, Duration: 100},
		},
	}

	groups := analyzer.groupBySkill(logs)
	assert.Len(t, groups, 2)
	assert.Len(t, groups["skill-a"], 2)
	assert.Len(t, groups["skill-b"], 1)
	assert.NotContains(t, groups, "cmd-a")
}

func TestAnalyzer_GetTimeOfDay(t *testing.T) {
	analyzer := NewAnalyzer()

	testCases := []struct {
		hour     int
		expected string
	}{
		{6, "morning"},
		{11, "morning"},
		{12, "afternoon"},
		{16, "afternoon"},
		{17, "evening"},
		{20, "evening"},
		{22, "night"},
		{3, "night"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			result := analyzer.getTimeOfDay(tc.hour)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestAnalyzer_GetPeakTimeOfDay(t *testing.T) {
	analyzer := NewAnalyzer()

	now := time.Now()
	morning := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
	afternoon := time.Date(now.Year(), now.Month(), now.Day(), 14, 0, 0, 0, now.Location())
	evening := time.Date(now.Year(), now.Month(), now.Day(), 18, 0, 0, 0, now.Location())

	logs := []UsageLog{
		{Timestamp: morning.Format(time.RFC3339)},
		{Timestamp: morning.Format(time.RFC3339)},
		{Timestamp: afternoon.Format(time.RFC3339)},
		{Timestamp: evening.Format(time.RFC3339)},
	}

	peakTime := analyzer.getPeakTimeOfDay(logs)
	assert.Equal(t, "morning", peakTime)
}
