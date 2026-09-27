// Package slack implements controller.ChatAdapter using Slack Socket Mode.
package slack

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	goslack "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/IniZio/nexus/internal/controller"
)

const LongAnswerThreshold = 11 * 1024

// eventSource abstracts where inbound socketmode.Events come from.
type eventSource interface {
	events() <-chan socketmode.Event
	ack(req *socketmode.Request)
	run(ctx context.Context) error
}

type realSource struct{ sm *socketmode.Client }

func (r *realSource) events() <-chan socketmode.Event { return r.sm.Events }
func (r *realSource) ack(req *socketmode.Request)     { r.sm.Ack(*req) }
func (r *realSource) run(ctx context.Context) error   { return r.sm.RunContext(ctx) }

// Adapter is a controller.ChatAdapter backed by Slack Socket Mode.
type Adapter struct {
	api    *goslack.Client
	src    eventSource
	botID  string
	userID string // bot's user ID (U…) from auth.test; used to deduplicate app_mention + message events
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
		userID: authResp.UserID,
		teamID: authResp.TeamID,
	}, nil
}

func NewAdapterFromParts(api *goslack.Client, sm *socketmode.Client, botID, userID, teamID string) *Adapter {
	return &Adapter{api: api, src: &realSource{sm: sm}, botID: botID, userID: userID, teamID: teamID}
}

func newWithSource(api *goslack.Client, src eventSource, botID, userID, teamID string) *Adapter {
	return &Adapter{api: api, src: src, botID: botID, userID: userID, teamID: teamID}
}

type chanSource struct{ ch <-chan socketmode.Event }

func (c *chanSource) events() <-chan socketmode.Event { return c.ch }
func (c *chanSource) ack(_ *socketmode.Request)       {}
func (c *chanSource) run(ctx context.Context) error   { <-ctx.Done(); return nil }

// NewAdapterWithFakeSource constructs an Adapter whose inbound events come from ch.
func NewAdapterWithFakeSource(api *goslack.Client, ch <-chan socketmode.Event, botID, userID, teamID string) *Adapter {
	return &Adapter{api: api, src: &chanSource{ch: ch}, botID: botID, userID: userID, teamID: teamID}
}

var reMention = regexp.MustCompile(`<@[^>]+>`)

func (a *Adapter) TeamID() string { return a.teamID }

func StripBotMention(text string) string {
	return strings.TrimSpace(reMention.ReplaceAllString(text, ""))
}

// Run blocks until ctx is done, dispatching incoming Slack events to h.
// Handler errors are logged and do not stop the loop; only ctx cancellation
// or a closed event channel terminates Run.
func (a *Adapter) Run(ctx context.Context, h controller.Handler) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := a.src.run(runCtx); err != nil && runCtx.Err() == nil {
			slog.Error("slack socketmode run error", "err", err)
		}
	}()

	slog.Info("slack adapter starting")
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-a.src.events():
			if !ok {
				return nil
			}
			switch ev.Type {
			case socketmode.EventTypeConnecting:
				slog.Info("slack connecting")
			case socketmode.EventTypeConnected:
				slog.Info("slack connected")
			case socketmode.EventTypeConnectionError:
				slog.Warn("slack connection error", "event", ev.Type)
			case socketmode.EventTypeDisconnect:
				slog.Info("slack disconnected")
			default:
				slog.Debug("slack event received", "type", ev.Type)
			}
			if err := a.dispatch(ctx, ev, h); err != nil {
				slog.Error("slack dispatch error", "type", ev.Type, "err", err)
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
		ref := controller.NewThreadRef(cmd.TeamID, cmd.ChannelID, "")
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
	ref := controller.NewThreadRef(cmd.TeamID, cmd.ChannelID, "")
	return controller.Event{
		Kind:      controller.EventSlashCommand,
		ThreadRef: ref,
		User:      cmd.UserID,
		Text:      strings.TrimSpace(cmd.Command + " " + cmd.Text),
	}
}

func (a *Adapter) handleEventsAPIRaw(ctx context.Context, ev socketmode.Event, h controller.Handler) error {
	type innerHolder interface {
		GetInnerEvent() interface{}
		GetTeamID() string
	}
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

// PostEphemeral sends an ephemeral message visible only to user. thread_ts is
// included only when the ref carries a real message timestamp.
func (a *Adapter) PostEphemeral(ctx context.Context, ref controller.ThreadRef, user, text string) error {
	_, channelID, threadTS, ok := a.parseThreadRef(ref)
	if !ok {
		return fmt.Errorf("slack: malformed ThreadRef: %q", ref)
	}
	opts := []goslack.MsgOption{goslack.MsgOptionText(text, false)}
	if threadTS != "" {
		opts = append(opts, goslack.MsgOptionTS(threadTS))
	}
	_, err := a.api.PostEphemeralContext(ctx, channelID, user, opts...)
	return err
}

// PostLong is an exported helper so tests can call Post and observe routing.
func PostLong(ctx context.Context, a *Adapter, ref controller.ThreadRef, text string) error {
	return a.Post(ctx, ref, text)
}

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

func (a *Adapter) Mention(user string) string {
	return "<@" + user + ">"
}

// NewTestAdapter returns an Adapter with no real connections for unit tests.
func NewTestAdapter(botID, userID, teamID string) *Adapter {
	return &Adapter{botID: botID, userID: userID, teamID: teamID}
}
