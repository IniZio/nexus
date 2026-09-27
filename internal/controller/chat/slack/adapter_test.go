package slack_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	goslack "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/IniZio/nexus/internal/controller"
	slackadapter "github.com/IniZio/nexus/internal/controller/chat/slack"
	"github.com/IniZio/nexus/internal/controller/chattest"
)

// fakeSource is an in-memory eventSource for testing that bypasses WebSocket.
type fakeSource struct {
	ch chan socketmode.Event
}

func newFakeSource() *fakeSource {
	return &fakeSource{ch: make(chan socketmode.Event, 64)}
}

// apiRecorder keys by "channel:threadTS" so teamID mismatches don't matter.
type apiRecorder struct {
	mu         sync.Mutex
	posts      map[string][]string
	ephemerals map[string][]string // key: "channel:user"
	files      map[string][]string
	reactions  map[string][]string
}

func newAPIRecorder() *apiRecorder {
	return &apiRecorder{
		posts:      make(map[string][]string),
		ephemerals: make(map[string][]string),
		files:      make(map[string][]string),
		reactions:  make(map[string][]string),
	}
}

func ephKey(channel, user string) string { return channel + ":user:" + user }

func chanKey(channel, threadTS string) string { return channel + ":" + threadTS }

func refKey(ref controller.ThreadRef) string {
	parts := strings.SplitN(string(ref), ":", 4)
	if len(parts) != 4 {
		return string(ref)
	}
	return parts[2] + ":" + parts[3]
}

// slackDriver implements chattest.Driver on top of fakeSource + httptest server.
type slackDriver struct {
	src      *fakeSource
	teamID   string
	recorder *apiRecorder
}

func (d *slackDriver) Inject(ctx context.Context, ev controller.Event) error {
	// Extract teamID from the ThreadRef itself, not from d.teamID,
	// so the contract suite's arbitrary refs (slack:T1:…) are preserved.
	parts := strings.SplitN(string(ev.ThreadRef), ":", 4)
	if len(parts) != 4 {
		return nil
	}
	teamID := parts[1]
	channelID := parts[2]
	threadTS := parts[3]

	var smEv socketmode.Event

	switch ev.Kind {
	case controller.EventMention:
		inner := &slackevents.AppMentionEvent{
			Type:            "app_mention",
			User:            ev.User,
			Text:            ev.Text,
			TimeStamp:       threadTS,
			ThreadTimeStamp: threadTS,
			Channel:         channelID,
		}
		outer := slackevents.EventsAPIEvent{
			TeamID:     teamID,
			InnerEvent: slackevents.EventsAPIInnerEvent{Type: "app_mention", Data: inner},
		}
		smEv = socketmode.Event{Type: socketmode.EventTypeEventsAPI, Data: outer}

	case controller.EventReply:
		inner := &slackevents.MessageEvent{
			Type:            "message",
			User:            ev.User,
			Text:            ev.Text,
			TimeStamp:       threadTS + ".reply",
			ThreadTimeStamp: threadTS,
			Channel:         channelID,
		}
		outer := slackevents.EventsAPIEvent{
			TeamID:     teamID,
			InnerEvent: slackevents.EventsAPIInnerEvent{Type: "message", Data: inner},
		}
		smEv = socketmode.Event{Type: socketmode.EventTypeEventsAPI, Data: outer}

	case controller.EventSlashCommand:
		cmd := goslack.SlashCommand{
			TeamID:    teamID,
			ChannelID: channelID,
			UserID:    ev.User,
			Command:   ev.Text,
			TriggerID: "trig_" + threadTS, // adapter ignores TriggerID; ts comes from ref
		}
		smEv = socketmode.Event{Type: socketmode.EventTypeSlashCommand, Data: cmd}
	}

	select {
	case d.src.ch <- smEv:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *slackDriver) Posts(ref controller.ThreadRef) []string {
	d.recorder.mu.Lock()
	defer d.recorder.mu.Unlock()
	return copySlice(d.recorder.posts[refKey(ref)])
}

func (d *slackDriver) Files(ref controller.ThreadRef) []string {
	d.recorder.mu.Lock()
	defer d.recorder.mu.Unlock()
	return copySlice(d.recorder.files[refKey(ref)])
}

func (d *slackDriver) Ephemerals(ref controller.ThreadRef, user string) []string {
	parts := strings.SplitN(string(ref), ":", 4)
	if len(parts) != 4 {
		return nil
	}
	channelID := parts[2]
	d.recorder.mu.Lock()
	defer d.recorder.mu.Unlock()
	return copySlice(d.recorder.ephemerals[ephKey(channelID, user)])
}

func (d *slackDriver) Reactions(ref controller.ThreadRef) []string {
	d.recorder.mu.Lock()
	defer d.recorder.mu.Unlock()
	return copySlice(d.recorder.reactions[refKey(ref)])
}

func copySlice(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// buildTestAdapter creates an Adapter backed by fakeSource + httptest server.
func buildTestAdapter(t *testing.T) (*slackadapter.Adapter, *slackDriver) {
	t.Helper()

	const teamID = "T_TEAM"
	const botID = "U_BOT"

	rec := newAPIRecorder()
	src := newFakeSource()

	var srvBaseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth.test":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true, "bot_id": botID, "team_id": teamID,
				"user_id": botID, "team": "test", "url": "https://example.slack.com/",
			})
		case "/api/chat.postMessage":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			channel := r.PostForm.Get("channel")
			text := r.PostForm.Get("text")
			threadTS := r.PostForm.Get("thread_ts")
			key := chanKey(channel, threadTS)
			rec.mu.Lock()
			rec.posts[key] = append(rec.posts[key], text)
			rec.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true, "ts": "2000.0", "channel": channel,
			})
		case "/api/chat.postEphemeral":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			channel := r.PostForm.Get("channel")
			user := r.PostForm.Get("user")
			text := r.PostForm.Get("text")
			key := ephKey(channel, user)
			rec.mu.Lock()
			rec.ephemerals[key] = append(rec.ephemerals[key], text)
			rec.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "message_ts": "2000.0"})
		case "/api/reactions.add":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			channel := r.PostForm.Get("channel")
			ts := r.PostForm.Get("timestamp")
			name := r.PostForm.Get("name")
			key := chanKey(channel, ts)
			rec.mu.Lock()
			rec.reactions[key] = append(rec.reactions[key], name)
			rec.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
		case "/api/files.getUploadURLExternal":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":         true,
				"upload_url": srvBaseURL + "/upload",
				"file_id":    "F_123",
			})
		case "/upload":
			w.WriteHeader(200)
		case "/api/files.completeUploadExternal":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			channels := r.PostForm.Get("channels")
			threadTS := r.PostForm.Get("thread_ts")
			if channels != "" {
				ch := strings.SplitN(channels, ",", 2)[0]
				key := chanKey(ch, threadTS)
				rec.mu.Lock()
				rec.files[key] = append(rec.files[key], "report.txt")
				rec.mu.Unlock()
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true, "files": []map[string]interface{}{{"id": "F_123"}},
			})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
		}
	}))
	srvBaseURL = srv.URL
	t.Cleanup(srv.Close)

	api := goslack.New("xoxb-fake", goslack.OptionAPIURL(srv.URL+"/api/"))
	adapter := slackadapter.NewAdapterWithFakeSource(api, src.ch, botID, teamID)
	drv := &slackDriver{src: src, teamID: teamID, recorder: rec}
	return adapter, drv
}

// TestSlackAdapterContract runs the shared adapter contract against the real Slack adapter.
func TestSlackAdapterContract(t *testing.T) {
	chattest.RunAdapterContract(t, func(t *testing.T) (controller.ChatAdapter, chattest.Driver) {
		return buildTestAdapter(t)
	})
}

// TestSlackMentionStripsBotAndBuildsThreadRef verifies mention stripping and ThreadRef.
func TestSlackMentionStripsBotAndBuildsThreadRef(t *testing.T) {
	stripped := slackadapter.StripBotMention("<@U_BOT> do the thing")
	if stripped != "do the thing" {
		t.Fatalf("StripBotMention = %q, want %q", stripped, "do the thing")
	}

	stripped2 := slackadapter.StripBotMention("  <@U_BOT>   hello world  ")
	if stripped2 != "hello world" {
		t.Fatalf("StripBotMention(leading) = %q, want %q", stripped2, "hello world")
	}

	ref := controller.NewThreadRef("T_TEAM", "C_CHAN", "1000.0")
	if !strings.HasPrefix(string(ref), "slack:T_TEAM:C_CHAN:") {
		t.Fatalf("ThreadRef = %q, unexpected format", ref)
	}
	if ref.Channel() != "C_CHAN" {
		t.Fatalf("Channel() = %q, want C_CHAN", ref.Channel())
	}

	adapter := slackadapter.NewTestAdapter("U_BOT", "T_TEAM")
	if mention := adapter.Mention("U123"); !strings.Contains(mention, "U123") {
		t.Fatalf("Mention(U123) = %q, does not contain user id", mention)
	}
}

// TestSlackLongAnswerUploadsFile verifies Post routes long answers to PostFile.
func TestSlackLongAnswerUploadsFile(t *testing.T) {
	var mu sync.Mutex
	var postCalled bool
	var uploadStarted bool

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth.test":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true, "bot_id": "U_BOT", "team_id": "T_TEAM",
				"user_id": "U_BOT", "team": "test", "url": "https://example.slack.com/",
			})
		case "/api/chat.postMessage":
			mu.Lock()
			postCalled = true
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "ts": "1000.0", "channel": "C_CHAN"})
		case "/api/files.getUploadURLExternal":
			mu.Lock()
			uploadStarted = true
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true, "upload_url": srvURL + "/upload", "file_id": "F_123",
			})
		case "/upload":
			w.WriteHeader(200)
		case "/api/files.completeUploadExternal":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true, "files": []map[string]interface{}{{"id": "F_123"}},
			})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
		}
	}))
	srvURL = srv.URL
	defer srv.Close()

	api := goslack.New("xoxb-fake", goslack.OptionAPIURL(srv.URL+"/api/"))
	src := newFakeSource()
	adapter := slackadapter.NewAdapterWithFakeSource(api, src.ch, "U_BOT", "T_TEAM")

	ref := controller.NewThreadRef("T_TEAM", "C_CHAN", "1000.0")
	longText := strings.Repeat("x", slackadapter.LongAnswerThreshold+1)

	if err := slackadapter.PostLong(context.Background(), adapter, ref, longText); err != nil {
		t.Fatalf("PostLong: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if postCalled {
		t.Error("chat.postMessage called for long answer; expected file upload instead")
	}
	if !uploadStarted {
		t.Error("file upload not started for long answer")
	}
}

// TestSlackRunContinuesAfterHandlerError verifies that a handler error does not
// kill the Run loop — subsequent events are still dispatched.
func TestSlackRunContinuesAfterHandlerError(t *testing.T) {
	adapter, drv := buildTestAdapter(t)

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	var mu sync.Mutex
	done := make(chan struct{})

	h := func(ctx context.Context, ev controller.Event) error {
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()
		if n == 1 {
			return errors.New("transient handler error")
		}
		// Second call succeeds; signal done.
		close(done)
		return nil
	}

	runDone := make(chan error, 1)
	go func() { runDone <- adapter.Run(ctx, h) }()

	ref := controller.NewThreadRef("T_TEAM", "C_CHAN", "1000.0")

	// First event — handler returns error; Run must not terminate.
	if err := drv.Inject(ctx, controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U1", Text: "/link github"}); err != nil {
		t.Fatalf("Inject 1: %v", err)
	}
	// Second event — should still be dispatched.
	if err := drv.Inject(ctx, controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U1", Text: "/link github"}); err != nil {
		t.Fatalf("Inject 2: %v", err)
	}

	select {
	case <-done:
	case <-runDone:
		t.Fatal("Run returned before second event was handled")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second event dispatch")
	}

	cancel()
	<-runDone

	mu.Lock()
	n := callCount
	mu.Unlock()
	if n < 2 {
		t.Fatalf("handler called %d times; want ≥2", n)
	}
}

// TestSlackSlashCommandRoutes verifies slash commands map to EventSlashCommand.
func TestSlackSlashCommandRoutes(t *testing.T) {
	cmd := goslack.SlashCommand{
		TeamID:    "T_TEAM",
		ChannelID: "C_CHAN",
		UserID:    "U_USER",
		Command:   "/link",
		Text:      "github",
		TriggerID: "TRIG_123",
	}

	ev := slackadapter.SlashCommandToEvent(cmd)
	if ev.Kind != controller.EventSlashCommand {
		t.Fatalf("Kind = %v, want EventSlashCommand", ev.Kind)
	}
	if ev.User != "U_USER" {
		t.Fatalf("User = %q, want U_USER", ev.User)
	}
	if !strings.HasPrefix(ev.Text, "/link") {
		t.Fatalf("Text = %q, expected /link prefix", ev.Text)
	}
	if ev.ThreadRef.Channel() != "C_CHAN" {
		t.Fatalf("Channel() = %q, want C_CHAN", ev.ThreadRef.Channel())
	}
	if ev.ThreadRef.TS() != "" {
		t.Fatalf("TS() = %q, want empty (slash commands have no real ts)", ev.ThreadRef.TS())
	}
}
