package slack

import (
	"os"

	"github.com/slack-go/slack"
)

// Config holds Slack configuration loaded from environment variables.
// Environment variables:
// - SLACK_BOT_TOKEN: Bot user OAuth token (xoxb-...)
// - SLACK_SIGNING_SECRET: Signing secret for verifying request signatures
type Config struct {
	BotToken       string
	SigningSecret  string
}

// NewConfig loads Slack configuration from environment variables.
func NewConfig() (*Config, error) {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	if botToken == "" {
		return nil, ErrSlackNotConfigured
	}

	signingSecret := os.Getenv("SLACK_SIGNING_SECRET")
	if signingSecret == "" {
		return nil, ErrSlackNotConfigured
	}

	return &Config{
		BotToken:      botToken,
		SigningSecret: signingSecret,
	}, nil
}

// Client wraps the Slack API client with Nexus-specific functionality.
type Client struct {
	api *slack.Client
}

// NewClient creates a new Slack client with the provided configuration.
func NewClient(cfg *Config) *Client {
	return &Client{
		api: slack.New(cfg.BotToken),
	}
}

// API returns the underlying Slack client for advanced operations.
func (c *Client) API() *slack.Client {
	return c.api
}
