package insights

import (
	"math"
	"strings"

	"github.com/nexus/nexus/pkg/feedback"
)

// SatisfactionPredictor predicts user satisfaction based on feedback features.
// It uses a heuristic scoring approach based on feedback category, sentiment
// keywords, and historical patterns.
type SatisfactionPredictor struct {
	// positiveKeywords maps to higher satisfaction scores
	positiveKeywords map[string]float64
	// negativeKeywords maps to lower satisfaction scores
	negativeKeywords map[string]float64
	// categoryScores provides base scores per feedback category
	categoryScores map[feedback.FeedbackType]float64
}

// NewSatisfactionPredictor creates a new SatisfactionPredictor with default
// keyword and category mappings.
func NewSatisfactionPredictor() *SatisfactionPredictor {
	return &SatisfactionPredictor{
		positiveKeywords: map[string]float64{
			"excellent":    0.9,
			"amazing":      0.95,
			"love":         0.85,
			"fantastic":    0.9,
			"great":        0.7,
			"helpful":      0.6,
			"works":        0.5,
			"good":         0.5,
			"satisfied":    0.6,
			"perfect":      0.95,
			"impressed":    0.8,
			"efficient":    0.65,
			"fast":         0.55,
			"intuitive":    0.7,
			"seamless":     0.75,
		},
		negativeKeywords: map[string]float64{
			"broken":       -0.8,
			"terrible":     -0.9,
			"awful":        -0.85,
			"hate":         -0.8,
			"frustrating":  -0.7,
			"frustrated":  -0.7,
			"slow":         -0.4,
			"bug":          -0.6,
			"buggy":        -0.7,
			"crash":        -0.8,
			"crashes":      -0.8,
			"error":        -0.5,
			"failed":       -0.6,
			"failure":      -0.65,
			"useless":      -0.85,
			"waste":        -0.6,
			"confusing":    -0.55,
			"confused":     -0.5,
			"difficult":    -0.4,
			"hard":         -0.35,
			"annoying":     -0.5,
			"annoyed":      -0.5,
			"problem":      -0.4,
			"issues":       -0.35,
			"problems":     -0.4,
		},
		categoryScores: map[feedback.FeedbackType]float64{
			feedback.FeedbackPraise:    4.5,
			feedback.FeedbackSuggestion: 3.2,
			feedback.FeedbackFeature:    3.0,
			feedback.FeedbackBug:        2.0,
			feedback.FeedbackWorkflow:   3.0,
		},
	}
}

// Predict predicts user satisfaction score (1-5) based on feedback category
// and message content analysis.
func (p *SatisfactionPredictor) Predict(category feedback.FeedbackType, message string) float64 {
	// Start with base score from category
	baseScore := p.categoryScores[category]
	if baseScore == 0 {
		baseScore = 3.0 // Neutral default
	}

	// Analyze sentiment in message
	messageLower := strings.ToLower(message)
	var sentimentAdjustment float64

	for keyword, weight := range p.positiveKeywords {
		if strings.Contains(messageLower, keyword) {
			sentimentAdjustment += weight
		}
	}

	for keyword, weight := range p.negativeKeywords {
		if strings.Contains(messageLower, keyword) {
			sentimentAdjustment += weight
		}
	}

	// Apply sentiment adjustment with dampening
	// We don't want extreme keywords to swing score too wildly
	adjustedScore := baseScore + (sentimentAdjustment * 0.3)

	// Clamp to valid range
	return math.Max(1.0, math.Min(5.0, adjustedScore))
}

// SkillRecommender recommends skills based on task type and success patterns.
// It uses a rule-based approach mapping task categories to proven skill combinations.
type SkillRecommender struct {
	// taskMappings maps task types to recommended skills
	taskMappings map[string][]string
	// successPatterns tracks successful skill combinations per task type
	successPatterns map[string][]string
}

// NewSkillRecommender creates a new SkillRecommender with default task-to-skill mappings.
func NewSkillRecommender() *SkillRecommender {
	return &SkillRecommender{
		taskMappings: map[string][]string{
			// Code development tasks
			"code_refactoring":   {"executor", "code-review", "tdd"},
			"bug_fix":            {"debugger", "executor", "build-fixer"},
			"feature_development": {"executor", "designer", "tdd"},
			"code_review":         {"code-reviewer", "security-reviewer"},
			"write_tests":        {"tdd", "qa-tester"},

			// Infrastructure tasks
			"setup_environment":  {"git-master", "executor"},
			"configure_ci":       {"git-master", "executor"},
			"deploy":            {"executor", "git-master"},

			// Research and analysis
			"research":          {"researcher", "analyst"},
			"documentation":     {"writer", "researcher"},
			"explore_codebase":  {"explore", "architect"},

			// Frontend tasks
			"frontend":          {"designer", "executor"},
			"ui_component":     {"designer", "executor"},
			"css_styling":      {"designer", "executor"},
			"frontend_debug":  {"debugger", "qa-tester"},
			"deployment":       {"executor", "git-master"},

			// Security tasks
			"security_audit":    {"security-reviewer", "code-reviewer"},
			"vulnerability_scan": {"security-reviewer"},

			// Data tasks
			"data_analysis":     {"scientist", "researcher"},
			"data_migration":    {"executor", "git-master"},

			// Default fallback
			"general":           {"executor", "explore"},
		},
		successPatterns: make(map[string][]string),
	}
}

// Recommend returns a list of recommended skills for the given task type.
func (r *SkillRecommender) Recommend(taskType string) []string {
	// Normalize task type
	normalized := strings.ToLower(strings.ReplaceAll(taskType, " ", "_"))

	// Direct lookup
	if skills, ok := r.taskMappings[normalized]; ok {
		return r.cloneSkills(skills)
	}

	// Partial matching for compound task types
	for task, skills := range r.taskMappings {
		if strings.Contains(normalized, task) || strings.Contains(task, normalized) {
			return r.cloneSkills(skills)
		}
	}

	// Check historical success patterns first
	if patterns, ok := r.successPatterns[normalized]; ok && len(patterns) > 0 {
		return r.cloneSkills(patterns)
	}

	// Fallback to general skills
	return r.cloneSkills(r.taskMappings["general"])
}

// RecordSuccess records a successful skill combination for a task type
// to build up historical success patterns.
func (r *SkillRecommender) RecordSuccess(taskType string, skills []string) {
	normalized := strings.ToLower(strings.ReplaceAll(taskType, " ", "_"))
	r.successPatterns[normalized] = skills
}

// cloneSkills creates a copy of the skills slice to prevent mutation.
func (r *SkillRecommender) cloneSkills(skills []string) []string {
	result := make([]string, len(skills))
	copy(result, skills)
	return result
}

// AnomalyDetector detects unusual patterns in numerical metrics using
// statistical methods including z-score analysis and moving averages.
type AnomalyDetector struct {
	// zScoreThreshold is the number of standard deviations for anomaly detection
	zScoreThreshold float64
	// movingAverageWindow is the number of data points for moving average
	movingAverageWindow int
}

// NewAnomalyDetector creates a new AnomalyDetector with default thresholds.
func NewAnomalyDetector() *AnomalyDetector {
	return &AnomalyDetector{
		zScoreThreshold:       2.0,  // 2 standard deviations
		movingAverageWindow:   10,   // 10 data points
	}
}

// IsAnomalous determines if a value is anomalous based on historical data.
// Returns true if the value deviates significantly from the norm.
// Requires at least 2 historical data points for meaningful analysis.
func (d *AnomalyDetector) IsAnomalous(value float64, history []float64) bool {
	if len(history) < 2 {
		// Not enough data to determine anomaly
		return false
	}

	// Calculate statistics
	mean := d.calculateMean(history)
	stdDev := d.calculateStdDev(history, mean)

	if stdDev == 0 {
		// No variance in history - check if value differs from constant
		return value != mean
	}

	// Calculate z-score
	zScore := math.Abs((value - mean) / stdDev)

	return zScore > d.zScoreThreshold
}

// IsAnomalousWithMovingAverage detects anomalies using a moving average comparison.
// This is useful for detecting trends and sudden changes.
func (d *AnomalyDetector) IsAnomalousWithMovingAverage(value float64, history []float64) bool {
	if len(history) < d.movingAverageWindow {
		return d.IsAnomalous(value, history)
	}

	// Calculate moving average from recent window
	windowStart := len(history) - d.movingAverageWindow
	recentHistory := history[windowStart:]
	movingAvg := d.calculateMean(recentHistory)

	// Calculate deviation from moving average
	deviation := math.Abs(value - movingAvg)

	// Calculate expected range based on historical variance
	historicalStdDev := d.calculateStdDev(history, d.calculateMean(history))

	// Value is anomalous if it deviates more than 2 std devs from moving avg
	return deviation > (d.zScoreThreshold * historicalStdDev)
}

// calculateMean computes the arithmetic mean of a slice of floats.
func (d *AnomalyDetector) calculateMean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// calculateStdDev computes the standard deviation of a slice of floats.
func (d *AnomalyDetector) calculateStdDev(values []float64, mean float64) float64 {
	if len(values) < 2 {
		return 0
	}

	var sumSquaredDiff float64
	for _, v := range values {
		diff := v - mean
		sumSquaredDiff += diff * diff
	}

	variance := sumSquaredDiff / float64(len(values)-1)
	return math.Sqrt(variance)
}

// SetZScoreThreshold allows customizing the z-score threshold.
func (d *AnomalyDetector) SetZScoreThreshold(threshold float64) {
	d.zScoreThreshold = threshold
}

// SetMovingAverageWindow allows customizing the moving average window size.
func (d *AnomalyDetector) SetMovingAverageWindow(window int) {
	d.movingAverageWindow = window
}
