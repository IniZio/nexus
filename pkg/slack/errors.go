package slack

import "errors"

// Sentinel errors for Slack operations.
var (
	// ErrSlackNotConfigured is returned when required Slack environment variables are not set.
	ErrSlackNotConfigured = errors.New("slack is not configured: missing SLACK_BOT_TOKEN or SLACK_SIGNING_SECRET")

	// ErrInvalidCommand is returned when a Slack command is malformed or unsupported.
	ErrInvalidCommand = errors.New("invalid slack command")

	// ErrNotificationFailed is returned when sending a Slack notification fails.
	ErrNotificationFailed = errors.New("failed to send slack notification")
)

// IsErrSlackNotConfigured checks if the error is due to missing Slack configuration.
func IsErrSlackNotConfigured(err error) bool {
	return errors.Is(err, ErrSlackNotConfigured)
}
