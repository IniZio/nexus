package mobile

import (
	"testing"
	"time"
)

// TestMobileDashboard tests the mobile dashboard.
func TestMobileDashboard(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	if dashboard == nil {
		t.Fatal("Dashboard should not be nil")
	}

	if dashboard.Config() == nil {
		t.Error("Config should not be nil")
	}

	if dashboard.Config().DefaultRefreshInterval != 30*time.Second {
		t.Error("Default refresh interval should be 30 seconds")
	}
}

// TestMobileDashboard_CustomConfig tests custom configuration.
func TestMobileDashboard_CustomConfig(t *testing.T) {
	config := &DashboardConfig{
		DefaultRefreshInterval:  60 * time.Second,
		MaxRefreshInterval:      10 * time.Minute,
		MinRefreshInterval:      10 * time.Second,
		MaxMetricsPerRequest:    50,
		MaxHistoryPoints:        200,
		DefaultTheme:            "dark",
		SupportedThemes:         []string{"light", "dark"},
		EnablePushNotifications: true,
		EnableOfflineMode:       true,
	}

	dashboard := NewMobileDashboard(config)

	if dashboard.Config().DefaultRefreshInterval != 60*time.Second {
		t.Error("Default refresh interval should be 60 seconds")
	}

	if dashboard.Config().DefaultTheme != "dark" {
		t.Error("Default theme should be dark")
	}
}

// TestGetDashboard tests getting dashboard for different views.
func TestGetDashboard(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	views := []DashboardView{ViewOverview, ViewMetrics, ViewWorkspaces, ViewAlerts, ViewSettings}

	for _, view := range views {
		layout, err := dashboard.GetDashboard(view)
		if err != nil {
			t.Fatalf("GetDashboard(%s) error = %v", view, err)
		}

		if layout.View != view {
			t.Errorf("Layout view = %v, want %v", layout.View, view)
		}

		if len(layout.Widgets) == 0 {
			t.Errorf("Layout for %s should have widgets", view)
		}
	}
}

// TestGetDashboard_DefaultWidgets tests widget count per view.
func TestGetDashboard_DefaultWidgets(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	tests := []struct {
		view       DashboardView
		minWidgets int
	}{
		{ViewOverview, 5},
		{ViewMetrics, 5},
		{ViewWorkspaces, 2},
		{ViewAlerts, 1},
		{ViewSettings, 4},
	}

	for _, tt := range tests {
		layout, _ := dashboard.GetDashboard(tt.view)
		if len(layout.Widgets) < tt.minWidgets {
			t.Errorf("View %s has %d widgets, want at least %d", tt.view, len(layout.Widgets), tt.minWidgets)
		}
	}
}

// TestGetQuickActions tests quick actions.
func TestGetQuickActions(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	actions := dashboard.GetQuickActions()

	if len(actions) != 4 {
		t.Errorf("Expected 4 quick actions, got %d", len(actions))
	}

	// Verify action structure
	for _, action := range actions {
		if action.ID == "" {
			t.Error("Action ID should not be empty")
		}
		if action.Title == "" {
			t.Error("Action title should not be empty")
		}
		if action.Action == "" {
			t.Error("Action should have an action identifier")
		}
	}
}

// Metric Tests

func TestAddMetric(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	metric := MetricSnapshot{
		Type:      "satisfaction",
		Value:     4.5,
		Label:     "User Satisfaction",
		Trend:     "up",
		UpdatedAt: time.Now(),
	}

	dashboard.AddMetric(metric)

	metrics := dashboard.GetMetrics("satisfaction", 0)
	if len(metrics) != 1 {
		t.Errorf("Expected 1 metric, got %d", len(metrics))
	}

	if metrics[0].Value != 4.5 {
		t.Errorf("Metric value = %v, want 4.5", metrics[0].Value)
	}
}

func TestGetMetrics_WithLimit(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	// Add 10 metrics
	for i := 0; i < 10; i++ {
		dashboard.AddMetric(MetricSnapshot{
			Type:      "velocity",
			Value:     float64(i),
			UpdatedAt: time.Now(),
		})
	}

	metrics := dashboard.GetMetrics("velocity", 5)
	if len(metrics) != 5 {
		t.Errorf("Expected 5 metrics with limit, got %d", len(metrics))
	}

	// Should get the last 5
	if metrics[0].Value != 5 {
		t.Errorf("First metric should be value 5, got %v", metrics[0].Value)
	}
}

func TestGetMetrics_NotFound(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	metrics := dashboard.GetMetrics("nonexistent", 0)
	if len(metrics) != 0 {
		t.Errorf("Expected 0 metrics for nonexistent type, got %d", len(metrics))
	}
}

// Alert Tests

func TestAddAlert(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	alert := MobileAlert{
		ID:          "alert-001",
		Type:        "warning",
		Title:       "Low Satisfaction",
		Message:     "Satisfaction dropped below threshold",
		Priority:    3,
		Dismissable: true,
		CreatedAt:   time.Now(),
		Read:        false,
	}

	dashboard.AddAlert(alert)

	alerts := dashboard.GetAlerts(0, 10)
	if len(alerts) != 1 {
		t.Errorf("Expected 1 alert, got %d", len(alerts))
	}

	if alerts[0].ID != "alert-001" {
		t.Errorf("Alert ID = %v, want alert-001", alerts[0].ID)
	}
}

func TestGetAlerts_Pagination(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	// Add 15 alerts
	for i := 0; i < 15; i++ {
		dashboard.AddAlert(MobileAlert{
			ID:        "alert-" + string(rune('a'+i)),
			Type:      "info",
			Title:     "Alert " + string(rune('a'+i)),
			CreatedAt: time.Now(),
		})
	}

	// Get first page
	page0 := dashboard.GetAlerts(0, 5)
	if len(page0) != 5 {
		t.Errorf("Page 0: expected 5 alerts, got %d", len(page0))
	}

	// Get second page
	page1 := dashboard.GetAlerts(1, 5)
	if len(page1) != 5 {
		t.Errorf("Page 1: expected 5 alerts, got %d", len(page1))
	}

	// Get third page (partial)
	page2 := dashboard.GetAlerts(2, 5)
	if len(page2) != 5 {
		t.Errorf("Page 2: expected 5 alerts, got %d", len(page2))
	}

	// Get beyond range
	page3 := dashboard.GetAlerts(3, 5)
	if len(page3) != 0 {
		t.Errorf("Page 3: expected 0 alerts, got %d", len(page3))
	}

	// Verify order (newest first)
	if page0[0].ID != "alert-o" {
		t.Errorf("First alert should be newest (alert-o), got %s", page0[0].ID)
	}
}

// Workspace Tests

func TestAddWorkspace(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	workspace := &MobileWorkspace{
		ID:            "ws-001",
		Name:          "Development",
		Status:        "running",
		ResourceUsage: "medium",
		LastActivity:  time.Now(),
		HasUpdates:    false,
	}

	dashboard.AddWorkspace(workspace)

	workspaces := dashboard.GetWorkspaces()
	if len(workspaces) != 1 {
		t.Errorf("Expected 1 workspace, got %d", len(workspaces))
	}

	if workspaces[0].ID != "ws-001" {
		t.Errorf("Workspace ID = %v, want ws-001", workspaces[0].ID)
	}
}

func TestGetWorkspaces_Multiple(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	// Add multiple workspaces
	workspaces := []string{"ws-1", "ws-2", "ws-3"}
	for _, id := range workspaces {
		dashboard.AddWorkspace(&MobileWorkspace{
			ID:     id,
			Name:   "Workspace " + id,
			Status: "running",
		})
	}

	result := dashboard.GetWorkspaces()
	if len(result) != 3 {
		t.Errorf("Expected 3 workspaces, got %d", len(result))
	}
}

// Metric Card Tests

func TestGetMetricCards(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	cards := dashboard.GetMetricCards()

	if len(cards) != 4 {
		t.Errorf("Expected 4 metric cards, got %d", len(cards))
	}

	// Check required fields
	for _, card := range cards {
		if card.WidgetID == "" {
			t.Error("Card should have WidgetID")
		}
		if card.Title == "" {
			t.Error("Card should have Title")
		}
		if card.Status == "" {
			t.Error("Card should have Status")
		}
	}

	// Check specific values
	for _, card := range cards {
		switch card.WidgetID {
		case "satisfaction":
			if card.Unit != "/5" {
				t.Error("Satisfaction should have /5 unit")
			}
		case "velocity":
			if card.Unit != "tasks/day" {
				t.Error("Velocity should have tasks/day unit")
			}
		}
	}
}

// Chart Data Tests

func TestGetChartData(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	// Add some metrics
	for i := 0; i < 5; i++ {
		dashboard.AddMetric(MetricSnapshot{
			Type:      "satisfaction",
			Value:     4.0 + float64(i)*0.1,
			UpdatedAt: time.Now(),
		})
	}

	chartData := dashboard.GetChartData("satisfaction", "line", "24h")

	if chartData.ChartType != "line" {
		t.Errorf("Chart type = %v, want line", chartData.ChartType)
	}

	if chartData.TimeRange != "24h" {
		t.Errorf("Time range = %v, want 24h", chartData.TimeRange)
	}

	if len(chartData.Datasets) != 1 {
		t.Errorf("Expected 1 dataset, got %d", len(chartData.Datasets))
	}

	if len(chartData.Labels) != 5 {
		t.Errorf("Expected 5 labels, got %d", len(chartData.Labels))
	}
}

func TestGetChartData_Empty(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	chartData := dashboard.GetChartData("nonexistent", "bar", "7d")

	if len(chartData.Labels) != 0 {
		t.Error("Empty chart should have no labels")
	}

	if chartData.MinValue != 0 {
		t.Error("Empty chart min value should be 0")
	}

	if chartData.MaxValue != 0 {
		t.Error("Empty chart max value should be 0")
	}
}

// List Data Tests

func TestGetListData_Alerts(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	// Add some alerts
	for i := 0; i < 3; i++ {
		dashboard.AddAlert(MobileAlert{
			ID:        "a" + string(rune('0'+i)),
			Type:      "warning",
			Title:     "Alert " + string(rune('0'+i)),
			Message:   "Message " + string(rune('0'+i)),
			CreatedAt: time.Now(),
		})
	}

	listData := dashboard.GetListData("alerts-list")

	if listData.WidgetID != "alerts-list" {
		t.Errorf("Widget ID = %v, want alerts-list", listData.WidgetID)
	}

	if listData.Title != "Recent Alerts" {
		t.Errorf("Title = %v, want Recent Alerts", listData.Title)
	}

	if len(listData.Items) != 3 {
		t.Errorf("Expected 3 items, got %d", len(listData.Items))
	}
}

func TestGetListData_Workspaces(t *testing.T) {
	dashboard := NewMobileDashboard(nil)

	// Add some workspaces
	for i := 0; i < 3; i++ {
		dashboard.AddWorkspace(&MobileWorkspace{
			ID:     "ws-" + string(rune('0'+i)),
			Name:   "Workspace " + string(rune('0'+i)),
			Status: "running",
		})
	}

	listData := dashboard.GetListData("active-workspaces")

	if listData.Title != "Active Workspaces" {
		t.Errorf("Title = %v, want Active Workspaces", listData.Title)
	}

	if len(listData.Items) != 3 {
		t.Errorf("Expected 3 items, got %d", len(listData.Items))
	}
}

// Configuration Tests

func TestDashboardConfig_Defaults(t *testing.T) {
	dashboard := NewMobileDashboard(nil)
	config := dashboard.Config()

	if config.DefaultRefreshInterval != 30*time.Second {
		t.Errorf("Default refresh = %v, want 30s", config.DefaultRefreshInterval)
	}

	if config.MaxRefreshInterval != 5*time.Minute {
		t.Errorf("Max refresh = %v, want 5m", config.MaxRefreshInterval)
	}

	if config.MinRefreshInterval != 5*time.Second {
		t.Errorf("Min refresh = %v, want 5s", config.MinRefreshInterval)
	}

	if config.MaxMetricsPerRequest != 20 {
		t.Errorf("Max metrics = %v, want 20", config.MaxMetricsPerRequest)
	}

	if config.MaxHistoryPoints != 100 {
		t.Errorf("Max history = %v, want 100", config.MaxHistoryPoints)
	}
}

func TestDashboardConfig_SupportedValues(t *testing.T) {
	dashboard := NewMobileDashboard(nil)
	config := dashboard.Config()

	// Check themes
	found := false
	for _, theme := range config.SupportedThemes {
		if theme == config.DefaultTheme {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default theme should be in supported themes")
	}

	// Check languages
	found = false
	for _, lang := range config.SupportedLanguages {
		if lang == config.DefaultLanguage {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default language should be in supported languages")
	}
}

// Widget Tests

func TestWidgetTypes(t *testing.T) {
	types := []WidgetType{WidgetMetricCard, WidgetChart, WidgetList, WidgetGauge, WidgetStatus, WidgetQuickAction}
	expected := []string{"metric_card", "chart", "list", "gauge", "status", "quick_action"}

	for i, widgetType := range types {
		if string(widgetType) != expected[i] {
			t.Errorf("WidgetType[%d] = %v, want %v", i, widgetType, expected[i])
		}
	}
}

func TestDashboardView_Constants(t *testing.T) {
	views := []DashboardView{ViewOverview, ViewMetrics, ViewWorkspaces, ViewAlerts, ViewSettings}
	expected := []string{"overview", "metrics", "workspaces", "alerts", "settings"}

	for i, view := range views {
		if string(view) != expected[i] {
			t.Errorf("DashboardView[%d] = %v, want %v", i, view, expected[i])
		}
	}
}

// API Response Tests

func TestAPIResponse_Success(t *testing.T) {
	response := APIResponse{
		Success: true,
		Data:    map[string]interface{}{"key": "value"},
	}

	if !response.Success {
		t.Error("Response should be success")
	}

	if response.Error != nil {
		t.Error("Success response should not have error")
	}
}

func TestAPIResponse_Error(t *testing.T) {
	response := APIResponse{
		Success: false,
		Error: &APIError{
			Code:    "INVALID_TOKEN",
			Message: "Token is invalid",
		},
	}

	if response.Success {
		t.Error("Response should not be success")
	}

	if response.Error == nil {
		t.Error("Error response should have error")
	}

	if response.Error.Code != "INVALID_TOKEN" {
		t.Errorf("Error code = %v, want INVALID_TOKEN", response.Error.Code)
	}
}

func TestPaginatedResponse(t *testing.T) {
	response := PaginatedResponse{
		Items:      []interface{}{"a", "b", "c"},
		Page:       1,
		PageSize:   10,
		TotalItems: 25,
		TotalPages: 3,
		HasMore:    true,
	}

	if response.Page != 1 {
		t.Errorf("Page = %v, want 1", response.Page)
	}

	if response.TotalPages != 3 {
		t.Errorf("TotalPages = %v, want 3", response.TotalPages)
	}

	if !response.HasMore {
		t.Error("HasMore should be true")
	}
}

// Error Tests

func TestMobileError(t *testing.T) {
	err := &MobileError{Message: "test error"}

	if err.Error() != "test error" {
		t.Errorf("Error message = %v, want test error", err.Error())
	}
}

func TestErrorConstants(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{ErrInvalidView, "invalid dashboard view"},
		{ErrInvalidWidget, "invalid widget type"},
		{ErrWidgetNotFound, "widget not found"},
		{ErrInvalidToken, "invalid device token"},
		{ErrRateLimited, "rate limit exceeded"},
		{ErrOffline, "offline mode active"},
		{ErrUnsupportedTheme, "unsupported theme"},
	}

	for _, tt := range tests {
		if tt.err.Error() != tt.expected {
			t.Errorf("Error = %v, want %v", tt.err.Error(), tt.expected)
		}
	}
}

// Helper Functions Tests

func TestMinMaxFloat(t *testing.T) {
	values := []float64{1.0, 5.0, 3.0, 2.0, 4.0}

	min := minFloat(values)
	if min != 1.0 {
		t.Errorf("minFloat = %v, want 1.0", min)
	}

	max := maxFloat(values)
	if max != 5.0 {
		t.Errorf("maxFloat = %v, want 5.0", max)
	}

	// Empty slice
	emptyMin := minFloat([]float64{})
	if emptyMin != 0 {
		t.Errorf("minFloat of empty = %v, want 0", emptyMin)
	}

	emptyMax := maxFloat([]float64{})
	if emptyMax != 0 {
		t.Errorf("maxFloat of empty = %v, want 0", emptyMax)
	}
}

// Accessibility Settings Tests

func TestAccessibilitySettings(t *testing.T) {
	settings := AccessibilitySettings{
		HighContrast:   true,
		LargeText:      true,
		ReduceMotion:   false,
		ScreenReader:   false,
		ColorBlindMode: "deuteranopia",
	}

	if !settings.HighContrast {
		t.Error("High contrast should be true")
	}

	if settings.ColorBlindMode != "deuteranopia" {
		t.Errorf("Color blind mode = %v, want deuteranopia", settings.ColorBlindMode)
	}
}

// Notification Preferences Tests

func TestNotificationPreferences(t *testing.T) {
	prefs := NotificationPreferences{
		Enabled:           true,
		SoundEnabled:      true,
		VibrationEnabled:  false,
		QuietHoursEnabled: true,
		QuietHoursStart:   "22:00",
		QuietHoursEnd:     "08:00",
		AlertTypes:        []string{"metrics", "workspaces"},
	}

	if !prefs.Enabled {
		t.Error("Notifications should be enabled")
	}

	if prefs.QuietHoursStart != "22:00" {
		t.Errorf("Quiet hours start = %v, want 22:00", prefs.QuietHoursStart)
	}

	if len(prefs.AlertTypes) != 2 {
		t.Errorf("Expected 2 alert types, got %d", len(prefs.AlertTypes))
	}
}

// User Preferences Tests

func TestUserPreferences(t *testing.T) {
	prefs := UserPreferences{
		UserID:   "user-001",
		Theme:    "dark",
		Language: "en",
		DashboardLayouts: map[DashboardView]*DashboardLayout{
			ViewOverview: {View: ViewOverview, Widgets: []Widget{}},
		},
		NotificationPrefs: NotificationPreferences{Enabled: true},
		WidgetDefaults:    map[WidgetType]WidgetConfig{},
		Accessibility:     AccessibilitySettings{},
	}

	if prefs.UserID != "user-001" {
		t.Errorf("UserID = %v, want user-001", prefs.UserID)
	}

	if prefs.DashboardLayouts[ViewOverview] == nil {
		t.Error("Overview layout should exist")
	}
}

// Metric Snapshot Tests

func TestMetricSnapshot(t *testing.T) {
	snapshot := MetricSnapshot{
		Type:      "satisfaction",
		Value:     4.5,
		Label:     "User Satisfaction",
		Trend:     "up",
		UpdatedAt: time.Now(),
	}

	if snapshot.Type != "satisfaction" {
		t.Errorf("Type = %v, want satisfaction", snapshot.Type)
	}

	if snapshot.Value != 4.5 {
		t.Errorf("Value = %v, want 4.5", snapshot.Value)
	}
}

// Mobile Alert Tests

func TestMobileAlert(t *testing.T) {
	alert := MobileAlert{
		ID:          "alert-001",
		Type:        "error",
		Title:       "Critical Error",
		Message:     "Something went wrong",
		Priority:    5,
		Dismissable: false,
		Read:        false,
	}

	if alert.Priority != 5 {
		t.Errorf("Priority = %v, want 5", alert.Priority)
	}

	if alert.Dismissable {
		t.Error("Alert should not be dismissable")
	}
}

// Quick Action Tests

func TestQuickAction(t *testing.T) {
	action := QuickAction{
		ID:       "create-ws",
		Title:    "New Workspace",
		Icon:     "plus",
		Action:   "create_workspace",
		Disabled: false,
		Style:    "primary",
	}

	if action.Style != "primary" {
		t.Errorf("Style = %v, want primary", action.Style)
	}

	if action.Disabled {
		t.Error("Action should not be disabled")
	}
}

// List Item Tests

func TestListItem(t *testing.T) {
	item := ListItem{
		ID:          "item-001",
		Title:       "Test Item",
		Subtitle:    "Subtitle",
		Icon:        "bell",
		Status:      "active",
		ActionURL:   "/item/001",
		Timestamp:   "12:00",
		Badge:       "new",
		SwipeAction: "delete",
	}

	if item.SwipeAction != "delete" {
		t.Errorf("Swipe action = %v, want delete", item.SwipeAction)
	}

	if item.Badge != "new" {
		t.Errorf("Badge = %v, want new", item.Badge)
	}
}

// Widget Config Tests

func TestWidgetConfig(t *testing.T) {
	config := WidgetConfig{
		MetricType:  "satisfaction",
		ChartType:   "line",
		TimeRange:   "7d",
		RefreshRate: 30,
		ShowTrend:   true,
		CompactMode: true,
		Actions:     []string{"view_details"},
	}

	if config.ChartType != "line" {
		t.Errorf("Chart type = %v, want line", config.ChartType)
	}

	if !config.ShowTrend {
		t.Error("Show trend should be true")
	}
}

// Benchmark Tests

func BenchmarkGetDashboard(b *testing.B) {
	dashboard := NewMobileDashboard(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dashboard.GetDashboard(ViewOverview)
	}
}

func BenchmarkGetMetricCards(b *testing.B) {
	dashboard := NewMobileDashboard(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dashboard.GetMetricCards()
	}
}

func BenchmarkAddMetric(b *testing.B) {
	dashboard := NewMobileDashboard(nil)
	metric := MetricSnapshot{
		Type:      "satisfaction",
		Value:     4.5,
		UpdatedAt: time.Now(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dashboard.AddMetric(metric)
	}
}
