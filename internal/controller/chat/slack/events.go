package slack

import (
	"context"
	"strings"

	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/IniZio/nexus/internal/controller"
)

// dispatchInnerEvent extracts the inner event from a socketmode EventsAPI envelope
// and maps it to a controller.Event dispatched to h.
func dispatchInnerEvent(ctx context.Context, ev socketmode.Event, a *Adapter, h controller.Handler) error {
	outer, ok := ev.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return nil
	}

	switch inner := outer.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if inner.BotID != "" {
			return nil
		}
		ts := inner.TimeStamp
		if inner.ThreadTimeStamp != "" {
			ts = inner.ThreadTimeStamp
		}
		ref := controller.NewThreadRef(outer.TeamID, inner.Channel, ts)
		return h(ctx, controller.Event{
			Kind:      controller.EventMention,
			ThreadRef: ref,
			User:      inner.User,
			Text:      StripBotMention(inner.Text),
		})

	case *slackevents.MessageEvent:
		if inner.BotID != "" {
			return nil
		}
		// Drop bot-mention replies: app_mention handles those to avoid double dispatch.
		if a.userID != "" && strings.Contains(inner.Text, "<@"+a.userID+">") {
			return nil
		}
		st := inner.SubType
		if st != "" && st != "file_share" && st != "thread_broadcast" {
			return nil
		}
		// Thread replies only: thread_ts set AND differs from ts.
		if inner.ThreadTimeStamp == "" || inner.ThreadTimeStamp == inner.TimeStamp {
			return nil
		}
		ref := controller.NewThreadRef(outer.TeamID, inner.Channel, inner.ThreadTimeStamp)
		return h(ctx, controller.Event{
			Kind:      controller.EventReply,
			ThreadRef: ref,
			User:      inner.User,
			Text:      inner.Text,
		})
	}
	return nil
}
