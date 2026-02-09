package coordination

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nexus/nexus/pkg/feedback"
	"github.com/nexus/nexus/pkg/metrics"
)

// CombinedAnalyticsService aggregates metrics from Nexus and Pulse
type CombinedAnalyticsService struct {
	workflowTracker   *metrics.WorkflowTracker
	feedbackCollector FeedbackCollector
	pulseClient      pulseClient
}

// PulseClient interface for Pulse metrics
type pulseClient interface {
	GetTasks(workspaceID string) ([]*pulseIssue, error)
	GetTaskMetrics() (*PulseMetrics, error)
}

// pulseIssue represents a Pulse issue/task
type pulseIssue struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Priority    int       `json:"priority"`
	CycleID     string    `json:"cycle_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// UsageMetrics from Nexus metrics
type UsageMetrics struct {
	TotalSessions          int                   `json:"totalSessions"`
	ActiveUsers            int                   `json:"activeUsers"`
	AverageSessionDuration float64               `json:"averageSessionDurationSeconds"`
	AverageSatisfaction   float64               `json:"averageSatisfaction"`
	SkillsFrequency        map[string]int       `json:"skillsFrequency"`
	NexusFeatureUsage      map[string]int       `json:"nexusFeatureUsage"`
	WorkflowStageTimes     map[string]int64     `json:"workflowStageTimes"`
}

// PulseMetrics from Pulse
type PulseMetrics struct {
	TasksCreated      int                    `json:"tasksCreated"`
	TasksCompleted    int                    `json:"tasksCompleted"`
	TasksInProgress   int                    `json:"tasksInProgress"`
	TasksFailed       int                    `json:"tasksFailed"`
	AverageCycleTime  float64                `json:"averageCycleTimeHours"`
	CycleTimeDistribution map[string]int     `json:"cycleTimeDistribution"`
	SyncLatency       time.Duration          `json:"syncLatency"`
}

// CombinedDashboard contains all metrics
type CombinedDashboard struct {
	GeneratedAt     string              `json:"generatedAt"`
	Period          string              `json:"period"`
	Usage           *UsageMetrics       `json:"usage"`
	Pulse           *PulseMetrics       `json:"pulse"`
	Feedback        *FeedbackStats     `json:"feedback"`
	Trends          *TrendAnalysis     `json:"trends"`
	Recommendations []string           `json:"recommendations"`
}

// TrendAnalysis shows changes over time
type TrendAnalysis struct {
	SessionChange      float64 `json:"sessionChange"`
	SatisfactionChange float64 `json:"satisfactionChange"`
	CycleTimeChange    float64 `json:"cycleTimeChange"`
	FeedbackTrend     string  `json:"feedbackTrend"`
}

// WorkflowAnalytics contains workflow-related metrics
type WorkflowAnalytics struct {
	TotalWorkflows    int              `json:"totalWorkflows"`
	CompletedWorkflows int              `json:"completedWorkflows"`
	FailedWorkflows   int              `json:"failedWorkflows"`
	AverageDuration   float64          `json:"averageDurationSeconds"`
	StageDurations   map[string]int64 `json:"stageDurations"`
	SuccessRate      float64          `json:"successRate"`
}

// FeedbackStats from feedback package
type FeedbackStats = feedback.FeedbackStats

// NewCombinedAnalyticsService creates a new CombinedAnalyticsService
func NewCombinedAnalyticsService(workflowTracker *metrics.WorkflowTracker, feedbackCollector FeedbackCollector, pulseClient pulseClient) *CombinedAnalyticsService {
	return &CombinedAnalyticsService{
		workflowTracker:    workflowTracker,
		feedbackCollector: feedbackCollector,
		pulseClient:       pulseClient,
	}
}

// GetUsageMetrics retrieves usage metrics from Nexus
func (s *CombinedAnalyticsService) GetUsageMetrics(days int) (*UsageMetrics, error) {
	metrics := &UsageMetrics{
		SkillsFrequency:     make(map[string]int),
		NexusFeatureUsage:  make(map[string]int),
		WorkflowStageTimes: make(map[string]int64),
	}

	// Get workflow stats from workflow tracker
	if s.workflowTracker != nil {
		stats, err := s.workflowTracker.GetStats()
		if err == nil {
			metrics.TotalSessions = stats.TotalSessions
			metrics.ActiveUsers = stats.ActiveUsers
			metrics.AverageSessionDuration = stats.AverageSessionDuration
			metrics.SkillsFrequency = stats.SkillsFrequency
		}
	}

	// Get feedback satisfaction if available
	if s.feedbackCollector != nil {
		stats, err := s.feedbackCollector.GetStats(days)
		if err == nil {
			metrics.AverageSatisfaction = stats.AverageSatisfaction
		}
	}

	return metrics, nil
}

// GetPulseMetrics retrieves metrics from Pulse
func (s *CombinedAnalyticsService) GetPulseMetrics() (*PulseMetrics, error) {
	metrics := &PulseMetrics{
		CycleTimeDistribution: make(map[string]int),
	}

	if s.pulseClient != nil {
		pulseMetrics, err := s.pulseClient.GetTaskMetrics()
		if err != nil {
			return nil, fmt.Errorf("failed to get pulse metrics: %w", err)
		}
		return pulseMetrics, nil
	}

	return metrics, nil
}

// GetFeedbackStats retrieves feedback statistics
func (s *CombinedAnalyticsService) GetFeedbackStats(days int) (*feedback.FeedbackStats, error) {
	if s.feedbackCollector == nil {
		return &feedback.FeedbackStats{}, nil
	}
	return s.feedbackCollector.GetStats(days)
}

// GetCombinedDashboard generates a complete analytics dashboard
func (s *CombinedAnalyticsService) GetCombinedDashboard(days int) (*CombinedDashboard, error) {
	period := fmt.Sprintf("last_%d_days", days)

	usage, err := s.GetUsageMetrics(days)
	if err != nil {
		return nil, fmt.Errorf("failed to get usage metrics: %w", err)
	}

	pulse, err := s.GetPulseMetrics()
	if err != nil {
		return nil, fmt.Errorf("failed to get pulse metrics: %w", err)
	}

	feedback, err := s.GetFeedbackStats(days)
	if err != nil {
		return nil, fmt.Errorf("failed to get feedback stats: %w", err)
	}

	trends := s.calculateTrends(feedback)
	recommendations := s.generateRecommendations(usage, pulse, feedback)

	return &CombinedDashboard{
		GeneratedAt:     time.Now().Format(time.RFC3339),
		Period:          period,
		Usage:           usage,
		Pulse:           pulse,
		Feedback:        feedback,
		Trends:          trends,
		Recommendations: recommendations,
	}, nil
}

// calculateTrends calculates trends based on historical data
func (s *CombinedAnalyticsService) calculateTrends(feedback *feedback.FeedbackStats) *TrendAnalysis {
	feedbackTrend := "stable"

	if feedback != nil && len(feedback.RecentTrend) >= 2 {
		var firstHalfSum, secondHalfSum float64
		var firstHalfCount, secondHalfCount int

		midPoint := len(feedback.RecentTrend) / 2
		for i, stat := range feedback.RecentTrend {
			if i < midPoint {
				firstHalfSum += stat.AvgSatisfaction
				firstHalfCount++
			} else {
				secondHalfSum += stat.AvgSatisfaction
				secondHalfCount++
			}
		}

		if firstHalfCount > 0 && secondHalfCount > 0 {
			change := (secondHalfSum / float64(secondHalfCount)) - (firstHalfSum / float64(firstHalfCount))
			if change > 0.2 {
				feedbackTrend = "improving"
			} else if change < -0.2 {
				feedbackTrend = "declining"
			}
		}
	}

	return &TrendAnalysis{
		SessionChange:       0,
		SatisfactionChange: 0,
		CycleTimeChange:     0,
		FeedbackTrend:       feedbackTrend,
	}
}

// generateRecommendations generates actionable recommendations
func (s *CombinedAnalyticsService) generateRecommendations(usage *UsageMetrics, pulse *PulseMetrics, feedback *feedback.FeedbackStats) []string {
	var recommendations []string

	// Satisfaction-based recommendations
	if feedback != nil && feedback.AverageSatisfaction > 0 {
		if feedback.AverageSatisfaction < 3.0 {
			recommendations = append(recommendations,
				"Low satisfaction detected. Review recent feedback for common pain points.")
		} else if feedback.AverageSatisfaction < 4.0 {
			recommendations = append(recommendations,
				"Good satisfaction. Focus on incremental improvements.")
		} else {
			recommendations = append(recommendations,
				"Excellent user satisfaction!")
		}
	}

	// Usage recommendations
	if usage != nil && usage.TotalSessions == 0 {
		recommendations = append(recommendations,
			"No usage recorded. Consider promoting feature adoption.")
	}

	if len(recommendations) == 0 {
		recommendations = append(recommendations, "All metrics look healthy.")
	}

	return recommendations
}

// GetWorkflowAnalytics retrieves workflow analytics
func (s *CombinedAnalyticsService) GetWorkflowAnalytics() (*WorkflowAnalytics, error) {
	analytics := &WorkflowAnalytics{
		StageDurations: make(map[string]int64),
	}

	if s.workflowTracker != nil {
		stats, err := s.workflowTracker.GetStats()
		if err == nil {
			analytics.TotalWorkflows = stats.TotalSessions
			analytics.StageDurations = stats.WorkflowStageTimes
			if stats.SessionsByOutcome["success"] > 0 {
				total := stats.SessionsByOutcome["success"] + stats.SessionsByOutcome["failure"]
				analytics.SuccessRate = float64(stats.SessionsByOutcome["success"]) / float64(total) * 100
			}
		}
	}

	return analytics, nil
}

// handleAnalyticsDashboard handles GET /api/analytics/dashboard
func (s *Server) handleAnalyticsDashboard(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}

	dashboard, err := s.analyticsService.GetCombinedDashboard(days)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dashboard)
}

// handleUsageAnalytics handles GET /api/analytics/usage
func (s *Server) handleUsageAnalytics(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}

	metrics, err := s.analyticsService.GetUsageMetrics(days)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// handlePulseAnalytics handles GET /api/analytics/pulse
func (s *Server) handlePulseAnalytics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.analyticsService.GetPulseMetrics()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// handleWorkflowAnalytics handles GET /api/analytics/workflow
func (s *Server) handleWorkflowAnalytics(w http.ResponseWriter, r *http.Request) {
	analytics, err := s.analyticsService.GetWorkflowAnalytics()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(analytics)
}

// handleRecommendations handles GET /api/analytics/recommendations
func (s *Server) handleRecommendations(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}

	dashboard, err := s.analyticsService.GetCombinedDashboard(days)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"recommendations": dashboard.Recommendations,
		"period":         dashboard.Period,
		"generatedAt":    dashboard.GeneratedAt,
	})
}
