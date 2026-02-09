package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var (
	feedbackType     string
	feedbackSeverity string
	feedbackWorkspace string
)

var feedbackCmd = &cobra.Command{
	Use:   "feedback \"Your message\"",
	Short: "Submit feedback to the Nexus server",
	Long: `Submit feedback, bug reports, or feature requests to the Nexus coordination server.

Feedback types:
  - bug: Report a problem or issue
  - feature: Suggest a new feature
  - feedback: General feedback or suggestions

Severity levels:
  - low: Minor inconvenience
  - medium:影响日常使用
  - high:严重阻碍工作
  - critical:系统不可用

Examples:
  nexus feedback "Great tool!"
  nexus feedback "Found a bug" --type bug --severity medium
  nexus feedback "Need dark mode" --type feature --workspace my-project`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runFeedback(args[0])
	},
}

func init() {
	rootCmd.AddCommand(feedbackCmd)
	feedbackCmd.Flags().StringVarP(&feedbackType, "type", "t", "feedback",
		"Feedback type: bug, feature, feedback")
	feedbackCmd.Flags().StringVarP(&feedbackSeverity, "severity", "s", "medium",
		"Severity: low, medium, high, critical")
	feedbackCmd.Flags().StringVar(&feedbackWorkspace, "workspace", "",
		"Workspace context for this feedback")
}

func runFeedback(message string) error {
	// Validate feedback type
	validTypes := map[string]bool{
		"bug":      true,
		"feature":  true,
		"feedback": true,
	}
	if !validTypes[feedbackType] {
		return fmt.Errorf("invalid feedback type: %s. Valid types: bug, feature, feedback", feedbackType)
	}

	// Validate severity
	validSeverities := map[string]bool{
		"low":      true,
		"medium":   true,
		"high":     true,
		"critical": true,
	}
	if !validSeverities[feedbackSeverity] {
		return fmt.Errorf("invalid severity: %s. Valid severities: low, medium, high, critical", feedbackSeverity)
	}

	// Get server URL from environment or use default
	serverURL := os.Getenv("NEXUS_SERVER_URL")
	if serverURL == "" {
		serverURL = "http://localhost:3001"
	}

	// Prepare feedback payload
	payload := map[string]interface{}{
		"message":   message,
		"type":     feedbackType,
		"severity": feedbackSeverity,
		"workspace": feedbackWorkspace,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	// Get user info if available
	username := os.Getenv("USER")
	if username == "" {
		username = "anonymous"
	}
	payload["user"] = username

	// Marshal payload to JSON
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal feedback: %w", err)
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Submit feedback to the coordination server
	url := serverURL + "/api/feedback"
	resp, err := client.Post(url, "application/json", bytes.NewReader(jsonPayload))
	if err != nil {
		return fmt.Errorf("failed to submit feedback: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("feedback submission failed with status: %d", resp.StatusCode)
	}

	// Parse response
	var response struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		// Non-critical: still show success even if response parsing fails
		fmt.Println("Feedback submitted successfully!")
		return nil
	}

	fmt.Println("Thank you for your feedback!")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Type:     %s\n", feedbackType)
	fmt.Printf("Severity: %s\n", feedbackSeverity)
	if feedbackWorkspace != "" {
		fmt.Printf("Workspace: %s\n", feedbackWorkspace)
	}
	fmt.Println("")
	fmt.Printf("ID: %s\n", response.ID)
	if response.Message != "" {
		fmt.Println(response.Message)
	}
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}
