package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/integrations"
	"github.com/spf13/cobra"
)

var slackCmd = &cobra.Command{
	Use:   "slack",
	Short: "Manage Slack integration",
	Long:  `Send notifications and manage Slack integration for Nexus.`,
}

var slackTestConnectionCmd = &cobra.Command{
	Use:   "test-connection",
	Short: "Test Slack connection",
	Long:  `Test the Slack connection by sending a test message.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runSlackTestConnection()
	},
}

var slackNotifyCmd = &cobra.Command{
	Use:   "notify <message>",
	Short: "Send a notification to Slack",
	Long:  `Send a notification message to the configured Slack channel.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runSlackNotify(args[0])
	},
}

var slackSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactive Slack setup",
	Long: `Set up Slack integration interactively.
This will configure the webhook URL and default channel.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runSlackSetup()
	},
}

var (
	slackWebhookURL string
	slackChannel    string
	slackEnabled     bool
)

func init() {
	rootCmd.AddCommand(slackCmd)
	slackCmd.AddCommand(slackTestConnectionCmd)
	slackCmd.AddCommand(slackNotifyCmd)
	slackCmd.AddCommand(slackSetupCmd)

	// Flags for slack commands
	slackNotifyCmd.Flags().StringVarP(&slackWebhookURL, "webhook", "w", "", "Slack webhook URL")
	slackNotifyCmd.Flags().StringVarP(&slackChannel, "channel", "c", "", "Slack channel")
	slackTestConnectionCmd.Flags().StringVarP(&slackWebhookURL, "webhook", "w", "", "Slack webhook URL")
	slackTestConnectionCmd.Flags().StringVarP(&slackChannel, "channel", "c", "", "Slack channel")
}

func runSlackTestConnection() error {
	fmt.Println("🔌 Testing Slack Connection")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Get webhook URL from flag, env, or prompt
	webhookURL := slackWebhookURL
	if webhookURL == "" {
		webhookURL = os.Getenv("NEXUS_SLACK_WEBHOOK_URL")
	}
	if webhookURL == "" {
		// Try loading from config
		webhookURL = getSlackWebhookFromConfig()
	}

	if webhookURL == "" {
		fmt.Println("⚠️  Slack webhook URL not configured")
		fmt.Println("")
		fmt.Println("📝 To configure Slack:")
		fmt.Println("   1. Set NEXUS_SLACK_WEBHOOK_URL environment variable, or")
		fmt.Println("   2. Run: nexus slack setup")
		fmt.Println("   3. Or pass --webhook flag")
		return nil
	}

	// Create Slack client
	client := integrations.NewSlackClient(integrations.SlackConfig{
		WebhookURL: webhookURL,
		Channel:    slackChannel,
		Enabled:    true,
	})

	if !client.IsEnabled() {
		fmt.Println("⚠️  Slack is not enabled")
		return nil
	}

	ctx := context.Background()

	// Send test message
	testMessage := integrations.SlackMessage{
		Text: ":test_tube: Nexus Slack connection test successful!",
		Attachments: []integrations.SlackAttachment{
			{
				Color:  string(integrations.SlackColorGood),
				Title:  "Connection Test",
				Text:   "Your Slack integration is configured correctly.",
				Footer: "Nexus",
			},
		},
	}

	if err := client.Send(ctx, testMessage); err != nil {
		return fmt.Errorf("failed to send test message: %w", err)
	}

	fmt.Println("✅ Slack connection test successful!")
	fmt.Printf("📢 Message sent to channel\n")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

func runSlackNotify(message string) error {
	fmt.Println("📢 Sending Slack Notification")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Get webhook URL from flag, env, or config
	webhookURL := slackWebhookURL
	if webhookURL == "" {
		webhookURL = os.Getenv("NEXUS_SLACK_WEBHOOK_URL")
	}
	if webhookURL == "" {
		webhookURL = getSlackWebhookFromConfig()
	}

	if webhookURL == "" {
		fmt.Println("⚠️  Slack webhook URL not configured")
		fmt.Println("")
		fmt.Println("📝 To configure Slack:")
		fmt.Println("   1. Set NEXUS_SLACK_WEBHOOK_URL environment variable, or")
		fmt.Println("   2. Run: nexus slack setup")
		fmt.Println("   3. Or pass --webhook flag")
		return nil
	}

	channel := slackChannel
	if channel == "" {
		channel = os.Getenv("NEXUS_SLACK_CHANNEL")
		if channel == "" {
			channel = "#nexus-alerts"
		}
	}

	// Create Slack client
	client := integrations.NewSlackClient(integrations.SlackConfig{
		WebhookURL: webhookURL,
		Channel:    channel,
		Enabled:    true,
	})

	ctx := context.Background()

	// Send the notification
	msg := integrations.SlackMessage{
		Channel: channel,
		Text:    message,
	}

	if err := client.Send(ctx, msg); err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}

	fmt.Println("✅ Notification sent successfully!")
	fmt.Printf("📢 Channel: %s\n", channel)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

func runSlackSetup() error {
	fmt.Println("🔧 Slack Integration Setup")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("")
	fmt.Println("This wizard will help you configure Slack integration for Nexus.")
	fmt.Println("")

	// Get webhook URL
	webhookURL := os.Getenv("NEXUS_SLACK_WEBHOOK_URL")
	if webhookURL == "" {
		webhookURL = getSlackWebhookFromConfig()
	}

	if webhookURL != "" {
		fmt.Printf("ℹ️  Existing webhook URL found: %s\n", maskURL(webhookURL))
		fmt.Println("   Press Enter to keep or type a new one:")
		fmt.Print("   > ")
		// Note: In non-interactive mode, we skip prompting
	} else {
		fmt.Println("📝 Step 1: Configure Webhook URL")
		fmt.Println("   To send messages to Slack, you need a Slack Incoming Webhook:")
		fmt.Println("   1. Go to https://api.slack.com/apps and create an app")
		fmt.Println("   2. Enable Incoming Webhooks and create a webhook URL")
		fmt.Println("   3. Copy the webhook URL below")
		fmt.Println("")
		fmt.Print("   Enter your Slack Webhook URL: ")
	}

	// Get channel
	channel := os.Getenv("NEXUS_SLACK_CHANNEL")
	if channel == "" {
		channel = "#nexus-alerts"
	}

	fmt.Println("")
	fmt.Printf("📝 Default Channel (default: %s): ", channel)
	fmt.Print("> ")

	// Save configuration
	configPath := config.GetUserConfigPath()
	configDir := filepath.Dir(configPath)

	if err := config.EnsureConfigDirectory(configDir); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	var userCfg *config.UserConfig
	if _, err := os.Stat(configPath); err == nil {
		userCfg, _ = config.LoadUserConfig(configPath)
		if userCfg == nil {
			userCfg = &config.UserConfig{}
		}
	} else {
		userCfg = &config.UserConfig{}
	}

	userCfg.Slack = &config.SlackConfig{
		WebhookURL: webhookURL,
		Channel:    channel,
		Enabled:    webhookURL != "",
	}

	if err := config.SaveUserConfig(configPath, userCfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println("")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✅ Slack setup complete!")
	fmt.Println("")
	fmt.Println("📝 Next steps:")
	fmt.Println("   - Set NEXUS_SLACK_WEBHOOK_URL environment variable, or")
	fmt.Println("   - Run: nexus slack test-connection")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

func getSlackWebhookFromConfig() string {
	configPath := config.GetUserConfigPath()
	if _, err := os.Stat(configPath); err != nil {
		return ""
	}

	userCfg, err := config.LoadUserConfig(configPath)
	if err != nil || userCfg == nil || userCfg.Slack == nil {
		return ""
	}

	return userCfg.Slack.WebhookURL
}

func maskURL(url string) string {
	if len(url) <= 16 {
		return "***"
	}
	return url[:8] + "..." + url[len(url)-8:]
}
