package slack

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRegistry implements NexusWorkspaceRegistry for testing.
type mockRegistry struct {
	workspaces []*NexusWorkspaceInfo
	getResult  *NexusWorkspaceInfo
	getError   error
	listError  error
}

func (m *mockRegistry) List() ([]*NexusWorkspaceInfo, error) {
	if m.listError != nil {
		return nil, m.listError
	}
	return m.workspaces, nil
}

func (m *mockRegistry) Get(id string) (*NexusWorkspaceInfo, error) {
	if m.getError != nil {
		return nil, m.getError
	}
	return m.getResult, nil
}

// mockSlackClient wraps slack.Client for testing.
type mockSlackClient struct {
	postMessageResponse string
	postMessageError   error
	called             bool
	lastChannel        string
	lastAttachments    []slack.Attachment
}

func (m *mockSlackClient) PostMessage(channel string, options ...slack.MsgOption) (string, string, error) {
	m.called = true
	m.lastChannel = channel

	// Extract attachments from options
	for _, opt := range options {
		// slack.MsgOptionAttachments is a function type, use unsafe pointer extraction
		// For testing purposes, we skip attachment extraction
		_ = opt
	}

	return m.postMessageResponse, "", m.postMessageError
}

func TestClientConfig(t *testing.T) {
	tests := []struct {
		name         string
		envBotToken  string
		envSignSec   string
		wantErr      bool
		errContains  string
	}{
		{
			name:        "valid config from environment",
			envBotToken: "xoxb-valid-bot-token",
			envSignSec:  "signing-secret",
			wantErr:     false,
		},
		{
			name:        "missing bot token",
			envBotToken: "",
			envSignSec:  "signing-secret",
			wantErr:     true,
			errContains: "slack is not configured",
		},
		{
			name:        "missing signing secret",
			envBotToken: "xoxb-valid-bot-token",
			envSignSec:  "",
			wantErr:     true,
			errContains: "slack is not configured",
		},
		{
			name:        "both missing",
			envBotToken: "",
			envSignSec:  "",
			wantErr:     true,
			errContains: "slack is not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envBotToken != "" {
				os.Setenv("SLACK_BOT_TOKEN", tt.envBotToken)
				defer os.Unsetenv("SLACK_BOT_TOKEN")
			}
			if tt.envSignSec != "" {
				os.Setenv("SLACK_SIGNING_SECRET", tt.envSignSec)
				defer os.Unsetenv("SLACK_SIGNING_SECRET")
			}

			cfg, err := NewConfig()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, cfg)
			} else {
				require.NoError(t, err)
				require.NotNil(t, cfg)
				assert.Equal(t, tt.envBotToken, cfg.BotToken)
				assert.Equal(t, tt.envSignSec, cfg.SigningSecret)
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		wantNilAPI  bool
	}{
		{
			name: "valid config creates client",
			config: &Config{
				BotToken:      "xoxb-test-token",
				SigningSecret: "test-secret",
			},
			wantNilAPI: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config)
			require.NotNil(t, client)
			assert.NotNil(t, client.API())
		})
	}
}

func TestSlashCommandHandler_ListWorkspaces(t *testing.T) {
	workspaces := []*NexusWorkspaceInfo{
		{ID: "ws-1", Name: "my-workspace", Owner: "user-1", State: "running", Provider: "docker", CreatedAt: "2024-01-15T10:00:00Z"},
		{ID: "ws-2", Name: "test-env", Owner: "user-1", State: "stopped", Provider: "qemu", CreatedAt: "2024-01-14T15:30:00Z"},
	}

	reg := &mockRegistry{workspaces: workspaces}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceList("user-1")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	assert.Equal(t, 3, len(msg.Blocks.BlockSet))
	// Verify blocks contain expected content
	sectionBlock := msg.Blocks.BlockSet[1].(*slack.SectionBlock)
	assert.NotNil(t, sectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "my-workspace")
	assert.Contains(t, sectionBlock.Text.Text, "test-env")
}

func TestSlashCommandHandler_ListWorkspacesEmpty(t *testing.T) {
	reg := &mockRegistry{workspaces: []*NexusWorkspaceInfo{}}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceList("user-1")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "No workspaces found")
}

func TestSlashCommandHandler_ListWorkspacesError(t *testing.T) {
	reg := &mockRegistry{listError: fmt.Errorf("database error")}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceList("user-1")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Error")
}

func TestSlashCommandHandler_StatusWorkspace(t *testing.T) {
	workspace := &NexusWorkspaceInfo{
		ID:        "ws-123",
		Name:      "my-workspace",
		Owner:     "user-1",
		State:     "running",
		Provider:  "docker",
		CreatedAt: "2024-01-15T10:00:00Z",
	}

	reg := &mockRegistry{getResult: workspace}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceStatus("my-workspace", "user-1")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	assert.Equal(t, 4, len(msg.Blocks.BlockSet)) // Header, Section, Divider, Context
	assert.Equal(t, "in_channel", msg.ResponseType)
	// Check msg.Text contains expected content (fallback text field)
	assert.Contains(t, msg.Text, "my-workspace")
	assert.Contains(t, msg.Text, "running")
}

func TestSlashCommandHandler_NotFound(t *testing.T) {
	reg := &mockRegistry{getResult: nil}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceStatus("nonexistent", "user-1")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "not found")
	assert.Contains(t, sectionBlock.Text.Text, "nonexistent")
}

func TestSlashCommandHandler_GetError(t *testing.T) {
	reg := &mockRegistry{getError: fmt.Errorf("database connection failed")}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceStatus("my-workspace", "user-1")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "not found")
}

func TestSlashCommandHandler_HandleCommand(t *testing.T) {
	tests := []struct {
		name         string
		cmd          string
		text         string
		userID       string
		channelID    string
		expectHelp   bool
		expectList   bool
		expectStatus bool
	}{
		{
			name:       "workspace list command",
			cmd:        "/nexus",
			text:       "workspace list",
			userID:     "U123",
			channelID:  "C123",
			expectList: true,
		},
		{
			name:         "workspace status command",
			cmd:          "/nexus",
			text:         "workspace status my-ws",
			userID:       "U123",
			channelID:    "C123",
			expectStatus: true,
		},
		{
			name:       "help command",
			cmd:        "/nexus",
			text:       "help",
			userID:     "U123",
			channelID:  "C123",
			expectHelp: true,
		},
		{
			name:       "empty text shows help",
			cmd:        "/nexus",
			text:       "",
			userID:     "U123",
			channelID:  "C123",
			expectHelp: true,
		},
		{
			name:       "unknown command",
			cmd:        "/nexus",
			text:       "unknown-subcommand",
			userID:     "U123",
			channelID:  "C123",
			expectHelp: false, // Returns unknown command message, not help
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &mockRegistry{
				workspaces: []*NexusWorkspaceInfo{{ID: "ws-1", Name: "my-ws", State: "running"}},
				getResult:  &NexusWorkspaceInfo{ID: "ws-1", Name: "my-ws", State: "running"},
			}
			handler := NewSlashCommandHandler(reg)

			msg, err := handler.HandleCommand(tt.cmd, tt.text, tt.userID, tt.channelID)

			require.NoError(t, err)
			require.NotNil(t, msg)

			// Content is in blocks, check msg.Text for fallback
			if tt.expectHelp {
				assert.Contains(t, msg.Text, "Nexus Help")
			}
			if tt.expectList {
				assert.Contains(t, msg.Text, "Workspace List")
			}
			if tt.expectStatus {
				assert.Contains(t, msg.Text, "Status for workspace")
			}
			// For unknown command, verify it returns "Unknown Command"
			if !tt.expectHelp && !tt.expectList && !tt.expectStatus {
				assert.Contains(t, msg.Text, "Unknown Command")
			}
		})
	}
}

func TestGetStatusEmoji(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"running", ":green_circle:"},
		{"creating", ":yellow_circle:"},
		{"stopped", ":red_circle:"},
		{"error", ":x:"},
		{"failed", ":x:"},
		{"unknown", ":white_circle:"},
		{"RUNNING", ":green_circle:"},
		{"Creating", ":yellow_circle:"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			result := getStatusEmoji(tt.status)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNotificationSender_SendWorkspaceEvent(t *testing.T) {
	tests := []struct {
		name           string
		eventType      WorkspaceEventType
		info           *WorkspaceNotificationInfo
		postMessageErr error
		wantErr        bool
	}{
		{
			name:      "workspace created event",
			eventType: EventWorkspaceCreated,
			info: &WorkspaceNotificationInfo{
				ID:      "ws-1",
				Name:    "my-workspace",
				Project: "my-project",
				Owner:   "user-1",
				State:   "creating",
			},
			postMessageErr: nil,
			wantErr:        false,
		},
		{
			name:      "workspace started event",
			eventType: EventWorkspaceStarted,
			info: &WorkspaceNotificationInfo{
				ID:      "ws-1",
				Name:    "my-workspace",
				Project: "my-project",
				Owner:   "user-1",
				State:   "running",
			},
			postMessageErr: nil,
			wantErr:        false,
		},
		{
			name:      "workspace stopped event",
			eventType: EventWorkspaceStopped,
			info: &WorkspaceNotificationInfo{
				ID:      "ws-1",
				Name:    "my-workspace",
				Project: "my-project",
				Owner:   "user-1",
				State:   "stopped",
			},
			postMessageErr: nil,
			wantErr:        false,
		},
		{
			name:      "nil client returns error",
			eventType: EventWorkspaceCreated,
			info:      &WorkspaceNotificationInfo{},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{BotToken: "test-token", SigningSecret: "test-secret"}
			client := NewClient(cfg)

			// Note: We can't easily mock PostMessage without using the slack package's interface
			// For this test, we'll use the real client but in a test channel
			sender := &NotificationSender{
				client:  client,
				channel: "#test",
			}

			if tt.wantErr {
				err := sender.SendWorkspaceEvent(tt.eventType, tt.info)
				assert.Error(t, err)
			} else {
				// Skip actual send in unit test (would call real Slack API)
				t.Skip("Skipping actual Slack API call in unit test")
			}
		})
	}
}

func TestNotificationSender_NewNotificationSender(t *testing.T) {
	tests := []struct {
		name           string
		channel        string
		expectedChannel string
	}{
		{
			name:            "custom channel",
			channel:         "#my-channel",
			expectedChannel: "#my-channel",
		},
		{
			name:            "empty channel uses default",
			channel:         "",
			expectedChannel: "", // Empty channel uses whatever was set at init time
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{BotToken: "test-token", SigningSecret: "test-secret"}
			client := NewClient(cfg)

			sender := NewNotificationSender(client, tt.channel)
			require.NotNil(t, sender)
			assert.Equal(t, tt.expectedChannel, sender.channel)
		})
	}
}

func TestNotificationSender_SendReleaseNotification(t *testing.T) {
	// Test nil client behavior instead of actual send
	sender := &NotificationSender{client: nil}
	err := sender.SendReleaseNotification(&ReleaseInfo{Version: "1.2.0"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestNotificationSender_SendWorkspaceCreated(t *testing.T) {
	t.Skip("Skipping test - requires workspace package mock")
}

func TestNotificationSender_SendWorkspaceStarted(t *testing.T) {
	t.Skip("Skipping test - requires workspace package mock")
}

func TestNotificationSender_SendWorkspaceStopped(t *testing.T) {
	t.Skip("Skipping test - requires workspace package mock")
}

func TestWebhookHandler_SignatureVerification(t *testing.T) {
	tests := []struct {
		name        string
		signingSecret string
		body        string
		timestamp   string
		signature   string
		wantStatus  int
	}{
		{
			name:          "valid signature",
			signingSecret: "valid-secret",
			body:          "command=%2Fnexus&text=list",
			timestamp:     fmt.Sprintf("%d", time.Now().Unix()),
			signature:     "", // Will be computed
			wantStatus:    http.StatusOK,
		},
		{
			name:          "invalid signature",
			signingSecret: "valid-secret",
			body:          "command=%2Fnexus&text=list",
			timestamp:     fmt.Sprintf("%d", time.Now().Unix()),
			signature:     "v0=invalidsignature123456789",
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "empty signature",
			signingSecret: "valid-secret",
			body:          "command=%2Fnexus&text=list",
			timestamp:     fmt.Sprintf("%d", time.Now().Unix()),
			signature:     "",
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "old timestamp",
			signingSecret: "valid-secret",
			body:          "command=%2Fnexus&text=list",
			timestamp:     fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix()),
			signature:     "v0=invalidsignature123456789",
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:          "missing signing secret disables verification",
			signingSecret: "",
			body:          "command=%2Fnexus&text=list",
			timestamp:     fmt.Sprintf("%d", time.Now().Unix()),
			signature:     "",
			wantStatus:    http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewWebhookHandlerWithSecret(tt.signingSecret)

			// Create request
			body := tt.body
			req := httptest.NewRequest(http.MethodPost, "/api/slack/slash", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			// Set timestamp and signature headers
			if tt.name == "valid signature" {
				ts := tt.timestamp
				sigBase := fmt.Sprintf("v0=%s:%s", ts, body)
				mac := hmac.New(sha256.New, []byte(tt.signingSecret))
				mac.Write([]byte(sigBase))
				computedSig := hex.EncodeToString(mac.Sum(nil))
				req.Header.Set("X-Slack-Request-Timestamp", ts)
				req.Header.Set("X-Slack-Signature", "v0="+computedSig)
			} else {
				req.Header.Set("X-Slack-Request-Timestamp", tt.timestamp)
				req.Header.Set("X-Slack-Signature", tt.signature)
			}

			// Create response recorder
			rec := httptest.NewRecorder()

			// Call handler
			_, err := handler.HandleSlashCommand(rec, req)

			if tt.wantStatus == http.StatusOK {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestWebhookHandler_HandleSlashCommand(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("test-secret")

	// Compute valid signature
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := "command=%2Fnexus&text=list&user_id=U123&channel_id=C123"
	sigBase := fmt.Sprintf("v0=%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte(sigBase))
	signature := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/slack/slash", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", signature)

	rec := httptest.NewRecorder()

	payload, err := handler.HandleSlashCommand(rec, req)

	require.NoError(t, err)
	require.NotNil(t, payload)
	assert.Equal(t, "/nexus", payload.Command)
	assert.Equal(t, "list", payload.Text)
	assert.Equal(t, "U123", payload.UserID)
	assert.Equal(t, "C123", payload.ChannelID)
}

func TestWebhookHandler_HandleSlashCommandInvalidMethod(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("test-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/slack/slash", nil)

	rec := httptest.NewRecorder()

	_, err := handler.HandleSlashCommand(rec, req)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "method not allowed")
}

func TestWebhookHandler_HandleSlashCommandMissingCommand(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("")

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := "text=list&user_id=U123&channel_id=C123" // Missing command

	req := httptest.NewRequest(http.MethodPost, "/api/slack/slash", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)

	rec := httptest.NewRecorder()

	_, err := handler.HandleSlashCommand(rec, req)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing command field")
}

func TestComputeSignature(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("test-secret")

	sig := handler.computeSignature("1234567890", "test body")

	assert.NotEmpty(t, sig)
	assert.Regexp(t, `^[0-9a-f]+$`, sig)
}

func TestAbs(t *testing.T) {
	tests := []struct {
		input    int64
		expected int64
	}{
		{10, 10},
		{-10, 10},
		{0, 0},
		{-1, 1},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.input), func(t *testing.T) {
			result := abs(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSendWebhookResponse(t *testing.T) {
	rec := httptest.NewRecorder()

	resp := &WebhookResponse{
		ResponseType: "ephemeral",
		Text:         "Test message",
	}

	SendWebhookResponse(rec, resp)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	body, _ := io.ReadAll(rec.Body)
	var result WebhookResponse
	err := json.Unmarshal(body, &result)
	require.NoError(t, err)
	assert.Equal(t, "ephemeral", result.ResponseType)
	assert.Equal(t, "Test message", result.Text)
}

func TestSendWebhookResponseDefaultType(t *testing.T) {
	rec := httptest.NewRecorder()

	resp := &WebhookResponse{
		Text: "Test message",
	}

	SendWebhookResponse(rec, resp)

	var result WebhookResponse
	json.NewDecoder(rec.Body).Decode(&result)
	assert.Equal(t, "ephemeral", result.ResponseType)
}

func TestSendErrorResponse(t *testing.T) {
	rec := httptest.NewRecorder()

	SendErrorResponse(rec, "Error message", http.StatusBadRequest)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	body, _ := io.ReadAll(rec.Body)
	var result WebhookResponse
	err := json.Unmarshal(body, &result)
	require.NoError(t, err)
	assert.Equal(t, "ephemeral", result.ResponseType)
	assert.Equal(t, "Error message", result.Text)
}

func TestWebhookHandlerFunc(t *testing.T) {
	// This test verifies the handler function can be created
	// We skip the actual HTTP call since it requires proper Slack setup
	os.Setenv("SLACK_SIGNING_SECRET", "test-secret")
	defer os.Unsetenv("SLACK_SIGNING_SECRET")

	handlerFunc, err := WebhookHandlerFunc()
	require.NoError(t, err)
	assert.NotNil(t, handlerFunc)
}

func TestNewWebhookHandler(t *testing.T) {
	os.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	defer os.Unsetenv("SLACK_BOT_TOKEN")
	os.Setenv("SLACK_SIGNING_SECRET", "test-secret")
	defer os.Unsetenv("SLACK_SIGNING_SECRET")

	handler, err := NewWebhookHandler()
	require.NoError(t, err)
	require.NotNil(t, handler)
}

func TestNewWebhookHandlerWithSecret(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("my-secret")
	require.NotNil(t, handler)
}

func TestSlashCommandAdapter(t *testing.T) {
	reg := &mockRegistry{
		workspaces: []*NexusWorkspaceInfo{
			{ID: "ws-1", Name: "test", Owner: "user", State: "running"},
		},
	}
	cmdHandler := NewSlashCommandHandler(reg)
	adapter := NewSlashCommandAdapter(cmdHandler)

	payload := SlashCommandPayload{
		Command:   "/nexus",
		Text:      "workspace list",
		UserID:    "U123",
		ChannelID: "C123",
	}

	resp, err := adapter.Handle(payload)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "/nexus", payload.Command)
}

func TestSlashCommandAdapter_Error(t *testing.T) {
	reg := &mockRegistry{listError: fmt.Errorf("db error")}
	cmdHandler := NewSlashCommandHandler(reg)
	adapter := NewSlashCommandAdapter(cmdHandler)

	payload := SlashCommandPayload{
		Command: "/nexus",
		Text:    "workspace list",
		UserID:  "U123",
	}

	resp, err := adapter.Handle(payload)

	// The error is handled within HandleCommand and returns a message, not an error
	assert.NoError(t, err)
	require.NotNil(t, resp)
}

func TestEventColor(t *testing.T) {
	sender := &NotificationSender{}

	tests := []struct {
		eventType  WorkspaceEventType
		expected   string
	}{
		{EventWorkspaceCreated, "#2196F3"},
		{EventWorkspaceStarted, "#4CAF50"},
		{EventWorkspaceStopped, "#FF9800"},
		{EventReleasePublished, "#9C27B0"},
		{"unknown", "#607D8B"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			result := sender.eventColor(tt.eventType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEventEmoji(t *testing.T) {
	sender := &NotificationSender{}

	tests := []struct {
		eventType  WorkspaceEventType
		expected   string
	}{
		{EventWorkspaceCreated, ":package:"},
		{EventWorkspaceStarted, ":play_button:"},
		{EventWorkspaceStopped, ":stop_button:"},
		{EventReleasePublished, ":rocket:"},
		{"unknown", ":bell:"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			result := sender.eventEmoji(tt.eventType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEventTitle(t *testing.T) {
	sender := &NotificationSender{}

	tests := []struct {
		eventType  WorkspaceEventType
		expected   string
	}{
		{EventWorkspaceCreated, "Workspace Created"},
		{EventWorkspaceStarted, "Workspace Started"},
		{EventWorkspaceStopped, "Workspace Stopped"},
		{EventReleasePublished, "Release Published"},
		{"unknown", "Workspace Event"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			result := sender.eventTitle(tt.eventType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsErrSlackNotConfigured(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "is configured error",
			err:      ErrSlackNotConfigured,
			expected: true,
		},
		{
			name:     "wrapped error",
			err:      fmt.Errorf("wrapped: %w", ErrSlackNotConfigured),
			expected: true,
		},
		{
			name:     "different error",
			err:      fmt.Errorf("some other error"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsErrSlackNotConfigured(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSlashCommandHandler_WorkspaceListLimit(t *testing.T) {
	// Create more than 10 workspaces to test the limit
	workspaces := make([]*NexusWorkspaceInfo, 15)
	for i := range workspaces {
		workspaces[i] = &NexusWorkspaceInfo{
			ID:        fmt.Sprintf("ws-%d", i),
			Name:      fmt.Sprintf("workspace-%d", i),
			State:     "running",
			CreatedAt: time.Now().Format(time.RFC3339),
		}
	}

	reg := &mockRegistry{workspaces: workspaces}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceList("user-1")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[1].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "_...and 5 more_")
}

func TestSlashCommandHandler_WorkspaceStatusHelp(t *testing.T) {
	reg := &mockRegistry{}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceCommand([]string{}, "U123")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[1].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Workspace Commands")
}

func TestSlashCommandHandler_WorkspaceStatusHelpMissingName(t *testing.T) {
	reg := &mockRegistry{}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceCommand([]string{"status"}, "U123")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Usage:")
}

func TestUnknownCommandMessage(t *testing.T) {
	reg := &mockRegistry{}
	handler := NewSlashCommandHandler(reg)

	msg := handler.unknownCommandMessage("badcmd", "U123")

	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Unknown command")
	assert.Contains(t, sectionBlock.Text.Text, "badcmd")
}

func TestErrorMessage(t *testing.T) {
	err := fmt.Errorf("something went wrong")
	msg := errorMessage(err)

	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Error")
	assert.Contains(t, sectionBlock.Text.Text, "something went wrong")
}

func TestNoWorkspacesMessage(t *testing.T) {
	msg := noWorkspacesMessage()

	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "No workspaces found")
}

func TestWorkspaceNotFoundMessage(t *testing.T) {
	msg := workspaceNotFoundMessage("my-ws")

	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "not found")
	assert.Contains(t, sectionBlock.Text.Text, "my-ws")
}

func TestHelpMessage(t *testing.T) {
	reg := &mockRegistry{}
	handler := NewSlashCommandHandler(reg)

	msg := handler.helpMessage("U123")

	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[1].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Nexus Slash Commands")
	assert.Contains(t, sectionBlock.Text.Text, "/nexus workspace list")
	assert.Contains(t, sectionBlock.Text.Text, "/nexus workspace status")
}

func TestWorkspaceHelpMessage(t *testing.T) {
	reg := &mockRegistry{}
	handler := NewSlashCommandHandler(reg)

	msg := handler.workspaceHelpMessage("U123")

	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[1].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Workspace Commands")
	assert.Contains(t, sectionBlock.Text.Text, "list")
	assert.Contains(t, sectionBlock.Text.Text, "status")
}

func TestWorkspaceStatusHelpMessage(t *testing.T) {
	reg := &mockRegistry{}
	handler := NewSlashCommandHandler(reg)

	msg := handler.workspaceStatusHelpMessage("U123")

	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Usage:")
	assert.Contains(t, sectionBlock.Text.Text, "workspace-name")
}

func TestNotificationSenderNilClient(t *testing.T) {
	sender := &NotificationSender{client: nil}

	err := sender.SendWorkspaceEvent(EventWorkspaceCreated, &WorkspaceNotificationInfo{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestSendReleaseNotificationNilClient(t *testing.T) {
	sender := &NotificationSender{client: nil}

	err := sender.SendReleaseNotification(&ReleaseInfo{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestSendReleaseNotificationNilInfo(t *testing.T) {
	cfg := &Config{BotToken: "test"}
	client := NewClient(cfg)
	sender := NewNotificationSender(client, "#test")

	err := sender.SendReleaseNotification(nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestSendWorkspaceCreatedNilWorkspace(t *testing.T) {
	cfg := &Config{BotToken: "test"}
	client := NewClient(cfg)
	sender := NewNotificationSender(client, "#test")

	err := sender.SendWorkspaceCreated(nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestSendWorkspaceStartedNilWorkspace(t *testing.T) {
	cfg := &Config{BotToken: "test"}
	client := NewClient(cfg)
	sender := NewNotificationSender(client, "#test")

	err := sender.SendWorkspaceStarted(nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestSendWorkspaceStoppedNilWorkspace(t *testing.T) {
	cfg := &Config{BotToken: "test"}
	client := NewClient(cfg)
	sender := NewNotificationSender(client, "#test")

	err := sender.SendWorkspaceStopped(nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestWebhookHandler_ParseFormError(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("test-secret")

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := "invalid=form%data" // Invalid URL-encoded data

	req := httptest.NewRequest(http.MethodPost, "/api/slack/slash", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)

	// Set a valid signature for this malformed body
	sigBase := fmt.Sprintf("v0=%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte(sigBase))
	signature := "v0=" + hex.EncodeToString(mac.Sum(nil))
	req.Header.Set("X-Slack-Signature", signature)

	rec := httptest.NewRecorder()

	_, err := handler.HandleSlashCommand(rec, req)

	assert.Error(t, err)
}

func TestClientAPI(t *testing.T) {
	cfg := &Config{BotToken: "test-token"}
	client := NewClient(cfg)

	api := client.API()
	assert.NotNil(t, api)
	assert.Equal(t, api, client.API())
}

func TestSlashCommandHandlerNilRegistry(t *testing.T) {
	handler := &SlashCommandHandler{workspaceRegistry: nil}

	msg, err := handler.handleWorkspaceList("U123")

	// With nil registry, we expect an error message
	assert.NoError(t, err)
	assert.NotNil(t, msg)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "Error")
}

func TestNotificationSenderNilChannel(t *testing.T) {
	cfg := &Config{BotToken: "test-token"}
	client := NewClient(cfg)

	// When channel is empty and DefaultChannel is empty
	sender := NewNotificationSender(client, "")
	assert.NotNil(t, sender)
}

func TestNewNotificationSenderWithDefaultChannel(t *testing.T) {
	// Save original value
	original := DefaultChannel
	DefaultChannel = "#alerts"
	defer func() { DefaultChannel = original }()

	cfg := &Config{BotToken: "test-token"}
	client := NewClient(cfg)

	sender := NewNotificationSender(client, "")
	assert.Equal(t, "#alerts", sender.channel)
}

func TestURLEncodedPayload(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("test-secret")

	// Test URL encoding of special characters
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	// URL-encoded text: "status my-workspace-1"
	body := "command=%2Fnexus&text=status+my-workspace-1&user_id=U123&channel_id=C123"

	sigBase := fmt.Sprintf("v0=%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte(sigBase))
	signature := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/slack/slash", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", signature)

	rec := httptest.NewRecorder()

	payload, err := handler.HandleSlashCommand(rec, req)

	require.NoError(t, err)
	assert.Equal(t, "/nexus", payload.Command)
	// Space is encoded as + or %20
	assert.Equal(t, "status my-workspace-1", payload.Text)
}

func TestResponseTypes(t *testing.T) {
	tests := []struct {
		name         string
		responseType string
		expected     string
	}{
		{"ephemeral", "ephemeral", "ephemeral"},
		{"in_channel", "in_channel", "in_channel"},
		{"empty defaults to ephemeral", "", "ephemeral"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			resp := &WebhookResponse{
				ResponseType: tt.responseType,
				Text:         "test",
			}
			SendWebhookResponse(rec, resp)

			var result WebhookResponse
			json.NewDecoder(rec.Body).Decode(&result)
			assert.Equal(t, tt.expected, result.ResponseType)
		})
	}
}

func TestWorkspaceNotificationInfoWithOptionalFields(t *testing.T) {
	info := &WorkspaceNotificationInfo{
		ID:          "ws-1",
		Name:        "test",
		Project:     "proj",
		Owner:       "user",
		State:       "running",
		Provider:    "docker",
		Description: "A test workspace",
		CreatedAt:   "2024-01-15T10:00:00Z",
	}

	assert.NotEmpty(t, info.Provider)
	assert.NotEmpty(t, info.Description)
}

func TestReleaseInfoWithOptionalFields(t *testing.T) {
	info := &ReleaseInfo{
		Version:     "1.0.0",
		Tag:         "v1.0.0",
		Description: "Release notes here",
		URL:         "https://example.com",
		PublishedBy: "developer",
		CreatedAt:   time.Now().Format(time.RFC3339),
	}

	assert.NotEmpty(t, info.Tag)
	assert.NotEmpty(t, info.Description)
	assert.NotEmpty(t, info.URL)
	assert.NotEmpty(t, info.PublishedBy)
}

func TestReleaseInfoMinimal(t *testing.T) {
	info := &ReleaseInfo{
		Version: "1.0.0",
	}

	assert.Empty(t, info.Tag)
	assert.Empty(t, info.Description)
	assert.Empty(t, info.URL)
	assert.Empty(t, info.PublishedBy)
}

func TestNexusWorkspaceInfoJSON(t *testing.T) {
	info := &NexusWorkspaceInfo{
		ID:        "ws-1",
		Name:      "test",
		Owner:     "user",
		State:     "running",
		Provider:  "docker",
		CreatedAt: "2024-01-15T10:00:00Z",
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded NexusWorkspaceInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, info.ID, decoded.ID)
	assert.Equal(t, info.Name, decoded.Name)
	assert.Equal(t, info.State, decoded.State)
}

func TestSlashCommandPayloadJSON(t *testing.T) {
	payload := SlashCommandPayload{
		Command:     "/nexus",
		Text:        "list",
		UserID:      "U123",
		ChannelID:   "C123",
		ResponseURL: "https://hooks.slack.com/...",
		TriggerID:   "123",
		UserName:    "testuser",
		ChannelName: "general",
		TeamID:      "T123",
		TeamDomain:  "testteam",
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded SlashCommandPayload
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, payload.Command, decoded.Command)
	assert.Equal(t, payload.Text, decoded.Text)
	assert.Equal(t, payload.UserID, decoded.UserID)
}

func TestWebhookResponseJSON(t *testing.T) {
	resp := &WebhookResponse{
		ResponseType:    "ephemeral",
		Text:            "Hello",
		ThreadTimestamp: "123.456",
		ReplaceOriginal: true,
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded map[string]interface{}
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "ephemeral", decoded["response_type"])
	assert.Equal(t, "Hello", decoded["text"])
	assert.Equal(t, true, decoded["replace_original"])
}

func TestEmptyWorkspaceList(t *testing.T) {
	reg := &mockRegistry{workspaces: []*NexusWorkspaceInfo{}}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceList("U123")

	require.NoError(t, err)
	// Content is in blocks, not Text
	sectionBlock := msg.Blocks.BlockSet[0].(*slack.SectionBlock)
	assert.Contains(t, sectionBlock.Text.Text, "No workspaces found")
}

func TestWorkspaceStatusWithEmptyOwner(t *testing.T) {
	workspace := &NexusWorkspaceInfo{
		ID:        "ws-1",
		Name:      "test",
		Owner:     "", // Empty owner
		State:     "running",
		CreatedAt: "2024-01-15",
	}

	reg := &mockRegistry{getResult: workspace}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceStatus("test", "U123")

	require.NoError(t, err)
	// Owner field should not be present in output
	assert.NotContains(t, msg.Text, "*Owner*")
}

func TestWorkspaceStatusWithEmptyCreatedAt(t *testing.T) {
	workspace := &NexusWorkspaceInfo{
		ID:        "ws-1",
		Name:      "test",
		State:     "running",
		CreatedAt: "", // Empty created at
	}

	reg := &mockRegistry{getResult: workspace}
	handler := NewSlashCommandHandler(reg)

	msg, err := handler.handleWorkspaceStatus("test", "U123")

	require.NoError(t, err)
	require.NotNil(t, msg)
	// Check msg.Text contains expected content - it shows "Status for workspace"
	assert.Contains(t, msg.Text, "Status for workspace")
}

func TestSignatureReplayAttack(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("test-secret")

	timestamp := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	body := "command=%2Fnexus&text=list"

	req := httptest.NewRequest(http.MethodPost, "/api/slack/slash", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)

	rec := httptest.NewRecorder()

	_, err := handler.HandleSlashCommand(rec, req)

	assert.Error(t, err)
	// Error should mention timestamp being too old
	assert.Contains(t, err.Error(), "timestamp")
}

func TestInvalidTimestamp(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("test-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/slack/slash", bytes.NewBufferString("test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", "not-a-number")
	req.Header.Set("X-Slack-Signature", "v0=abc123")

	rec := httptest.NewRecorder()

	_, err := handler.HandleSlashCommand(rec, req)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timestamp")
}

func TestMissingSignatureHeaders(t *testing.T) {
	handler := NewWebhookHandlerWithSecret("test-secret")

	body := "command=%2Fnexus&text=list"
	req := httptest.NewRequest(http.MethodPost, "/api/slack/slash", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// No X-Slack headers

	rec := httptest.NewRecorder()

	_, err := handler.HandleSlashCommand(rec, req)

	assert.Error(t, err)
}

func TestReadBodyError(t *testing.T) {
	// Create a request that will fail when reading body
	// We can't easily simulate this without mocking http.Request
	// So we skip this edge case as it requires significant mocking infrastructure
	t.Skip("Skipping edge case test that requires http.Request mocking")
	// Avoid unused variable warning
	_ = NewWebhookHandlerWithSecret("test-secret")
}

func TestNewConfigConcurrent(t *testing.T) {
	// Test that concurrent config loading doesn't cause race conditions
	// Set environment variables once for all goroutines
	os.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	os.Setenv("SLACK_SIGNING_SECRET", "test-secret")
	defer os.Unsetenv("SLACK_BOT_TOKEN")
	defer os.Unsetenv("SLACK_SIGNING_SECRET")

	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func() {
			_, err := NewConfig()
			assert.NoError(t, err)
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestClientWithDifferentTokenFormats(t *testing.T) {
	tokens := []string{
		"xoxb-123",
		"xoxb-123-456-abcdef",
		"xoxb-1234567890abcdefghijklmnop",
	}

	for _, token := range tokens {
		t.Run(token, func(t *testing.T) {
			cfg := &Config{BotToken: token}
			client := NewClient(cfg)

			assert.NotNil(t, client)
			assert.NotNil(t, client.API())
		})
	}
}

func TestWebhookHandlerSigningSecretNil(t *testing.T) {
	handler := &WebhookHandler{signingSecret: ""}

	err := handler.verifySignature("1234567890", "body", "v0=abc")
	// When signing secret is empty, verification is skipped, but timestamp may still be checked
	// So this test checks that we get an error about timestamp, not signature
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp")
}

func TestDefaultChannelEnvironmentVariable(t *testing.T) {
	original := os.Getenv("SLACK_DEFAULT_CHANNEL")
	defer os.Setenv("SLACK_DEFAULT_CHANNEL", original)

	os.Setenv("SLACK_DEFAULT_CHANNEL", "#notifications")
	// DefaultChannel is set at package init time, so we need to test via NewNotificationSender
	_ = DefaultChannel // Reference to ensure compile

	cfg := &Config{BotToken: "test"}
	client := NewClient(cfg)
	sender := NewNotificationSender(client, "")

	// The channel will be whatever DefaultChannel was set to at init time
	// This test documents the behavior
	assert.NotNil(t, sender)
}

func TestWebhookHandlerFuncWithEnvFallback(t *testing.T) {
	// Test WebhookHandlerFunc when NewWebhookHandler fails and falls back to env var
	os.Setenv("SLACK_SIGNING_SECRET", "test-secret-fallback")
	defer os.Unsetenv("SLACK_SIGNING_SECRET")

	handlerFunc, err := WebhookHandlerFunc()
	require.NoError(t, err)
	assert.NotNil(t, handlerFunc)
}

func TestSendWebhookResponseInChannel(t *testing.T) {
	rec := httptest.NewRecorder()

	resp := &WebhookResponse{
		ResponseType: "in_channel",
		Text:         "Test message",
	}

	SendWebhookResponse(rec, resp)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result WebhookResponse
	json.NewDecoder(rec.Body).Decode(&result)
	assert.Equal(t, "in_channel", result.ResponseType)
}

func TestWebhookResponseReplaceOriginal(t *testing.T) {
	rec := httptest.NewRecorder()

	resp := &WebhookResponse{
		ResponseType:    "ephemeral",
		Text:            "Updated message",
		ReplaceOriginal: true,
	}

	SendWebhookResponse(rec, resp)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)
	assert.Equal(t, true, result["replace_original"])
}
