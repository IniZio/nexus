// Package alerts provides custom alert rules for Nexus metrics monitoring.
// It allows users to define configurable conditions and actions for automated alerting.
package alerts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
	"time"
)

// AlertSeverity represents the severity level of an alert.
type AlertSeverity string

const (
	SeverityLow      AlertSeverity = "low"
	SeverityMedium   AlertSeverity = "medium"
	SeverityHigh     AlertSeverity = "high"
	SeverityCritical AlertSeverity = "critical"
)

// ConditionType defines the type of condition for an alert rule.
type ConditionType string

const (
	ConditionThreshold     ConditionType = "threshold"      // Value exceeds threshold
	ConditionTrend         ConditionType = "trend"          // Value trending up/down
	ConditionAnomaly       ConditionType = "anomaly"        // Statistical anomaly detected
	ConditionChange        ConditionType = "change"         // Value changed by percentage
	ConditionTime          ConditionType = "time"            // Time-based condition
	ConditionCompound      ConditionType = "compound"        // Multiple conditions combined
)

// ActionType defines the type of action when an alert triggers.
type ActionType string

const (
	ActionSlack        ActionType = "slack"         // Send Slack notification
	ActionWebhook      ActionType = "webhook"       // Call external webhook
	ActionCreateTask   ActionType = "create_task"    // Create Pulse task
	ActionLog          ActionType = "log"           // Log to system
	ActionEscalate     ActionType = "escalate"      // Escalate to higher severity
)

// ConditionOperator defines the comparison operator for conditions.
type ConditionOperator string

const (
	OpGreaterThan    ConditionOperator = "gt"    // >
	OpLessThan       ConditionOperator = "lt"    // <
	OpEqual          ConditionOperator = "eq"    // ==
	OpNotEqual       ConditionOperator = "neq"   // !=
	OpGreaterOrEqual ConditionOperator = "gte"   // >=
	OpLessOrEqual    ConditionOperator = "lte"  // <=
)

// CompoundOperator defines how compound conditions are combined.
type CompoundOperator string

const (
	CompoundAnd CompoundOperator = "and"
	CompoundOr  CompoundOperator = "or"
)

// AlertRule represents a custom alert rule with configurable conditions and actions.
type AlertRule struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Enabled     bool              `json:"enabled"`
	MetricType  string            `json:"metric_type"`  // satisfaction, duration, feedback_count, etc.
	Severity    AlertSeverity     `json:"severity"`

	// Conditions
	ConditionType  ConditionType     `json:"condition_type"`
	Condition      ConditionConfig   `json:"condition"`
	CompoundRules  []AlertRule       `json:"compound_rules,omitempty"`  // For compound conditions
	CompoundOp     CompoundOperator  `json:"compound_op,omitempty"`

	// Actions
	Actions       []ActionConfig    `json:"actions"`

	// Scheduling
	Schedule     *ScheduleConfig   `json:"schedule,omitempty"`

	// Throttling (prevent alert spam)
	Cooldown     time.Duration     `json:"cooldown"`        // Min time between alerts
	LastTriggered time.Time        `json:"last_triggered,omitempty"`

	// Metadata
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	CreatedBy    string            `json:"created_by,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// ConditionConfig defines the configuration for a single condition.
type ConditionConfig struct {
	Operator    ConditionOperator `json:"operator"`
	Value       float64           `json:"value"`
	Window      time.Duration     `json:"window,omitempty"`       // Time window for aggregation
	Threshold   float64           `json:"threshold,omitempty"`   // For threshold conditions
	Trend       string            `json:"trend,omitempty"`       // "increasing", "decreasing", "volatile"
	PercentChange float64         `json:"percent_change,omitempty"` // For change conditions
	Duration    time.Duration     `json:"duration,omitempty"`      // How long condition must persist
}

// ScheduleConfig defines when an alert rule is active.
type ScheduleConfig struct {
	Timezone    string            `json:"timezone"`
	ActiveHours [2]int            `json:"active_hours"`        // e.g., [9, 17] = 9am to 5pm
	ActiveDays  [7]bool           `json:"active_days"`         // Sunday = 0
	StartDate   *time.Time        `json:"start_date,omitempty"`
	EndDate     *time.Time        `json:"end_date,omitempty"`
}

// ActionConfig defines an action to take when an alert triggers.
type ActionConfig struct {
	Type       ActionType         `json:"type"`
	Enabled    bool               `json:"enabled"`

	// Slack action
	SlackChannel string           `json:"slack_channel,omitempty"`
	SlackText   string           `json:"slack_text,omitempty"`

	// Webhook action
	WebhookURL  string            `json:"webhook_url,omitempty"`
	WebhookMethod string         `json:"webhook_method,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`

	// Task creation action
	TaskTitle   string           `json:"task_title,omitempty"`
	TaskProject string           `json:"task_project,omitempty"`
	TaskPriority int             `json:"task_priority,omitempty"`

	// Escalation action
	EscalateTo  string           `json:"escalate_to,omitempty"` // Rule ID to escalate to

	// Rate limiting for this action
	MaxPerHour  int              `json:"max_per_hour,omitempty"`
}

// AlertTriggered represents an alert that has been triggered.
type AlertTriggered struct {
	ID          string        `json:"id"`
	RuleID      string        `json:"rule_id"`
	RuleName    string        `json:"rule_name"`
	Severity    AlertSeverity `json:"severity"`
	MetricType  string        `json:"metric_type"`
	CurrentValue float64      `json:"current_value"`
	Threshold   float64      `json:"threshold"`
	Condition   string        `json:"condition"`
	TriggeredAt time.Time     `json:"triggered_at"`
	Message     string        `json:"message"`
	ActionsSent []string      `json:"actions_sent"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// MetricValue represents a metric value for evaluation.
type MetricValue struct {
	Type      string    `json:"type"`
	Value     float64   `json:"value"`
	Timestamp time.Time `json:"timestamp"`
	Project   string    `json:"project,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// AlertEngine manages and evaluates alert rules.
type AlertEngine struct {
	rules      map[string]*AlertRule
	mu         sync.RWMutex
	evaluators map[ConditionType]ConditionEvaluator
	actions    map[ActionType]ActionHandler

	// Callbacks for external integrations
	SlackCallback    func(ctx context.Context, alert AlertTriggered, channel string) error
	WebhookCallback  func(ctx context.Context, alert AlertTriggered, url string, method string, headers map[string]string) error
	CreateTaskCallback func(ctx context.Context, title, project string, priority int) error

	// Internal state
	triggeredAlerts map[string][]AlertTriggered
}

// ConditionEvaluator evaluates a specific condition type.
type ConditionEvaluator interface {
	Evaluate(rule *AlertRule, currentValue float64, history []MetricValue) bool
}

// ActionHandler executes a specific action type.
type ActionHandler interface {
	Execute(ctx context.Context, engine *AlertEngine, rule *AlertRule, alert AlertTriggered) error
}

// NewAlertEngine creates a new alert engine with default evaluators and handlers.
func NewAlertEngine() *AlertEngine {
	engine := &AlertEngine{
		rules:           make(map[string]*AlertRule),
		evaluators:      make(map[ConditionType]ConditionEvaluator),
		actions:         make(map[ActionType]ActionHandler),
		triggeredAlerts: make(map[string][]AlertTriggered),
	}

	// Register default condition evaluators
	engine.evaluators[ConditionThreshold] = &ThresholdEvaluator{}
	engine.evaluators[ConditionTrend] = &TrendEvaluator{}
	engine.evaluators[ConditionAnomaly] = &AnomalyEvaluator{}
	engine.evaluators[ConditionChange] = &ChangeEvaluator{}
	engine.evaluators[ConditionTime] = &TimeEvaluator{}

	// Register default action handlers
	engine.actions[ActionSlack] = &SlackActionHandler{}
	engine.actions[ActionWebhook] = &WebhookActionHandler{}
	engine.actions[ActionCreateTask] = &CreateTaskActionHandler{}
	engine.actions[ActionLog] = &LogActionHandler{}

	return engine
}

// CreateRule creates a new alert rule with a generated ID.
func (e *AlertEngine) CreateRule(rule *AlertRule) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if rule.ID == "" {
		rule.ID = generateID()
	}
	rule.CreatedAt = time.Now()
	rule.UpdatedAt = rule.CreatedAt
	rule.Enabled = true // Enable by default

	// Validate rule
	if err := e.validateRule(rule); err != nil {
		return fmt.Errorf("invalid rule: %w", err)
	}

	e.rules[rule.ID] = rule
	return nil
}

// validateRule validates an alert rule configuration.
func (e *AlertEngine) validateRule(rule *AlertRule) error {
	if rule.Name == "" {
		return fmt.Errorf("rule name is required")
	}
	if rule.MetricType == "" {
		return fmt.Errorf("metric_type is required")
	}
	if rule.Severity == "" {
		return fmt.Errorf("severity is required")
	}

	// Validate condition type has corresponding evaluator
	if _, ok := e.evaluators[rule.ConditionType]; !ok && rule.ConditionType != ConditionCompound {
		return fmt.Errorf("unknown condition type: %s", rule.ConditionType)
	}

	// Validate at least one action is configured
	if len(rule.Actions) == 0 {
		return fmt.Errorf("at least one action is required")
	}

	// Validate schedule if provided
	if rule.Schedule != nil {
		if err := e.validateSchedule(rule.Schedule); err != nil {
			return fmt.Errorf("invalid schedule: %w", err)
		}
	}

	return nil
}

// validateSchedule validates a schedule configuration.
func (e *AlertEngine) validateSchedule(schedule *ScheduleConfig) error {
	if schedule.ActiveHours[0] < 0 || schedule.ActiveHours[0] > 23 {
		return fmt.Errorf("active_hours start must be 0-23")
	}
	if schedule.ActiveHours[1] < 0 || schedule.ActiveHours[1] > 23 {
		return fmt.Errorf("active_hours end must be 0-23")
	}
	return nil
}

// GetRule retrieves a rule by ID.
func (e *AlertEngine) GetRule(id string) (*AlertRule, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rule, ok := e.rules[id]
	if !ok {
		return nil, fmt.Errorf("rule not found: %s", id)
	}
	return rule, nil
}

// ListRules returns all rules.
func (e *AlertEngine) ListRules() []*AlertRule {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rules := make([]*AlertRule, 0, len(e.rules))
	for _, rule := range e.rules {
		rules = append(rules, rule)
	}
	return rules
}

// ListEnabledRules returns only enabled rules.
func (e *AlertEngine) ListEnabledRules() []*AlertRule {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rules := make([]*AlertRule, 0)
	for _, rule := range e.rules {
		if rule.Enabled {
			rules = append(rules, rule)
		}
	}
	return rules
}

// UpdateRule updates an existing rule.
func (e *AlertEngine) UpdateRule(id string, updates map[string]interface{}) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rule, ok := e.rules[id]
	if !ok {
		return fmt.Errorf("rule not found: %s", id)
	}

	// Apply updates
	if name, ok := updates["name"].(string); ok {
		rule.Name = name
	}
	if desc, ok := updates["description"].(string); ok {
		rule.Description = desc
	}
	if enabled, ok := updates["enabled"].(bool); ok {
		rule.Enabled = enabled
	}
	if severity, ok := updates["severity"].(string); ok {
		rule.Severity = AlertSeverity(severity)
	}

	rule.UpdatedAt = time.Now()

	// Re-validate
	return e.validateRule(rule)
}

// DeleteRule removes a rule.
func (e *AlertEngine) DeleteRule(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, ok := e.rules[id]; !ok {
		return fmt.Errorf("rule not found: %s", id)
	}
	delete(e.rules, id)
	return nil
}

// EvaluateMetric evaluates all enabled rules against a metric value.
func (e *AlertEngine) EvaluateMetric(ctx context.Context, metric MetricValue) []AlertTriggered {
	e.mu.RLock()
	rules := e.ListEnabledRules()
	e.mu.RUnlock()

	var triggered []AlertTriggered

	for _, rule := range rules {
		if rule.MetricType != metric.Type {
			continue
		}

		// Check schedule if configured
		if rule.Schedule != nil && !e.isScheduleActive(rule.Schedule) {
			continue
		}

		// Check cooldown
		if rule.Cooldown > 0 && time.Since(rule.LastTriggered) < rule.Cooldown {
			continue
		}

		// Evaluate condition
		if e.evaluateCondition(rule, metric.Value, nil) {
			alert := e.createAlert(rule, metric)
			triggered = append(triggered, alert)
			rule.LastTriggered = time.Now()

			// Execute actions
			for _, action := range rule.Actions {
				if !action.Enabled {
					continue
				}
				if err := e.executeAction(ctx, rule, alert, action); err != nil {
					// Log error but continue with other actions
					fmt.Printf("alert: action %s failed: %v\n", action.Type, err)
				}
			}
		}
	}

	return triggered
}

// evaluateCondition evaluates a rule's condition against a value.
func (e *AlertEngine) evaluateCondition(rule *AlertRule, currentValue float64, history []MetricValue) bool {
	evaluator, ok := e.evaluators[rule.ConditionType]
	if !ok {
		return false
	}
	return evaluator.Evaluate(rule, currentValue, history)
}

// createAlert creates a triggered alert from a rule evaluation.
func (e *AlertEngine) createAlert(rule *AlertRule, metric MetricValue) AlertTriggered {
	return AlertTriggered{
		ID:           generateID()[:8],
		RuleID:       rule.ID,
		RuleName:     rule.Name,
		Severity:     rule.Severity,
		MetricType:   metric.Type,
		CurrentValue: metric.Value,
		Threshold:    rule.Condition.Value,
		Condition:   fmt.Sprintf("%s %.2f", rule.Condition.Operator, rule.Condition.Value),
		TriggeredAt:  time.Now(),
		Message:      fmt.Sprintf("Alert '%s' triggered: %s = %.2f (threshold: %s %.2f)", rule.Name, metric.Type, metric.Value, rule.Condition.Operator, rule.Condition.Value),
		ActionsSent:  []string{},
	}
}

// executeAction executes a single action for an alert.
func (e *AlertEngine) executeAction(ctx context.Context, rule *AlertRule, alert AlertTriggered, action ActionConfig) error {
	handler, ok := e.actions[action.Type]
	if !ok {
		return fmt.Errorf("unknown action type: %s", action.Type)
	}

	return handler.Execute(ctx, e, rule, alert)
}

// isScheduleActive checks if the current time is within the schedule.
func (e *AlertEngine) isScheduleActive(schedule *ScheduleConfig) bool {
	now := time.Now()

	// Parse timezone (simplified - uses local for now)
	// In production, use a proper timezone database

	// Check active hours
	currentHour := now.Hour()
	if currentHour < schedule.ActiveHours[0] || currentHour >= schedule.ActiveHours[1] {
		return false
	}

	// Check active days
	currentDay := int(now.Weekday())
	if !schedule.ActiveDays[currentDay] {
		return false
	}

	// Check date range if specified
	if schedule.StartDate != nil && now.Before(*schedule.StartDate) {
		return false
	}
	if schedule.EndDate != nil && now.After(*schedule.EndDate) {
		return false
	}

	return true
}

// GetTriggeredAlerts returns recent triggered alerts for a rule.
func (e *AlertEngine) GetTriggeredAlerts(ruleID string) []AlertTriggered {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.triggeredAlerts[ruleID]
}

// ThresholdEvaluator evaluates threshold conditions.
type ThresholdEvaluator struct{}

func (e *ThresholdEvaluator) Evaluate(rule *AlertRule, currentValue float64, history []MetricValue) bool {
	switch rule.Condition.Operator {
	case OpGreaterThan:
		return currentValue > rule.Condition.Value
	case OpLessThan:
		return currentValue < rule.Condition.Value
	case OpEqual:
		return currentValue == rule.Condition.Value
	case OpNotEqual:
		return currentValue != rule.Condition.Value
	case OpGreaterOrEqual:
		return currentValue >= rule.Condition.Value
	case OpLessOrEqual:
		return currentValue <= rule.Condition.Value
	default:
		return false
	}
}

// TrendEvaluator evaluates trend conditions.
type TrendEvaluator struct{}

func (e *TrendEvaluator) Evaluate(rule *AlertRule, currentValue float64, history []MetricValue) bool {
	if len(history) < 3 {
		return false
	}

	// Calculate trend direction
	var trendChanges float64
	for i := 1; i < len(history); i++ {
		trendChanges += history[i].Value - history[i-1].Value
	}

	avgChange := trendChanges / float64(len(history)-1)

	switch rule.Condition.Trend {
	case "increasing":
		return avgChange > 0
	case "decreasing":
		return avgChange < 0
	case "volatile":
		// Check if value fluctuates significantly
		var minVal, maxVal float64 = history[0].Value, history[0].Value
		for _, h := range history {
			if h.Value < minVal {
				minVal = h.Value
			}
			if h.Value > maxVal {
				maxVal = h.Value
			}
		}
		rangeRatio := (maxVal - minVal) / ((maxVal + minVal) / 2)
		return rangeRatio > rule.Condition.Value // Value is percentage threshold
	}
	return false
}

// AnomalyEvaluator evaluates anomaly conditions using simple statistical methods.
type AnomalyEvaluator struct{}

func (e *AnomalyEvaluator) Evaluate(rule *AlertRule, currentValue float64, history []MetricValue) bool {
	if len(history) < 5 {
		return false
	}

	// Calculate mean and stddev
	var sum float64
	for _, h := range history {
		sum += h.Value
	}
	mean := sum / float64(len(history))

	var sumSqDiff float64
	for _, h := range history {
		diff := h.Value - mean
		sumSqDiff += diff * diff
	}
	stdDev := sqrt(sumSqDiff / float64(len(history)-1))

	if stdDev == 0 {
		return currentValue != mean
	}

	// Z-score based anomaly detection
	zScore := (currentValue - mean) / stdDev
	return zScore > rule.Condition.Value // Value is z-score threshold
}

// ChangeEvaluator evaluates percentage change conditions.
type ChangeEvaluator struct{}

func (e *ChangeEvaluator) Evaluate(rule *AlertRule, currentValue float64, history []MetricValue) bool {
	if len(history) == 0 {
		return false
	}

	previousValue := history[len(history)-1].Value
	if previousValue == 0 {
		return false
	}

	percentChange := ((currentValue - previousValue) / previousValue) * 100

	switch rule.Condition.Operator {
	case OpGreaterThan:
		return percentChange > rule.Condition.PercentChange
	case OpLessThan:
		return percentChange < rule.Condition.PercentChange
	default:
		return false
	}
}

// TimeEvaluator evaluates time-based conditions.
type TimeEvaluator struct{}

func (e *TimeEvaluator) Evaluate(rule *AlertRule, currentValue float64, history []MetricValue) bool {
	now := time.Now()

	// Check if current time meets the condition
	// This is typically used for scheduled alerts
	switch rule.Condition.Operator {
	case OpEqual:
		// Check if hour matches
		if int64(now.Hour()) == int64(rule.Condition.Value) {
			return true
		}
	}
	return false
}

// Action Handlers

// SlackActionHandler sends Slack notifications.
type SlackActionHandler struct{}

func (h *SlackActionHandler) Execute(ctx context.Context, engine *AlertEngine, rule *AlertRule, alert AlertTriggered) error {
	if engine.SlackCallback == nil {
		return nil // No callback configured
	}

	// Get channel from action or use default
	channel := "#nexus-alerts"
	for _, action := range rule.Actions {
		if action.Type == ActionSlack && action.SlackChannel != "" {
			channel = action.SlackChannel
			break
		}
	}

	return engine.SlackCallback(ctx, alert, channel)
}

// WebhookActionHandler calls external webhooks.
type WebhookActionHandler struct{}

func (h *WebhookActionHandler) Execute(ctx context.Context, engine *AlertEngine, rule *AlertRule, alert AlertTriggered) error {
	if engine.WebhookCallback == nil {
		return nil
	}

	for _, action := range rule.Actions {
		if action.Type == ActionWebhook && action.WebhookURL != "" {
			if err := engine.WebhookCallback(ctx, alert, action.WebhookURL, action.WebhookMethod, action.Headers); err != nil {
				return err
			}
		}
	}
	return nil
}

// CreateTaskActionHandler creates Pulse tasks.
type CreateTaskActionHandler struct{}

func (h *CreateTaskActionHandler) Execute(ctx context.Context, engine *AlertEngine, rule *AlertRule, alert AlertTriggered) error {
	if engine.CreateTaskCallback == nil {
		return nil
	}

	title := fmt.Sprintf("Alert: %s", rule.Name)
	priority := 2 // Default medium priority

	for _, action := range rule.Actions {
		if action.Type == ActionCreateTask {
			if action.TaskTitle != "" {
				title = action.TaskTitle
			}
			if action.TaskProject != "" {
				priority = action.TaskPriority
			}
			return engine.CreateTaskCallback(ctx, title, action.TaskProject, priority)
		}
	}
	return nil
}

// LogActionHandler logs alerts to the system.
type LogActionHandler struct{}

func (h *LogActionHandler) Execute(ctx context.Context, engine *AlertEngine, rule *AlertRule, alert AlertTriggered) error {
	fmt.Printf("[ALERT] %s - %s (severity: %s, value: %.2f)\n", alert.TriggeredAt.Format(time.RFC3339), alert.RuleName, alert.Severity, alert.CurrentValue)
	return nil
}

// Helper function for square root
func sqrt(x float64) float64 {
	if x < 0 {
		return 0
	}
	return math.Sqrt(x)
}

// generateID generates a random ID.
func generateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
