package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardData_Structure(t *testing.T) {
	data := &DashboardData{
		Timestamp: time.Now(),
		Metrics: &DashboardMetrics{
			TotalSessions:      100,
			ActiveUsers:         25,
			AvgSatisfaction:    4.5,
			AvgSessionDuration: 30.0,
			TotalFeedback:      50,
			TasksCreated:       120,
			TasksCompleted:     100,
		},
		Trends: &DashboardTrends{
			SessionsTrend: TrendData{
				Current:   100,
				Previous:  90,
				Change:    10,
				ChangePct: 11.11,
				Direction: "up",
			},
		},
		Charts: &DashboardCharts{
			SkillsBarChart: BarChartData{
				Labels: []string{"executor", "explore"},
				Values: []float64{45, 32},
				Colors: []string{"#6366f1", "#8b5cf6"},
			},
		},
		Alerts: []Alert{
			{ID: "test-alert", Type: "info", Message: "Test alert"},
		},
	}

	assert.Equal(t, 100, data.Metrics.TotalSessions)
	assert.Equal(t, 25, data.Metrics.ActiveUsers)
	assert.Equal(t, 4.5, data.Metrics.AvgSatisfaction)
	assert.Equal(t, "up", data.Trends.SessionsTrend.Direction)
	assert.Len(t, data.Charts.SkillsBarChart.Labels, 2)
	assert.Len(t, data.Alerts, 1)
}

func TestTrendData_Calculation(t *testing.T) {
	tests := []struct {
		name     string
		current  float64
		previous float64
		expected TrendData
	}{
		{
			name:     "positive change",
			current:  150,
			previous:  100,
			expected: TrendData{Current: 150, Previous: 100, Change: 50, ChangePct: 50, Direction: "up"},
		},
		{
			name:     "negative change",
			current:  80,
			previous:  100,
			expected: TrendData{Current: 80, Previous: 100, Change: -20, ChangePct: -20, Direction: "down"},
		},
		{
			name:     "stable (small change)",
			current:  101,
			previous:  100,
			expected: TrendData{Current: 101, Previous: 100, Change: 1, ChangePct: 1, Direction: "stable"},
		},
		{
			name:     "zero previous with current",
			current:  50,
			previous:  0,
			expected: TrendData{Current: 50, Previous: 0, Change: 50, ChangePct: 100, Direction: "up"},
		},
		{
			name:     "zero previous and current",
			current:  0,
			previous:  0,
			expected: TrendData{Current: 0, Previous: 0, Change: 0, ChangePct: 0, Direction: "stable"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateTrend(tt.current, tt.previous)
			assert.Equal(t, tt.expected.Current, result.Current)
			assert.Equal(t, tt.expected.Previous, result.Previous)
			assert.Equal(t, tt.expected.Change, result.Change)
			assert.Equal(t, tt.expected.ChangePct, result.ChangePct)
			assert.Equal(t, tt.expected.Direction, result.Direction)
		})
	}
}

func TestTrendsFromCounts(t *testing.T) {
	trend := TrendsFromCounts(150, 100)
	assert.Equal(t, 150.0, trend.Current)
	assert.Equal(t, 100.0, trend.Previous)
	assert.Equal(t, 50.0, trend.Change)
	assert.Equal(t, 50.0, trend.ChangePct)
	assert.Equal(t, "up", trend.Direction)
}

func TestCalculateDateRange(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		tr       TimeRange
		expected time.Duration
	}{
		{"7 days", TimeRange7Days, 7 * 24 * time.Hour},
		{"30 days", TimeRange30Days, 30 * 24 * time.Hour},
		{"90 days", TimeRange90Days, 90 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := calculateDateRange(tt.tr, time.Time{}, time.Time{})
			diff := end.Sub(start)
			assert.Equal(t, tt.expected, diff)
			assert.Equal(t, now.Day(), end.Day())
		})
	}
}

func TestParseTimeRange(t *testing.T) {
	tests := []struct {
		input    string
		expected TimeRange
	}{
		{"7d", TimeRange7Days},
		{"7days", TimeRange7Days},
		{"30d", TimeRange30Days},
		{"30days", TimeRange30Days},
		{"90d", TimeRange90Days},
		{"90days", TimeRange90Days},
		{"custom", TimeRangeCustom},
		{"unknown", TimeRange7Days},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ParseTimeRange(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateSkillsBarChart(t *testing.T) {
	skillUsage := map[string]int{
		"executor":    45,
		"explore":     32,
		"designer":    28,
		"researcher":  22,
	}

	chart := GenerateSkillsBarChart(skillUsage)

	assert.Len(t, chart.Labels, 4)
	assert.Len(t, chart.Values, 4)
	assert.Len(t, chart.Colors, 4)

	for i, skill := range chart.Labels {
		assert.Equal(t, skillUsage[skill], int(chart.Values[i]))
	}

	for _, color := range chart.Colors {
		assert.True(t, strings.HasPrefix(color, "#"))
		assert.Len(t, color, 7)
	}
}

func TestGenerateHeatmapData(t *testing.T) {
	sessions := []SessionLog{
		{Timestamp: time.Date(2024, 1, 8, 9, 0, 0, 0, time.UTC), SessionID: "s1"},
		{Timestamp: time.Date(2024, 1, 8, 10, 0, 0, 0, time.UTC), SessionID: "s2"},
		{Timestamp: time.Date(2024, 1, 8, 10, 0, 0, 0, time.UTC), SessionID: "s3"},
		{Timestamp: time.Date(2024, 1, 9, 10, 0, 0, 0, time.UTC), SessionID: "s4"},
	}

	heatmap := GenerateHeatmapData(sessions)

	assert.NotEmpty(t, heatmap)

	var monday10am HeatmapRow
	for _, row := range heatmap {
		if row.Day == "Monday" && row.Hour == 10 {
			monday10am = row
			break
		}
	}
	assert.Equal(t, 2, monday10am.Session)
	assert.Equal(t, 1.0, monday10am.Value)
}

func TestValidateExportFormat(t *testing.T) {
	tests := []struct {
		input    string
		expected ExportFormat
		valid    bool
	}{
		{"json", ExportJSON, true},
		{"csv", ExportCSV, true},
		{"prometheus", ExportPrometheus, true},
		{"invalid", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, valid := ValidateExportFormat(tt.input)
			assert.Equal(t, tt.valid, valid)
			if tt.valid {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestExportData_JSON(t *testing.T) {
	svc := NewDashboardService(nil, nil)
	data := &DashboardData{
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Metrics:   &DashboardMetrics{TotalSessions: 100},
	}

	result, err := svc.ExportData(data, ExportJSON)
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(result, &parsed)
	require.NoError(t, err)
	assert.Contains(t, parsed, "metrics")
}

func TestExportData_CSV(t *testing.T) {
	svc := NewDashboardService(nil, nil)
	data := &DashboardData{
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Metrics: &DashboardMetrics{
			TotalSessions:      100,
			ActiveUsers:        25,
			AvgSatisfaction:    4.5,
			AvgSessionDuration: 30.0,
			TotalFeedback:      50,
			TasksCreated:       120,
			TasksCompleted:     100,
		},
		Trends: &DashboardTrends{
			SessionsTrend: TrendData{
				Current:   100,
				Previous:  90,
				Change:    10,
				ChangePct: 11.11,
				Direction: "up",
			},
		},
		Charts: &DashboardCharts{
			SkillsBarChart: BarChartData{
				Labels: []string{"executor"},
				Values: []float64{45},
			},
		},
	}

	result, err := svc.ExportData(data, ExportCSV)
	require.NoError(t, err)

	content := string(result)
	assert.Contains(t, content, "=== Metrics ===")
	assert.Contains(t, content, "Total Sessions,100")
	assert.Contains(t, content, "=== Trends ===")
	assert.Contains(t, content, "Sessions,100.00")
	assert.Contains(t, content, "=== Skills Distribution ===")
}

func TestExportData_Prometheus(t *testing.T) {
	svc := NewDashboardService(nil, nil)
	data := &DashboardData{
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Metrics: &DashboardMetrics{
			TotalSessions:      100,
			ActiveUsers:        25,
			AvgSatisfaction:    4.5,
			AvgSessionDuration: 30.0,
			TotalFeedback:      50,
			TasksCreated:       120,
			TasksCompleted:     100,
		},
		Trends: &DashboardTrends{
			SessionsTrend: TrendData{
				Current:   100,
				Previous:  90,
				Change:    10,
				ChangePct: 11.11,
				Direction: "up",
			},
		},
		Charts: &DashboardCharts{
			SkillsBarChart: BarChartData{
				Labels: []string{"executor"},
				Values: []float64{45},
			},
		},
	}

	result, err := svc.ExportData(data, ExportPrometheus)
	require.NoError(t, err)

	content := string(result)
	assert.Contains(t, content, "# Nexus Dashboard Metrics")
	assert.Contains(t, content, "nexus_dashboard_satisfaction_avg")
	assert.Contains(t, content, "# TYPE")
	assert.Contains(t, content, "# HELP")
}

func TestHandler_Endpoints(t *testing.T) {
	svc := NewDashboardService(nil, nil)
	handler := Handler(svc)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		checkContent   func(t *testing.T, body []byte)
	}{
		{
			name:           "dashboard endpoint",
			path:           "/api/dashboard",
			expectedStatus: http.StatusOK,
			checkContent: func(t *testing.T, body []byte) {
				var data DashboardData
				err := json.Unmarshal(body, &data)
				require.NoError(t, err)
				assert.NotNil(t, data.Metrics)
				assert.NotNil(t, data.Trends)
				assert.NotNil(t, data.Charts)
			},
		},
		{
			name:           "metrics endpoint",
			path:           "/api/dashboard/metrics",
			expectedStatus: http.StatusOK,
			checkContent: func(t *testing.T, body []byte) {
				var metrics DashboardMetrics
				err := json.Unmarshal(body, &metrics)
				require.NoError(t, err)
				assert.Equal(t, 156, metrics.TotalSessions)
			},
		},
		{
			name:           "trends endpoint",
			path:           "/api/dashboard/trends",
			expectedStatus: http.StatusOK,
			checkContent: func(t *testing.T, body []byte) {
				var trends DashboardTrends
				err := json.Unmarshal(body, &trends)
				require.NoError(t, err)
				assert.Equal(t, "up", trends.SessionsTrend.Direction)
			},
		},
		{
			name:           "charts endpoint",
			path:           "/api/dashboard/charts",
			expectedStatus: http.StatusOK,
			checkContent: func(t *testing.T, body []byte) {
				var charts DashboardCharts
				err := json.Unmarshal(body, &charts)
				require.NoError(t, err)
				assert.NotEmpty(t, charts.SkillsBarChart.Labels)
				assert.NotEmpty(t, charts.SatisfactionChart.Labels)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			tt.checkContent(t, w.Body.Bytes())
		})
	}
}

func TestHandler_ExportFormats(t *testing.T) {
	svc := NewDashboardService(nil, nil)
	handler := Handler(svc)

	tests := []struct {
		name         string
		query        string
		expectedType string
	}{
		{"json export", "/api/dashboard/export?format=json", "application/json"},
		{"csv export", "/api/dashboard/export?format=csv", "text/csv"},
		{"prometheus export", "/api/dashboard/export?format=prometheus", "text/plain"},
		{"default format", "/api/dashboard/export", "application/json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.query, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tt.expectedType, w.Header().Get("Content-Type"))
			assert.Contains(t, w.Header().Get("Content-Disposition"), "filename=")
		})
	}
}

func TestHandler_TimeRange(t *testing.T) {
	svc := NewDashboardService(nil, nil)
	handler := Handler(svc)

	tests := []struct {
		name         string
		query        string
		expectedType string
	}{
		{"7 days", "/api/dashboard?range=7d", "application/json"},
		{"30 days", "/api/dashboard?range=30d", "application/json"},
		{"90 days", "/api/dashboard?range=90d", "application/json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.query, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestDashboardCharts_DataTypes(t *testing.T) {
	charts := &DashboardCharts{
		SkillsBarChart: BarChartData{
			Labels: []string{"skill1", "skill2"},
			Values: []float64{10.5, 20.3},
			Colors: []string{"#ff0000", "#00ff00"},
		},
		SatisfactionChart: PieChartData{
			Labels:      []string{"Good", "Bad"},
			Values:      []float64{75.0, 25.0},
			Colors:      []string{"#00ff00", "#ff0000"},
			CenterLabel: "Satisfaction",
		},
		TimelineChart: TimelineData{
			Labels: []string{"Mon", "Tue", "Wed"},
			Datasets: []TimelineDataset{
				{Label: "Sessions", Values: []float64{10, 20, 15}, Color: "#6366f1"},
				{Label: "Tasks", Values: []float64{8, 18, 12}, Color: "#22c55e"},
			},
		},
		HeatmapData: []HeatmapRow{
			{Day: "Monday", Hour: 9, Session: 5, Value: 0.8},
			{Day: "Tuesday", Hour: 10, Session: 8, Value: 1.0},
		},
	}

	assert.Len(t, charts.SkillsBarChart.Labels, 2)
	assert.Len(t, charts.SatisfactionChart.Labels, 2)
	assert.Len(t, charts.TimelineChart.Datasets, 2)
	assert.Len(t, charts.TimelineChart.Datasets[0].Values, 3)
	assert.Len(t, charts.HeatmapData, 2)
}

func TestAlert_Structure(t *testing.T) {
	alert := Alert{
		ID:        "test-alert-1",
		Type:      "warning",
		Message:   "High CPU usage detected",
		Timestamp: time.Now(),
		Severity:  "high",
	}

	assert.Equal(t, "test-alert-1", alert.ID)
	assert.Equal(t, "warning", alert.Type)
	assert.Equal(t, "high", alert.Severity)
}

func TestPieChartData_JSON(t *testing.T) {
	chart := PieChartData{
		Labels:      []string{"A", "B", "C"},
		Values:      []float64{1.0, 2.0, 3.0},
		Colors:      []string{"#f00", "#0f0", "#00f"},
		CenterLabel: "Test",
	}

	data, err := json.Marshal(chart)
	require.NoError(t, err)

	var parsed PieChartData
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, chart.Labels, parsed.Labels)
	assert.Equal(t, chart.Values, parsed.Values)
	assert.Equal(t, chart.Colors, parsed.Colors)
	assert.Equal(t, chart.CenterLabel, parsed.CenterLabel)
}

func TestTimelineData_JSON(t *testing.T) {
	timeline := TimelineData{
		Labels: []string{"Mon", "Tue", "Wed"},
		Datasets: []TimelineDataset{
			{Label: "Dataset1", Values: []float64{1, 2, 3}, Color: "#ff0000"},
		},
	}

	data, err := json.Marshal(timeline)
	require.NoError(t, err)

	var parsed TimelineData
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, timeline.Labels, parsed.Labels)
	assert.Len(t, parsed.Datasets, 1)
	assert.Equal(t, timeline.Datasets[0].Label, parsed.Datasets[0].Label)
	assert.Equal(t, timeline.Datasets[0].Values, parsed.Datasets[0].Values)
}

func TestBarChartData_JSON(t *testing.T) {
	chart := BarChartData{
		Labels: []string{"A", "B", "C"},
		Values: []float64{10, 20, 30},
		Colors: []string{"#111", "#222", "#333"},
	}

	data, err := json.Marshal(chart)
	require.NoError(t, err)

	var parsed BarChartData
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, chart.Labels, parsed.Labels)
	assert.Equal(t, chart.Values, parsed.Values)
	assert.Equal(t, chart.Colors, parsed.Colors)
}
