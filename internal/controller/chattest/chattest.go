// Package chattest provides a goroutine-safe in-memory ChatAdapter fake
// and the RunAdapterContract suite any ChatAdapter implementation must pass.
package chattest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
)

// Driver is the inspection seam used by the contract suite.
type Driver interface {
	Inject(ctx context.Context, ev controller.Event) error
	Posts(ref controller.ThreadRef) []string
	Ephemerals(ref controller.ThreadRef, user string) []string
	Files(ref controller.ThreadRef) []string
	Reactions(ref controller.ThreadRef) []string
}

type ephemeralKey struct {
	ref  controller.ThreadRef
	user string
}

// Fake is a goroutine-safe in-memory ChatAdapter that also satisfies Driver.
type Fake struct {
	mu         sync.Mutex
	pending    []controller.Event
	posts      map[controller.ThreadRef][]string
	ephemerals map[ephemeralKey][]string
	files      map[controller.ThreadRef][]string
	reactions  map[controller.ThreadRef][]string

	injectCh chan controller.Event
	doneCh   chan struct{}
	running  bool
	done     bool
}

// New returns a fresh Fake.
func New() *Fake {
	return &Fake{
		posts:      make(map[controller.ThreadRef][]string),
		ephemerals: make(map[ephemeralKey][]string),
		files:      make(map[controller.ThreadRef][]string),
		reactions:  make(map[controller.ThreadRef][]string),
		injectCh:   make(chan controller.Event, 256),
		doneCh:     make(chan struct{}),
	}
}

// Run delivers injected events to h in injection order until ctx is cancelled.
// Returns nil when ctx is cancelled.
func (f *Fake) Run(ctx context.Context, h controller.Handler) error {
	f.mu.Lock()
	f.running = true
	for _, ev := range f.pending {
		f.injectCh <- ev
	}
	f.pending = nil
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.done = true
		f.running = false
		close(f.doneCh)
		f.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-f.injectCh:
			if err := h(ctx, ev); err != nil {
				return err
			}
		}
	}
}

// Inject enqueues an event. Buffered before Run; live-sent during Run;
// returns an error if Run has already returned.
func (f *Fake) Inject(ctx context.Context, ev controller.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return errors.New("chattest: Inject called after Run returned")
	}
	if f.running {
		select {
		case f.injectCh <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	f.pending = append(f.pending, ev)
	return nil
}

// Post records text for ref.
func (f *Fake) Post(_ context.Context, ref controller.ThreadRef, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts[ref] = append(f.posts[ref], text)
	return nil
}

// PostEphemeral records an ephemeral message for ref+user.
func (f *Fake) PostEphemeral(_ context.Context, ref controller.ThreadRef, user, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := ephemeralKey{ref: ref, user: user}
	f.ephemerals[k] = append(f.ephemerals[k], text)
	return nil
}

// PostFile records the file name for ref.
func (f *Fake) PostFile(_ context.Context, ref controller.ThreadRef, name string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[ref] = append(f.files[ref], name)
	return nil
}

// React records the emoji for ref.
func (f *Fake) React(_ context.Context, ref controller.ThreadRef, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions[ref] = append(f.reactions[ref], emoji)
	return nil
}

// Mention returns "<@user>" — the adapter-native mention markup.
func (f *Fake) Mention(user string) string {
	return "<@" + user + ">"
}

// Posts returns a copy of all texts posted to ref.
func (f *Fake) Posts(ref controller.ThreadRef) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copySlice(f.posts[ref])
}

// Ephemerals returns a copy of all ephemeral texts posted to ref for user.
func (f *Fake) Ephemerals(ref controller.ThreadRef, user string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copySlice(f.ephemerals[ephemeralKey{ref: ref, user: user}])
}

// Files returns a copy of all file names posted to ref.
func (f *Fake) Files(ref controller.ThreadRef) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copySlice(f.files[ref])
}

// Reactions returns a copy of all reactions posted to ref.
func (f *Fake) Reactions(ref controller.ThreadRef) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copySlice(f.reactions[ref])
}

func copySlice(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// RunAdapterContract runs the behavioral contract suite against any ChatAdapter+Driver pair.
// newAdapter is called once per subtest to produce a fresh instance.
func RunAdapterContract(t *testing.T, newAdapter func(t *testing.T) (controller.ChatAdapter, Driver)) {
	t.Helper()

	t.Run("inbound events delivered in order", func(t *testing.T) {
		t.Helper()
		adapter, drv := newAdapter(t)
		ref := controller.NewThreadRef("T1", "C1", "ts1")
		slashRef := controller.NewThreadRef("T1", "C1", "")
		events := []controller.Event{
			{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "hello"},
			{Kind: controller.EventReply, ThreadRef: ref, User: "U2", Text: "world"},
			{Kind: controller.EventSlashCommand, ThreadRef: slashRef, User: "U3", Text: "/cmd"},
		}
		for _, ev := range events {
			if err := drv.Inject(context.Background(), ev); err != nil {
				t.Fatalf("Inject: %v", err)
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var mu sync.Mutex
		var received []controller.Event
		runDone := make(chan error, 1)

		go func() {
			runDone <- adapter.Run(ctx, func(_ context.Context, ev controller.Event) error {
				mu.Lock()
				received = append(received, ev)
				if len(received) >= len(events) {
					cancel()
				}
				mu.Unlock()
				return nil
			})
		}()

		select {
		case err := <-runDone:
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for events")
		}

		mu.Lock()
		defer mu.Unlock()
		if len(received) != len(events) {
			t.Fatalf("got %d events, want %d", len(received), len(events))
		}
		for i, want := range events {
			got := received[i]
			if got.Kind != want.Kind || got.ThreadRef != want.ThreadRef ||
				got.User != want.User || got.Text != want.Text {
				t.Errorf("event[%d]: got %+v, want %+v", i, got, want)
			}
		}
	})

	t.Run("Run returns nil on ctx cancel", func(t *testing.T) {
		t.Helper()
		adapter, _ := newAdapter(t)
		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() {
			runDone <- adapter.Run(ctx, func(_ context.Context, _ controller.Event) error { return nil })
		}()
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Fatalf("Run returned non-nil after cancel: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return promptly after ctx cancel")
		}
	})

	t.Run("Post recorded by Posts", func(t *testing.T) {
		t.Helper()
		adapter, drv := newAdapter(t)
		ref := controller.NewThreadRef("T1", "C1", "ts1")
		if err := adapter.Post(context.Background(), ref, "hi there"); err != nil {
			t.Fatalf("Post: %v", err)
		}
		posts := drv.Posts(ref)
		if !containsStr(posts, "hi there") {
			t.Fatalf("Posts(%v) = %v, want to contain %q", ref, posts, "hi there")
		}
	})

	t.Run("posts to two threads stay separate", func(t *testing.T) {
		t.Helper()
		adapter, drv := newAdapter(t)
		ref1 := controller.NewThreadRef("T1", "C1", "ts1")
		ref2 := controller.NewThreadRef("T1", "C2", "ts2")
		if err := adapter.Post(context.Background(), ref1, "msg1"); err != nil {
			t.Fatalf("Post ref1: %v", err)
		}
		if err := adapter.Post(context.Background(), ref2, "msg2"); err != nil {
			t.Fatalf("Post ref2: %v", err)
		}
		p1 := drv.Posts(ref1)
		p2 := drv.Posts(ref2)
		if len(p1) != 1 || p1[0] != "msg1" {
			t.Errorf("ref1 posts: got %v, want [msg1]", p1)
		}
		if len(p2) != 1 || p2[0] != "msg2" {
			t.Errorf("ref2 posts: got %v, want [msg2]", p2)
		}
	})

	t.Run("PostFile records name", func(t *testing.T) {
		t.Helper()
		adapter, drv := newAdapter(t)
		ref := controller.NewThreadRef("T1", "C1", "ts1")
		if err := adapter.PostFile(context.Background(), ref, "report.txt", []byte("data")); err != nil {
			t.Fatalf("PostFile: %v", err)
		}
		files := drv.Files(ref)
		if !containsStr(files, "report.txt") {
			t.Fatalf("Files(%v) = %v, want to contain %q", ref, files, "report.txt")
		}
	})

	t.Run("React records emoji", func(t *testing.T) {
		t.Helper()
		adapter, drv := newAdapter(t)
		ref := controller.NewThreadRef("T1", "C1", "ts1")
		if err := adapter.React(context.Background(), ref, "thumbsup"); err != nil {
			t.Fatalf("React: %v", err)
		}
		reactions := drv.Reactions(ref)
		if !containsStr(reactions, "thumbsup") {
			t.Fatalf("Reactions(%v) = %v, want to contain %q", ref, reactions, "thumbsup")
		}
	})

	t.Run("Mention contains user id", func(t *testing.T) {
		t.Helper()
		adapter, _ := newAdapter(t)
		m := adapter.Mention("U123")
		if m == "" {
			t.Fatal("Mention returned empty string")
		}
		if !strings.Contains(m, "U123") {
			t.Fatalf("Mention(%q) = %q does not contain user id", "U123", m)
		}
	})

	t.Run("PostEphemeral recorded by Ephemerals", func(t *testing.T) {
		t.Helper()
		adapter, drv := newAdapter(t)
		ref := controller.NewThreadRef("T1", "C1", "ts1")
		if err := adapter.PostEphemeral(context.Background(), ref, "U42", "secret code"); err != nil {
			t.Fatalf("PostEphemeral: %v", err)
		}
		msgs := drv.Ephemerals(ref, "U42")
		if !containsStr(msgs, "secret code") {
			t.Fatalf("Ephemerals(%v, U42) = %v, want to contain %q", ref, msgs, "secret code")
		}
	})
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
