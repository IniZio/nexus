package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSlackClient(t *testing.T) {
	cfg := SlackConfig{
		WebhookURL: "https://hooks.slack.com/services/test",
		Channel:    "#test",
		Enabled:    true,
		RateLimit:  5 * time.Second,
		MaxRetries: 3,
		Timeout:    30 * time.Second,
	}

	client := NewSlackClient(cfg)

	assert.Equal(t, "https://hooks.slack.com/services/test", client.webhookURL)
	assert.Equal(t, "#test", client.channel)
	assert.True(t, client.enabled)
	assert.Equal(t, 3, client.maxRetries)
}

func TestNewSlackClientFromEnv(t *testing.T) {
	// Store original env and restore after test
	originalWebURL := os.Getenv("NEXUS_SLACK_WEBHOOK_URL")
	originalChannel := os.Getenv("NEXUS_SLACK_CHANNEL")
	originalEnabled := os.Getenv("NEXUS_SLACK_ENABLED")
	defer func() {
		os.Setenv("NEXUS_SLACK_WEBHOOK_URL", originalWebURL)
		os.Setenv("NEXUS_SLACK_CHANNEL", originalChannel)
		os.Setenv("NEXUS_SLACK_ENABLED", originalEnabled)
	}()

	t.Setenv("NEXUS_SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/env-test")
	t.Setenv("NEXUS_SLACK_CHANNEL", "#env-channel")
	t.Setenv("NEXUS_SLACK_ENABLED", "true")

	client := NewSlackClientFromEnv()

	assert.Equal(t, "https://hooks.slack.com/services/env-test", client.webhookURL)
	assert.Equal(t, "#env-channel", client.channel)
	assert.True(t, client.enabled)
}

func TestNewSlackClientFromEnvDisabled(t *testing.T) {
	t.Setenv("NEXUS_SLACK_WEBHOOK_URL", "")
	t.Setenv("NEXUS_SLACK_ENABLED", "false")

	client := NewSlackClientFromEnv()

	assert.False(t, client.enabled)
	assert.Equal(t, "", client.webhookURL)
}

func TestSlackClient_IsEnabled(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		webhookURL string
		expected  bool
	}{
		{
			name:      "enabled with webhook",
			enabled:   true,
			webhookURL: "https://hooks.slack.com/test",
			expected:  true,
		},
		{
			name:      "disabled with webhook",
			enabled:   false,
			webhookURL: "https://hooks.slack.com/test",
			expected:  false,
		},
		{
			name:      "enabled without webhook",
			enabled:   true,
			webhookURL: "",
			expected:  false,
		},
		{
			name:      "disabled without webhook",
			enabled:   false,
			webhookURL: "",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &SlackClient{
				webhookURL: tt.webhookURL,
				enabled:    tt.enabled,
				httpClient: &http.Client{},
			}
			assert.Equal(t, tt.expected, client.IsEnabled())
		})
	}
}

func TestSendSatisfactionAlert(t *testing.T) {
	var receivedMsg SlackMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.Header.Get("Content-Type"), "application/json")

		err := json.NewDecoder(r.Body).Decode(&receivedMsg)
		require.NoError(t, err)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Channel:    "#alerts",
		Enabled:    true,
	})

	ctx := context.Background()

	// Test normal satisfaction (85% > 80% but < 96% threshold*1.2, so warning)
	err := client.SendSatisfactionAlert(ctx, 0.85, 0.80)
	require.NoError(t, err)
	assert.Equal(t, "#alerts", receivedMsg.Channel)
	assert.Len(t, receivedMsg.Blocks, 2)
	assert.Contains(t, receivedMsg.Blocks[1].Text.Text, "85.0%")
	assert.Contains(t, receivedMsg.Blocks[1].Text.Text, "80.0%")
	assert.Equal(t, string(SlackColorWarning), receivedMsg.Attachments[0].Color)

	// Test below threshold
	receivedMsg = SlackMessage{}
	err = client.SendSatisfactionAlert(ctx, 0.65, 0.80)
	require.NoError(t, err)
	assert.Equal(t, string(SlackColorDanger), receivedMsg.Attachments[0].Color)
	assert.Contains(t, receivedMsg.Blocks[1].Text.Text, "65.0%")
}

func TestSendSatisfactionAlert_DisabledClient(t *testing.T) {
	client := NewSlackClient(SlackConfig{
		WebhookURL: "",
		Enabled:    false,
	})

	ctx := context.Background()
	err := client.SendSatisfactionAlert(ctx, 0.65, 0.80)
	assert.NoError(t, err) // Should log and not error
}

func TestSendWeeklyDigest(t *testing.T) {
	var receivedMsg SlackMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&receivedMsg)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Enabled:    true,
	})

	ctx := context.Background()
	metrics := MetricsSummary{
		Period:             "2024-01-01 to 2024-01-07",
		TotalInvocations:   1500,
		SkillInvocations:   800,
		CommandInvocations: 700,
		AverageDuration:    2.5,
		SuccessRate:        92.5,
		TasksCompleted:     45,
		TotalTasks:         50,
		TopSkills: []SkillMetric{
			{Name: "code-review", Count: 200, AvgDuration: 3.5, SuccessRate: 95},
			{Name: "debug", Count: 150, AvgDuration: 5.2, SuccessRate: 88},
		},
	}

	err := client.SendWeeklyDigest(ctx, metrics)
	require.NoError(t, err)

	assert.Contains(t, receivedMsg.Blocks[0].Text.Text, "Weekly Nexus Metrics Digest")
	assert.Contains(t, receivedMsg.Blocks[1].Text.Text, "2024-01-01 to 2024-01-07")
	assert.Len(t, receivedMsg.Attachments, 1)
	assert.Equal(t, string(SlackColorInfo), receivedMsg.Attachments[0].Color)
}

func TestSendTaskNotification(t *testing.T) {
	var receivedMsg SlackMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&receivedMsg)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Enabled:    true,
	})

	ctx := context.Background()

	t.Run("task created", func(t *testing.T) {
		receivedMsg = SlackMessage{}
		task := TaskInfo{
			ID:        "TASK-001",
			Name:      "Implement login feature",
			Status:    "created",
			Project:   "myapp",
			Assignee:  "developer",
			CreatedAt: time.Now(),
		}

		err := client.SendTaskNotification(ctx, task)
		require.NoError(t, err)

		assert.Contains(t, receivedMsg.Blocks[0].Text.Text, "New Task Created")
		assert.Contains(t, receivedMsg.Blocks[1].Text.Text, "Implement login feature")
		assert.Contains(t, receivedMsg.Blocks[1].Text.Text, "myapp")
		assert.Equal(t, "TASK-001", receivedMsg.Attachments[0].Fields[0].Value)
		assert.Len(t, receivedMsg.Attachments[0].Actions, 1)
	})

	t.Run("task completed with duration", func(t *testing.T) {
		receivedMsg = SlackMessage{}
		task := TaskInfo{
			ID:          "TASK-002",
			Name:        "Fix bug in parser",
			Status:      "completed",
			Project:     "myapp",
			Duration:    2*time.Hour + 30*time.Minute,
			CompletedAt: time.Now(),
		}

		err := client.SendTaskNotification(ctx, task)
		require.NoError(t, err)

		assert.Contains(t, receivedMsg.Blocks[0].Text.Text, "Task Completed")
		assert.Equal(t, string(SlackColorGood), receivedMsg.Attachments[0].Color)
		assert.Contains(t, receivedMsg.Attachments[0].Fields[1].Value, "Completed")
	})

	t.Run("task failed", func(t *testing.T) {
		receivedMsg = SlackMessage{}
		task := TaskInfo{
			ID:      "TASK-003",
			Name:    "Deploy to production",
			Status:  "failed",
			Project: "myapp",
		}

		err := client.SendTaskNotification(ctx, task)
		require.NoError(t, err)

		assert.Contains(t, receivedMsg.Blocks[0].Text.Text, "Task Failed")
		assert.Equal(t, string(SlackColorDanger), receivedMsg.Attachments[0].Color)
	})
}

func TestSendAnomalyAlert(t *testing.T) {
	var receivedMsg SlackMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&receivedMsg)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Enabled:    true,
	})

	ctx := context.Background()

	t.Run("critical anomaly", func(t *testing.T) {
		receivedMsg = SlackMessage{}
		anomaly := AnomalyInfo{
			Type:        "high_failure_rate",
			Description: "Failure rate exceeded 20% threshold",
			Severity:    "critical",
			Value:       25.5,
			Threshold:   20.0,
			Timestamp:   time.Now(),
			Project:     "api-service",
		}

		err := client.SendAnomalyAlert(ctx, anomaly)
		require.NoError(t, err)

		assert.Equal(t, "CRITICAL", receivedMsg.Attachments[0].Fields[0].Value)
		assert.Equal(t, string(SlackColorDanger), receivedMsg.Attachments[0].Color)
		assert.Contains(t, receivedMsg.Blocks[1].Text.Text, "high_failure_rate")
		assert.Len(t, receivedMsg.Attachments[0].Actions, 1)
	})

	t.Run("low severity anomaly", func(t *testing.T) {
		receivedMsg = SlackMessage{}
		anomaly := AnomalyInfo{
			Type:        "latency_spike",
			Description: "Response time increased slightly",
			Severity:    "low",
			Value:       150,
			Threshold:   200,
			Timestamp:   time.Now(),
		}

		err := client.SendAnomalyAlert(ctx, anomaly)
		require.NoError(t, err)

		assert.Equal(t, "LOW", receivedMsg.Attachments[0].Fields[0].Value)
		assert.Equal(t, string(SlackColorInfo), receivedMsg.Attachments[0].Color)
	})
}

func TestSlackClient_ConfigurationUpdates(t *testing.T) {
	client := NewSlackClient(SlackConfig{
		WebhookURL: "https://hooks.slack.com/original",
		Channel:    "#original",
		Enabled:    true,
	})

	// Test SetWebhookURL
	client.SetWebhookURL("https://hooks.slack.com/updated")
	assert.Equal(t, "https://hooks.slack.com/updated", client.webhookURL)

	// Test SetChannel
	client.SetChannel("#updated")
	assert.Equal(t, "#updated", client.channel)

	// Test SetEnabled
	client.SetEnabled(false)
	assert.False(t, client.enabled)

	client.SetEnabled(true)
	assert.True(t, client.enabled)
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(2, 1) // capacity 2, refill 1 per second

	// First two should pass
	assert.True(t, rl.allow())
	assert.True(t, rl.allow())

	// Third should fail
	assert.False(t, rl.allow())
}

func TestRateLimiter_Refill(t *testing.T) {
	rl := newRateLimiter(2, 2) // capacity 2, refill 2 per second (1 token per 500ms)

	// Use all tokens
	assert.True(t, rl.allow())
	assert.True(t, rl.allow())
	assert.False(t, rl.allow())

	// Wait for refill
	time.Sleep(600 * time.Millisecond)

	// Should have tokens again
	assert.True(t, rl.allow())
}

func TestSlackMessage_JSON(t *testing.T) {
	msg := SlackMessage{
		Channel: "#test",
		Text:    "Test message",
		Blocks: []SlackBlock{
			{
				Type: "section",
				Text: &SlackText{
					Type:  "mrkdwn",
					Text: "Hello",
				},
			},
		},
		Attachments: []SlackAttachment{
			{
				Color: "#ff0000",
				Title: "Test",
				Fields: []SlackField{
					{Title: "Field1", Value: "Value1", Short: true},
				},
			},
		},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded SlackMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "#test", decoded.Channel)
	assert.Equal(t, "Test message", decoded.Text)
	assert.Len(t, decoded.Blocks, 1)
	assert.Len(t, decoded.Attachments, 1)
}

func TestSlackClient_SendServerError(t *testing.T) {
	errorCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errorCount++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Enabled:    true,
		MaxRetries: 2,
	})

	ctx := context.Background()
	err := client.SendSatisfactionAlert(ctx, 0.5, 0.8)

	assert.Error(t, err)
	assert.Equal(t, 3, errorCount) // 1 initial + 2 retries
}

func TestSlackClient_SendSuccessAfterRetry(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Enabled:    true,
		MaxRetries: 3,
	})

	ctx := context.Background()
	err := client.SendSatisfactionAlert(ctx, 0.9, 0.8)

	assert.NoError(t, err)
	assert.Equal(t, 2, attempt)
}

func TestSlackClient_NoRetryOnClientError(t *testing.T) {
	errorCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errorCount++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Enabled:    true,
		MaxRetries: 3,
	})

	ctx := context.Background()
	err := client.SendSatisfactionAlert(ctx, 0.9, 0.8)

	assert.Error(t, err)
	assert.Equal(t, 1, errorCount) // No retry on 4xx
}

func TestSendWeeklyDigest_EmptyTopSkills(t *testing.T) {
	var receivedMsg SlackMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedMsg)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Enabled:    true,
	})

	ctx := context.Background()
	metrics := MetricsSummary{
		Period:    "2024-01-01 to 2024-01-07",
		TopSkills: []SkillMetric{},
	}

	err := client.SendWeeklyDigest(ctx, metrics)
	require.NoError(t, err)

	// Should not have top skills section if empty
	assert.Len(t, receivedMsg.Blocks, 2) // Header + Period only
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "2m"},      // 90s = 1.5m -> rounds to 2m
		{65 * time.Minute, "1h 0m"},   // 65m = 1.083h -> 1h, 0m (1.083 - 1*60 = 0.083m)
		{2*time.Hour + 30*time.Minute, "2h 0m"}, // 2.5h -> 2h, 0m
	}

	for _, tt := range tests {
		result := formatDuration(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

func TestGetSatisfactionStatus(t *testing.T) {
	tests := []struct {
		current   float64
		threshold float64
		expected  string
	}{
		{0.50, 0.80, ":x: Critical"},
		{0.75, 0.80, ":warning: Below Threshold"},
		{0.85, 0.80, ":white_check_mark: Healthy"},
	}

	for _, tt := range tests {
		result := getSatisfactionStatus(tt.current, tt.threshold)
		assert.Equal(t, tt.expected, result)
	}
}

func BenchmarkSlackClient_Send(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(SlackConfig{
		WebhookURL: server.URL,
		Enabled:    true,
	})

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = client.SendSatisfactionAlert(ctx, 0.85, 0.80)
	}
}

// Test helper to verify message content
func verifySlackMessage(t *testing.T, msg SlackMessage, expectedFields map[string]string) {
	for _, att := range msg.Attachments {
		for _, field := range att.Fields {
			if expected, ok := expectedFields[field.Title]; ok {
				assert.Equal(t, expected, field.Value)
			}
		}
	}
}

// Helper to check string contains (replicates strings.Contains behavior)
func stringContains(s, substr string) bool {
	return strings.Contains(s, substr)
}
