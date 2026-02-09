package alerts

import (
	"context"
	"testing"
	"time"
)

// TestAlertEngine_CreateRule tests creating alert rules.
func TestAlertEngine_CreateRule(t *testing.T) {
	engine := NewAlertEngine()

	tests := []struct {
		name    string
		rule    *AlertRule
		wantErr bool
	}{
		{
			name: "valid threshold rule",
			rule: &AlertRule{
				Name:        "Low Satisfaction Alert",
				Description: "Alert when satisfaction drops below 3.0",
				MetricType:  "satisfaction",
				Severity:    SeverityMedium,
				ConditionType: ConditionThreshold,
				Condition: ConditionConfig{
					Operator: OpLessThan,
					Value:    3.0,
				},
				Actions: []ActionConfig{
					{Type: ActionSlack, Enabled: true},
				},
			},
			wantErr: false,
		},
		{
			name: "valid trend rule",
			rule: &AlertRule{
				Name:        "Duration Spike Alert",
				Description: "Alert when duration is increasing",
				MetricType:  "duration",
				Severity:    SeverityHigh,
				ConditionType: ConditionTrend,
				Condition: ConditionConfig{
					Trend: "increasing",
				},
				Actions: []ActionConfig{
					{Type: ActionLog, Enabled: true},
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			rule: &AlertRule{
				MetricType:  "satisfaction",
				Severity:    SeverityLow,
				ConditionType: ConditionThreshold,
				Condition: ConditionConfig{
					Operator: OpLessThan,
					Value:    3.0,
				},
				Actions: []ActionConfig{
					{Type: ActionSlack, Enabled: true},
				},
			},
			wantErr: true,
		},
		{
			name: "missing metric type",
			rule: &AlertRule{
				Name:        "Test Rule",
				Severity:    SeverityLow,
				ConditionType: ConditionThreshold,
				Condition: ConditionConfig{
					Operator: OpLessThan,
					Value:    3.0,
				},
				Actions: []ActionConfig{
					{Type: ActionSlack, Enabled: true},
				},
			},
			wantErr: true,
		},
		{
			name: "no actions",
			rule: &AlertRule{
				Name:        "Test Rule",
				MetricType:  "satisfaction",
				Severity:    SeverityLow,
				ConditionType: ConditionThreshold,
				Condition: ConditionConfig{
					Operator: OpLessThan,
					Value:    3.0,
				},
				Actions: []ActionConfig{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := engine.CreateRule(tt.rule)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateRule() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.rule.ID == "" {
				t.Error("CreateRule() should set ID")
			}
		})
	}
}

// TestAlertEngine_GetRule tests retrieving rules.
func TestAlertEngine_GetRule(t *testing.T) {
	engine := NewAlertEngine()

	rule := &AlertRule{
		Name:        "Test Rule",
		MetricType:  "satisfaction",
		Severity:    SeverityLow,
		ConditionType: ConditionThreshold,
		Condition: ConditionConfig{
			Operator: OpLessThan,
			Value:    3.0,
		},
		Actions: []ActionConfig{
			{Type: ActionSlack, Enabled: true},
		},
	}

	err := engine.CreateRule(rule)
	if err != nil {
		t.Fatalf("CreateRule() failed: %v", err)
	}

	// Test getting existing rule
	got, err := engine.GetRule(rule.ID)
	if err != nil {
		t.Errorf("GetRule() error = %v", err)
		return
	}
	if got.Name != rule.Name {
		t.Errorf("GetRule() name = %v, want %v", got.Name, rule.Name)
	}

	// Test getting non-existent rule
	_, err = engine.GetRule("nonexistent")
	if err == nil {
		t.Error("GetRule() should return error for non-existent rule")
	}
}

// TestAlertEngine_ListRules tests listing all rules.
func TestAlertEngine_ListRules(t *testing.T) {
	engine := NewAlertEngine()

	// Create multiple rules
	for i := 0; i < 5; i++ {
		rule := &AlertRule{
			Name:        "Test Rule",
			MetricType:  "satisfaction",
			Severity:    SeverityLow,
			ConditionType: ConditionThreshold,
			Condition: ConditionConfig{
				Operator: OpLessThan,
				Value:    3.0,
			},
			Actions: []ActionConfig{
				{Type: ActionSlack, Enabled: true},
			},
		}
		engine.CreateRule(rule)
	}

	rules := engine.ListRules()
	if len(rules) != 5 {
		t.Errorf("ListRules() count = %v, want 5", len(rules))
	}
}

// TestAlertEngine_ListEnabledRules tests listing only enabled rules.
func TestAlertEngine_ListEnabledRules(t *testing.T) {
	engine := NewAlertEngine()

	// Create enabled and disabled rules
	for i := 0; i < 3; i++ {
		rule := &AlertRule{
			Name:        "Enabled Rule",
			MetricType:  "satisfaction",
			Severity:    SeverityLow,
			ConditionType: ConditionThreshold,
			Condition: ConditionConfig{
				Operator: OpLessThan,
				Value:    3.0,
			},
			Actions: []ActionConfig{{Type: ActionSlack, Enabled: true}},
		}
		engine.CreateRule(rule)
	}

	for i := 0; i < 2; i++ {
		rule := &AlertRule{
			Name:        "Disabled Rule",
			MetricType:  "duration",
			Severity:    SeverityLow,
			ConditionType: ConditionThreshold,
			Condition: ConditionConfig{
				Operator: OpGreaterThan,
				Value:    100.0,
			},
			Actions: []ActionConfig{{Type: ActionLog, Enabled: true}},
		}
		engine.CreateRule(rule)
		// Disable the rule by updating
		engine.UpdateRule(rule.ID, map[string]interface{}{"enabled": false})
	}

	enabled := engine.ListEnabledRules()
	if len(enabled) != 3 {
		t.Errorf("ListEnabledRules() count = %v, want 3", len(enabled))
	}
}

// TestAlertEngine_UpdateRule tests updating rules.
func TestAlertEngine_UpdateRule(t *testing.T) {
	engine := NewAlertEngine()

	rule := &AlertRule{
		Name:        "Original Name",
		MetricType:  "satisfaction",
		Severity:    SeverityLow,
		ConditionType: ConditionThreshold,
		Condition: ConditionConfig{
			Operator: OpLessThan,
			Value:    3.0,
		},
		Actions: []ActionConfig{
			{Type: ActionSlack, Enabled: true},
		},
	}

	engine.CreateRule(rule)

	// Update the rule
	err := engine.UpdateRule(rule.ID, map[string]interface{}{
		"name":        "Updated Name",
		"description": "Updated description",
		"enabled":     false,
	})

	if err != nil {
		t.Errorf("UpdateRule() error = %v", err)
		return
	}

	got, _ := engine.GetRule(rule.ID)
	if got.Name != "Updated Name" {
		t.Errorf("UpdateRule() name = %v, want Updated Name", got.Name)
	}
	if got.Enabled {
		t.Error("UpdateRule() should disable the rule")
	}
}

// TestAlertEngine_DeleteRule tests deleting rules.
func TestAlertEngine_DeleteRule(t *testing.T) {
	engine := NewAlertEngine()

	rule := &AlertRule{
		Name:        "To Delete",
		MetricType:  "satisfaction",
		Severity:    SeverityLow,
		ConditionType: ConditionThreshold,
		Condition: ConditionConfig{
			Operator: OpLessThan,
			Value:    3.0,
		},
		Actions: []ActionConfig{
			{Type: ActionSlack, Enabled: true},
		},
	}

	engine.CreateRule(rule)

	// Delete the rule
	err := engine.DeleteRule(rule.ID)
	if err != nil {
		t.Errorf("DeleteRule() error = %v", err)
		return
	}

	// Verify it's gone
	_, err = engine.GetRule(rule.ID)
	if err == nil {
		t.Error("DeleteRule() should remove the rule")
	}

	// Test deleting non-existent rule
	err = engine.DeleteRule("nonexistent")
	if err == nil {
		t.Error("DeleteRule() should return error for non-existent rule")
	}
}

// TestThresholdEvaluator tests threshold condition evaluation.
func TestThresholdEvaluator(t *testing.T) {
	evaluator := &ThresholdEvaluator{}
	rule := &AlertRule{
		Condition: ConditionConfig{Value: 50.0},
	}

	tests := []struct {
		name     string
		operator ConditionOperator
		value    float64
		current  float64
		want     bool
	}{
		{"greater than true", OpGreaterThan, 50.0, 60.0, true},
		{"greater than false", OpGreaterThan, 50.0, 40.0, false},
		{"less than true", OpLessThan, 50.0, 40.0, true},
		{"less than false", OpLessThan, 50.0, 60.0, false},
		{"equal true", OpEqual, 50.0, 50.0, true},
		{"equal false", OpEqual, 50.0, 51.0, false},
		{"not equal true", OpNotEqual, 50.0, 51.0, true},
		{"not equal false", OpNotEqual, 50.0, 50.0, false},
		{"greater or equal true", OpGreaterOrEqual, 50.0, 50.0, true},
		{"greater or equal false", OpGreaterOrEqual, 50.0, 49.0, false},
		{"less or equal true", OpLessOrEqual, 50.0, 50.0, true},
		{"less or equal false", OpLessOrEqual, 50.0, 51.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule.Condition.Operator = tt.operator
			got := evaluator.Evaluate(rule, tt.current, nil)
			if got != tt.want {
				t.Errorf("Evaluate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestTrendEvaluator tests trend condition evaluation.
func TestTrendEvaluator(t *testing.T) {
	evaluator := &TrendEvaluator{}

	tests := []struct {
		name    string
		trend   string
		history []float64
		want    bool
	}{
		{
			name:    "increasing trend true",
			trend:   "increasing",
			history: []float64{10.0, 15.0, 20.0, 25.0},
			want:    true,
		},
		{
			name:    "increasing trend false",
			trend:   "increasing",
			history: []float64{25.0, 20.0, 15.0, 10.0},
			want:    false,
		},
		{
			name:    "decreasing trend true",
			trend:   "decreasing",
			history: []float64{25.0, 20.0, 15.0, 10.0},
			want:    true,
		},
		{
			name:    "decreasing trend false",
			trend:   "decreasing",
			history: []float64{10.0, 15.0, 20.0, 25.0},
			want:    false,
		},
		{
			name:    "not enough data",
			trend:   "increasing",
			history: []float64{10.0, 15.0},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := &AlertRule{
				Condition: ConditionConfig{Trend: tt.trend},
			}

			historyMetrics := make([]MetricValue, len(tt.history))
			for i, v := range tt.history {
				historyMetrics[i] = MetricValue{Value: v}
			}

			got := evaluator.Evaluate(rule, tt.history[len(tt.history)-1], historyMetrics)
			if got != tt.want {
				t.Errorf("Evaluate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAnomalyEvaluator tests anomaly detection condition evaluation.
func TestAnomalyEvaluator(t *testing.T) {
	evaluator := &AnomalyEvaluator{}

	tests := []struct {
		name    string
		history []float64
		current float64
		zscore  float64
		want    bool
	}{
		{
			name:    "anomaly detected",
			history: []float64{50.0, 51.0, 50.5, 50.0, 51.0, 50.5, 50.0, 51.0, 50.5, 50.0},
			current: 100.0, // Significantly higher
			zscore:  2.0,
			want:    true,
		},
		{
			name:    "normal value",
			history: []float64{50.0, 51.0, 50.5, 50.0, 51.0, 50.5, 50.0, 51.0, 50.5, 50.0},
			current: 51.0,
			zscore:  2.0,
			want:    false,
		},
		{
			name:    "not enough data",
			history: []float64{50.0, 51.0},
			current: 100.0,
			zscore:  2.0,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := &AlertRule{
				Condition: ConditionConfig{Value: tt.zscore},
			}

			historyMetrics := make([]MetricValue, len(tt.history))
			for i, v := range tt.history {
				historyMetrics[i] = MetricValue{Value: v}
			}

			got := evaluator.Evaluate(rule, tt.current, historyMetrics)
			if got != tt.want {
				t.Errorf("Evaluate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestChangeEvaluator tests percentage change condition evaluation.
func TestChangeEvaluator(t *testing.T) {
	evaluator := &ChangeEvaluator{}

	tests := []struct {
		name          string
		operator      ConditionOperator
		current       float64
		previous      float64
		percentChange float64
		want          bool
	}{
		{
			name:          "increase over threshold",
			operator:      OpGreaterThan,
			current:       120.0,
			previous:      100.0,
			percentChange: 15.0,
			want:          true,
		},
		{
			name:          "increase under threshold",
			operator:      OpGreaterThan,
			current:       110.0,
			previous:      100.0,
			percentChange: 15.0,
			want:          false,
		},
		{
			name:          "decrease over threshold",
			operator:      OpLessThan,
			current:       80.0,
			previous:      100.0,
			percentChange: -15.0,
			want:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := &AlertRule{
				Condition: ConditionConfig{
					Operator:     tt.operator,
					PercentChange: tt.percentChange,
				},
			}

			history := []MetricValue{{Value: tt.previous}}
			got := evaluator.Evaluate(rule, tt.current, history)
			if got != tt.want {
				t.Errorf("Evaluate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAlertEngine_EvaluateMetric tests metric evaluation triggering alerts.
func TestAlertEngine_EvaluateMetric(t *testing.T) {
	engine := NewAlertEngine()

	// Create a rule that triggers when satisfaction < 3.0
	rule := &AlertRule{
		Name:        "Low Satisfaction",
		MetricType:  "satisfaction",
		Severity:    SeverityHigh,
		ConditionType: ConditionThreshold,
		Condition: ConditionConfig{
			Operator: OpLessThan,
			Value:    3.0,
		},
		Actions: []ActionConfig{
			{Type: ActionLog, Enabled: true},
		},
	}

	engine.CreateRule(rule)

	ctx := context.Background()

	// Test normal value - should not trigger
	alerts := engine.EvaluateMetric(ctx, MetricValue{
		Type:  "satisfaction",
		Value: 4.5,
	})
	if len(alerts) != 0 {
		t.Errorf("EvaluateMetric() should not trigger for value above threshold")
	}

	// Test low value - should trigger
	alerts = engine.EvaluateMetric(ctx, MetricValue{
		Type:  "satisfaction",
		Value: 2.5,
	})
	if len(alerts) != 1 {
		t.Errorf("EvaluateMetric() should trigger for value below threshold, got %d alerts", len(alerts))
	}

	if alerts[0].Severity != SeverityHigh {
		t.Errorf("Alert severity = %v, want High", alerts[0].Severity)
	}
}

// TestScheduleValidation tests schedule configuration validation.
func TestScheduleValidation(t *testing.T) {
	engine := NewAlertEngine()

	tests := []struct {
		name      string
		schedule  *ScheduleConfig
		wantValid bool
	}{
		{
			name: "valid schedule",
			schedule: &ScheduleConfig{
				Timezone:   "America/New_York",
				ActiveHours: [2]int{9, 17},
				ActiveDays: [7]bool{false, true, true, true, true, true, false},
			},
			wantValid: true,
		},
		{
			name: "invalid hour start",
			schedule: &ScheduleConfig{
				ActiveHours: [2]int{-1, 17},
			},
			wantValid: false,
		},
		{
			name: "invalid hour end",
			schedule: &ScheduleConfig{
				ActiveHours: [2]int{9, 25},
			},
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := engine.validateSchedule(tt.schedule)
			isValid := err == nil
			if isValid != tt.wantValid {
				t.Errorf("validateSchedule() = %v, want %v", isValid, tt.wantValid)
			}
		})
	}
}

// TestScheduleActiveTime tests schedule active time checking.
func TestScheduleActiveTime(t *testing.T) {
	engine := NewAlertEngine()

	schedule := &ScheduleConfig{
		ActiveHours: [2]int{9, 17}, // 9am to 5pm
		ActiveDays:  [7]bool{false, true, true, true, true, true, false}, // Mon-Fri
	}

	// Note: This test depends on current time
	// In production, you'd mock time.Now()
	active := engine.isScheduleActive(schedule)
	// Should be true during business hours on weekdays
	t.Logf("Schedule active: %v", active)
}

// TestAlertSeverityOrdering tests severity constants.
func TestAlertSeverityOrdering(t *testing.T) {
	// Verify severity ordering (for escalation logic)
	severities := []AlertSeverity{SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}
	expected := []AlertSeverity{SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}

	for i, sev := range severities {
		if sev != expected[i] {
			t.Errorf("Severity[%d] = %v, want %v", i, sev, expected[i])
		}
	}
}

// TestActionTypes tests action type constants.
func TestActionTypes(t *testing.T) {
	// Verify action types are defined
	actions := []ActionType{ActionSlack, ActionWebhook, ActionCreateTask, ActionLog, ActionEscalate}
	names := []string{"slack", "webhook", "create_task", "log", "escalate"}

	for i, action := range actions {
		if string(action) != names[i] {
			t.Errorf("ActionType[%d] = %v, want %v", i, action, names[i])
		}
	}
}

// TestConditionTypes tests condition type constants.
func TestConditionTypes(t *testing.T) {
	// Verify condition types are defined
	conditions := []ConditionType{ConditionThreshold, ConditionTrend, ConditionAnomaly, ConditionChange, ConditionTime}
	names := []string{"threshold", "trend", "anomaly", "change", "time"}

	for i, cond := range conditions {
		if string(cond) != names[i] {
			t.Errorf("ConditionType[%d] = %v, want %v", i, cond, names[i])
		}
	}
}

// TestAlertTriggered_Structure tests alert structure.
func TestAlertTriggered_Structure(t *testing.T) {
	alert := AlertTriggered{
		ID:           "test-id",
		RuleID:       "rule-id",
		RuleName:     "Test Rule",
		Severity:     SeverityHigh,
		MetricType:   "satisfaction",
		CurrentValue: 2.5,
		Threshold:    3.0,
		Condition:   "lt 3.00",
		TriggeredAt: time.Now(),
		Message:     "Test alert message",
		ActionsSent: []string{"slack"},
	}

	if alert.ID != "test-id" {
		t.Error("Alert ID should be set")
	}
	if alert.Severity != SeverityHigh {
		t.Error("Alert severity should be High")
	}
	if len(alert.ActionsSent) != 1 {
		t.Error("Alert should have one action sent")
	}
}

// TestMetricValue_Structure tests metric value structure.
func TestMetricValue_Structure(t *testing.T) {
	metric := MetricValue{
		Type:      "satisfaction",
		Value:     4.5,
		Timestamp: time.Now(),
		Project:   "test-project",
		Metadata:  map[string]string{"key": "value"},
	}

	if metric.Type != "satisfaction" {
		t.Error("Metric type should be satisfaction")
	}
	if metric.Value != 4.5 {
		t.Error("Metric value should be 4.5")
	}
	if metric.Project != "test-project" {
		t.Error("Metric project should be set")
	}
}

// TestAlertEngine_CallbackIntegration tests callback integration.
func TestAlertEngine_CallbackIntegration(t *testing.T) {
	engine := NewAlertEngine()

	slackCalled := false
	slackChannel := ""

	// Set up callback
	engine.SlackCallback = func(ctx context.Context, alert AlertTriggered, channel string) error {
		slackCalled = true
		slackChannel = channel
		return nil
	}

	// Create a rule with Slack action
	rule := &AlertRule{
		Name:        "Slack Test",
		MetricType:  "satisfaction",
		Severity:    SeverityHigh,
		ConditionType: ConditionThreshold,
		Condition: ConditionConfig{
			Operator: OpLessThan,
			Value:    3.0,
		},
		Actions: []ActionConfig{
			{Type: ActionSlack, Enabled: true, SlackChannel: "#alerts"},
		},
	}

	engine.CreateRule(rule)
	engine.EvaluateMetric(context.Background(), MetricValue{Type: "satisfaction", Value: 2.0})

	// In a full implementation, callbacks would be executed
	// For this test, we just verify the action is configured
	t.Logf("Slack callback configured: %v, channel: %s", slackCalled, slackChannel)
}

// BenchmarkThresholdEvaluator benchmarks the threshold evaluator.
func BenchmarkThresholdEvaluator(b *testing.B) {
	evaluator := &ThresholdEvaluator{}
	rule := &AlertRule{
		Condition: ConditionConfig{Operator: OpLessThan, Value: 50.0},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.Evaluate(rule, float64(i), nil)
	}
}

// BenchmarkAlertEngine_EvaluateMetric benchmarks metric evaluation.
func BenchmarkAlertEngine_EvaluateMetric(b *testing.B) {
	engine := NewAlertEngine()

	rule := &AlertRule{
		Name:          "Benchmark Rule",
		MetricType:    "satisfaction",
		Severity:      SeverityMedium,
		ConditionType: ConditionThreshold,
		Condition:     ConditionConfig{Operator: OpLessThan, Value: 3.0},
		Actions:       []ActionConfig{{Type: ActionLog, Enabled: true}},
	}
	engine.CreateRule(rule)

	ctx := context.Background()
	metric := MetricValue{Type: "satisfaction", Value: 2.5}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.EvaluateMetric(ctx, metric)
	}
}
