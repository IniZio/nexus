package slack

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/IniZio/nexus/internal/controller"
)

const (
	testTeamID    = "T_TEST"
	testChannel   = "C_TEST"
	testThreadTS  = "1000.0"
	testBotUserID = "U_BOT"
)

func makeMessageEvent(subType, text, threadTS, ts string) socketmode.Event {
	inner := &slackevents.MessageEvent{
		SubType:         subType,
		Text:            text,
		TimeStamp:       ts,
		ThreadTimeStamp: threadTS,
		Channel:         testChannel,
		User:            "U_USER",
	}
	outer := slackevents.EventsAPIEvent{
		TeamID: testTeamID,
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: "message",
			Data: inner,
		},
	}
	return socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: outer,
	}
}

// TestMentionReplyDispatchedOnce verifies that a thread reply @mentioning the bot
// is dispatched exactly once (via app_mention), not twice with message.channels.
func TestMentionReplyDispatchedOnce(t *testing.T) {
	var calls atomic.Int32
	h := func(_ context.Context, _ controller.Event) error {
		calls.Add(1)
		return nil
	}

	a := &Adapter{userID: testBotUserID, teamID: testTeamID}
	ctx := context.Background()

	// Simulate app_mention — this should dispatch.
	mentionInner := &slackevents.AppMentionEvent{
		Text:            "<@" + testBotUserID + "> hello",
		TimeStamp:       "1001.0",
		ThreadTimeStamp: testThreadTS,
		Channel:         testChannel,
		User:            "U_USER",
	}
	mentionOuter := slackevents.EventsAPIEvent{
		TeamID: testTeamID,
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: "app_mention",
			Data: mentionInner,
		},
	}
	mentionEv := socketmode.Event{Type: socketmode.EventTypeEventsAPI, Data: mentionOuter}
	if err := dispatchInnerEvent(ctx, mentionEv, a, h); err != nil {
		t.Fatalf("app_mention dispatch: %v", err)
	}

	// Simulate message.channels with same @mention text — should be dropped.
	msgEv := makeMessageEvent("", "<@"+testBotUserID+"> hello", testThreadTS, "1001.0")
	if err := dispatchInnerEvent(ctx, msgEv, a, h); err != nil {
		t.Fatalf("message dispatch: %v", err)
	}

	if n := calls.Load(); n != 1 {
		t.Errorf("handler called %d times, want exactly 1 (mention should not double-dispatch)", n)
	}
}

// TestFileShareSubtypeDispatches verifies that file_share thread replies yield EventReply.
func TestFileShareSubtypeDispatches(t *testing.T) {
	var got []controller.Event
	h := func(_ context.Context, ev controller.Event) error {
		got = append(got, ev)
		return nil
	}

	a := &Adapter{userID: testBotUserID, teamID: testTeamID}
	ev := makeMessageEvent("file_share", "see attached", testThreadTS, "1001.0")
	if err := dispatchInnerEvent(context.Background(), ev, a, h); err != nil {
		t.Fatalf("dispatchInnerEvent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("handler called %d times, want 1", len(got))
	}
	if got[0].Kind != controller.EventReply {
		t.Errorf("event kind = %q, want EventReply", got[0].Kind)
	}
}

// TestThreadBroadcastSubtypeDispatches verifies that thread_broadcast thread replies yield EventReply.
func TestThreadBroadcastSubtypeDispatches(t *testing.T) {
	var got []controller.Event
	h := func(_ context.Context, ev controller.Event) error {
		got = append(got, ev)
		return nil
	}

	a := &Adapter{userID: testBotUserID, teamID: testTeamID}
	ev := makeMessageEvent("thread_broadcast", "also to channel", testThreadTS, "1001.0")
	if err := dispatchInnerEvent(context.Background(), ev, a, h); err != nil {
		t.Fatalf("dispatchInnerEvent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("handler called %d times, want 1", len(got))
	}
	if got[0].Kind != controller.EventReply {
		t.Errorf("event kind = %q, want EventReply", got[0].Kind)
	}
}

// TestMessageChangedIsDropped verifies that message_changed subtype events are not dispatched.
func TestMessageChangedIsDropped(t *testing.T) {
	h := func(_ context.Context, _ controller.Event) error {
		return nil
	}
	called := false
	wrapped := func(ctx context.Context, ev controller.Event) error {
		called = true
		return h(ctx, ev)
	}

	a := &Adapter{userID: testBotUserID, teamID: testTeamID}
	ev := makeMessageEvent("message_changed", "edited text", testThreadTS, "1001.0")
	if err := dispatchInnerEvent(context.Background(), ev, a, wrapped); err != nil {
		t.Fatalf("dispatchInnerEvent: %v", err)
	}
	if called {
		t.Error("handler must not be called for message_changed subtype")
	}
}
