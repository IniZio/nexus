package insights

import (
	"testing"

	"github.com/nexus/nexus/pkg/feedback"
	"github.com/stretchr/testify/assert"
)

func TestSatisfactionPredictor_Predict(t *testing.T) {
	p := NewSatisfactionPredictor()

	tests := []struct {
		name     string
		category feedback.FeedbackType
		message  string
		minScore float64
		maxScore float64
	}{
		{
			name:     "praise with positive keywords",
			category: feedback.FeedbackPraise,
			message:  "This is amazing! I love how excellent and seamless the workflow is.",
			minScore: 3.5,
			maxScore: 5.0,
		},
		{
			name:     "bug report with negative keywords",
			category: feedback.FeedbackBug,
			message:  "This is terrible! The app keeps crashing and it's frustrating to use.",
			minScore: 1.0,
			maxScore: 3.0,
		},
		{
			name:     "feature request neutral",
			category: feedback.FeedbackFeature,
			message:  "Could you add support for dark mode?",
			minScore: 2.5,
			maxScore: 4.0,
		},
		{
			name:     "workflow feedback with mixed sentiment",
			category: feedback.FeedbackWorkflow,
			message:  "The setup is fast but sometimes confusing during configuration.",
			minScore: 2.0,
			maxScore: 4.0,
		},
		{
			name:     "suggestion with positive words",
			category: feedback.FeedbackSuggestion,
			message:  "Great idea! This feature works well and is very helpful.",
			minScore: 3.5,
			maxScore: 5.0,
		},
		{
			name:     "bug with severe negative keywords",
			category: feedback.FeedbackBug,
			message:  "Broken! Waste of time! Completely useless and buggy!",
			minScore: 1.0,
			maxScore: 2.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := p.Predict(tt.category, tt.message)
			assert.GreaterOrEqual(t, score, tt.minScore, "score should be >= min")
			assert.LessOrEqual(t, score, tt.maxScore, "score should be <= max")
		})
	}
}

func TestSatisfactionPredictor_CategoryScores(t *testing.T) {
	p := NewSatisfactionPredictor()

	// Test that different categories have different base scores
	praiseScore := p.Predict(feedback.FeedbackPraise, "")
	bugScore := p.Predict(feedback.FeedbackBug, "")
	suggestionScore := p.Predict(feedback.FeedbackSuggestion, "")

	assert.Greater(t, praiseScore, bugScore, "praise should score higher than bug")
	assert.Greater(t, suggestionScore, bugScore, "suggestion should score higher than bug")
}

func TestSatisfactionPredictor_KeywordEffects(t *testing.T) {
	p := NewSatisfactionPredictor()

	// Same category, different messages should yield different scores
	baseScore := p.Predict(feedback.FeedbackBug, "I found an issue")
	positiveScore := p.Predict(feedback.FeedbackBug, "This is amazing and excellent")
	negativeScore := p.Predict(feedback.FeedbackBug, "This is terrible and useless")

	assert.Greater(t, positiveScore, baseScore, "positive keywords should increase score")
	assert.Less(t, negativeScore, baseScore, "negative keywords should decrease score")
}

func TestSatisfactionPredictor_ScoreBounds(t *testing.T) {
	p := NewSatisfactionPredictor()

	// Extreme positive message
	highScore := p.Predict(feedback.FeedbackPraise, "This is absolutely perfect and amazing!")
	assert.LessOrEqual(t, highScore, 5.0, "score should not exceed 5.0")

	// Extreme negative message
	lowScore := p.Predict(feedback.FeedbackBug, "This is broken and terrible and useless and a waste!")
	assert.GreaterOrEqual(t, lowScore, 1.0, "score should not go below 1.0")
}

func TestSkillRecommender_Recommend(t *testing.T) {
	r := NewSkillRecommender()

	tests := []struct {
		name       string
		taskType   string
		wantSkills []string
	}{
		{
			name:       "code refactoring",
			taskType:   "code_refactoring",
			wantSkills: []string{"executor", "code-review", "tdd"},
		},
		{
			name:       "bug fix",
			taskType:   "bug_fix",
			wantSkills: []string{"debugger", "executor", "build-fixer"},
		},
		{
			name:       "feature development",
			taskType:   "feature_development",
			wantSkills: []string{"executor", "designer", "tdd"},
		},
		{
			name:       "code review",
			taskType:   "code_review",
			wantSkills: []string{"code-reviewer", "security-reviewer"},
		},
		{
			name:       "setup environment",
			taskType:   "setup_environment",
			wantSkills: []string{"git-master", "executor"},
		},
		{
			name:       "security audit",
			taskType:   "security_audit",
			wantSkills: []string{"security-reviewer", "code-reviewer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skills := r.Recommend(tt.taskType)
			assert.ElementsMatch(t, tt.wantSkills, skills, "should recommend expected skills")
		})
	}
}

func TestSkillRecommender_Fallback(t *testing.T) {
	r := NewSkillRecommender()

	// Unknown task type should fall back to general skills
	skills := r.Recommend("unknown_task_type")
	assert.Contains(t, skills, "executor", "should include executor in fallback")

	// Test with spaces instead of underscores
	skills = r.Recommend("setup environment")
	assert.Contains(t, skills, "git-master", "should handle spaces in task type")
}

func TestSkillRecommender_SuccessPatternRecording(t *testing.T) {
	r := NewSkillRecommender()

	// Record a success pattern
	r.RecordSuccess("custom_task", []string{"custom-skill-1", "custom-skill-2"})

	// Should now use recorded pattern
	skills := r.Recommend("custom_task")
	assert.Contains(t, skills, "custom-skill-1", "should use recorded success pattern")
	assert.Contains(t, skills, "custom-skill-2", "should use recorded success pattern")
}

func TestSkillRecommender_PartialMatching(t *testing.T) {
	r := NewSkillRecommender()

	// Partial match should work with "frontend" key
	skills := r.Recommend("frontend-component")
	assert.Contains(t, skills, "designer", "should match frontend tasks")

	// Partial match should work with "deployment" key
	skills = r.Recommend("backend-deployment")
	assert.Contains(t, skills, "git-master", "should match deployment tasks")
}

func TestAnomalyDetector_IsAnomalous(t *testing.T) {
	d := NewAnomalyDetector()

	tests := []struct {
		name    string
		value   float64
		history []float64
		want    bool
	}{
		{
			name:    "no history",
			value:   100,
			history: nil,
			want:    false,
		},
		{
			name:    "single history point",
			value:   100,
			history: []float64{50},
			want:    false,
		},
		{
			name:    "value within normal range",
			value:   52,
			history: []float64{50, 52, 48, 51, 49, 50, 53, 47, 51, 52},
			want:    false,
		},
		{
			name:    "value slightly above mean",
			value:   54,
			history: []float64{50, 52, 48, 51, 49, 50, 53, 47, 51, 52},
			want:    false,
		},
		{
			name:    "anomalous high value",
			value:   100,
			history: []float64{50, 52, 48, 51, 49},
			want:    true,
		},
		{
			name:    "anomalous low value",
			value:   10,
			history: []float64{50, 52, 48, 51, 49},
			want:    true,
		},
		{
			name:    "constant history - same value",
			value:   50,
			history: []float64{50, 50, 50, 50},
			want:    false,
		},
		{
			name:    "constant history - different value",
			value:   60,
			history: []float64{50, 50, 50, 50},
			want:    true,
		},
		{
			name:    "large history with minor deviation",
			value:   50,
			history: []float64{50, 50, 50, 50, 50, 50, 50, 50, 50, 50},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.IsAnomalous(tt.value, tt.history)
			assert.Equal(t, tt.want, got, "anomaly detection should match expected")
		})
	}
}

func TestAnomalyDetector_IsAnomalousWithMovingAverage(t *testing.T) {
	d := NewAnomalyDetector()

	// Create a history with a clear trend
	history := []float64{10, 12, 11, 13, 12, 14, 13, 15, 14, 16, 50}

	// Value matching recent trend should not be anomalous
	notAnomalous := d.IsAnomalousWithMovingAverage(17, history)
	assert.False(t, notAnomalous, "value following trend should not be anomalous")

	// Sudden spike should be anomalous
	anomalous := d.IsAnomalousWithMovingAverage(50, history)
	assert.True(t, anomalous, "sudden spike should be anomalous")

	// Small history should fall back to regular IsAnomalous
	shortHistory := []float64{10, 12, 11}
	_ = d.IsAnomalousWithMovingAverage(100, shortHistory)
	// Should not panic and should use fallback
}

func TestAnomalyDetector_Statistics(t *testing.T) {
	d := NewAnomalyDetector()

	// Test mean calculation
	values := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	mean := d.calculateMean(values)
	assert.Equal(t, 5.0, mean, "mean should be calculated correctly")

	// Test standard deviation (sample std dev with n-1 denominator)
	// Expected: sqrt(sum((x-mean)^2)/(n-1)) = sqrt(40/7) = ~2.138
	stdDev := d.calculateStdDev(values, mean)
	assert.InDelta(t, 2.138, stdDev, 0.01, "std dev should be calculated correctly")
}

func TestAnomalyDetector_Configuration(t *testing.T) {
	d := NewAnomalyDetector()

	// Test custom thresholds
	d.SetZScoreThreshold(3.0)
	assert.Equal(t, 3.0, d.zScoreThreshold, "z-score threshold should be configurable")

	d.SetMovingAverageWindow(20)
	assert.Equal(t, 20, d.movingAverageWindow, "moving average window should be configurable")
}

func TestAnomalyDetector_EdgeCases(t *testing.T) {
	d := NewAnomalyDetector()

	// Empty history
	assert.False(t, d.IsAnomalous(100, []float64{}), "empty history should not flag anomaly")

	// Single value history
	assert.False(t, d.IsAnomalous(100, []float64{100}), "single value should not flag anomaly")

	// All same values with same test value
	assert.False(t, d.IsAnomalous(50, []float64{50, 50, 50}), "same values should not be anomalous")
}

func TestNewSatisfactionPredictor_Defaults(t *testing.T) {
	p := NewSatisfactionPredictor()

	assert.NotNil(t, p.positiveKeywords, "positive keywords should be initialized")
	assert.NotNil(t, p.negativeKeywords, "negative keywords should be initialized")
	assert.NotNil(t, p.categoryScores, "category scores should be initialized")
	assert.Greater(t, len(p.positiveKeywords), 10, "should have positive keywords")
	assert.Greater(t, len(p.negativeKeywords), 10, "should have negative keywords")
}

func TestNewSkillRecommender_Defaults(t *testing.T) {
	r := NewSkillRecommender()

	assert.NotNil(t, r.taskMappings, "task mappings should be initialized")
	assert.NotNil(t, r.successPatterns, "success patterns should be initialized")
	assert.Greater(t, len(r.taskMappings), 10, "should have task mappings")
}

func TestNewAnomalyDetector_Defaults(t *testing.T) {
	d := NewAnomalyDetector()

	assert.Equal(t, 2.0, d.zScoreThreshold, "default z-score threshold should be 2.0")
	assert.Equal(t, 10, d.movingAverageWindow, "default moving average window should be 10")
}
