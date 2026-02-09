package slack

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nexus/nexus/pkg/workspace"
	"github.com/slack-go/slack"
)

// WorkspaceEventType represents the type of workspace event.
type WorkspaceEventType string

const (
	EventWorkspaceCreated  WorkspaceEventType = "workspace_created"
	EventWorkspaceStarted  WorkspaceEventType = "workspace_started"
	EventWorkspaceStopped  WorkspaceEventType = "workspace_stopped"
	EventReleasePublished  WorkspaceEventType = "release_published"
)

// DefaultChannel is the default Slack channel for notifications.
var DefaultChannel = os.Getenv("SLACK_DEFAULT_CHANNEL")

// WorkspaceNotificationInfo contains information about a workspace for notifications.
type WorkspaceNotificationInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Project     string `json:"project"`
	Owner       string `json:"owner"`
	State       string `json:"state"`
	Provider    string `json:"provider,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// ReleaseInfo contains information about a release for notifications.
type ReleaseInfo struct {
	Version     string `json:"version"`
	Tag         string `json:"tag"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	PublishedBy string `json:"published_by,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// NotificationSender sends notifications to Slack.
type NotificationSender struct {
	client  *Client
	channel string
}

// NewNotificationSender creates a new NotificationSender.
func NewNotificationSender(client *Client, channel string) *NotificationSender {
	if channel == "" {
		channel = DefaultChannel
	}
	return &NotificationSender{
		client:  client,
		channel: channel,
	}
}

// SendWorkspaceEvent sends a workspace lifecycle event notification.
func (s *NotificationSender) SendWorkspaceEvent(eventType WorkspaceEventType, info *WorkspaceNotificationInfo) error {
	if s.client == nil {
		return ErrSlackNotConfigured
	}

	color := s.eventColor(eventType)
	emoji := s.eventEmoji(eventType)
	title := s.eventTitle(eventType)

	var fields []slack.AttachmentField
	if info != nil {
		fields = []slack.AttachmentField{
			{Title: "Workspace", Value: info.Name, Short: true},
			{Title: "ID", Value: info.ID, Short: true},
			{Title: "Project", Value: info.Project, Short: true},
			{Title: "Owner", Value: info.Owner, Short: true},
			{Title: "State", Value: info.State, Short: true},
		}
		if info.Provider != "" {
			fields = append(fields, slack.AttachmentField{
				Title: "Provider", Value: info.Provider, Short: true,
			})
		}
	}

	attachment := slack.Attachment{
		Color: color,
		Blocks: slack.Blocks{
			BlockSet: []slack.Block{
				slack.NewHeaderBlock(
					slack.NewTextBlockObject(slack.PlainTextType, fmt.Sprintf("%s %s", emoji, title), false, false),
				),
			},
		},
		Fields: fields,
		Ts:     json.Number(fmt.Sprintf("%d", time.Now().Unix())),
	}

	_, _, err := s.client.api.PostMessage(
		s.channel,
		slack.MsgOptionAttachments(attachment),
		slack.MsgOptionUsername("Nexus"),
		slack.MsgOptionIconEmoji(":computer:"),
	)
	if err != nil {
		return fmt.Errorf("failed to post workspace event: %w", err)
	}

	return nil
}

// SendReleaseNotification sends a release notification.
func (s *NotificationSender) SendReleaseNotification(info *ReleaseInfo) error {
	if s.client == nil {
		return ErrSlackNotConfigured
	}

	if info == nil {
		return fmt.Errorf("release info is nil")
	}

	color := "#36a64f" // Green for releases
	emoji := ":rocket:"

	text := fmt.Sprintf("Version %s has been released!", info.Version)
	if info.Description != "" {
		text = info.Description
	}

	var fields []*slack.TextBlockObject
	if info.Tag != "" {
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*Tag:* %s", info.Tag), false, false))
	}
	if info.PublishedBy != "" {
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*Published by:* %s", info.PublishedBy), false, false))
	}
	if info.URL != "" {
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*Release Notes:* <%s|View Release>", info.URL), false, false))
	}

	attachment := slack.Attachment{
		Color: color,
		Blocks: slack.Blocks{
			BlockSet: []slack.Block{
				slack.NewHeaderBlock(
					slack.NewTextBlockObject(slack.PlainTextType, fmt.Sprintf("%s Release Published", emoji), false, false),
				),
				slack.NewSectionBlock(
					slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
					fields,
					nil,
				),
			},
		},
		Ts: json.Number(fmt.Sprintf("%d", time.Now().Unix())),
	}

	_, _, err := s.client.api.PostMessage(
		s.channel,
		slack.MsgOptionAttachments(attachment),
		slack.MsgOptionUsername("Nexus"),
		slack.MsgOptionIconEmoji(":rocket:"),
	)
	if err != nil {
		return fmt.Errorf("failed to post release notification: %w", err)
	}

	return nil
}

// SendWorkspaceCreated sends a notification for a newly created workspace.
func (s *NotificationSender) SendWorkspaceCreated(ws *workspace.Workspace) error {
	if ws == nil {
		return fmt.Errorf("workspace is nil")
	}

	info := &WorkspaceNotificationInfo{
		ID:          ws.ID,
		Name:        ws.Name,
		Project:     ws.Project,
		Owner:       ws.Owner,
		State:       string(ws.State),
		Provider:    ws.Config.Environment["NEXUS_PROVIDER"],
		Description: ws.Description,
		CreatedAt:   ws.CreatedAt.Format(time.RFC3339),
	}

	return s.SendWorkspaceEvent(EventWorkspaceCreated, info)
}

// SendWorkspaceStarted sends a notification when a workspace is started.
func (s *NotificationSender) SendWorkspaceStarted(ws *workspace.Workspace) error {
	if ws == nil {
		return fmt.Errorf("workspace is nil")
	}

	info := &WorkspaceNotificationInfo{
		ID:        ws.ID,
		Name:      ws.Name,
		Project:   ws.Project,
		Owner:     ws.Owner,
		State:     string(ws.State),
		CreatedAt: ws.CreatedAt.Format(time.RFC3339),
	}

	return s.SendWorkspaceEvent(EventWorkspaceStarted, info)
}

// SendWorkspaceStopped sends a notification when a workspace is stopped.
func (s *NotificationSender) SendWorkspaceStopped(ws *workspace.Workspace) error {
	if ws == nil {
		return fmt.Errorf("workspace is nil")
	}

	info := &WorkspaceNotificationInfo{
		ID:        ws.ID,
		Name:      ws.Name,
		Project:   ws.Project,
		Owner:     ws.Owner,
		State:     string(ws.State),
		CreatedAt: ws.CreatedAt.Format(time.RFC3339),
	}

	return s.SendWorkspaceEvent(EventWorkspaceStopped, info)
}

// eventColor returns the color for the event type.
func (s *NotificationSender) eventColor(eventType WorkspaceEventType) string {
	switch eventType {
	case EventWorkspaceCreated:
		return "#2196F3" // Blue
	case EventWorkspaceStarted:
		return "#4CAF50" // Green
	case EventWorkspaceStopped:
		return "#FF9800" // Orange
	case EventReleasePublished:
		return "#9C27B0" // Purple
	default:
		return "#607D8B" // Grey
	}
}

// eventEmoji returns the emoji for the event type.
func (s *NotificationSender) eventEmoji(eventType WorkspaceEventType) string {
	switch eventType {
	case EventWorkspaceCreated:
		return ":package:"
	case EventWorkspaceStarted:
		return ":play_button:"
	case EventWorkspaceStopped:
		return ":stop_button:"
	case EventReleasePublished:
		return ":rocket:"
	default:
		return ":bell:"
	}
}

// eventTitle returns the title for the event type.
func (s *NotificationSender) eventTitle(eventType WorkspaceEventType) string {
	switch eventType {
	case EventWorkspaceCreated:
		return "Workspace Created"
	case EventWorkspaceStarted:
		return "Workspace Started"
	case EventWorkspaceStopped:
		return "Workspace Stopped"
	case EventReleasePublished:
		return "Release Published"
	default:
		return "Workspace Event"
	}
}
