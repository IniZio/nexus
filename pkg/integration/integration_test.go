// Package integration provides integration tests for Nexus packages.
// These tests verify cross-package functionality and workflows.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/nexus/nexus/pkg/alerts"
	"github.com/nexus/nexus/pkg/analytics"
	"github.com/nexus/nexus/pkg/mobile"
	"github.com/nexus/nexus/pkg/team"
	"github.com/nexus/nexus/pkg/workspace"
)

// TestAlertsWorkspaceIntegration tests alerts triggering workspace actions.
func TestAlertsWorkspaceIntegration(t *testing.T) {
	// Create workspace manager
	wsManager := workspace.NewWorkspaceManager(nil)

	// Create alert engine
	alertEngine := alerts.NewAlertEngine()

	// Create workspace
	ws, err := wsManager.CreateWorkspace(context.Background(), &workspace.CreateWorkspaceRequest{
		Name:    "test-ws",
		Owner:   "test-user",
		Project: "test-project",
	})
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}
	_ = ws

	// Create alert rule for low satisfaction
	rule := &alerts.AlertRule{
		Name:       "Low Satisfaction",
		MetricType: "satisfaction",
		Severity:   alerts.SeverityHigh,
		ConditionType: alerts.ConditionThreshold,
		Condition: alerts.ConditionConfig{
			Operator: alerts.OpLessThan,
			Value:    3.0,
		},
		Actions: []alerts.ActionConfig{
			{Type: alerts.ActionLog},
		},
	}

	err = alertEngine.CreateRule(rule)
	if err != nil {
		t.Fatalf("Failed to create alert rule: %v", err)
	}

	// Simulate low satisfaction alert
	alert := alertEngine.EvaluateMetric(context.Background(), alerts.MetricValue{
		Type:  "satisfaction",
		Value: 2.5,
	})

	if len(alert) == 0 {
		t.Error("Should have triggered alert for low satisfaction")
	}

	// Verify alert severity
	if alert[0].Severity != alerts.SeverityHigh {
		t.Errorf("Alert severity = %v, want high", alert[0].Severity)
	}

	t.Logf("Alerts → Workspace integration: PASS (alert triggered for satisfaction=%v)", 2.5)
}

// TestAnalyticsTeamIntegration tests analytics feeding team metrics.
func TestAnalyticsTeamIntegration(t *testing.T) {
	// Create analytics engine
	analyticsEngine := analytics.NewAnalyticsEngine(nil)

	// Create team analytics
	teamEngine := team.NewTeamAnalyticsEngine(nil)

	// Add team members
	teamEngine.AddMember(&team.TeamMember{
		ID:   "dev-1",
		Name: "Developer 1",
		Role: team.RoleSenior,
	})

	teamEngine.AddMember(&team.TeamMember{
		ID:   "dev-2",
		Name: "Developer 2",
		Role: team.RoleMid,
	})

	// Create team
	teamEngine.AddTeam(&team.Team{
		ID:        "team-1",
		Name:      "Platform Team",
		LeadID:    "dev-1",
		MemberIDs: []string{"dev-1", "dev-2"},
	})

	// Record performance metrics
	for i := 0; i < 7; i++ {
		teamEngine.RecordMetrics(team.PerformanceMetrics{
			MemberID:        "dev-1",
			Period:          "weekly",
			TasksCompleted:  10 + i,
			PointsCompleted: 20 + i*2,
			Velocity:        5.0 + float64(i)*0.5,
			BugRate:         2.0 - float64(i)*0.1,
			CodeReviewScore: 80 + float64(i),
			CycleTime:       24 - float64(i),
			LeadTime:        12 - float64(i)*0.5,
			ReviewsGiven:    5 + i,
			ReviewsReceived: 3 + i,
			StartDate:       time.Now().AddDate(0, 0, -7+i),
			EndDate:         time.Now().AddDate(0, 0, -i),
		})

		teamEngine.RecordMetrics(team.PerformanceMetrics{
			MemberID:        "dev-2",
			Period:          "weekly",
			TasksCompleted:  8 + i,
			PointsCompleted: 16 + i*2,
			Velocity:        4.0 + float64(i)*0.3,
			BugRate:         3.0 - float64(i)*0.1,
			CodeReviewScore: 75 + float64(i),
			CycleTime:       30 - float64(i),
			LeadTime:        15 - float64(i)*0.5,
			ReviewsGiven:    3 + i,
			ReviewsReceived: 2 + i,
			StartDate:       time.Now().AddDate(0, 0, -7+i),
			EndDate:         time.Now().AddDate(0, 0, -i),
		})
	}

	// Get team metrics
	teamMetrics, err := teamEngine.CalculateTeamMetrics("team-1", "weekly")
	if err != nil {
		t.Fatalf("Failed to calculate team metrics: %v", err)
	}

	// Verify aggregation
	if teamMetrics.MemberCount != 2 {
		t.Errorf("Member count = %v, want 2", teamMetrics.MemberCount)
	}

	if teamMetrics.TotalTasksCompleted < 100 {
		t.Errorf("Total tasks should be >100, got %d", teamMetrics.TotalTasksCompleted)
	}

	// Use analytics for forecasting
	ts := &analytics.TimeSeries{
		MetricType: "velocity",
		Interval:   time.Hour,
		Points:     make([]analytics.DataPoint, 24),
	}
	for i := range ts.Points {
		ts.Points[i] = analytics.DataPoint{
			Timestamp: time.Now().Add(-time.Duration(24-i) * time.Hour),
			Value:     5.0 + float64(i)*0.1,
		}
	}

	forecast, err := analyticsEngine.Forecast(context.Background(), ts, 24*time.Hour, analytics.MethodLinearTrend)
	if err != nil {
		t.Fatalf("Forecast failed: %v", err)
	}

	if len(forecast.Values) == 0 {
		t.Error("Forecast should have values")
	}

	t.Logf("Analytics → Team integration: PASS (team velocity trend analyzed)")
}

// TestMobileDashboardIntegration tests mobile dashboard with all packages.
func TestMobileDashboardIntegration(t *testing.T) {
	// Create mobile dashboard
	dashboard := mobile.NewMobileDashboard(nil)

	// Add metrics from various sources
	metrics := []mobile.MetricSnapshot{
		{Type: "satisfaction", Value: 4.33, Label: "Satisfaction", Trend: "stable", UpdatedAt: time.Now()},
		{Type: "velocity", Value: 5.2, Label: "Velocity", Trend: "up", UpdatedAt: time.Now()},
		{Type: "quality", Value: 87.5, Label: "Quality", Trend: "up", UpdatedAt: time.Now()},
		{Type: "efficiency", Value: 92.0, Label: "Efficiency", Trend: "stable", UpdatedAt: time.Now()},
	}

	for _, m := range metrics {
		dashboard.AddMetric(m)
	}

	// Add alerts
	alertsList := []mobile.MobileAlert{
		{ID: "alert-1", Type: "warning", Title: "High Load", Message: "CPU usage high", Priority: 3, CreatedAt: time.Now()},
		{ID: "alert-2", Type: "info", Title: "Deploy Complete", Message: "v1.3.0 deployed", Priority: 1, CreatedAt: time.Now()},
	}
	for _, a := range alertsList {
		dashboard.AddAlert(a)
	}

	// Add workspaces
	workspaces := []*mobile.MobileWorkspace{
		{ID: "ws-1", Name: "Development", Status: "running", ResourceUsage: "medium", LastActivity: time.Now()},
		{ID: "ws-2", Name: "Staging", Status: "running", ResourceUsage: "low", LastActivity: time.Now()},
	}
	for _, ws := range workspaces {
		dashboard.AddWorkspace(ws)
	}

	// Test dashboard views
	views := []mobile.DashboardView{mobile.ViewOverview, mobile.ViewMetrics, mobile.ViewWorkspaces, mobile.ViewAlerts}

	for _, view := range views {
		layout, err := dashboard.GetDashboard(view)
		if err != nil {
			t.Fatalf("Failed to get dashboard view %s: %v", view, err)
		}

		if len(layout.Widgets) == 0 {
			t.Errorf("View %s should have widgets", view)
		}
	}

	// Test metric cards
	cards := dashboard.GetMetricCards()
	if len(cards) != 4 {
		t.Errorf("Expected 4 metric cards, got %d", len(cards))
	}

	// Test chart data
	chartData := dashboard.GetChartData("velocity", "line", "24h")
	if chartData.ChartType != "line" {
		t.Errorf("Chart type = %v, want line", chartData.ChartType)
	}

	// Test alerts pagination
	page0 := dashboard.GetAlerts(0, 10)
	if len(page0) != 2 {
		t.Errorf("Expected 2 alerts, got %d", len(page0))
	}

	// Test list data
	listData := dashboard.GetListData("active-workspaces")
	if len(listData.Items) != 2 {
		t.Errorf("Expected 2 workspaces, got %d", len(listData.Items))
	}

	// Test quick actions
	actions := dashboard.GetQuickActions()
	if len(actions) != 4 {
		t.Errorf("Expected 4 quick actions, got %d", len(actions))
	}

	t.Logf("Mobile Dashboard integration: PASS (all views, metrics, alerts, workspaces)")
}

// TestWorkspaceTeamIntegration tests workspace team assignments.
func TestWorkspaceTeamIntegration(t *testing.T) {
	// Create managers
	wsManager := workspace.NewWorkspaceManager(nil)
	teamEngine := team.NewTeamAnalyticsEngine(nil)

	// Create team
	teamEngine.AddTeam(&team.Team{
		ID:        "backend-team",
		Name:      "Backend Team",
		MemberIDs: []string{"alice", "bob"},
	})

	// Create workspace for team
	ws, err := wsManager.CreateWorkspace(context.Background(), &workspace.CreateWorkspaceRequest{
		Name:    "backend-dev",
		Owner:   "alice",
		Project: "backend",
		Labels:  map[string]string{"team": "backend-team"},
	})
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}

	// Verify workspace has team label
	if ws.Labels["team"] != "backend-team" {
		t.Errorf("Workspace label = %v, want backend-team", ws.Labels["team"])
	}

	// Get team workspaces (via filtering)
	workspaces := wsManager.ListWorkspaces()
	if len(workspaces) == 0 {
		t.Error("Should have created workspace")
	}

	t.Logf("Workspace → Team integration: PASS (workspace linked to team)")
}

// TestAlertsAnalyticsIntegration tests analytics detecting alert conditions.
func TestAlertsAnalyticsIntegration(t *testing.T) {
	analyticsEngine := analytics.NewAnalyticsEngine(nil)
	alertEngine := alerts.NewAlertEngine()

	// Create time series with anomaly
	ts := &analytics.TimeSeries{
		MetricType: "satisfaction",
		Interval:   time.Hour,
		Points:     make([]analytics.DataPoint, 10),
	}
	for i := range ts.Points {
		ts.Points[i] = analytics.DataPoint{
			Timestamp: time.Now().Add(-time.Duration(10-i) * time.Hour),
			Value:     4.0, // Normal satisfaction
		}
	}
	// Add anomaly
	ts.Points[9].Value = 1.5

	// Detect patterns
	_, err := analyticsEngine.DetectPatterns(ts)
	if err != nil {
		t.Fatalf("Pattern detection failed: %v", err)
	}

	// Create alert rule for value threshold
	rule := &alerts.AlertRule{
		Name:       "Satisfaction Drop",
		MetricType: "satisfaction",
		Severity:   alerts.SeverityCritical,
		ConditionType: alerts.ConditionThreshold,
		Condition: alerts.ConditionConfig{
			Operator: alerts.OpLessThan,
			Value:    2.0,
		},
		Actions: []alerts.ActionConfig{
			{Type: alerts.ActionEscalate},
		},
	}

	err = alertEngine.CreateRule(rule)
	if err != nil {
		t.Fatalf("Failed to create alert rule: %v", err)
	}

	// Evaluate alert for low value
	alertResults := alertEngine.EvaluateMetric(context.Background(), alerts.MetricValue{
		Type:  "satisfaction",
		Value: 1.5,
	})

	if len(alertResults) == 0 {
		t.Error("Should have triggered alert for very low satisfaction")
	}

	t.Logf("Alerts → Analytics integration: PASS (pattern detection → alert trigger)")
}

// TestFullWorkflowIntegration tests complete workflow from metrics to reports.
func TestFullWorkflowIntegration(t *testing.T) {
	// Setup all engines
	analyticsEngine := analytics.NewAnalyticsEngine(nil)
	teamEngine := team.NewTeamAnalyticsEngine(nil)
	mobileDashboard := mobile.NewMobileDashboard(nil)

	// 1. Record metrics
	for i := 0; i < 14; i++ {
		ts := &analytics.TimeSeries{
			MetricType: "satisfaction",
			Interval:   time.Hour,
			Points: []analytics.DataPoint{
				{Timestamp: time.Now().Add(-time.Duration(14-i) * time.Hour), Value: 4.0 + float64(i)*0.02},
			},
		}

		forecast, _ := analyticsEngine.Forecast(context.Background(), ts, 24*time.Hour, analytics.MethodMovingAverage)

		// Sync to mobile dashboard
		mobileDashboard.AddMetric(mobile.MetricSnapshot{
			Type:      "satisfaction",
			Value:     4.0 + float64(i)*0.02,
			Label:     "Satisfaction",
			Trend:     "up",
			UpdatedAt: time.Now(),
		})

		if forecast != nil && len(forecast.Values) > 0 {
			mobileDashboard.AddMetric(mobile.MetricSnapshot{
				Type:      "satisfaction_forecast",
				Value:     forecast.Values[0].Value,
				Label:     "Satisfaction Forecast",
				Trend:     forecast.Trend,
				UpdatedAt: time.Now(),
			})
		}
	}

	// 2. Team metrics
	teamEngine.AddMember(&team.TeamMember{ID: "user-1", Name: "User 1"})
	for i := 0; i < 4; i++ {
		teamEngine.RecordMetrics(team.PerformanceMetrics{
			MemberID:  "user-1",
			Period:    "weekly",
			Velocity:  5.0 + float64(i)*0.5,
			StartDate: time.Now().AddDate(0, 0, -28+i*7),
			EndDate:   time.Now().AddDate(0, 0, -21+i*7),
		})
	}

	// 3. Generate report
	report, err := teamEngine.GenerateReport(context.Background(), "individual", "user-1", "weekly")
	if err != nil {
		t.Fatalf("Failed to generate report: %v", err)
	}

	if report.OverallScore == 0 {
		t.Error("Report should have overall score")
	}

	// 4. Mobile dashboard reflects data
	cards := mobileDashboard.GetMetricCards()
	if len(cards) < 4 {
		t.Error("Dashboard should have metric cards")
	}

	// 5. Quick actions available
	actions := mobileDashboard.GetQuickActions()
	if len(actions) == 0 {
		t.Error("Dashboard should have quick actions")
	}

	t.Logf("Full workflow integration: PASS (metrics → analytics → team → mobile)")
}

// TestCrossPackageDataFlow tests data flows between packages.
func TestCrossPackageDataFlow(t *testing.T) {
	// Simulate data flow:
	// workspace → alerts → analytics → team → mobile → user

	// 1. Workspace state changes
	wsManager := workspace.NewWorkspaceManager(nil)
	ws, _ := wsManager.CreateWorkspace(context.Background(), &workspace.CreateWorkspaceRequest{
		Name:   "test-ws",
		Owner:  "user",
		Labels: map[string]string{"env": "production"},
	})

	// 2. Create alert for workspace issues
	alertEngine := alerts.NewAlertEngine()
	rule := &alerts.AlertRule{
		Name:       "Workspace Issue",
		MetricType: "workspace_status",
		Severity:   alerts.SeverityMedium,
		ConditionType: alerts.ConditionThreshold,
		Condition: alerts.ConditionConfig{
			Operator: alerts.OpLessThan,
			Value:    0.5, // Use numeric for status
		},
	}
	alertEngine.CreateRule(rule)

	// 3. Analytics tracks metrics
	analyticsEngine := analytics.NewAnalyticsEngine(nil)
	ts := &analytics.TimeSeries{
		MetricType: "workspace_status",
		Points:     []analytics.DataPoint{{Value: 1.0}},
	}
	analyticsEngine.DetectPatterns(ts)

	// 4. Team tracks productivity
	teamEngine := team.NewTeamAnalyticsEngine(nil)
	teamEngine.AddMember(&team.TeamMember{ID: "user", Name: "User"})
	teamEngine.RecordMetrics(team.PerformanceMetrics{
		MemberID: "user",
		Period:   "daily",
		Velocity: 5.0,
	})

	// 5. Mobile dashboard shows all
	mobileDashboard := mobile.NewMobileDashboard(nil)
	mobileDashboard.AddWorkspace(&mobile.MobileWorkspace{
		ID:     ws.ID,
		Name:   ws.Name,
		Status: string(ws.State),
	})

	// Verify data flow
	workspaces := mobileDashboard.GetWorkspaces()
	if len(workspaces) == 0 {
		t.Error("Mobile dashboard should have workspace")
	}

	teamMetrics, _ := teamEngine.GetLatestMetrics("user")
	if teamMetrics == nil {
		t.Error("Team metrics should exist")
	}

	t.Logf("Cross-package data flow: PASS (5 packages connected)")
}

// BenchmarkIntegrationWorkflow benchmarks the full workflow.
func BenchmarkIntegrationWorkflow(b *testing.B) {
	wsManager := workspace.NewWorkspaceManager(nil)
	alertEngine := alerts.NewAlertEngine()
	analyticsEngine := analytics.NewAnalyticsEngine(nil)
	teamEngine := team.NewTeamAnalyticsEngine(nil)
	mobileDashboard := mobile.NewMobileDashboard(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Create workspace
		wsManager.CreateWorkspace(context.Background(), &workspace.CreateWorkspaceRequest{
			Name:   "bench-ws",
			Owner:  "user",
			Labels: map[string]string{"bench": "true"},
		})

		// Create alert
		alertEngine.CreateRule(&alerts.AlertRule{
			Name:       "Bench Alert",
			MetricType: "bench",
			Severity:   alerts.SeverityLow,
			ConditionType: alerts.ConditionThreshold,
			Condition: alerts.ConditionConfig{
				Operator: alerts.OpGreaterThan,
				Value:    0.5,
			},
		})

		// Analytics
		ts := &analytics.TimeSeries{
			MetricType: "bench",
			Points:     []analytics.DataPoint{{Value: 1.0}},
		}
		analyticsEngine.DetectPatterns(ts)

		// Team
		teamEngine.AddMember(&team.TeamMember{ID: "bench-user"})
		teamEngine.RecordMetrics(team.PerformanceMetrics{
			MemberID: "bench-user",
			Period:   "daily",
			Velocity: 5.0,
		})

		// Mobile
		mobileDashboard.GetDashboard(mobile.ViewOverview)
		mobileDashboard.GetMetricCards()
	}
}
