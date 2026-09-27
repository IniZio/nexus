package controller

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- in-test fakes ---

type fakeStore struct {
	mu    sync.Mutex
	tasks map[ThreadRef]Task
	clock func() time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tasks: make(map[ThreadRef]Task),
		clock: time.Now,
	}
}

func (s *fakeStore) Get(_ context.Context, ref ThreadRef) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[ref]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t, nil
}

func (s *fakeStore) Upsert(_ context.Context, t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ThreadRef] = t
	return nil
}

func (s *fakeStore) Transition(_ context.Context, ref ThreadRef, from, to Status, seq uint64) error {
	if !ValidTransition(from, to) {
		return ErrInvalidTransition
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[ref]
	if !ok {
		return ErrNotFound
	}
	if t.Status != from {
		return ErrConflict
	}
	t.Status = to
	t.StateChangeSeq = seq
	s.tasks[ref] = t
	return nil
}

func (s *fakeStore) ListIdle(_ context.Context, before time.Time) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Task
	for _, t := range s.tasks {
		if (t.Status == StatusIdle || t.Status == StatusWaitingOnUser || t.Status == StatusPaused) &&
			t.LastActivityAt.Before(before) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *fakeStore) TouchActivity(_ context.Context, ref ThreadRef, author string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[ref]
	if !ok {
		return ErrNotFound
	}
	t.LastAuthor = author
	t.LastActivityAt = s.clock()
	s.tasks[ref] = t
	return nil
}

// fakeFlows records calls and delegates to optional function hooks.
type fakeFlows struct {
	mu           sync.Mutex
	onMentionFn  func(context.Context, Task, Event) error
	onReplyFn    func(context.Context, Task, Event) error
	onTickFn     func(context.Context, Task, time.Time) error
	mentionCalls []Event
	replyCalls   []Event
	tickCalls    []ThreadRef
}

func (f *fakeFlows) OnMention(ctx context.Context, t Task, ev Event) error {
	f.mu.Lock()
	f.mentionCalls = append(f.mentionCalls, ev)
	fn := f.onMentionFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, t, ev)
	}
	return nil
}

func (f *fakeFlows) OnReply(ctx context.Context, t Task, ev Event) error {
	f.mu.Lock()
	f.replyCalls = append(f.replyCalls, ev)
	fn := f.onReplyFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, t, ev)
	}
	return nil
}

func (f *fakeFlows) OnTick(ctx context.Context, t Task, now time.Time) error {
	f.mu.Lock()
	f.tickCalls = append(f.tickCalls, t.ThreadRef)
	fn := f.onTickFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, t, now)
	}
	return nil
}

// --- tests ---

// TestRouterSerializesPerThread verifies that flow calls for thread A never
// overlap (max 1 in-flight) and that thread B can complete while thread A is
// blocked inside a flow call.
func TestRouterSerializesPerThread(t *testing.T) {
	refA := NewThreadRef("T", "C", "A")
	refB := NewThreadRef("T", "C", "B")

	taskA := Task{ThreadRef: refA, Status: StatusWaitingOnUser, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now()}
	taskB := Task{ThreadRef: refB, Status: StatusWaitingOnUser, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now()}

	store := newFakeStore()
	_ = store.Upsert(context.Background(), taskA)
	_ = store.Upsert(context.Background(), taskB)

	const N = 5

	var aInFlight atomic.Int32
	var aMaxInFlight atomic.Int32

	// firstABlocked is closed when the first thread-A flow call is entered.
	firstABlocked := make(chan struct{})
	firstAOnce := sync.Once{}
	// unblockA is closed to release all blocked thread-A flow calls.
	unblockA := make(chan struct{})
	// bDone is closed when thread B's flow call completes.
	bDone := make(chan struct{})
	bOnce := sync.Once{}

	flows := &fakeFlows{}
	flows.onReplyFn = func(ctx context.Context, tk Task, ev Event) error {
		if tk.ThreadRef == refA {
			curr := aInFlight.Add(1)
			for {
				old := aMaxInFlight.Load()
				if curr <= old || aMaxInFlight.CompareAndSwap(old, curr) {
					break
				}
			}
			// Block the first call until thread B is done.
			firstAOnce.Do(func() { close(firstABlocked) })
			<-unblockA
			aInFlight.Add(-1)
		} else if tk.ThreadRef == refB {
			bOnce.Do(func() { close(bDone) })
		}
		return nil
	}

	r := NewRouter(store, flows)

	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if err := r.Handle(ctx, Event{Kind: EventReply, ThreadRef: refA, User: "u"}); err != nil {
				t.Errorf("Handle(A): %v", err)
			}
		}()
	}

	select {
	case <-firstABlocked:
	case <-time.After(5 * time.Second):
		t.Fatal("thread A flow never started")
	}

	if err := r.Handle(ctx, Event{Kind: EventReply, ThreadRef: refB, User: "u"}); err != nil {
		t.Fatalf("Handle(B): %v", err)
	}
	select {
	case <-bDone:
	case <-time.After(5 * time.Second):
		t.Fatal("thread B did not complete while thread A was blocked")
	}

	close(unblockA)
	wg.Wait()
	r.Close()

	if got := aMaxInFlight.Load(); got > 1 {
		t.Errorf("thread A had %d concurrent flow calls, want ≤1", got)
	}
	flows.mu.Lock()
	var aCount int
	for _, ev := range flows.replyCalls {
		if ev.ThreadRef == refA {
			aCount++
		}
	}
	flows.mu.Unlock()
	if aCount != N {
		t.Errorf("thread A reply count = %d, want %d", aCount, N)
	}
}

// TestRouterNewMentionCreatesTask checks that a Mention with no existing task
// creates a StatusStarting task and calls OnMention.
func TestRouterNewMentionCreatesTask(t *testing.T) {
	ref := NewThreadRef("T", "C", "ts1")
	store := newFakeStore()
	flows := &fakeFlows{}

	r := NewRouter(store, flows)
	ctx := context.Background()

	if err := r.Handle(ctx, Event{Kind: EventMention, ThreadRef: ref, User: "alice"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	r.Close()

	if len(flows.mentionCalls) != 1 {
		t.Fatalf("OnMention called %d times, want 1", len(flows.mentionCalls))
	}
	task, err := store.Get(ctx, ref)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if task.Status != StatusStarting {
		t.Errorf("task.Status = %q, want starting", task.Status)
	}
	if task.Owner != "alice" {
		t.Errorf("task.Owner = %q, want alice", task.Owner)
	}
}

// TestRouterReplyToUnknownDropped verifies that a Reply with no existing task is dropped.
func TestRouterReplyToUnknownDropped(t *testing.T) {
	ref := NewThreadRef("T", "C", "ts2")
	store := newFakeStore()
	flows := &fakeFlows{}

	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventReply, ThreadRef: ref, User: "u"})
	r.Close()

	if len(flows.replyCalls) != 0 || len(flows.mentionCalls) != 0 {
		t.Error("expected no flow calls for reply to unknown thread")
	}
}

// TestRouterReplyOnWaitingOnUser verifies routing for a live thread.
func TestRouterReplyOnWaitingOnUser(t *testing.T) {
	ref := NewThreadRef("T", "C", "ts3")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusWaitingOnUser, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now(),
	})
	flows := &fakeFlows{}

	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventReply, ThreadRef: ref, User: "bob"})
	r.Close()

	if len(flows.replyCalls) != 1 {
		t.Fatalf("OnReply called %d times, want 1", len(flows.replyCalls))
	}
}

// TestRouterLastAuthorUpdated confirms TouchActivity is called and last_author is updated.
func TestRouterLastAuthorUpdated(t *testing.T) {
	ref := NewThreadRef("T", "C", "ts4")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusWaitingOnUser, Owner: "alice", LastAuthor: "alice",
		LastActivityAt: time.Now(),
	})

	var seenLastAuthor string
	flows := &fakeFlows{}
	flows.onReplyFn = func(_ context.Context, tk Task, _ Event) error {
		seenLastAuthor = tk.LastAuthor
		return nil
	}

	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventReply, ThreadRef: ref, User: "bob"})
	r.Close()

	if seenLastAuthor != "bob" {
		t.Errorf("last_author = %q, want bob", seenLastAuthor)
	}
}

// TestRouterSlashCommandBypassesQueue confirms slash commands go directly to the handler.
func TestRouterSlashCommandBypassesQueue(t *testing.T) {
	ref := NewThreadRef("T", "C", "ts5")
	store := newFakeStore()
	flows := &fakeFlows{}

	var called bool
	r := NewRouter(store, flows, WithCommandHandler(func(ctx context.Context, ev Event) error {
		called = true
		return nil
	}))

	_ = r.Handle(context.Background(), Event{Kind: EventSlashCommand, ThreadRef: ref, User: "u"})
	r.Close()

	if !called {
		t.Error("command handler was not called")
	}
	if len(flows.mentionCalls) != 0 || len(flows.replyCalls) != 0 {
		t.Error("slash command leaked into flow calls")
	}
}

// TestRouterSlashCommandNilHandler ensures nil handler is a no-op.
func TestRouterSlashCommandNilHandler(t *testing.T) {
	store := newFakeStore()
	r := NewRouter(store, &fakeFlows{})
	err := r.Handle(context.Background(), Event{Kind: EventSlashCommand, User: "u"})
	r.Close()
	if err != nil {
		t.Errorf("nil command handler returned error: %v", err)
	}
}

// TestRouterTickEnqueuesOnTick verifies Tick queues OnTick for each idle task.
func TestRouterTickEnqueuesOnTick(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	past := time.Now().Add(-10 * time.Minute)

	refs := []ThreadRef{
		NewThreadRef("T", "C", "idle1"),
		NewThreadRef("T", "C", "idle2"),
	}
	for _, ref := range refs {
		_ = store.Upsert(ctx, Task{
			ThreadRef: ref, Status: StatusIdle, Owner: "u", LastAuthor: "u",
			LastActivityAt: past,
		})
	}

	flows := &fakeFlows{}
	r := NewRouter(store, flows)
	if err := r.Tick(ctx, time.Now()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	r.Close()

	if len(flows.tickCalls) != 2 {
		t.Errorf("OnTick called %d times, want 2", len(flows.tickCalls))
	}
}

// TestRouterCloseRejectsNew verifies Handle and Tick return ErrRouterClosed after Close.
func TestRouterCloseRejectsNew(t *testing.T) {
	store := newFakeStore()
	r := NewRouter(store, &fakeFlows{})
	r.Close()

	err := r.Handle(context.Background(), Event{Kind: EventMention, ThreadRef: NewThreadRef("T", "C", "x")})
	if err != ErrRouterClosed {
		t.Errorf("Handle after Close = %v, want ErrRouterClosed", err)
	}
	err = r.Tick(context.Background(), time.Now())
	if err != ErrRouterClosed {
		t.Errorf("Tick after Close = %v, want ErrRouterClosed", err)
	}
}

// TestRouterClosesDrains ensures Close waits for in-flight work.
func TestRouterCloseDrains(t *testing.T) {
	ref := NewThreadRef("T", "C", "drain")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusWaitingOnUser, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now(),
	})

	started := make(chan struct{})
	proceed := make(chan struct{})
	var done atomic.Bool

	flows := &fakeFlows{}
	flows.onReplyFn = func(_ context.Context, _ Task, _ Event) error {
		close(started)
		<-proceed
		done.Store(true)
		return nil
	}

	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventReply, ThreadRef: ref, User: "u"})
	<-started

	// Close must not return until the in-flight handler finishes.
	closeDone := make(chan struct{})
	go func() { r.Close(); close(closeDone) }()

	select {
	case <-closeDone:
		t.Fatal("Close returned before in-flight handler finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(proceed)
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after handler finished")
	}
	if !done.Load() {
		t.Error("handler did not complete before Close returned")
	}
}

// TestRouterMentionOnClosedTaskDropped checks closed+Mention → drop (terminal).
func TestRouterMentionOnClosedTaskDropped(t *testing.T) {
	ref := NewThreadRef("T", "C", "closed1")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusClosed, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now(),
	})

	flows := &fakeFlows{}
	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventMention, ThreadRef: ref, User: "u"})
	r.Close()

	if len(flows.mentionCalls) != 0 || len(flows.replyCalls) != 0 {
		t.Error("expected no flow calls for mention on closed thread")
	}
}

// TestRouterReplyOnClosedTaskDropped checks closed+Reply → drop.
func TestRouterReplyOnClosedTaskDropped(t *testing.T) {
	ref := NewThreadRef("T", "C", "closed2")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusClosed, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now(),
	})

	flows := &fakeFlows{}
	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventReply, ThreadRef: ref, User: "u"})
	r.Close()

	if len(flows.replyCalls) != 0 || len(flows.mentionCalls) != 0 {
		t.Error("expected no flow calls for reply on closed thread")
	}
}

// TestRouterPreservesPerThreadOrder confirms that events on a single thread
// are delivered to the flow in submission order.
func TestRouterPreservesPerThreadOrder(t *testing.T) {
	ref := NewThreadRef("T", "C", "order1")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusWaitingOnUser, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now(),
	})

	const M = 20
	var seen []string

	flows := &fakeFlows{}
	flows.onReplyFn = func(_ context.Context, _ Task, ev Event) error {
		seen = append(seen, ev.Text)
		return nil
	}

	r := NewRouter(store, flows)
	ctx := context.Background()
	for i := 0; i < M; i++ {
		if err := r.Handle(ctx, Event{Kind: EventReply, ThreadRef: ref, User: "u", Text: strconv.Itoa(i)}); err != nil {
			t.Fatalf("Handle(%d): %v", i, err)
		}
	}
	r.Close()

	if len(seen) != M {
		t.Fatalf("got %d events, want %d", len(seen), M)
	}
	for i, s := range seen {
		if want := strconv.Itoa(i); s != want {
			t.Errorf("event[%d] = %q, want %q", i, s, want)
		}
	}
}

// TestRouterReplyOnStoppedTaskCallsOnReply checks stopped+Reply → OnReply (revive).
func TestRouterReplyOnStoppedTaskCallsOnReply(t *testing.T) {
	ref := NewThreadRef("T", "C", "stopped1")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusStopped, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now(),
	})

	flows := &fakeFlows{}
	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventReply, ThreadRef: ref, User: "u"})
	r.Close()

	if len(flows.replyCalls) != 1 || len(flows.mentionCalls) != 0 {
		t.Errorf("stopped+Reply: OnReply=%d OnMention=%d, want 1/0",
			len(flows.replyCalls), len(flows.mentionCalls))
	}
}

// TestRouterMentionOnIdleTaskCallsOnReply checks idle+Mention → OnReply (not OnMention).
func TestRouterMentionOnIdleTaskCallsOnReply(t *testing.T) {
	ref := NewThreadRef("T", "C", "idle-mention1")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusIdle, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now(),
	})

	flows := &fakeFlows{}
	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventMention, ThreadRef: ref, User: "u"})
	r.Close()

	if len(flows.replyCalls) != 1 || len(flows.mentionCalls) != 0 {
		t.Errorf("idle+Mention: OnReply=%d OnMention=%d, want 1/0",
			len(flows.replyCalls), len(flows.mentionCalls))
	}
}

// TestRouterMentionOnFailedTaskCallsOnMention checks failed+Mention → OnMention (reopen).
func TestRouterMentionOnFailedTaskCallsOnMention(t *testing.T) {
	ref := NewThreadRef("T", "C", "failed-mention1")
	store := newFakeStore()
	_ = store.Upsert(context.Background(), Task{
		ThreadRef: ref, Status: StatusFailed, Owner: "u", LastAuthor: "u",
		LastActivityAt: time.Now(),
	})

	flows := &fakeFlows{}
	r := NewRouter(store, flows)
	_ = r.Handle(context.Background(), Event{Kind: EventMention, ThreadRef: ref, User: "u"})
	r.Close()

	if len(flows.mentionCalls) != 1 || len(flows.replyCalls) != 0 {
		t.Errorf("failed+Mention: OnMention=%d OnReply=%d, want 1/0",
			len(flows.mentionCalls), len(flows.replyCalls))
	}
}

// TestRouterMentionOnLiveThreadIsReply verifies that a Mention into a live
// (starting/working/idle/waiting_on_user/paused/stopped) thread is treated as OnReply.
func TestRouterMentionOnLiveThreadIsReply(t *testing.T) {
	liveStatuses := []Status{
		StatusStarting, StatusWorking, StatusIdle, StatusWaitingOnUser, StatusPaused, StatusStopped,
	}
	for _, st := range liveStatuses {
		st := st
		t.Run(string(st), func(t *testing.T) {
			ref := NewThreadRef("T", "C", string(st))
			store := newFakeStore()
			_ = store.Upsert(context.Background(), Task{
				ThreadRef: ref, Status: st, Owner: "u", LastAuthor: "u",
				LastActivityAt: time.Now(),
			})

			flows := &fakeFlows{}
			r := NewRouter(store, flows)
			_ = r.Handle(context.Background(), Event{Kind: EventMention, ThreadRef: ref, User: "u"})
			r.Close()

			if len(flows.replyCalls) != 1 || len(flows.mentionCalls) != 0 {
				t.Errorf("status %s: OnReply=%d OnMention=%d, want 1/0",
					st, len(flows.replyCalls), len(flows.mentionCalls))
			}
		})
	}
}
