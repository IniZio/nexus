package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// SlashCommandPayload represents a Slack slash command payload.
type SlashCommandPayload struct {
	Command     string `form:"command"`
	Text        string `form:"text"`
	UserID      string `form:"user_id"`
	ChannelID   string `form:"channel_id"`
	ResponseURL string `form:"response_url"`
	TriggerID   string `form:"trigger_id"`
	UserName    string `form:"user_name"`
	ChannelName string `form:"channel_name"`
	TeamID      string `form:"team_id"`
	TeamDomain  string `form:"team_domain"`
	EnterpriseID   string `form:"enterprise_id"`
	EnterpriseName string `form:"enterprise_name"`
}

// WebhookResponse is the response sent back to Slack for a slash command.
type WebhookResponse struct {
	ResponseType    string `json:"response_type,omitempty"`    // "in_channel" or "ephemeral"
	Text            string `json:"text"`                       // Main response text
	Blocks          any    `json:"blocks,omitempty"`           // Block Kit UI
	Attachments      any    `json:"attachments,omitempty"`      // Legacy attachments
	ThreadTimestamp string `json:"thread_ts,omitempty"`        // Reply in thread
	ReplaceOriginal bool   `json:"replace_original,omitempty"` // Replace original message
	DeleteOriginal  bool   `json:"delete_original,omitempty"`  // Delete original message
}

// WebhookHandler handles Slack webhook requests.
type WebhookHandler struct {
	signingSecret string
	adapter      *SlashCommandAdapter
}

// NewWebhookHandler creates a new WebhookHandler.
func NewWebhookHandler() (*WebhookHandler, error) {
	cfg, err := NewConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load slack config: %w", err)
	}

	return &WebhookHandler{
		signingSecret: cfg.SigningSecret,
	}, nil
}

// NewWebhookHandlerWithSecret creates a WebhookHandler with explicit signing secret.
func NewWebhookHandlerWithSecret(signingSecret string) *WebhookHandler {
	return &WebhookHandler{
		signingSecret: signingSecret,
	}
}

// NewWebhookHandlerWithAdapter creates a WebhookHandler with a slash command adapter.
func NewWebhookHandlerWithAdapter(adapter *SlashCommandAdapter) *WebhookHandler {
	return &WebhookHandler{
		signingSecret: "",
		adapter:       adapter,
	}
}

// HandleSlashCommand handles POST requests to /api/slack/slash.
// It validates the Slack request signature, parses the form data,
// processes the command, and writes the response.
func (h *WebhookHandler) HandleSlashCommand(w http.ResponseWriter, r *http.Request) (*SlashCommandPayload, error) {
	// Only allow POST method
	if r.Method != http.MethodPost {
		return nil, fmt.Errorf("method not allowed")
	}

	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")

	// Read body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}

	// Verify signature
	if h.signingSecret != "" {
		if err := h.verifySignature(timestamp, string(bodyBytes), signature); err != nil {
			return nil, fmt.Errorf("invalid signature: %w", err)
		}
	}

	// Parse form from body
	bodyStr := string(bodyBytes)
	values, err := url.ParseQuery(bodyStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse form data: %w", err)
	}

	command := SlashCommandPayload{
		Command:     values.Get("command"),
		Text:        values.Get("text"),
		UserID:      values.Get("user_id"),
		ChannelID:   values.Get("channel_id"),
		ResponseURL: values.Get("response_url"),
		TriggerID:   values.Get("trigger_id"),
		UserName:    values.Get("user_name"),
		ChannelName: values.Get("channel_name"),
		TeamID:      values.Get("team_id"),
		TeamDomain:  values.Get("team_domain"),
	}

	if command.Command == "" {
		return nil, fmt.Errorf("missing command field")
	}

	log.Printf("Received Slack slash command: %s %s from user %s in channel %s",
		command.Command, command.Text, command.UserID, command.ChannelID)

	// If adapter is set, process the command
	if h.adapter != nil {
		resp, err := h.adapter.Handle(command)
		if err != nil {
			log.Printf("Error processing Slack command: %v", err)
			SendErrorResponse(w, err.Error(), http.StatusInternalServerError)
			return &command, nil
		}
		if resp != nil {
			SendWebhookResponse(w, &WebhookResponse{
				ResponseType: resp.ResponseType,
				Text:        resp.Text,
			})
		}
	}

	return &command, nil
}

// verifySignature validates the Slack request signature.
func (h *WebhookHandler) verifySignature(timestamp, body, signature string) error {
	if signature == "" || timestamp == "" {
		return fmt.Errorf("missing signature or timestamp headers")
	}

	// Check timestamp to prevent replay attacks
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}

	now := time.Now().Unix()
	if abs(now-ts) > 60*5 {
		return fmt.Errorf("request timestamp too old")
	}

	// Verify signature
	expectedSig := h.computeSignature(timestamp, body)
	receivedSig := strings.TrimPrefix(signature, "v0=")

	if !hmac.Equal([]byte(expectedSig), []byte(receivedSig)) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// computeSignature computes the expected signature for a request.
func (h *WebhookHandler) computeSignature(timestamp, body string) string {
	sigBase := fmt.Sprintf("v0=%s:%s", timestamp, body)

	mac := hmac.New(sha256.New, []byte(h.signingSecret))
	mac.Write([]byte(sigBase))
	return hex.EncodeToString(mac.Sum(nil))
}

// abs returns the absolute value of an int64.
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// SendWebhookResponse sends a JSON response to Slack.
func SendWebhookResponse(w http.ResponseWriter, resp *WebhookResponse) {
	w.Header().Set("Content-Type", "application/json")

	// Set default response type
	if resp.ResponseType == "" {
		resp.ResponseType = "ephemeral"
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}

// SendErrorResponse sends an error response to Slack.
func SendErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	resp := WebhookResponse{
		ResponseType: "ephemeral",
		Text:         message,
	}

	json.NewEncoder(w).Encode(resp)
}

// WebhookHandlerFunc returns an http.HandlerFunc for slash commands.
// This is a convenience function for HTTP server registration.
func WebhookHandlerFunc() (http.HandlerFunc, error) {
	h, err := NewWebhookHandler()
	if err != nil {
		// Fall back to using environment variable directly
		signingSecret := os.Getenv("SLACK_SIGNING_SECRET")
		if signingSecret == "" {
			log.Println("Warning: SLACK_SIGNING_SECRET not set, webhook verification disabled")
			signingSecret = "dummy-secret"
		}
		h = NewWebhookHandlerWithSecret(signingSecret)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		command, err := h.HandleSlashCommand(w, r)
		if err != nil {
			SendErrorResponse(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Return success acknowledgment with parsed command
		resp := &WebhookResponse{
			ResponseType: "ephemeral",
			Text:         fmt.Sprintf("Received `/%s %s` from <@%s>", command.Command, command.Text, command.UserID),
		}
		SendWebhookResponse(w, resp)
	}, nil
}

// SlashCommandAdapter adapts SlashCommandHandler struct to work with WebhookHandler.
type SlashCommandAdapter struct {
	handler *SlashCommandHandler
}

// NewSlashCommandAdapter creates a new adapter.
func NewSlashCommandAdapter(handler *SlashCommandHandler) *SlashCommandAdapter {
	return &SlashCommandAdapter{handler: handler}
}

// Handle implements the SlashCommandHandler interface for use with WebhookHandler.
func (a *SlashCommandAdapter) Handle(command SlashCommandPayload) (*WebhookResponse, error) {
	msg, err := a.handler.HandleCommand(command.Command, command.Text, command.UserID, command.ChannelID)
	if err != nil {
		return nil, err
	}

	// Convert slack.Msg to WebhookResponse
	resp := &WebhookResponse{
		ResponseType: msg.ResponseType,
		Text:         msg.Text,
		// Blocks and Attachments would need conversion
	}

	return resp, nil
}
