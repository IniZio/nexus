// Package team provides team performance analytics for Nexus.
// It tracks individual and team metrics, benchmarks, and performance trends.
package team

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"
)

// MemberRole represents a team member's role.
type MemberRole string

const (
	RoleLead     MemberRole = "lead"
	RoleSenior   MemberRole = "senior"
	RoleMid      MemberRole = "mid"
	RoleJunior   MemberRole = "junior"
	RoleContractor MemberRole = "contractor"
)

// MetricCategory represents categories of metrics.
type MetricCategory string

const (
	CategoryVelocity   MetricCategory = "velocity"
	CategoryQuality    MetricCategory = "quality"
	CategoryEfficiency MetricCategory = "efficiency"
	CategoryCollaboration MetricCategory = "collaboration"
	CategoryGrowth    MetricCategory = "growth"
)

// TeamMember represents a team member.
type TeamMember struct {
	ID        string     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      MemberRole `json:"role"`
	TeamID    string    `json:"team_id"`
	JoinedAt  time.Time `json:"joined_at"`
	Active    bool      `json:"active"`
}

// Team represents a team.
type Team struct {
	ID          string       `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	LeadID      string      `json:"lead_id"`
	MemberIDs   []string    `json:"member_ids"`
	CreatedAt   time.Time   `json:"created_at"`
	Active      bool        `json:"active"`
}

// PerformanceMetrics represents individual performance metrics.
type PerformanceMetrics struct {
	MemberID    string           `json:"member_id"`
	Period      string          `json:"period"` // "weekly", "monthly", "quarterly"
	StartDate   time.Time       `json:"start_date"`
	EndDate     time.Time       `json:"end_date"`

	// Velocity metrics
	TasksCompleted int       `json:"tasks_completed"`
	PointsCompleted int      `json:"points_completed"`
	Velocity       float64   `json:"velocity"` // tasks/points per day

	// Quality metrics
	BugRate      float64   `json:"bug_rate"` // bugs per 100 tasks
	CodeReviewScore float64 `json:"code_review_score"` // 0-100
	PRMergeRate  float64   `json:"pr_merge_rate"` // percentage

	// Efficiency metrics
	CycleTime    float64   `json:"cycle_time"` // hours
	LeadTime     float64   `json:"lead_time"` // hours
	WorkInProgress float64 `json:"wip_avg"` // average WIP

	// Collaboration metrics
	ReviewsGiven  int     `json:"reviews_given"`
	ReviewsReceived int   `json:"reviews_received"`
	CommentsAdded int     `json:"comments_added"`
	HelpRequests  int     `json:"help_requests"`

	// Growth metrics
	SkillsAcquired []string `json:"skills_acquired"`
	Certifications []string  `json:"certifications"`
	TrainingHours float64   `json:"training_hours"`
}

// TeamMetrics represents aggregated team metrics.
type TeamMetrics struct {
	TeamID      string           `json:"team_id"`
	Period      string          `json:"period"`
	StartDate   time.Time       `json:"start_date"`
	EndDate     time.Time       `json:"end_date"`

	// Aggregate velocity
	TotalTasksCompleted int     `json:"total_tasks_completed"`
	TotalPointsCompleted int    `json:"total_points_completed"`
	TeamVelocity        float64 `json:"team_velocity"`
	VelocityTrend       string  `json:"velocity_trend"` // "up", "down", "stable"

	// Aggregate quality
	AvgBugRate          float64 `json:"avg_bug_rate"`
	AvgCodeReviewScore  float64 `json:"avg_code_review_score"`
	BugTrend            string  `json:"bug_trend"`

	// Aggregate efficiency
	AvgCycleTime         float64 `json:"avg_cycle_time"`
	AvgLeadTime          float64 `json:"avg_lead_time"`
	EfficiencyTrend      string  `json:"efficiency_trend"`

	// Collaboration
	TotalReviewsGiven    int     `json:"total_reviews_given"`
	TotalReviewsReceived int    `json:"total_reviews_received"`
	CollaborationScore   float64 `json:"collaboration_score"`

	// Member breakdown
	MemberCount         int     `json:"member_count"`
	ActiveMembers       int     `json:"active_members"`
	ParticipationRate   float64 `json:"participation_rate"`
}

// Benchmark represents a performance benchmark.
type Benchmark struct {
	ID          string         `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Category    MetricCategory `json:"category"`
	MetricType  string        `json:"metric_type"`
	TeamID      string        `json:"team_id,omitempty"` // Team-specific, empty for org-wide

	// Benchmark values
	MinValue    float64       `json:"min_value"`
	TargetValue float64       `json:"target_value"`
	MaxValue    float64       `json:"max_value"`
	Percentile  float64       `json:"percentile"` // e.g., 0.5 = median

	// Context
	Period      string       `json:"period"`
	IndustryAvg float64      `json:"industry_avg,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// PerformanceReport represents a generated performance report.
type PerformanceReport struct {
	ID            string           `json:"id"`
	Title         string          `json:"title"`
	Type          string          `json:"type"` // "individual", "team", "comparison"
	SubjectID     string          `json:"subject_id"` // member or team ID
	SubjectName   string          `json:"subject_name"`
	Period        string          `json:"period"`
	StartDate     time.Time       `json:"start_date"`
	EndDate       time.Time       `json:"end_date"`

	// Summary
	OverallScore  float64         `json:"overall_score"` // 0-100
	Highlights    []string        `json:"highlights"`
	Improvements  []string        `json:"improvements"`

	// Detailed metrics
	Metrics       PerformanceMetrics `json:"metrics,omitempty"`
	TeamMetrics   *TeamMetrics     `json:"team_metrics,omitempty"`
	Benchmarks    []BenchmarkScore  `json:"benchmarks"`

	// Trends
	PreviousPeriod *PerformanceMetrics `json:"previous_period,omitempty"`
	TrendAnalysis  []TrendPoint      `json:"trend_analysis"`

	GeneratedAt   time.Time        `json:"generated_at"`
}

// BenchmarkScore represents how a metric compares to benchmarks.
type BenchmarkScore struct {
	BenchmarkID   string  `json:"benchmark_id"`
	BenchmarkName  string  `json:"benchmark_name"`
	MetricType    string  `json:"metric_type"`
	ActualValue   float64 `json:"actual_value"`
	BenchmarkValue float64 `json:"benchmark_value"`
	Score         float64 `json:"score"` // -1 to 1, negative = below benchmark
	Percentile    float64 `json:"percentile"`
}

// TrendPoint represents a point in a trend analysis.
type TrendPoint struct {
	Period      string  `json:"period"`
	Value       float64 `json:"value"`
	Change      float64 `json:"change"` // percentage change from previous
	Trend       string  `json:"trend"` // "up", "down", "stable"
}

// LeaderboardEntry represents an entry on a leaderboard.
type LeaderboardEntry struct {
	Rank        int       `json:"rank"`
	MemberID    string    `json:"member_id"`
	MemberName  string    `json:"member_name"`
	TeamID     string    `json:"team_id,omitempty"`
	Score      float64   `json:"score"`
	MetricType string    `json:"metric_type"`
	Period     string    `json:"period"`
	Details    map[string]float64 `json:"details,omitempty"`
}

// TeamAnalyticsEngine provides team performance analytics.
type TeamAnalyticsEngine struct {
	mu sync.RWMutex

	// Data stores
	members  map[string]*TeamMember
	teams    map[string]*Team
	metrics  map[string][]PerformanceMetrics
	benchmarks map[string]*Benchmark

	// Configuration
	config *AnalyticsConfig
}

// AnalyticsConfig holds configuration for team analytics.
type AnalyticsConfig struct {
	// Benchmark periods
	DefaultPeriod string
	MaxHistoryPeriod time.Duration

	// Scoring weights
	VelocityWeight  float64
	QualityWeight   float64
	EfficiencyWeight float64
	CollaborationWeight float64

	// Thresholds
	MinParticipationRate float64
	ActiveThresholdHours float64
}

// NewTeamAnalyticsEngine creates a new team analytics engine.
func NewTeamAnalyticsEngine(config *AnalyticsConfig) *TeamAnalyticsEngine {
	if config == nil {
		config = &AnalyticsConfig{
			DefaultPeriod:       "monthly",
			MaxHistoryPeriod:    365 * 24 * time.Hour,
			VelocityWeight:      0.30,
			QualityWeight:       0.30,
			EfficiencyWeight:    0.25,
			CollaborationWeight: 0.15,
			MinParticipationRate: 0.5,
			ActiveThresholdHours: 40,
		}
	}

	return &TeamAnalyticsEngine{
		members:    make(map[string]*TeamMember),
		teams:      make(map[string]*Team),
		metrics:    make(map[string][]PerformanceMetrics),
		benchmarks: make(map[string]*Benchmark),
		config:     config,
	}
}

// Member Management

// AddMember adds a team member.
func (e *TeamAnalyticsEngine) AddMember(member *TeamMember) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if member.ID == "" {
		return ErrInvalidMemberID
	}
	if member.Name == "" {
		return ErrInvalidMemberName
	}

	member.JoinedAt = time.Now()
	member.Active = true
	e.members[member.ID] = member
	return nil
}

// GetMember retrieves a team member.
func (e *TeamAnalyticsEngine) GetMember(id string) (*TeamMember, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	member, ok := e.members[id]
	if !ok {
		return nil, ErrMemberNotFound
	}
	return member, nil
}

// ListMembers returns all team members.
func (e *TeamAnalyticsEngine) ListMembers() []*TeamMember {
	e.mu.RLock()
	defer e.mu.RUnlock()

	members := make([]*TeamMember, 0, len(e.members))
	for _, m := range e.members {
		members = append(members, m)
	}
	return members
}

// UpdateMember updates a team member.
func (e *TeamAnalyticsEngine) UpdateMember(id string, updates map[string]interface{}) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	member, ok := e.members[id]
	if !ok {
		return ErrMemberNotFound
	}

	if name, ok := updates["name"].(string); ok {
		member.Name = name
	}
	if role, ok := updates["role"].(MemberRole); ok {
		member.Role = role
	}
	if active, ok := updates["active"].(bool); ok {
		member.Active = active
	}

	return nil
}

// Team Management

// AddTeam adds a team.
func (e *TeamAnalyticsEngine) AddTeam(team *Team) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if team.ID == "" {
		return ErrInvalidTeamID
	}
	if team.Name == "" {
		return ErrInvalidTeamName
	}

	team.CreatedAt = time.Now()
	team.Active = true
	e.teams[team.ID] = team
	return nil
}

// GetTeam retrieves a team.
func (e *TeamAnalyticsEngine) GetTeam(id string) (*Team, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	team, ok := e.teams[id]
	if !ok {
		return nil, ErrTeamNotFound
	}
	return team, nil
}

// AddTeamMember adds a member to a team.
func (e *TeamAnalyticsEngine) AddTeamMember(teamID, memberID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	team, ok := e.teams[teamID]
	if !ok {
		return ErrTeamNotFound
	}

	_, ok = e.members[memberID]
	if !ok {
		return ErrMemberNotFound
	}

	for _, id := range team.MemberIDs {
		if id == memberID {
			return ErrMemberAlreadyInTeam
		}
	}

	team.MemberIDs = append(team.MemberIDs, memberID)
	return nil
}

// Metrics Management

// RecordMetrics records performance metrics for a member.
func (e *TeamAnalyticsEngine) RecordMetrics(metrics PerformanceMetrics) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if metrics.MemberID == "" {
		return ErrInvalidMemberID
	}

	_, ok := e.members[metrics.MemberID]
	if !ok {
		return ErrMemberNotFound
	}

	e.metrics[metrics.MemberID] = append(e.metrics[metrics.MemberID], metrics)
	return nil
}

// GetMemberMetrics retrieves metrics for a member.
func (e *TeamAnalyticsEngine) GetMemberMetrics(memberID string, period string) ([]PerformanceMetrics, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	allMetrics, ok := e.metrics[memberID]
	if !ok {
		return nil, ErrMetricsNotFound
	}

	var filtered []PerformanceMetrics
	for _, m := range allMetrics {
		if m.Period == period {
			filtered = append(filtered, m)
		}
	}

	return filtered, nil
}

// GetLatestMetrics retrieves the latest metrics for a member.
func (e *TeamAnalyticsEngine) GetLatestMetrics(memberID string) (*PerformanceMetrics, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	metrics, ok := e.metrics[memberID]
	if !ok || len(metrics) == 0 {
		return nil, ErrMetricsNotFound
	}

	// Return the most recent
	return &metrics[len(metrics)-1], nil
}

// CalculateTeamMetrics calculates aggregated metrics for a team.
func (e *TeamAnalyticsEngine) CalculateTeamMetrics(teamID string, period string) (*TeamMetrics, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	team, ok := e.teams[teamID]
	if !ok {
		return nil, ErrTeamNotFound
	}

	metrics := make([]*PerformanceMetrics, 0)
	for _, memberID := range team.MemberIDs {
		memberMetrics, ok := e.metrics[memberID]
		if !ok {
			continue
		}
		for _, m := range memberMetrics {
			if m.Period == period {
				metrics = append(metrics, &m)
			}
		}
	}

	if len(metrics) == 0 {
		return nil, ErrMetricsNotFound
	}

	// Calculate aggregates
	result := &TeamMetrics{
		TeamID:    teamID,
		Period:    period,
		StartDate: metrics[0].StartDate,
		EndDate:   metrics[0].EndDate,
	}

	// Velocity
	var totalTasks, totalPoints int
	var velocities []float64
	for _, m := range metrics {
		totalTasks += m.TasksCompleted
		totalPoints += m.PointsCompleted
		velocities = append(velocities, m.Velocity)
	}
	result.TotalTasksCompleted = totalTasks
	result.TotalPointsCompleted = totalPoints
	result.TeamVelocity = e.mean(velocities)

	// Quality
	var bugRates, reviewScores []float64
	for _, m := range metrics {
		bugRates = append(bugRates, m.BugRate)
		reviewScores = append(reviewScores, m.CodeReviewScore)
	}
	result.AvgBugRate = e.mean(bugRates)
	result.AvgCodeReviewScore = e.mean(reviewScores)

	// Efficiency
	var cycleTimes, leadTimes []float64
	for _, m := range metrics {
		cycleTimes = append(cycleTimes, m.CycleTime)
		leadTimes = append(leadTimes, m.LeadTime)
	}
	result.AvgCycleTime = e.mean(cycleTimes)
	result.AvgLeadTime = e.mean(leadTimes)

	// Collaboration
	var reviewsGiven, reviewsReceived int
	activeMembers := 0
	for _, m := range metrics {
		reviewsGiven += m.ReviewsGiven
		reviewsReceived += m.ReviewsReceived
		if m.TasksCompleted > 0 {
			activeMembers++
		}
	}
	result.TotalReviewsGiven = reviewsGiven
	result.TotalReviewsReceived = reviewsReceived
	result.MemberCount = len(team.MemberIDs)
	result.ActiveMembers = activeMembers
	if len(team.MemberIDs) > 0 {
		result.ParticipationRate = float64(activeMembers) / float64(len(team.MemberIDs))
	}

	// Calculate collaboration score (simple formula)
	result.CollaborationScore = e.calculateCollaborationScore(reviewsGiven, reviewsReceived, len(team.MemberIDs))

	return result, nil
}

// GenerateReport generates a performance report.
func (e *TeamAnalyticsEngine) GenerateReport(ctx context.Context, reportType, subjectID string, period string) (*PerformanceReport, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	switch reportType {
	case "individual":
		return e.generateIndividualReport(subjectID, period)
	case "team":
		return e.generateTeamReport(subjectID, period)
	default:
		return nil, ErrInvalidReportType
	}
}

// generateIndividualReport generates an individual performance report.
func (e *TeamAnalyticsEngine) generateIndividualReport(memberID, period string) (*PerformanceReport, error) {
	member, ok := e.members[memberID]
	if !ok {
		return nil, ErrMemberNotFound
	}

	metrics, err := e.GetMemberMetrics(memberID, period)
	if err != nil {
		return nil, err
	}

	if len(metrics) == 0 {
		return nil, ErrMetricsNotFound
	}

	// Calculate average metrics
	var avgMetrics PerformanceMetrics
	for _, m := range metrics {
		avgMetrics.TasksCompleted += m.TasksCompleted
		avgMetrics.PointsCompleted += m.PointsCompleted
		avgMetrics.Velocity += m.Velocity
		avgMetrics.BugRate += m.BugRate
		avgMetrics.CodeReviewScore += m.CodeReviewScore
		avgMetrics.PRMergeRate += m.PRMergeRate
		avgMetrics.CycleTime += m.CycleTime
		avgMetrics.LeadTime += m.LeadTime
		avgMetrics.ReviewsGiven += m.ReviewsGiven
		avgMetrics.ReviewsReceived += m.ReviewsReceived
		avgMetrics.CommentsAdded += m.CommentsAdded
	}
	n := float64(len(metrics))
	avgMetrics.TasksCompleted = int(float64(avgMetrics.TasksCompleted) / n)
	avgMetrics.PointsCompleted = int(float64(avgMetrics.PointsCompleted) / n)
	avgMetrics.Velocity /= n
	avgMetrics.BugRate /= n
	avgMetrics.CodeReviewScore /= n
	avgMetrics.PRMergeRate /= n
	avgMetrics.CycleTime /= n
	avgMetrics.LeadTime /= n
	avgMetrics.ReviewsGiven = int(float64(avgMetrics.ReviewsGiven) / n)
	avgMetrics.ReviewsReceived = int(float64(avgMetrics.ReviewsReceived) / n)
	avgMetrics.CommentsAdded = int(float64(avgMetrics.CommentsAdded) / n)

	// Calculate overall score
	overallScore := e.calculateOverallScore(&avgMetrics)

	// Generate highlights and improvements
	highlights := e.generateHighlights(&avgMetrics)
	improvements := e.generateImprovements(&avgMetrics)

	// Get benchmarks
	benchmarks := e.compareToBenchmarks(&avgMetrics)

	// Generate trend analysis
	trends := e.generateTrendAnalysis(metrics)

	return &PerformanceReport{
		ID:            generateID(),
		Type:          "individual",
		SubjectID:     memberID,
		SubjectName:  member.Name,
		Period:       period,
		StartDate:    metrics[0].StartDate,
		EndDate:      metrics[len(metrics)-1].EndDate,
		OverallScore: overallScore,
		Highlights:   highlights,
		Improvements: improvements,
		Metrics:      avgMetrics,
		Benchmarks:   benchmarks,
		TrendAnalysis: trends,
		GeneratedAt:  time.Now(),
	}, nil
}

// generateTeamReport generates a team performance report.
func (e *TeamAnalyticsEngine) generateTeamReport(teamID, period string) (*PerformanceReport, error) {
	team, ok := e.teams[teamID]
	if !ok {
		return nil, ErrTeamNotFound
	}

	metrics, err := e.CalculateTeamMetrics(teamID, period)
	if err != nil {
		return nil, err
	}

	overallScore := e.calculateTeamScore(metrics)

	highlights := e.generateTeamHighlights(metrics)
	improvements := e.generateTeamImprovements(metrics)

	// Get benchmark comparisons
	var benchmarkScores []BenchmarkScore
	for _, b := range e.benchmarks {
		if b.TeamID == "" || b.TeamID == teamID {
			var actualValue float64
			switch b.MetricType {
			case "velocity":
				actualValue = metrics.TeamVelocity
			case "bug_rate":
				actualValue = metrics.AvgBugRate
			case "code_review_score":
				actualValue = metrics.AvgCodeReviewScore
			case "collaboration":
				actualValue = metrics.CollaborationScore
			}
			benchmarkScores = append(benchmarkScores, BenchmarkScore{
				BenchmarkID:   b.ID,
				BenchmarkName:  b.Name,
				MetricType:    b.MetricType,
				ActualValue:   actualValue,
				BenchmarkValue: b.TargetValue,
				Score:         (actualValue - b.TargetValue) / b.TargetValue,
			})
		}
	}

	return &PerformanceReport{
		ID:            generateID(),
		Type:          "team",
		SubjectID:     teamID,
		SubjectName:  team.Name,
		Period:       period,
		StartDate:    metrics.StartDate,
		EndDate:      metrics.EndDate,
		OverallScore: overallScore,
		Highlights:   highlights,
		Improvements: improvements,
		TeamMetrics:  metrics,
		Benchmarks:  benchmarkScores,
		GeneratedAt: time.Now(),
	}, nil
}

// Leaderboards

// GetLeaderboard returns a leaderboard for a metric.
func (e *TeamAnalyticsEngine) GetLeaderboard(metricType, period string, limit int) ([]LeaderboardEntry, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	entries := make([]LeaderboardEntry, 0)

	for memberID, metrics := range e.metrics {
		member, ok := e.members[memberID]
		if !ok {
			continue
		}

		var score float64
		for _, m := range metrics {
			if m.Period == period {
				switch metricType {
				case "velocity":
					score = m.Velocity
				case "quality":
					score = m.CodeReviewScore
				case "efficiency":
					score = 100 - m.CycleTime // Lower is better, so invert
				case "collaboration":
					score = float64(m.ReviewsGiven + m.ReviewsReceived)
				}
				break
			}
		}

		if score != 0 {
			entries = append(entries, LeaderboardEntry{
				MemberID:   memberID,
				MemberName: member.Name,
				TeamID:    member.TeamID,
				Score:     score,
				MetricType: metricType,
				Period:    period,
			})
		}
	}

	// Sort by score descending
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Score > entries[j].Score
	})

	// Add ranks
	for i := range entries {
		entries[i].Rank = i + 1
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	return entries, nil
}

// Benchmark Management

// AddBenchmark adds a performance benchmark.
func (e *TeamAnalyticsEngine) AddBenchmark(benchmark *Benchmark) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if benchmark.ID == "" {
		benchmark.ID = generateID()
	}
	benchmark.CreatedAt = time.Now()
	benchmark.UpdatedAt = benchmark.CreatedAt
	e.benchmarks[benchmark.ID] = benchmark
	return nil
}

// GetBenchmark retrieves a benchmark.
func (e *TeamAnalyticsEngine) GetBenchmark(id string) (*Benchmark, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	b, ok := e.benchmarks[id]
	if !ok {
		return nil, ErrBenchmarkNotFound
	}
	return b, nil
}

// Helper functions

func (e *TeamAnalyticsEngine) mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func (e *TeamAnalyticsEngine) calculateOverallScore(metrics *PerformanceMetrics) float64 {
	// Weighted average of different aspects
	velocityScore := math.Min(metrics.Velocity/10*100, 100) // Normalize velocity
	qualityScore := metrics.CodeReviewScore
	efficiencyScore := math.Min((100-metrics.CycleTime/24*100), 100)
	collaborationScore := math.Min(float64(metrics.ReviewsGiven+metrics.ReviewsReceived)/50*100, 100)

	score := velocityScore*e.config.VelocityWeight +
		qualityScore*e.config.QualityWeight +
		efficiencyScore*e.config.EfficiencyWeight +
		collaborationScore*e.config.CollaborationWeight

	return math.Round(score)
}

func (e *TeamAnalyticsEngine) calculateTeamScore(metrics *TeamMetrics) float64 {
	velocityScore := math.Min(metrics.TeamVelocity/10*100, 100)
	qualityScore := metrics.AvgCodeReviewScore
	efficiencyScore := math.Min((100-metrics.AvgCycleTime/24*100), 100)
	collaborationScore := metrics.CollaborationScore

	score := velocityScore*e.config.VelocityWeight +
		qualityScore*e.config.QualityWeight +
		efficiencyScore*e.config.EfficiencyWeight +
		collaborationScore*e.config.CollaborationWeight

	return math.Round(score)
}

func (e *TeamAnalyticsEngine) calculateCollaborationScore(reviewsGiven, reviewsReceived, memberCount int) float64 {
	if memberCount == 0 {
		return 0
	}
	totalReviews := reviewsGiven + reviewsReceived
	return math.Min(float64(totalReviews)/float64(memberCount)/10*100, 100)
}

func (e *TeamAnalyticsEngine) generateHighlights(metrics *PerformanceMetrics) []string {
	highlights := make([]string, 0)

	if metrics.Velocity > 8 {
		highlights = append(highlights, "Excellent velocity consistently above team average")
	}
	if metrics.CodeReviewScore > 90 {
		highlights = append(highlights, "Outstanding code review quality score")
	}
	if metrics.PRMergeRate > 0.9 {
		highlights = append(highlights, "High PR merge rate indicating quality contributions")
	}
	if metrics.ReviewsGiven > 20 {
		highlights = append(highlights, "Strong contributor to team reviews")
	}

	return highlights
}

func (e *TeamAnalyticsEngine) generateImprovements(metrics *PerformanceMetrics) []string {
	improvements := make([]string, 0)

	if metrics.BugRate > 5 {
		improvements = append(improvements, "Consider focus on bug prevention techniques")
	}
	if metrics.CycleTime > 48 {
		improvements = append(improvements, "Look for opportunities to reduce cycle time")
	}
	if metrics.ReviewsGiven < 5 {
		improvements = append(improvements, "Increase participation in code reviews")
	}

	return improvements
}

func (e *TeamAnalyticsEngine) generateTeamHighlights(metrics *TeamMetrics) []string {
	highlights := make([]string, 0)

	if metrics.VelocityTrend == "up" {
		highlights = append(highlights, "Team velocity showing positive trend")
	}
	if metrics.ParticipationRate > 0.9 {
		highlights = append(highlights, "Excellent team participation rate")
	}
	if metrics.CollaborationScore > 80 {
		highlights = append(highlights, "Strong collaboration culture")
	}

	return highlights
}

func (e *TeamAnalyticsEngine) generateTeamImprovements(metrics *TeamMetrics) []string {
	improvements := make([]string, 0)

	if metrics.AvgBugRate > 5 {
		improvements = append(improvements, "Team should focus on quality improvements")
	}
	if metrics.ParticipationRate < 0.7 {
		improvements = append(improvements, "Increase team participation across all members")
	}

	return improvements
}

func (e *TeamAnalyticsEngine) compareToBenchmarks(metrics *PerformanceMetrics) []BenchmarkScore {
	scores := make([]BenchmarkScore, 0)

	for _, b := range e.benchmarks {
		if b.TeamID != "" {
			continue // Skip team-specific benchmarks
		}

		var actualValue float64
		switch b.MetricType {
		case "velocity":
			actualValue = metrics.Velocity
		case "bug_rate":
			actualValue = metrics.BugRate
		case "code_review_score":
			actualValue = metrics.CodeReviewScore
		case "efficiency":
			actualValue = metrics.CycleTime
		}

		score := (actualValue - b.TargetValue) / b.TargetValue
		if b.MetricType == "bug_rate" || b.MetricType == "efficiency" {
			score = -score // Lower is better for these
		}

		scores = append(scores, BenchmarkScore{
			BenchmarkID:   b.ID,
			BenchmarkName: b.Name,
			MetricType:   b.MetricType,
			ActualValue:  actualValue,
			BenchmarkValue: b.TargetValue,
			Score:        score,
		})
	}

	return scores
}

func (e *TeamAnalyticsEngine) generateTrendAnalysis(metrics []PerformanceMetrics) []TrendPoint {
	if len(metrics) < 2 {
		return nil
	}

	trends := make([]TrendPoint, 0)
	for i, m := range metrics {
		var change float64
		var trend string
		if i > 0 {
			prev := metrics[i-1]
			if prev.Velocity > 0 {
				change = (m.Velocity - prev.Velocity) / prev.Velocity * 100
			}
			if change > 5 {
				trend = "up"
			} else if change < -5 {
				trend = "down"
			} else {
				trend = "stable"
			}
		}

		trends = append(trends, TrendPoint{
			Period: m.Period,
			Value:  m.Velocity,
			Change: change,
			Trend:  trend,
		})
	}

	return trends
}

// Errors

var (
	ErrInvalidMemberID      = &ValidationError{Message: "invalid member ID"}
	ErrInvalidMemberName    = &ValidationError{Message: "invalid member name"}
	ErrInvalidTeamID        = &ValidationError{Message: "invalid team ID"}
	ErrInvalidTeamName      = &ValidationError{Message: "invalid team name"}
	ErrMemberNotFound       = &ValidationError{Message: "member not found"}
	ErrTeamNotFound         = &ValidationError{Message: "team not found"}
	ErrMemberAlreadyInTeam  = &ValidationError{Message: "member already in team"}
	ErrMetricsNotFound      = &ValidationError{Message: "metrics not found"}
	ErrBenchmarkNotFound    = &ValidationError{Message: "benchmark not found"}
	ErrInvalidReportType    = &ValidationError{Message: "invalid report type"}
)

// ValidationError represents a validation error.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// Helper for generating IDs
func generateID() string {
	b := make([]byte, 8)
	for i := range b {
		b[i] = "abcdefghijklmnop"[time.Now().UnixNano()%16]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
