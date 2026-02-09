package slack

import (
	"fmt"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

// NexusWorkspaceRegistry defines the interface for workspace operations.
type NexusWorkspaceRegistry interface {
	List() ([]*NexusWorkspaceInfo, error)
	Get(id string) (*NexusWorkspaceInfo, error)
}

// NexusWorkspaceInfo represents workspace data for Slack responses.
type NexusWorkspaceInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	State     string `json:"state"`
	Provider  string `json:"provider,omitempty"`
	CreatedAt string `json:"created_at"`
}

// SlashCommandHandler handles Slack slash commands for Nexus.
type SlashCommandHandler struct {
	workspaceRegistry NexusWorkspaceRegistry
}

// NewSlashCommandHandler creates a new slash command handler.
func NewSlashCommandHandler(registry NexusWorkspaceRegistry) *SlashCommandHandler {
	return &SlashCommandHandler{
		workspaceRegistry: registry,
	}
}

// HandleCommand processes a slash command and returns a Slack message response.
func (h *SlashCommandHandler) HandleCommand(cmd, text, userID, channelID string) (*slack.Msg, error) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return h.helpMessage(userID), nil
	}

	subCmd := parts[0]
	args := parts[1:]

	switch subCmd {
	case "workspace":
		return h.handleWorkspaceCommand(args, userID)
	case "help":
		return h.helpMessage(userID), nil
	default:
		return h.unknownCommandMessage(subCmd, userID), nil
	}
}

// handleWorkspaceCommand handles workspace-related subcommands.
func (h *SlashCommandHandler) handleWorkspaceCommand(args []string, userID string) (*slack.Msg, error) {
	if len(args) == 0 {
		return h.workspaceHelpMessage(userID), nil
	}

	subCmd := args[0]

	switch subCmd {
	case "list":
		return h.handleWorkspaceList(userID)
	case "status":
		if len(args) < 2 {
			return h.workspaceStatusHelpMessage(userID), nil
		}
		workspaceName := args[1]
		return h.handleWorkspaceStatus(workspaceName, userID)
	default:
		return h.workspaceHelpMessage(userID), nil
	}
}

// handleWorkspaceList returns a list of all workspaces.
func (h *SlashCommandHandler) handleWorkspaceList(userID string) (*slack.Msg, error) {
	if h.workspaceRegistry == nil {
		return errorMessage(fmt.Errorf("workspace registry not configured")), nil
	}
	workspaces, err := h.workspaceRegistry.List()
	if err != nil {
		return errorMessage(fmt.Errorf("failed to list workspaces: %w", err)), nil
	}

	if len(workspaces) == 0 {
		return noWorkspacesMessage(), nil
	}

	var workspaceLines []string
	for i, ws := range workspaces {
		if i >= 10 {
			break // Limit to 10 for readability
		}
		statusEmoji := getStatusEmoji(ws.State)
		workspaceLines = append(workspaceLines, fmt.Sprintf("%s *%s* (%s) - %s", statusEmoji, ws.Name, ws.ID, ws.State))
	}

	text := fmt.Sprintf("*Your Workspaces*\n%s", strings.Join(workspaceLines, "\n"))

	if len(workspaces) > 10 {
		text += fmt.Sprintf("\n_...and %d more_", len(workspaces)-10)
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject(slack.PlainTextType, "Workspace List", true, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil,
			nil,
		),
		slack.NewContextBlock("",
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("Total: %d workspaces", len(workspaces)), false, false),
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text: "Workspace List",
	}, nil
}

// handleWorkspaceStatus returns the status of a specific workspace.
func (h *SlashCommandHandler) handleWorkspaceStatus(workspaceName, userID string) (*slack.Msg, error) {
	ws, err := h.workspaceRegistry.Get(workspaceName)
	if err != nil {
		return workspaceNotFoundMessage(workspaceName), nil
	}
	if ws == nil {
		return workspaceNotFoundMessage(workspaceName), nil
	}

	fields := []*slack.TextBlockObject{
		slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*Status*\n%s", ws.State), false, false),
		slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*Provider*\n%s", ws.Provider), false, false),
	}

	if ws.Owner != "" {
		fields = append(fields,
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*Owner*\n%s", ws.Owner), false, false),
		)
	}

	createdAt := ws.CreatedAt
	if createdAt == "" {
		createdAt = time.Now().Format("Jan 2, 2006 15:04")
	}
	fields = append(fields,
		slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*Created*\n%s", createdAt), false, false),
	)

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject(slack.PlainTextType, fmt.Sprintf("Workspace: %s", ws.Name), true, false),
		),
		slack.NewSectionBlock(
			nil,
			fields,
			nil,
		),
		slack.NewDividerBlock(),
		slack.NewContextBlock("",
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("Workspace ID: %s", ws.ID), false, false),
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text:         fmt.Sprintf("Status for workspace %s: %s", ws.Name, ws.State),
		ResponseType: "in_channel",
	}, nil
}

// helpMessage returns the main help message.
func (h *SlashCommandHandler) helpMessage(userID string) *slack.Msg {
	text := "*Nexus Slash Commands*\n\n" +
		"Available commands:\n" +
		"* `/nexus workspace list` - List all your workspaces\n" +
		"* `/nexus workspace status <name>` - Get status of a specific workspace\n" +
		"* `/nexus help` - Show this help message"

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject(slack.PlainTextType, "Nexus Help", true, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil,
			nil,
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text: "Nexus Help",
	}
}

// workspaceHelpMessage returns help for workspace commands.
func (h *SlashCommandHandler) workspaceHelpMessage(userID string) *slack.Msg {
	text := "*Workspace Commands*\n\n" +
		"Usage: `/nexus workspace <subcommand> [options]`\n\n" +
		"Subcommands:\n" +
		"* `list` - List all workspaces\n" +
		"* `status <name>` - Show status of a workspace by name"

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject(slack.PlainTextType, "Workspace Help", true, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil,
			nil,
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text: "Workspace Help",
	}
}

// workspaceStatusHelpMessage returns help for workspace status command.
func (h *SlashCommandHandler) workspaceStatusHelpMessage(userID string) *slack.Msg {
	text := "*Usage:* `/nexus workspace status <workspace-name>`\n\n" +
		"Please provide a workspace name."

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil,
			nil,
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text: "Workspace Status Help",
	}
}

// unknownCommandMessage returns a message for unknown commands.
func (h *SlashCommandHandler) unknownCommandMessage(cmd, userID string) *slack.Msg {
	text := fmt.Sprintf("Unknown command: `%s`. Type `/nexus help` for available commands.", cmd)

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil,
			nil,
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text: fmt.Sprintf("Unknown Command\n\n%s", text),
	}
}

// errorMessage returns an error message.
func errorMessage(err error) *slack.Msg {
	text := fmt.Sprintf(":x: Error: %s", err.Error())

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil,
			nil,
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text: "Error",
	}
}

// noWorkspacesMessage returns a message when there are no workspaces.
func noWorkspacesMessage() *slack.Msg {
	text := "No workspaces found. Create one with the Nexus CLI."

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil,
			nil,
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text: "No Workspaces",
	}
}

// workspaceNotFoundMessage returns a message when a workspace is not found.
func workspaceNotFoundMessage(workspaceName string) *slack.Msg {
	text := fmt.Sprintf(":mag: Workspace `%s` not found. Use `/nexus workspace list` to see available workspaces.", workspaceName)

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil,
			nil,
		),
	}

	return &slack.Msg{
		Blocks: slack.Blocks{
			BlockSet: blocks,
		},
		Text: "Workspace Not Found",
	}
}

// getStatusEmoji returns an emoji for the workspace status.
func getStatusEmoji(status string) string {
	switch strings.ToLower(status) {
	case "running":
		return ":green_circle:"
	case "creating":
		return ":yellow_circle:"
	case "stopped":
		return ":red_circle:"
	case "error", "failed":
		return ":x:"
	default:
		return ":white_circle:"
	}
}
