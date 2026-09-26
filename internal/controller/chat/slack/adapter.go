// Package slack implements controller.ChatAdapter using Slack Socket Mode.
package slack

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	goslack "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/IniZio/nexus/internal/controller"
)

const LongAnswerThreshold = 11 * 1024

// eventSource abstracts where inbound socketmode.Events come from.
// The real implementation reads from sm.Events; tests inject directly.
type eventSource interface {
	events() <-chan socketmode.Event
	ack(req *socketmode.Request)
	run(ctx context.Context) error
}

// realSource wraps a socketmode.Client.
type realSource struct{ sm *socketmode.Client }

func (r *realSource) events() <-chan socketmode.Event { return r.sm.Events }
func (r *realSource) ack(req *socketmode.Request)     { r.sm.Ack(*req) }
func (r *realSource) run(ctx context.Context) error   { return r.sm.RunContext(ctx) }

// Adapter is a controller.ChatAdapter backed by Slack Socket Mode.
type Adapter struct {
	api    *goslack.Client
	src    eventSource
	botID  string
	teamID string
}

// New creates an Adapter. appToken must be xapp-; botToken must be xoxb-.
func New(appToken, botToken string) (*Adapter, error) {
	api := goslack.New(botToken, goslack.OptionAppLevelToken(appToken))
	sm := socketmode.New(api)

	authResp, err := api.AuthTest()
	if err != nil {
		return nil, fmt.Errorf("slack: auth.test: %w", err)
	}

	return &Adapter{
		api:    api,
		src:    &realSource{sm: sm},
		botID:  authResp.BotID,
		teamID: authResp.TeamID,
	}, nil
}

// NewAdapterFromParts constructs an Adapter from pre-resolved credentials and a socketmode client.
// Used in integration tests that supply an httptest-backed API server.
func NewAdapterFromParts(api *goslack.Client, sm *socketmode.Client, botID, teamID string) *Adapter {
	return &Adapter{api: api, src: &realSource{sm: sm}, botID: botID, teamID: teamID}
}

// newWithSource constructs an Adapter with a custom event source (for unit tests).
func newWithSource(api *goslack.Client, src eventSource, botID, teamID string) *Adapter {
	return &Adapter{api: api, src: src, botID: botID, teamID: teamID}
}

// chanSource is an eventSource backed by a plain channel, used in tests.
type chanSource struct{ ch <-chan socketmode.Event }

func (c *chanSource) events() <-chan socketmode.Event { return c.ch }
func (c *chanSource) ack(_ *socketmode.Request)       {}
func (c *chanSource) run(ctx context.Context) error   { <-ctx.Done(); return nil }

// NewAdapterWithFakeSource constructs an Adapter whose inbound events come from ch.
// Used in tests to bypass the WebSocket connection.
func NewAdapterWithFakeSource(api *goslack.Client, ch <-chan socketmode.Event, botID, teamID string) *Adapter {
	return &Adapter{api: api, src: &chanSource{ch: ch}, botID: botID, teamID: teamID}
}

var reMention = regexp.MustCompile(`<@[^>]+>`)

// StripBotMention removes all <@…> mentions from text and trims whitespace.
func StripBotMention(text string) string {
	return strings.TrimSpace(reMention.ReplaceAllString(text, ""))
}

// Run blocks until ctx is done, dispatching incoming Slack events to h.
func (a *Adapter) Run(ctx context.Context, h controller.Handler) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() { _ = a.src.run(runCtx) }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-a.src.events():
			if !ok {
				return nil
			}
			if err := a.dispatch(ctx, ev, h); err != nil {
				return err
			}
		}
	}
}

func (a *Adapter) dispatch(ctx context.Context, ev socketmode.Event, h controller.Handler) error {
	switch ev.Type {
	case socketmode.EventTypeEventsAPI:
		if ev.Request != nil {
			a.src.ack(ev.Request)
		}
		return a.handleEventsAPIRaw(ctx, ev, h)

	case socketmode.EventTypeSlashCommand:
		if ev.Request != nil {
			a.src.ack(ev.Request)
		}
		cmd, ok := ev.Data.(goslack.SlashCommand)
		if !ok {
			return nil
		}
		ref := controller.NewThreadRef(cmd.TeamID, cmd.ChannelID, cmd.TriggerID)
		return h(ctx, controller.Event{
			Kind:      controller.EventSlashCommand,
			ThreadRef: ref,
			User:      cmd.UserID,
			Text:      strings.TrimSpace(cmd.Command + " " + cmd.Text),
		})
	}
	return nil
}

// SlashCommandToEvent converts a SlashCommand to a controller.Event (exported for tests).
func SlashCommandToEvent(cmd goslack.SlashCommand) controller.Event {
	ref := controller.NewThreadRef(cmd.TeamID, cmd.ChannelID, cmd.TriggerID)
	return controller.Event{
		Kind:      controller.EventSlashCommand,
		ThreadRef: ref,
		User:      cmd.UserID,
		Text:      strings.TrimSpace(cmd.Command + " " + cmd.Text),
	}
}

func (a *Adapter) handleEventsAPIRaw(ctx context.Context, ev socketmode.Event, h controller.Handler) error {
	// ev.Data is slackevents.EventsAPIEvent after socketmode parsing
	type innerHolder interface {
		GetInnerEvent() interface{}
		GetTeamID() string
	}

	// Use the raw approach: ev.Data contains the parsed outer event
	// socketmode delivers slackevents.EventsAPIEvent in ev.Data
	// We use type assertion through the socketmode package types
	return dispatchInnerEvent(ctx, ev, a, h)
}

func (a *Adapter) parseThreadRef(ref controller.ThreadRef) (teamID, channelID, threadTS string, ok bool) {
	parts := strings.SplitN(string(ref), ":", 4)
	if len(parts) != 4 {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}

// Post sends text to the thread. Answers longer than LongAnswerThreshold are uploaded as a file.
func (a *Adapter) Post(ctx context.Context, ref controller.ThreadRef, text string) error {
	if len(text) > LongAnswerThreshold {
		return a.PostFile(ctx, ref, "answer.txt", []byte(text))
	}
	_, channelID, threadTS, ok := a.parseThreadRef(ref)
	if !ok {
		return fmt.Errorf("slack: malformed ThreadRef: %q", ref)
	}
	_, _, err := a.api.PostMessageContext(ctx, channelID,
		goslack.MsgOptionText(text, false),
		goslack.MsgOptionTS(threadTS),
	)
	return err
}

// PostLong is an exported helper so tests can call Post and observe routing.
func PostLong(ctx context.Context, a *Adapter, ref controller.ThreadRef, text string) error {
	return a.Post(ctx, ref, text)
}

// PostFile uploads content as a snippet in the thread.
func (a *Adapter) PostFile(ctx context.Context, ref controller.ThreadRef, name string, content []byte) error {
	_, channelID, threadTS, ok := a.parseThreadRef(ref)
	if !ok {
		return fmt.Errorf("slack: malformed ThreadRef: %q", ref)
	}
	_, err := a.api.UploadFileContext(ctx, goslack.UploadFileParameters{
		Filename:        name,
		Content:         string(content),
		FileSize:        len(content),
		Channels:        []string{channelID},
		ThreadTimestamp: threadTS,
	})
	return err
}

// React adds an emoji reaction to the thread's root message.
func (a *Adapter) React(ctx context.Context, ref controller.ThreadRef, emoji string) error {
	_, channelID, threadTS, ok := a.parseThreadRef(ref)
	if !ok {
		return fmt.Errorf("slack: malformed ThreadRef: %q", ref)
	}
	return a.api.AddReactionContext(ctx, emoji, goslack.ItemRef{
		Channel:   channelID,
		Timestamp: threadTS,
	})
}

// Mention returns the Slack-native mention markup.
func (a *Adapter) Mention(user string) string {
	return "<@" + user + ">"
}

// NewTestAdapter returns an Adapter with no real connections for unit tests
// that only need Mention or StripBotMention behaviour.
func NewTestAdapter(botID, teamID string) *Adapter {
	// api is nil; tests must not call Post/PostFile/React on this instance.
	return &Adapter{botID: botID, teamID: teamID}
}
