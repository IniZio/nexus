// Package mobile provides mobile-responsive dashboard APIs for Nexus.
// Optimized for mobile clients with responsive data and touch-friendly interactions.
package mobile

import (
	"sync"
	"time"
)

// DashboardConfig holds mobile dashboard configuration.
type DashboardConfig struct {
	// Refresh intervals
	DefaultRefreshInterval time.Duration
	MaxRefreshInterval     time.Duration
	MinRefreshInterval     time.Duration

	// Data limits
	MaxMetricsPerRequest     int
	MaxHistoryPoints         int
	MaxNotificationsPerPage  int

	// UI preferences
	DefaultTheme             string
	SupportedThemes          []string
	DefaultLanguage          string
	SupportedLanguages       []string

	// Feature flags
	EnablePushNotifications  bool
	EnableOfflineMode        bool
	EnableBiometricAuth      bool
}

// MobileDashboard provides mobile-optimized dashboard data.
type MobileDashboard struct {
	mu sync.RWMutex

	// Data stores
	metrics    map[string][]MetricSnapshot
	alerts     []MobileAlert
	workspaces map[string]*MobileWorkspace

	// Configuration
	config *DashboardConfig
}

// MetricSnapshot represents a metric at a point in time.
type MetricSnapshot struct {
	Type      string    `json:"type"`
	Value     float64   `json:"value"`
	Label     string    `json:"label"`
	Trend     string    `json:"trend"` // "up", "down", "stable"
	UpdatedAt time.Time `json:"updated_at"`
}

// MobileAlert represents a mobile-optimized alert.
type MobileAlert struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // "info", "warning", "error", "success"
	Title       string    `json:"title"`
	Message     string    `json:"message"`
	Priority    int       `json:"priority"` // 1-5, 5 is highest
	ActionURL   string    `json:"action_url,omitempty"`
	Dismissable bool      `json:"dismissable"`
	CreatedAt   time.Time `json:"created_at"`
	Read        bool      `json:"read"`
}

// MobileWorkspace represents a workspace optimized for mobile.
type MobileWorkspace struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"` // "running", "stopped", "creating"
	ResourceUsage string   `json:"resource_usage"` // "low", "medium", "high"
	LastActivity time.Time `json:"last_activity"`
	HasUpdates   bool      `json:"has_updates"`
}

// DashboardView represents different dashboard views.
type DashboardView string

const (
	ViewOverview   DashboardView = "overview"
	ViewMetrics    DashboardView = "metrics"
	ViewWorkspaces DashboardView = "workspaces"
	ViewAlerts     DashboardView = "alerts"
	ViewSettings   DashboardView = "settings"
)

// Widget represents a dashboard widget.
type Widget struct {
	ID          string       `json:"id"`
	Type        WidgetType   `json:"type"`
	Title       string       `json:"title"`
	Position    WidgetPosition `json:"position"`
	Size        WidgetSize   `json:"size"`
	Config      WidgetConfig `json:"config"`
	Visible     bool         `json:"visible"`
}

// WidgetType represents the type of widget.
type WidgetType string

const (
	WidgetMetricCard  WidgetType = "metric_card"
	WidgetChart       WidgetType = "chart"
	WidgetList        WidgetType = "list"
	WidgetGauge       WidgetType = "gauge"
	WidgetStatus      WidgetType = "status"
	WidgetQuickAction WidgetType = "quick_action"
)

// WidgetPosition represents widget position on grid.
type WidgetPosition struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// WidgetSize represents widget size.
type WidgetSize struct {
	Rows    int `json:"rows"`
	Columns int `json:"columns"`
}

// WidgetConfig holds widget-specific configuration.
type WidgetConfig struct {
	MetricType    string   `json:"metric_type,omitempty"`
	ChartType     string   `json:"chart_type,omitempty"` // "line", "bar", "pie"
	TimeRange     string   `json:"time_range,omitempty"`
	RefreshRate   int      `json:"refresh_rate,omitempty"`
	ShowTrend     bool     `json:"show_trend"`
	CompactMode   bool     `json:"compact_mode"`
	Actions       []string `json:"actions,omitempty"`
}

// DashboardLayout represents a user's dashboard layout.
type DashboardLayout struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	View      DashboardView   `json:"view"`
	Widgets   []Widget        `json:"widgets"`
	Theme     string          `json:"theme"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// NotificationPreferences represents notification settings.
type NotificationPreferences struct {
	Enabled           bool     `json:"enabled"`
	SoundEnabled      bool     `json:"sound_enabled"`
	VibrationEnabled  bool     `json:"vibration_enabled"`
	QuietHoursEnabled bool     `json:"quiet_hours_enabled"`
	QuietHoursStart   string   `json:"quiet_hours_start"` // "22:00"
	QuietHoursEnd     string   `json:"quiet_hours_end"`   // "08:00"
	AlertTypes        []string `json:"alert_types"`       // ["metrics", "workspaces", "system"]
}

// UserPreferences represents user preferences for mobile.
type UserPreferences struct {
	UserID           string                 `json:"user_id"`
	Theme            string                 `json:"theme"`
	Language         string                 `json:"language"`
	DashboardLayouts map[DashboardView]*DashboardLayout `json:"dashboard_layouts"`
	NotificationPrefs NotificationPreferences `json:"notification_prefs"`
	WidgetDefaults   map[WidgetType]WidgetConfig `json:"widget_defaults"`
	Accessibility    AccessibilitySettings  `json:"accessibility"`
}

// AccessibilitySettings represents accessibility preferences.
type AccessibilitySettings struct {
	HighContrast    bool   `json:"high_contrast"`
	LargeText       bool   `json:"large_text"`
	ReduceMotion    bool   `json:"reduce_motion"`
	ScreenReader    bool   `json:"screen_reader"`
	ColorBlindMode  string `json:"color_blind_mode"` // "none", "protanopia", "deuteranopia"
}

// MetricCardData represents data for a metric card widget.
type MetricCardData struct {
	WidgetID    string  `json:"widget_id"`
	Title       string  `json:"title"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	Trend       string  `json:"trend"`
	TrendValue  float64 `json:"trend_value"`
	ChangePercent float64 `json:"change_percent"`
	Status      string  `json:"status"` // "normal", "warning", "critical"
	Description string  `json:"description"`
	ActionURL   string  `json:"action_url,omitempty"`
}

// ChartData represents chart data for mobile.
type ChartData struct {
	WidgetID   string      `json:"widget_id"`
	Title      string      `json:"title"`
	ChartType  string      `json:"chart_type"`
	TimeRange  string      `json:"time_range"`
	Labels     []string    `json:"labels"`
	Datasets   []ChartDataset `json:"datasets"`
	MinValue   float64     `json:"min_value"`
	MaxValue   float64     `json:"max_value"`
	CompactMode bool       `json:"compact_mode"`
}

// ChartDataset represents a dataset in a chart.
type ChartDataset struct {
	Label   string    `json:"label"`
	Values  []float64 `json:"values"`
	Color   string    `json:"color"`
	Fill    bool      `json:"fill"`
}

// ListData represents list data for mobile widgets.
type ListData struct {
	WidgetID string     `json:"widget_id"`
	Title    string     `json:"title"`
	Items    []ListItem `json:"items"`
	MaxItems int        `json:"max_items"`
}

// ListItem represents an item in a list.
type ListItem struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle"`
	Icon        string   `json:"icon"`
	Status      string   `json:"status"`
	ActionURL   string   `json:"action_url,omitempty"`
	Timestamp   string   `json:"timestamp"`
	Badge       string   `json:"badge,omitempty"`
	SwipeAction string   `json:"swipe_action,omitempty"` // "delete", "action"
}

// QuickAction represents a quick action button.
type QuickAction struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Icon     string `json:"icon"`
	Action   string `json:"action"` // API endpoint or action name
	Params   string `json:"params,omitempty"`
	Disabled bool   `json:"disabled"`
	Style    string `json:"style"` // "primary", "secondary", "danger"
}

// RefreshTokenRequest represents a token refresh request.
type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// DeviceTokenRequest represents a device token registration.
type DeviceTokenRequest struct {
	Token     string `json:"token"`
	Platform  string `json:"platform"` // "ios", "android"
	AppVersion string `json:"app_version"`
}

// APIResponse represents a standard API response.
type APIResponse struct {
	Success   bool        `json:"success"`
	Data      interface{} `json:"data,omitempty"`
	Error     *APIError   `json:"error,omitempty"`
	Meta      ResponseMeta `json:"meta,omitempty"`
}

// APIError represents an API error.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// ResponseMeta represents response metadata.
type ResponseMeta struct {
	RequestID    string `json:"request_id"`
	Timestamp    string `json:"timestamp"`
	CacheHit     bool   `json:"cache_hit"`
	NextRefresh  string `json:"next_refresh,omitempty"`
}

// PaginatedResponse represents a paginated response.
type PaginatedResponse struct {
	Items      []interface{} `json:"items"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	TotalItems int           `json:"total_items"`
	TotalPages int           `json:"total_pages"`
	HasMore    bool          `json:"has_more"`
}

// NewMobileDashboard creates a new mobile dashboard.
func NewMobileDashboard(config *DashboardConfig) *MobileDashboard {
	if config == nil {
		config = &DashboardConfig{
			DefaultRefreshInterval: 30 * time.Second,
			MaxRefreshInterval:     5 * time.Minute,
			MinRefreshInterval:     5 * time.Second,
			MaxMetricsPerRequest:   20,
			MaxHistoryPoints:       100,
			MaxNotificationsPerPage: 50,
			DefaultTheme:           "system",
			SupportedThemes:        []string{"light", "dark", "system"},
			DefaultLanguage:        "en",
			SupportedLanguages:     []string{"en", "es", "fr", "de", "ja", "zh"},
			EnablePushNotifications: true,
			EnableOfflineMode:      true,
			EnableBiometricAuth:    false,
		}
	}

	return &MobileDashboard{
		metrics:    make(map[string][]MetricSnapshot),
		alerts:     make([]MobileAlert, 0),
		workspaces: make(map[string]*MobileWorkspace),
		config:     config,
	}
}

// GetDashboard returns the dashboard for a specific view.
func (d *MobileDashboard) GetDashboard(view DashboardView) (*DashboardLayout, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	layout := &DashboardLayout{
		ID:        string(view),
		Name:      string(view),
		View:      view,
		Widgets:   d.getDefaultWidgets(view),
		Theme:     d.config.DefaultTheme,
		UpdatedAt: time.Now(),
	}

	return layout, nil
}

// getDefaultWidgets returns default widgets for a view.
func (d *MobileDashboard) getDefaultWidgets(view DashboardView) []Widget {
	widgets := []Widget{}

	switch view {
	case ViewOverview:
		widgets = []Widget{
			{ID: "quick-actions", Type: WidgetQuickAction, Title: "Quick Actions", Position: WidgetPosition{X: 0, Y: 0}, Size: WidgetSize{Rows: 1, Columns: 4}, Visible: true},
			{ID: "satisfaction", Type: WidgetMetricCard, Title: "Satisfaction", Position: WidgetPosition{X: 0, Y: 1}, Size: WidgetSize{Rows: 1, Columns: 2}, Visible: true},
			{ID: "active-workspaces", Type: WidgetMetricCard, Title: "Workspaces", Position: WidgetPosition{X: 2, Y: 1}, Size: WidgetSize{Rows: 1, Columns: 2}, Visible: true},
			{ID: "alerts-summary", Type: WidgetList, Title: "Recent Alerts", Position: WidgetPosition{X: 0, Y: 2}, Size: WidgetSize{Rows: 2, Columns: 4}, Visible: true},
			{ID: "satisfaction-chart", Type: WidgetChart, Title: "Satisfaction Trend", Position: WidgetPosition{X: 0, Y: 4}, Size: WidgetSize{Rows: 2, Columns: 4}, Visible: true},
		}
	case ViewMetrics:
		widgets = []Widget{
			{ID: "velocity", Type: WidgetMetricCard, Title: "Velocity", Position: WidgetPosition{X: 0, Y: 0}, Size: WidgetSize{Rows: 1, Columns: 2}, Visible: true},
			{ID: "quality", Type: WidgetMetricCard, Title: "Quality", Position: WidgetPosition{X: 2, Y: 0}, Size: WidgetSize{Rows: 1, Columns: 2}, Visible: true},
			{ID: "efficiency", Type: WidgetMetricCard, Title: "Efficiency", Position: WidgetPosition{X: 0, Y: 1}, Size: WidgetSize{Rows: 1, Columns: 2}, Visible: true},
			{ID: "collaboration", Type: WidgetMetricCard, Title: "Collaboration", Position: WidgetPosition{X: 2, Y: 1}, Size: WidgetSize{Rows: 1, Columns: 2}, Visible: true},
			{ID: "metrics-chart", Type: WidgetChart, Title: "Metrics Over Time", Position: WidgetPosition{X: 0, Y: 2}, Size: WidgetSize{Rows: 2, Columns: 4}, Visible: true},
		}
	case ViewWorkspaces:
		widgets = []Widget{
			{ID: "workspace-status", Type: WidgetStatus, Title: "Workspace Status", Position: WidgetPosition{X: 0, Y: 0}, Size: WidgetSize{Rows: 1, Columns: 4}, Visible: true},
			{ID: "active-workspaces", Type: WidgetList, Title: "Active Workspaces", Position: WidgetPosition{X: 0, Y: 1}, Size: WidgetSize{Rows: 3, Columns: 4}, Visible: true},
		}
	case ViewAlerts:
		widgets = []Widget{
			{ID: "alerts-list", Type: WidgetList, Title: "All Alerts", Position: WidgetPosition{X: 0, Y: 0}, Size: WidgetSize{Rows: 4, Columns: 4}, Visible: true},
		}
	case ViewSettings:
		widgets = []Widget{
			{ID: "theme-settings", Type: WidgetList, Title: "Appearance", Position: WidgetPosition{X: 0, Y: 0}, Size: WidgetSize{Rows: 1, Columns: 4}, Visible: true},
			{ID: "notification-settings", Type: WidgetList, Title: "Notifications", Position: WidgetPosition{X: 0, Y: 1}, Size: WidgetSize{Rows: 1, Columns: 4}, Visible: true},
			{ID: "security-settings", Type: WidgetList, Title: "Security", Position: WidgetPosition{X: 0, Y: 2}, Size: WidgetSize{Rows: 1, Columns: 4}, Visible: true},
			{ID: "about", Type: WidgetList, Title: "About", Position: WidgetPosition{X: 0, Y: 3}, Size: WidgetSize{Rows: 1, Columns: 4}, Visible: true},
		}
	}

	return widgets
}

// GetQuickActions returns available quick actions.
func (d *MobileDashboard) GetQuickActions() []QuickAction {
	return []QuickAction{
		{ID: "create-workspace", Title: "New Workspace", Icon: "plus", Action: "create_workspace", Style: "primary"},
		{ID: "view-analytics", Title: "Analytics", Icon: "chart", Action: "view_analytics", Style: "secondary"},
		{ID: "view-alerts", Title: "Alerts", Icon: "bell", Action: "view_alerts", Style: "secondary"},
		{ID: "sync", Title: "Sync", Icon: "refresh", Action: "sync", Style: "secondary"},
	}
}

// AddMetric adds a metric snapshot.
func (d *MobileDashboard) AddMetric(metric MetricSnapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.metrics[metric.Type] = append(d.metrics[metric.Type], metric)

	// Limit history
	maxPoints := d.config.MaxHistoryPoints
	if len(d.metrics[metric.Type]) > maxPoints {
		d.metrics[metric.Type] = d.metrics[metric.Type][len(d.metrics[metric.Type])-maxPoints:]
	}
}

// GetMetrics returns metrics for a type.
func (d *MobileDashboard) GetMetrics(metricType string, limit int) []MetricSnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()

	metrics := d.metrics[metricType]
	if limit > 0 && len(metrics) > limit {
		return metrics[len(metrics)-limit:]
	}
	return metrics
}

// AddAlert adds a mobile alert.
func (d *MobileDashboard) AddAlert(alert MobileAlert) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.alerts = append([]MobileAlert{alert}, d.alerts...)
}

// GetAlerts returns alerts with pagination.
func (d *MobileDashboard) GetAlerts(page, pageSize int) []MobileAlert {
	d.mu.RLock()
	defer d.mu.RUnlock()

	start := page * pageSize
	if start >= len(d.alerts) {
		return []MobileAlert{}
	}

	end := start + pageSize
	if end > len(d.alerts) {
		end = len(d.alerts)
	}

	return d.alerts[start:end]
}

// AddWorkspace adds a workspace.
func (d *MobileDashboard) AddWorkspace(workspace *MobileWorkspace) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.workspaces[workspace.ID] = workspace
}

// GetWorkspaces returns all workspaces.
func (d *MobileDashboard) GetWorkspaces() []*MobileWorkspace {
	d.mu.RLock()
	defer d.mu.RUnlock()

	workspaces := make([]*MobileWorkspace, 0, len(d.workspaces))
	for _, ws := range d.workspaces {
		workspaces = append(workspaces, ws)
	}
	return workspaces
}

// GetMetricCards returns metric card data.
func (d *MobileDashboard) GetMetricCards() []MetricCardData {
	d.mu.RLock()
	defer d.mu.RUnlock()

	cards := []MetricCardData{
		{
			WidgetID:    "satisfaction",
			Title:       "Satisfaction",
			Value:       4.33,
			Unit:        "/5",
			Trend:       "stable",
			Status:      "normal",
			Description: "Average user satisfaction",
		},
		{
			WidgetID:    "velocity",
			Title:       "Velocity",
			Value:       5.2,
			Unit:        "tasks/day",
			Trend:       "up",
			ChangePercent: 12.5,
			Status:      "normal",
			Description: "Tasks completed per day",
		},
		{
			WidgetID:    "quality",
			Title:       "Quality Score",
			Value:       87.5,
			Unit:        "%",
			Trend:       "up",
			ChangePercent: 3.2,
			Status:      "normal",
			Description: "Code review pass rate",
		},
		{
			WidgetID:    "efficiency",
			Title:       "Efficiency",
			Value:       92.0,
			Unit:        "%",
			Trend:       "stable",
			Status:      "normal",
			Description: "Task completion rate",
		},
	}

	return cards
}

// GetChartData returns chart data for a metric.
func (d *MobileDashboard) GetChartData(metricType, chartType, timeRange string) *ChartData {
	d.mu.RLock()
	defer d.mu.RUnlock()

	metrics := d.metrics[metricType]
	labels := make([]string, len(metrics))
	values := make([]float64, len(metrics))

	for i, m := range metrics {
		labels[i] = m.UpdatedAt.Format("15:04")
		values[i] = m.Value
	}

	return &ChartData{
		WidgetID:   metricType,
		Title:      metricType,
		ChartType:  chartType,
		TimeRange:  timeRange,
		Labels:     labels,
		Datasets: []ChartDataset{
			{Label: metricType, Values: values, Color: "#3B82F6", Fill: false},
		},
		MinValue:    minFloat(values),
		MaxValue:    maxFloat(values),
		CompactMode: true,
	}
}

func minFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	min := vals[0]
	for _, v := range vals[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

func maxFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	max := vals[0]
	for _, v := range vals[1:] {
		if v > max {
			max = v
		}
	}
	return max
}

// GetListData returns list data for a widget.
func (d *MobileDashboard) GetListData(widgetID string) *ListData {
	d.mu.RLock()
	defer d.mu.RUnlock()

	switch widgetID {
	case "alerts-list":
		items := make([]ListItem, 0, len(d.alerts))
		for _, alert := range d.alerts[:min(len(d.alerts), 10)] {
			items = append(items, ListItem{
				ID:          alert.ID,
				Title:       alert.Title,
				Subtitle:    alert.Message,
				Icon:        alert.Type,
				Status:      alert.Type,
				Timestamp:   alert.CreatedAt.Format("15:04"),
				Badge:       alert.Type,
			})
		}
		return &ListData{WidgetID: widgetID, Title: "Recent Alerts", Items: items, MaxItems: 10}
	case "active-workspaces":
		items := make([]ListItem, 0, len(d.workspaces))
		for id, ws := range d.workspaces {
			items = append(items, ListItem{
				ID:          id,
				Title:       ws.Name,
				Subtitle:    ws.Status,
				Icon:        "desktop",
				Status:      ws.Status,
				Timestamp:   ws.LastActivity.Format("15:04"),
			})
		}
		return &ListData{WidgetID: widgetID, Title: "Active Workspaces", Items: items, MaxItems: 20}
	default:
		return &ListData{WidgetID: widgetID, Title: widgetID, Items: []ListItem{}, MaxItems: 0}
	}
}

// Config returns the dashboard configuration.
func (d *MobileDashboard) Config() *DashboardConfig {
	return d.config
}

// Errors

var (
	ErrInvalidView      = &MobileError{Message: "invalid dashboard view"}
	ErrInvalidWidget    = &MobileError{Message: "invalid widget type"}
	ErrWidgetNotFound   = &MobileError{Message: "widget not found"}
	ErrInvalidToken     = &MobileError{Message: "invalid device token"}
	ErrRateLimited      = &MobileError{Message: "rate limit exceeded"}
	ErrOffline          = &MobileError{Message: "offline mode active"}
	ErrUnsupportedTheme = &MobileError{Message: "unsupported theme"}
)

// MobileError represents a mobile API error.
type MobileError struct {
	Message string
}

func (e *MobileError) Error() string {
	return e.Message
}
