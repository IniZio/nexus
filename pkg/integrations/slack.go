// Package integrations provides integration clients for external services
// like Slack for notifications and alerts.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// SlackColor represents the color indicator for Slack message attachments.
type SlackColor string

const (
	SlackColorGood    SlackColor = "#36a64f"
	SlackColorWarning SlackColor = "#ffcc00"
	SlackColorDanger  SlackColor = "#dc3545"
	SlackColorInfo    SlackColor = "#0e7aed"
)

// Config represents the Slack integration configuration.
type SlackConfig struct {
	WebhookURL      string            `yaml:"webhook_url"`
	Channel         string            `yaml:"channel"`
	Enabled         bool              `yaml:"enabled"`
	RateLimit       time.Duration     `yaml:"rate_limit"`
	MaxRetries      int               `yaml:"max_retries"`
	Timeout         time.Duration     `yaml:"timeout"`
	DefaultChannels map[string]string `yaml:"default_channels"`
}

// MetricsSummary represents a summary of metrics for weekly digest.
type MetricsSummary struct {
	Period             string       `json:"period"`
	TotalInvocations  int64        `json:"total_invocations"`
	SkillInvocations  int64        `json:"skill_invocations"`
	CommandInvocations int64       `json:"command_invocations"`
	AverageDuration   float64      `json:"average_duration"`
	SuccessRate       float64      `json:"success_rate"`
	TopSkills         []SkillMetric `json:"top_skills"`
	CompletionRate    float64      `json:"completion_rate"`
	TasksCompleted    int          `json:"tasks_completed"`
	TotalTasks        int          `json:"total_tasks"`
}

// SkillMetric represents metrics for a specific skill.
type SkillMetric struct {
	Name       string  `json:"name"`
	Count      int     `json:"count"`
	AvgDuration float64 `json:"avg_duration"`
	SuccessRate float64 `json:"success_rate"`
}

// TaskInfo represents task information for notifications.
type TaskInfo struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Status      string        `json:"status"` // created, started, completed, failed
	Project     string        `json:"project"`
	Assignee    string        `json:"assignee,omitempty"`
	Duration    time.Duration `json:"duration,omitempty"`
	CompletedAt time.Time     `json:"completed_at,omitempty"`
	CreatedAt   time.Time     `json:"created_at,omitempty"`
}

// AnomalyInfo represents anomaly detection information.
type AnomalyInfo struct {
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Severity    string    `json:"severity"` // low, medium, high, critical
	Value       float64   `json:"value"`
	Threshold   float64   `json:"threshold"`
	Timestamp   time.Time `json:"timestamp"`
	Project     string    `json:"project,omitempty"`
}

// SlackBlock represents a Slack Block Kit block.
type SlackBlock struct {
	Type     string          `json:"type"`
	Text     *SlackText      `json:"text,omitempty"`
	Elements []interface{}   `json:"elements,omitempty"`
	Accessory interface{}    `json:"accessory,omitempty"`
}

// SlackText represents text content in Slack blocks.
type SlackText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// SlackAttachment represents a Slack message attachment.
type SlackAttachment struct {
	Color      string         `json:"color,omitempty"`
	Title      string         `json:"title,omitempty"`
	TitleLink  string         `json:"title_link,omitempty"`
	Text       string         `json:"text,omitempty"`
	Fields     []SlackField   `json:"fields,omitempty"`
	Footer     string         `json:"footer,omitempty"`
	FooterIcon string         `json:"footer_icon,omitempty"`
	Ts         int64          `json:"ts,omitempty"`
	Actions    []SlackAction  `json:"actions,omitempty"`
}

// SlackField represents a field in a Slack attachment.
type SlackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// SlackAction represents an action button in Slack.
type SlackAction struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	URL   string `json:"url,omitempty"`
	Style string `json:"style,omitempty"` // primary, danger
}

// SlackMessage represents a complete Slack webhook message.
type SlackMessage struct {
	Channel     string            `json:"channel,omitempty"`
	Text        string            `json:"text,omitempty"`
	Blocks      []SlackBlock      `json:"blocks,omitempty"`
	Attachments []SlackAttachment `json:"attachments,omitempty"`
}

// SlackClient provides methods to send notifications to Slack.
type SlackClient struct {
	webhookURL   string
	channel      string
	enabled      bool
	httpClient   *http.Client
	rateLimiter  *rateLimiter
	mu           sync.RWMutex
	maxRetries   int
}

// rateLimiter implements simple token bucket rate limiting.
type rateLimiter struct {
	tokens     float64
	capacity   float64
	refillRate float64
	lastRefill time.Time
	mu         sync.Mutex
}

// newRateLimiter creates a rate limiter with the specified capacity and refill rate.
func newRateLimiter(capacity float64, refillRate float64) *rateLimiter {
	return &rateLimiter{
		tokens:     capacity,
		capacity:   capacity,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// allow checks if a request is allowed under the rate limit.
func (rl *rateLimiter) allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	rl.tokens = min(rl.capacity, rl.tokens+elapsed*rl.refillRate)
	rl.lastRefill = now

	if rl.tokens >= 1 {
		rl.tokens--
		return true
	}
	return false
}

// wait blocks until a request is allowed or the context is cancelled.
func (rl *rateLimiter) wait(ctx context.Context) error {
	maxWait := 5 * time.Second
	start := time.Now()

	for {
		if rl.allow() {
			return nil
		}

		if time.Since(start) > maxWait {
			return fmt.Errorf("rate limit timeout")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// NewSlackClient creates a new Slack client with the given configuration.
func NewSlackClient(cfg SlackConfig) *SlackClient {
	client := &SlackClient{
		webhookURL: cfg.WebhookURL,
		channel:    cfg.Channel,
		enabled:    cfg.Enabled,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		rateLimiter: newRateLimiter(
			10, // capacity
			2,  // refill rate per second
		),
		maxRetries: cfg.MaxRetries,
	}

	if cfg.RateLimit > 0 {
		client.rateLimiter = newRateLimiter(
			10,
			1/cfg.RateLimit.Seconds(),
		)
	}

	return client
}

// NewSlackClientFromEnv creates a Slack client from environment variables.
// Uses NEXUS_SLACK_WEBHOOK_URL, NEXUS_SLACK_CHANNEL, NEXUS_SLACK_ENABLED.
func NewSlackClientFromEnv() *SlackClient {
	webhookURL := getEnv("NEXUS_SLACK_WEBHOOK_URL", "")
	channel := getEnv("NEXUS_SLACK_CHANNEL", "#nexus-alerts")
	enabled := getEnv("NEXUS_SLACK_ENABLED", "false") == "true"

	return NewSlackClient(SlackConfig{
		WebhookURL: webhookURL,
		Channel:    channel,
		Enabled:    enabled,
		RateLimit:  5 * time.Second,
		MaxRetries: 3,
		Timeout:    30 * time.Second,
	})
}

// IsEnabled returns whether the Slack client is enabled.
func (c *SlackClient) IsEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled && c.webhookURL != ""
}

// send sends a Slack message with retries and rate limiting.
func (c *SlackClient) send(ctx context.Context, msg SlackMessage) error {
	if !c.IsEnabled() {
		log.Printf("[slack] Slack not configured, skipping notification")
		return nil
	}

	// Apply rate limiting
	if err := c.rateLimiter.wait(ctx); err != nil {
		return fmt.Errorf("rate limit exceeded: %w", err)
	}

	// Set channel if not already set
	if msg.Channel == "" {
		msg.Channel = c.channel
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	var lastErr error
	for i := 0; i <= c.maxRetries; i++ {
		req, err := http.NewRequestWithContext(ctx, "POST", c.webhookURL, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		body, _ := io.ReadAll(resp.Body)
		lastErr = fmt.Errorf("slack API error: %s (%d)", string(body), resp.StatusCode)

		// Don't retry on client errors (4xx)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			break
		}
	}

	return lastErr
}

// Send sends a Slack message to the configured webhook.
// This is a public wrapper around the internal send method.
func (c *SlackClient) Send(ctx context.Context, msg SlackMessage) error {
	return c.send(ctx, msg)
}

// SendSatisfactionAlert sends a notification when satisfaction drops below threshold.
func (c *SlackClient) SendSatisfactionAlert(ctx context.Context, satisfaction, threshold float64) error {
	color := SlackColorGood
	emoji := ":white_check_mark:"

	if satisfaction < threshold {
		color = SlackColorDanger
		emoji = ":rotating_light:"
	} else if satisfaction < threshold*1.2 {
		color = SlackColorWarning
		emoji = ":warning:"
	}

	blocks := []SlackBlock{
		{
			Type: "header",
			Text: &SlackText{
				Type:  "plain_text",
				Text:  fmt.Sprintf("%s Satisfaction Alert", emoji),
				Emoji: true,
			},
		},
		{
			Type: "section",
			Text: &SlackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf(
					"Satisfaction level has dropped to *%.1f%%* (threshold: *%.1f%%*)",
					satisfaction*100, threshold*100,
				),
			},
		},
	}

	fields := []SlackField{
		{Title: "Current", Value: fmt.Sprintf("%.1f%%", satisfaction*100), Short: true},
		{Title: "Threshold", Value: fmt.Sprintf("%.1f%%", threshold*100), Short: true},
		{Title: "Status", Value: getSatisfactionStatus(satisfaction, threshold), Short: true},
	}

	attachments := []SlackAttachment{
		{
			Color:  string(color),
			Fields: fields,
			Footer: "Nexus Metrics",
			Ts:     time.Now().Unix(),
		},
	}

	msg := SlackMessage{
		Blocks:      blocks,
		Attachments: attachments,
	}

	return c.send(ctx, msg)
}

// SendWeeklyDigest sends a weekly metrics digest.
func (c *SlackClient) SendWeeklyDigest(ctx context.Context, metrics MetricsSummary) error {
	blocks := []SlackBlock{
		{
			Type: "header",
			Text: &SlackText{
				Type:  "plain_text",
				Text:  ":bar_chart: Weekly Nexus Metrics Digest",
				Emoji: true,
			},
		},
		{
			Type: "section",
			Text: &SlackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Period:* %s", metrics.Period),
			},
		},
	}

	fields := []SlackField{
		{Title: "Total Invocations", Value: fmt.Sprintf("%d", metrics.TotalInvocations), Short: true},
		{Title: "Success Rate", Value: fmt.Sprintf("%.1f%%", metrics.SuccessRate), Short: true},
		{Title: "Avg Duration", Value: fmt.Sprintf("%.2fs", metrics.AverageDuration), Short: true},
		{Title: "Tasks Completed", Value: fmt.Sprintf("%d/%d", metrics.TasksCompleted, metrics.TotalTasks), Short: true},
	}

	// Add top skills section
	if len(metrics.TopSkills) > 0 {
		var skillsText string
		for i, skill := range metrics.TopSkills {
			if i >= 5 {
				break
			}
			skillsText += fmt.Sprintf("%d. *%s* - %d invocations (%.2fs avg)\n", i+1, skill.Name, skill.Count, skill.AvgDuration)
		}

		blocks = append(blocks, SlackBlock{
			Type: "section",
			Text: &SlackText{
				Type: "mrkdwn",
				Text: "*Top Skills*\n" + skillsText,
			},
		})
	}

	attachments := []SlackAttachment{
		{
			Color:  string(SlackColorInfo),
			Fields: fields,
			Footer: "Nexus Metrics",
			Ts:     time.Now().Unix(),
		},
	}

	msg := SlackMessage{
		Blocks:      blocks,
		Attachments: attachments,
	}

	return c.send(ctx, msg)
}

// SendTaskNotification sends a notification for task creation or completion.
func (c *SlackClient) SendTaskNotification(ctx context.Context, task TaskInfo) error {
	var emoji, title, statusColor string

	switch task.Status {
	case "created":
		emoji = ":clipboard:"
		title = "New Task Created"
		statusColor = string(SlackColorInfo)
	case "started":
		emoji = ":play_button:"
		title = "Task Started"
		statusColor = string(SlackColorInfo)
	case "completed":
		emoji = ":white_check_mark:"
		title = "Task Completed"
		statusColor = string(SlackColorGood)
	case "failed":
		emoji = ":x:"
		title = "Task Failed"
		statusColor = string(SlackColorDanger)
	default:
		emoji = ":information_source:"
		title = "Task Update"
		statusColor = string(SlackColorInfo)
	}

	blocks := []SlackBlock{
		{
			Type: "header",
			Text: &SlackText{
				Type:  "plain_text",
				Text:  fmt.Sprintf("%s %s", emoji, title),
				Emoji: true,
			},
		},
		{
			Type: "section",
			Text: &SlackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Task:* %s\n*Project:* %s", task.Name, task.Project),
			},
		},
	}

	fields := []SlackField{
		{Title: "ID", Value: task.ID, Short: true},
		{Title: "Status", Value: strings.Title(task.Status), Short: true},
	}

	if task.Assignee != "" {
		fields = append(fields, SlackField{Title: "Assignee", Value: task.Assignee, Short: true})
	}

	if task.Duration > 0 {
		fields = append(fields, SlackField{Title: "Duration", Value: formatDuration(task.Duration), Short: true})
	}

	attachments := []SlackAttachment{
		{
			Color:  statusColor,
			Fields: fields,
			Footer: "Nexus Tasks",
			Ts:     time.Now().Unix(),
		},
	}

	if task.Status == "created" || task.Status == "completed" {
		attachments[0].Actions = []SlackAction{
			{
				Type:  "button",
				Text:  "View Task",
				URL:   fmt.Sprintf("https://nexus.example.com/tasks/%s", task.ID),
				Style: "primary",
			},
		}
	}

	msg := SlackMessage{
		Blocks:      blocks,
		Attachments: attachments,
	}

	return c.send(ctx, msg)
}

// SendAnomalyAlert sends an alert when an anomaly is detected.
func (c *SlackClient) SendAnomalyAlert(ctx context.Context, anomaly AnomalyInfo) error {
	var color SlackColor
	var emoji string

	switch anomaly.Severity {
	case "critical":
		color = SlackColorDanger
		emoji = ":rotating_light:"
	case "high":
		color = SlackColorDanger
		emoji = ":warning:"
	case "medium":
		color = SlackColorWarning
		emoji = ":large_orange_diamond:"
	case "low":
		color = SlackColorInfo
		emoji = ":information_source:"
	default:
		color = SlackColorInfo
		emoji = ":mag:"
	}

	blocks := []SlackBlock{
		{
			Type: "header",
			Text: &SlackText{
				Type:  "plain_text",
				Text:  fmt.Sprintf("%s Anomaly Detected", emoji),
				Emoji: true,
			},
		},
		{
			Type: "section",
			Text: &SlackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Type:* %s\n*Description:* %s", anomaly.Type, anomaly.Description),
			},
		},
	}

	fields := []SlackField{
		{Title: "Severity", Value: strings.ToUpper(anomaly.Severity), Short: true},
		{Title: "Current Value", Value: fmt.Sprintf("%.2f", anomaly.Value), Short: true},
		{Title: "Threshold", Value: fmt.Sprintf("%.2f", anomaly.Threshold), Short: true},
		{Title: "Time", Value: anomaly.Timestamp.Format("2006-01-02 15:04:05"), Short: true},
	}

	if anomaly.Project != "" {
		fields = append(fields, SlackField{Title: "Project", Value: anomaly.Project, Short: true})
	}

	attachments := []SlackAttachment{
		{
			Color:  string(color),
			Fields: fields,
			Footer: "Nexus Anomaly Detection",
			Ts:     time.Now().Unix(),
			Actions: []SlackAction{
				{
					Type:  "button",
					Text:  "View Details",
					URL:   fmt.Sprintf("https://nexus.example.com/anomalies?type=%s", anomaly.Type),
					Style: "danger",
				},
			},
		},
	}

	msg := SlackMessage{
		Blocks:      blocks,
		Attachments: attachments,
	}

	return c.send(ctx, msg)
}

// SetEnabled enables or disables the Slack client.
func (c *SlackClient) SetEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = enabled
}

// SetWebhookURL updates the webhook URL.
func (c *SlackClient) SetWebhookURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.webhookURL = url
}

// SetChannel updates the default channel.
func (c *SlackClient) SetChannel(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channel = channel
}

// Helper functions

func getEnv(key, defaultValue string) string {
	if val, ok := lookupEnv(key); ok && val != "" {
		return val
	}
	return defaultValue
}

func lookupEnv(key string) (string, bool) {
	val := os.Getenv(key)
	return val, val != ""
}

func setLookupEnv(fn func(key string) (string, bool)) {
	// This would be used in tests to inject env lookup
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func getSatisfactionStatus(current, threshold float64) string {
	ratio := current / threshold
	switch {
	case ratio < 0.8:
		return ":x: Critical"
	case ratio < 1.0:
		return ":warning: Below Threshold"
	default:
		return ":white_check_mark: Healthy"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	h := d.Hours()
	m := d.Minutes() - h*60
	return fmt.Sprintf("%.0fh %.0fm", h, m)
}

var _ = setLookupEnv // Import unused function to avoid errors
