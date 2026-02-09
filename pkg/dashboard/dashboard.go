package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nexus/nexus/pkg/feedback"
	"github.com/nexus/nexus/pkg/metrics"
)

// TimeRange represents the time range for dashboard queries
type TimeRange string

const (
	TimeRange7Days  TimeRange = "7d"
	TimeRange30Days TimeRange = "30d"
	TimeRange90Days TimeRange = "90d"
	TimeRangeCustom TimeRange = "custom"
)

// DashboardData represents the full dashboard response
type DashboardData struct {
	Timestamp time.Time         `json:"timestamp"`
	Metrics   *DashboardMetrics `json:"metrics"`
	Trends    *DashboardTrends  `json:"trends"`
	Charts    *DashboardCharts  `json:"charts"`
	Alerts    []Alert          `json:"alerts"`
}

// DashboardMetrics contains core metrics for the dashboard
type DashboardMetrics struct {
	TotalSessions      int     `json:"total_sessions"`
	ActiveUsers        int     `json:"active_users"`
	AvgSatisfaction    float64 `json:"avg_satisfaction"`
	AvgSessionDuration float64 `json:"avg_session_duration"`
	TotalFeedback      int     `json:"total_feedback"`
	TasksCreated       int     `json:"tasks_created"`
	TasksCompleted     int     `json:"tasks_completed"`
}

// DashboardTrends contains trend analysis data
type DashboardTrends struct {
	SessionsTrend     TrendData `json:"sessions_trend"`
	SatisfactionTrend TrendData `json:"satisfaction_trend"`
	FeedbackTrend     TrendData `json:"feedback_trend"`
	DurationTrend     TrendData `json:"duration_trend"`
}

// TrendData represents a single trend with change analysis
type TrendData struct {
	Current   float64 `json:"current"`
	Previous  float64 `json:"previous"`
	Change    float64 `json:"change"`
	ChangePct float64 `json:"change_pct"`
	Direction string  `json:"direction"` // "up", "down", "stable"
}

// DashboardCharts contains all chart data for visualizations
type DashboardCharts struct {
	SkillsBarChart    BarChartData   `json:"skills_bar_chart"`
	SatisfactionChart PieChartData   `json:"satisfaction_chart"`
	TimelineChart     TimelineData   `json:"timeline_chart"`
	HeatmapData       []HeatmapRow   `json:"heatmap_data"`
}

// BarChartData represents bar chart data
type BarChartData struct {
	Labels []string  `json:"labels"`
	Values []float64 `json:"values"`
	Colors []string  `json:"colors"`
}

// PieChartData represents pie/doughnut chart data
type PieChartData struct {
	Labels      []string  `json:"labels"`
	Values      []float64 `json:"values"`
	Colors      []string  `json:"colors"`
	CenterLabel string    `json:"center_label,omitempty"`
}

// TimelineData represents time series data for line/area charts
type TimelineData struct {
	Labels   []string          `json:"labels"`
	Datasets []TimelineDataset `json:"datasets"`
}

// TimelineDataset represents a single line in the timeline chart
type TimelineDataset struct {
	Label   string    `json:"label"`
	Values  []float64 `json:"values"`
	Color   string    `json:"color"`
}

// HeatmapRow represents a row in the heatmap
type HeatmapRow struct {
	Day     string  `json:"day"`
	Hour    int     `json:"hour"`
	Session int     `json:"session"`
	Value   float64 `json:"value"`
}

// Alert represents a dashboard alert
type Alert struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`       // "warning", "info", "success", "error"
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Severity  string    `json:"severity"` // "low", "medium", "high"
}

// ExportFormat represents the export format
type ExportFormat string

const (
	ExportJSON      ExportFormat = "json"
	ExportCSV       ExportFormat = "csv"
	ExportPrometheus ExportFormat = "prometheus"
)

// DashboardService provides dashboard data and operations
type DashboardService struct {
	metricsAnalyzer   *metrics.Analyzer
	feedbackCollector *feedback.FeedbackCollector
}

// NewDashboardService creates a new dashboard service
func NewDashboardService(analyzer *metrics.Analyzer, collector *feedback.FeedbackCollector) *DashboardService {
	return &DashboardService{
		metricsAnalyzer:   analyzer,
		feedbackCollector: collector,
	}
}

// GetDashboardData returns complete dashboard data for visualization
func (s *DashboardService) GetDashboardData(rangeTime TimeRange, startDate, endDate time.Time) (*DashboardData, error) {
	metrics, err := s.GetMetrics(rangeTime, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics: %w", err)
	}

	trends, err := s.GetTrends(rangeTime, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to get trends: %w", err)
	}

	charts, err := s.GetCharts(rangeTime, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to get charts: %w", err)
	}

	alerts, err := s.GetAlerts(rangeTime, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to get alerts: %w", err)
	}

	return &DashboardData{
		Timestamp: time.Now(),
		Metrics:   metrics,
		Trends:    trends,
		Charts:    charts,
		Alerts:    alerts,
	}, nil
}

// GetMetrics returns dashboard metrics
func (s *DashboardService) GetMetrics(rangeTime TimeRange, startDate, endDate time.Time) (*DashboardMetrics, error) {
	// Calculate date range based on TimeRange
	start, end := calculateDateRange(rangeTime, startDate, endDate)
	_ = start
	_ = end

	// Placeholder implementation - in real usage, these would query the database
	metrics := &DashboardMetrics{
		TotalSessions:      156,
		ActiveUsers:        42,
		AvgSatisfaction:    4.2,
		AvgSessionDuration: 45.5,
		TotalFeedback:      89,
		TasksCreated:       234,
		TasksCompleted:     198,
	}

	return metrics, nil
}

// GetTrends returns trend analysis data
func (s *DashboardService) GetTrends(rangeTime TimeRange, startDate, endDate time.Time) (*DashboardTrends, error) {
	trends := &DashboardTrends{
		SessionsTrend: TrendData{
			Current:   156,
			Previous:  142,
			Change:    14,
			ChangePct: 9.86,
			Direction: "up",
		},
		SatisfactionTrend: TrendData{
			Current:   4.2,
			Previous:  4.0,
			Change:    0.2,
			ChangePct: 5.0,
			Direction: "up",
		},
		FeedbackTrend: TrendData{
			Current:   89,
			Previous:  95,
			Change:    -6,
			ChangePct: -6.32,
			Direction: "down",
		},
		DurationTrend: TrendData{
			Current:   45.5,
			Previous:  42.3,
			Change:    3.2,
			ChangePct: 7.57,
			Direction: "up",
		},
	}

	return trends, nil
}

// GetCharts returns chart data for visualizations
func (s *DashboardService) GetCharts(rangeTime TimeRange, startDate, endDate time.Time) (*DashboardCharts, error) {
	charts := &DashboardCharts{
		SkillsBarChart: BarChartData{
			Labels: []string{"executor", "explore", "designer", "researcher", "qa-tester", "git-master"},
			Values: []float64{45, 32, 28, 22, 18, 15},
			Colors: []string{
				"#6366f1",
				"#8b5cf6",
				"#a855f7",
				"#d946ef",
				"#ec4899",
				"#f43f5e",
			},
		},
		SatisfactionChart: PieChartData{
			Labels:   []string{"Very Satisfied", "Satisfied", "Neutral", "Dissatisfied", "Very Dissatisfied"},
			Values:   []float64{35, 42, 15, 6, 2},
			Colors:   []string{"#22c55e", "#84cc16", "#eab308", "#f97316", "#ef4444"},
			CenterLabel: "Satisfaction",
		},
		TimelineChart: TimelineData{
			Labels: []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"},
			Datasets: []TimelineDataset{
				{
					Label:  "Sessions",
					Values: []float64{22, 28, 25, 32, 30, 15, 12},
					Color:  "#6366f1",
				},
				{
					Label:  "Tasks",
					Values: []float64{18, 24, 21, 28, 26, 12, 10},
					Color:  "#22c55e",
				},
			},
		},
		HeatmapData: []HeatmapRow{
			{Day: "Monday", Hour: 9, Session: 5, Value: 0.8},
			{Day: "Monday", Hour: 10, Session: 8, Value: 1.0},
			{Day: "Monday", Hour: 11, Session: 7, Value: 0.9},
			{Day: "Tuesday", Hour: 9, Session: 4, Value: 0.7},
			{Day: "Tuesday", Hour: 10, Session: 9, Value: 1.0},
			{Day: "Tuesday", Hour: 14, Session: 6, Value: 0.85},
			{Day: "Wednesday", Hour: 10, Session: 7, Value: 0.9},
			{Day: "Wednesday", Hour: 11, Session: 8, Value: 1.0},
			{Day: "Thursday", Hour: 9, Session: 6, Value: 0.85},
			{Day: "Friday", Hour: 10, Session: 5, Value: 0.75},
			{Day: "Saturday", Hour: 11, Session: 3, Value: 0.5},
			{Day: "Sunday", Hour: 12, Session: 2, Value: 0.4},
		},
	}

	return charts, nil
}

// GetAlerts returns dashboard alerts
func (s *DashboardService) GetAlerts(rangeTime TimeRange, startDate, endDate time.Time) ([]Alert, error) {
	alerts := []Alert{
		{
			ID:        "alert-1",
			Type:      "success",
			Message:   "Task completion rate improved by 12%",
			Timestamp: time.Now(),
			Severity:  "low",
		},
		{
			ID:        "alert-2",
			Type:      "info",
			Message:   "New skill usage increased: designer (+25%)",
			Timestamp: time.Now().Add(-1 * time.Hour),
			Severity:  "low",
		},
		{
			ID:        "alert-3",
			Type:      "warning",
			Message:   "Session duration decreased during weekends",
			Timestamp: time.Now().Add(-2 * time.Hour),
			Severity:  "medium",
		},
	}

	return alerts, nil
}

// ExportData exports dashboard data in the specified format
func (s *DashboardService) ExportData(data *DashboardData, format ExportFormat) ([]byte, error) {
	switch format {
	case ExportJSON:
		return json.MarshalIndent(data, "", "  ")
	case ExportCSV:
		return exportToCSV(data)
	case ExportPrometheus:
		return exportToPrometheus(data)
	default:
		return json.MarshalIndent(data, "", "  ")
	}
}

// exportToCSV exports dashboard data to CSV format
func exportToCSV(data *DashboardData) ([]byte, error) {
	var sb strings.Builder

	// Metrics section
	sb.WriteString("=== Metrics ===\n")
	sb.WriteString("Metric,Value\n")
	sb.WriteString(fmt.Sprintf("Total Sessions,%d\n", data.Metrics.TotalSessions))
	sb.WriteString(fmt.Sprintf("Active Users,%d\n", data.Metrics.ActiveUsers))
	sb.WriteString(fmt.Sprintf("Avg Satisfaction,%.2f\n", data.Metrics.AvgSatisfaction))
	sb.WriteString(fmt.Sprintf("Avg Session Duration,%.2f\n", data.Metrics.AvgSessionDuration))
	sb.WriteString(fmt.Sprintf("Total Feedback,%d\n", data.Metrics.TotalFeedback))
	sb.WriteString(fmt.Sprintf("Tasks Created,%d\n", data.Metrics.TasksCreated))
	sb.WriteString(fmt.Sprintf("Tasks Completed,%d\n", data.Metrics.TasksCompleted))

	// Trends section
	sb.WriteString("\n=== Trends ===\n")
	sb.WriteString("Trend,Current,Previous,Change,ChangePct,Direction\n")
	trends := []struct {
		Name string
		Data TrendData
	}{
		{"Sessions", data.Trends.SessionsTrend},
		{"Satisfaction", data.Trends.SatisfactionTrend},
		{"Feedback", data.Trends.FeedbackTrend},
		{"Duration", data.Trends.DurationTrend},
	}
	for _, t := range trends {
		sb.WriteString(fmt.Sprintf("%s,%.2f,%.2f,%.2f,%.2f,%s\n",
			t.Name, t.Data.Current, t.Data.Previous, t.Data.Change, t.Data.ChangePct, t.Data.Direction))
	}

	// Skills Chart
	sb.WriteString("\n=== Skills Distribution ===\n")
	sb.WriteString("Skill,Count\n")
	for i, label := range data.Charts.SkillsBarChart.Labels {
		sb.WriteString(fmt.Sprintf("%s,%.0f\n", label, data.Charts.SkillsBarChart.Values[i]))
	}

	return []byte(sb.String()), nil
}

// exportToPrometheus exports dashboard data in Prometheus metrics format
func exportToPrometheus(data *DashboardData) ([]byte, error) {
	var sb strings.Builder

	// Metrics
	sb.WriteString("# Nexus Dashboard Metrics\n")
	sb.WriteString(fmt.Sprintf("# Total sessions: %d\n", data.Metrics.TotalSessions))
	sb.WriteString(fmt.Sprintf("# Active users: %d\n", data.Metrics.ActiveUsers))

	sb.WriteString("\n# HELP nexus_dashboard_satisfaction_avg Average satisfaction score\n")
	sb.WriteString("# TYPE nexus_dashboard_satisfaction_avg gauge\n")
	sb.WriteString(fmt.Sprintf("nexus_dashboard_satisfaction_avg %.2f\n", data.Metrics.AvgSatisfaction))

	sb.WriteString("\n# HELP nexus_dashboard_session_duration_avg Average session duration in minutes\n")
	sb.WriteString("# TYPE nexus_dashboard_session_duration_avg gauge\n")
	sb.WriteString(fmt.Sprintf("nexus_dashboard_session_duration_avg %.2f\n", data.Metrics.AvgSessionDuration))

	sb.WriteString("\n# HELP nexus_dashboard_tasks_created_total Total tasks created\n")
	sb.WriteString("# TYPE nexus_dashboard_tasks_created_total counter\n")
	sb.WriteString(fmt.Sprintf("nexus_dashboard_tasks_created_total %d\n", data.Metrics.TasksCreated))

	sb.WriteString("\n# HELP nexus_dashboard_tasks_completed_total Total tasks completed\n")
	sb.WriteString("# TYPE nexus_dashboard_tasks_completed_total counter\n")
	sb.WriteString(fmt.Sprintf("nexus_dashboard_tasks_completed_total %d\n", data.Metrics.TasksCompleted))

	sb.WriteString("\n# HELP nexus_dashboard_feedback_total Total feedback received\n")
	sb.WriteString("# TYPE nexus_dashboard_feedback_total counter\n")
	sb.WriteString(fmt.Sprintf("nexus_dashboard_feedback_total %d\n", data.Metrics.TotalFeedback))

	// Trends as gauges with direction
	sb.WriteString("\n# Trend metrics with direction indicators\n")
	sb.WriteString("# TYPE nexus_dashboard_sessions_trend gauge\n")
	sb.WriteString(fmt.Sprintf("nexus_dashboard_sessions_trend{type=\"current\"} %.2f\n", data.Trends.SessionsTrend.Current))
	sb.WriteString(fmt.Sprintf("nexus_dashboard_sessions_trend{type=\"previous\"} %.2f\n", data.Trends.SessionsTrend.Previous))

	// Skills
	sb.WriteString("\n# Skill usage metrics\n")
	for i, label := range data.Charts.SkillsBarChart.Labels {
		metricName := "nexus_skill_usage_" + strings.ReplaceAll(label, "-", "_")
		sb.WriteString(fmt.Sprintf("# HELP %s Skill usage count\n", metricName))
		sb.WriteString(fmt.Sprintf("# TYPE %s counter\n", metricName))
		sb.WriteString(fmt.Sprintf("%s %.0f\n", metricName, data.Charts.SkillsBarChart.Values[i]))
	}

	return []byte(sb.String()), nil
}

// calculateDateRange calculates start and end dates based on TimeRange
func calculateDateRange(rangeTime TimeRange, startDate, endDate time.Time) (time.Time, time.Time) {
	now := time.Now()

	switch rangeTime {
	case TimeRange7Days:
		return now.AddDate(0, 0, -7), now
	case TimeRange30Days:
		return now.AddDate(0, 0, -30), now
	case TimeRange90Days:
		return now.AddDate(0, 0, -90), now
	case TimeRangeCustom:
		return startDate, endDate
	default:
		return now.AddDate(0, 0, -7), now
	}
}

// ParseTimeRange parses a time range string
func ParseTimeRange(s string) TimeRange {
	switch strings.ToLower(s) {
	case "7d", "7days":
		return TimeRange7Days
	case "30d", "30days":
		return TimeRange30Days
	case "90d", "90days":
		return TimeRange90Days
	case "custom":
		return TimeRangeCustom
	default:
		return TimeRange7Days // default to 7 days
	}
}

// Handler returns HTTP handlers for dashboard API
func Handler(svc *DashboardService) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/dashboard", func(w http.ResponseWriter, r *http.Request) {
		rangeTime := ParseTimeRange(r.URL.Query().Get("range"))
		startDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("start_date"))
		endDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("end_date"))

		data, err := svc.GetDashboardData(rangeTime, startDate, endDate)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get dashboard data: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	})

	mux.HandleFunc("/api/dashboard/metrics", func(w http.ResponseWriter, r *http.Request) {
		rangeTime := ParseTimeRange(r.URL.Query().Get("range"))
		startDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("start_date"))
		endDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("end_date"))

		metrics, err := svc.GetMetrics(rangeTime, startDate, endDate)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get metrics: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metrics)
	})

	mux.HandleFunc("/api/dashboard/trends", func(w http.ResponseWriter, r *http.Request) {
		rangeTime := ParseTimeRange(r.URL.Query().Get("range"))
		startDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("start_date"))
		endDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("end_date"))

		trends, err := svc.GetTrends(rangeTime, startDate, endDate)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get trends: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(trends)
	})

	mux.HandleFunc("/api/dashboard/charts", func(w http.ResponseWriter, r *http.Request) {
		rangeTime := ParseTimeRange(r.URL.Query().Get("range"))
		startDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("start_date"))
		endDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("end_date"))

		charts, err := svc.GetCharts(rangeTime, startDate, endDate)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get charts: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(charts)
	})

	mux.HandleFunc("/api/dashboard/export", func(w http.ResponseWriter, r *http.Request) {
		rangeTime := ParseTimeRange(r.URL.Query().Get("range"))
		startDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("start_date"))
		endDate, _ := time.Parse("2006-01-02", r.URL.Query().Get("end_date"))

		format := ExportFormat(strings.ToLower(r.URL.Query().Get("format")))
		if format == "" {
			format = ExportJSON
		}

		data, err := svc.GetDashboardData(rangeTime, startDate, endDate)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get dashboard data: %v", err), http.StatusInternalServerError)
			return
		}

		export, err := svc.ExportData(data, format)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to export data: %v", err), http.StatusInternalServerError)
			return
		}

		switch format {
		case ExportCSV:
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", "attachment; filename=dashboard.csv")
		case ExportPrometheus:
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", "attachment; filename=dashboard.prom")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", "attachment; filename=dashboard.json")
		}

		w.Write(export)
	})

	return mux
}

// CalculateTrend calculates trend data from current and previous values
func CalculateTrend(current, previous float64) TrendData {
	var change, changePct float64
	var direction string

	if previous != 0 {
		change = current - previous
		changePct = (change / previous) * 100
	} else if current > 0 {
		// No previous data but have current - consider as growth
		change = current
		changePct = 100
		direction = "up"
	}

	// Determine direction only if we haven't set it already
	if direction == "" {
		if change > 1.0 {
			direction = "up"
		} else if change < -1.0 {
			direction = "down"
		} else {
			direction = "stable"
		}
	}

	return TrendData{
		Current:   current,
		Previous:  previous,
		Change:    change,
		ChangePct: changePct,
		Direction: direction,
	}
}

// TrendsFromCounts creates trends from current and previous count values
func TrendsFromCounts(current, previous int) TrendData {
	return CalculateTrend(float64(current), float64(previous))
}

// ValidateExportFormat validates the export format parameter
func ValidateExportFormat(format string) (ExportFormat, bool) {
	switch ExportFormat(format) {
	case ExportJSON, ExportCSV, ExportPrometheus:
		return ExportFormat(format), true
	default:
		return "", false
	}
}

// GenerateSkillsBarChart generates bar chart data from skill usage map
func GenerateSkillsBarChart(skillUsage map[string]int) BarChartData {
	colors := []string{
		"#6366f1", "#8b5cf6", "#a855f7", "#d946ef", "#ec4899",
		"#f43f5e", "#f97316", "#eab308", "#84cc16", "#22c55e",
		"#14b8a6", "#06b6d4", "#0ea5e9", "#3b82f6", "#6366f1",
	}

	labels := make([]string, 0, len(skillUsage))
	values := make([]float64, 0, len(skillUsage))

	for skill, count := range skillUsage {
		labels = append(labels, skill)
		values = append(values, float64(count))
	}

	chartColors := make([]string, len(labels))
	for i := range chartColors {
		chartColors[i] = colors[i%len(colors)]
	}

	return BarChartData{
		Labels: labels,
		Values: values,
		Colors: chartColors,
	}
}

// GenerateHeatmapData generates heatmap data from session logs
func GenerateHeatmapData(sessions []SessionLog) []HeatmapRow {
	heatmap := make(map[string]map[int]int)

	for _, s := range sessions {
		day := s.Timestamp.Format("Monday")
		hour := s.Timestamp.Hour()
		if heatmap[day] == nil {
			heatmap[day] = make(map[int]int)
		}
		heatmap[day][hour]++
	}

	rows := make([]HeatmapRow, 0)
	maxSessions := 0
	for _, hours := range heatmap {
		for _, count := range hours {
			if count > maxSessions {
				maxSessions = count
			}
		}
	}

	for day, hours := range heatmap {
		for hour, count := range hours {
			var value float64
			if maxSessions > 0 {
				value = float64(count) / float64(maxSessions)
			}
			rows = append(rows, HeatmapRow{
				Day:     day,
				Hour:    hour,
				Session: count,
				Value:   value,
			})
		}
	}

	return rows
}

// SessionLog represents a session for heatmap generation
type SessionLog struct {
	Timestamp time.Time
	SessionID string
}
