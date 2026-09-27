package controller_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/backendtest"
	"github.com/IniZio/nexus/internal/controller/chattest"
	"github.com/IniZio/nexus/internal/controller/storetest"
	"github.com/IniZio/nexus/internal/herdragent"
)

func newBlockedDeps(t *testing.T) (controller.Deps, *chattest.Fake, *storetest.Fake, *backendtest.Fake) {
	t.Helper()
	ch := chattest.New()
	st := storetest.New()
	be := backendtest.New()
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: &fakeLc{},
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "myproject"},
	}
	return d, ch, st, be
}

func TestBlockedPostsQuestionTaggingOwnerAndLastAuthor(t *testing.T) {
	ctx := context.Background()
	d, ch, st, be := newBlockedDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts1")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	be.Script(agRef,
		herdragent.State{Status: herdragent.StatusBlocked, Settled: true, Question: "what color?"},
	)

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "O1",
		LastAuthor:     "A2",
		Status:         controller.StatusStarting,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "O1", Text: "go"}
	_ = c.OnMention(ctx, task, ev)

	posts := ch.Posts(ref)
	if len(posts) == 0 {
		t.Fatal("no posts")
	}
	msg := strings.Join(posts, " ")
	if !strings.Contains(msg, "what color?") {
		t.Errorf("question not in posts: %v", posts)
	}
	if !strings.Contains(msg, "O1") {
		t.Errorf("owner not mentioned: %v", posts)
	}
	if !strings.Contains(msg, "A2") {
		t.Errorf("last author not mentioned: %v", posts)
	}

	got, _ := st.Get(ctx, ref)
	if got.Status != controller.StatusWaitingOnUser {
		t.Fatalf("status=%v; want waiting_on_user", got.Status)
	}
}

func TestBlockedDigitReplySentAsMenuKey(t *testing.T) {
	ctx := context.Background()
	d, _, st, be := newBlockedDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts2")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "3"}
	if err := c.OnReply(ctx, task, ev); err != nil {
		t.Fatalf("OnReply: %v", err)
	}

	answers := be.Answered()
	if len(answers) == 0 {
		t.Fatal("no Answer call recorded")
	}
	if answers[0].Key != "3" || answers[0].Text != "" {
		t.Fatalf("answer=%+v; want Key=\"3\" (digit forwarded as-is to approval dialog select)", answers[0])
	}
}

func TestBlockedReplySkipsStaleBlockedState(t *testing.T) {
	ctx := context.Background()
	d, ch, st, be := newBlockedDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-stale")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Queue: one stale blocked (arrives right after Answer), then done.
	be.Script(agRef,
		herdragent.State{Status: herdragent.StatusBlocked, Settled: true, Question: "stale?"},
		herdragent.State{Status: herdragent.StatusDone, Settled: true},
	)

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "yes"}
	if err := c.OnReply(ctx, task, ev); err != nil {
		t.Fatalf("OnReply: %v", err)
	}

	// The stale blocked state must not cause a second Post (question re-ask).
	posts := ch.Posts(ref)
	for _, p := range posts {
		if strings.Contains(p, "stale?") {
			t.Errorf("stale blocked question re-posted after Answer: %v", posts)
		}
	}

	// Final store status must not be waiting_on_user.
	got, _ := st.Get(ctx, ref)
	if got.Status == controller.StatusWaitingOnUser {
		t.Errorf("task still waiting_on_user after reply; stale blocked was not skipped")
	}
}

func TestBlockedTextReplySentAsText(t *testing.T) {
	ctx := context.Background()
	d, _, st, be := newBlockedDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts3")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "please use main branch"}
	if err := c.OnReply(ctx, task, ev); err != nil {
		t.Fatalf("OnReply: %v", err)
	}

	answers := be.Answered()
	if len(answers) == 0 {
		t.Fatal("no Answer call recorded")
	}
	if answers[0].Text != "please use main branch" || answers[0].Key != "" {
		t.Fatalf("answer=%+v; want Text='please use main branch'", answers[0])
	}
}

// TestIdleReplyRepromptsAndPostsAnswer: reply in StatusIdle re-prompts the agent
// and the answer is eventually posted back to the thread.
func TestIdleReplyRepromptsAndPostsAnswer(t *testing.T) {
	ctx := context.Background()
	d, ch, st, be := newBlockedDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-idle-reply")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusIdle,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "continue with plan B"}
	if err := c.OnReply(ctx, task, ev); err != nil {
		t.Fatalf("OnReply: %v", err)
	}

	// Backend default behavior: Prompt with no prior script sets readAns = "echo: <text>".
	// runObserveLoop reads that answer and posts it; proves idle -> working path ran.
	posts := ch.Posts(ref)
	found := false
	for _, p := range posts {
		if strings.Contains(p, "continue with plan B") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected echo of prompt in posts; got: %v", posts)
	}

	// Final status is idle (working -> idle after observe loop completes).
	got, _ := st.Get(ctx, ref)
	if got.Status != controller.StatusIdle {
		t.Errorf("expected final status idle; got %v", got.Status)
	}
}

// TestOnReplyUnlinkedUserGetsReasonPost: user who has not linked the integration receives
// a warning reaction AND an in-thread post explaining the failure.
func TestOnReplyUnlinkedUserGetsReasonPost(t *testing.T) {
	ctx := context.Background()
	ch := chattest.New()
	st := storetest.New()
	be := backendtest.New()
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: &fakeLc{},
		Linker:    &fakeLinker{err: fmt.Errorf("%w: link with /link", controller.ErrNotLinked)},
		Projects:  &fakeProjects{project: "myproject"},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-unlinked-post")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U2", Text: "help"}
	gotErr := c.OnReply(ctx, task, ev)
	if !errors.Is(gotErr, controller.ErrNotLinked) {
		t.Fatalf("expected ErrNotLinked; got %v", gotErr)
	}

	posts := ch.Posts(ref)
	found := false
	for _, p := range posts {
		if strings.Contains(p, "link") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected in-thread reason post containing 'link'; posts=%v", posts)
	}
}

// TestOnReplyUnlinkedUserRefused: user who has not linked the integration is refused.
func TestOnReplyUnlinkedUserRefused(t *testing.T) {
	ctx := context.Background()
	ch := chattest.New()
	st := storetest.New()
	be := backendtest.New()
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: &fakeLc{},
		Linker:    &fakeLinker{err: controller.ErrNotLinked},
		Projects:  &fakeProjects{project: "myproject"},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-unlinked")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "do it"}
	gotErr := c.OnReply(ctx, task, ev)
	if !errors.Is(gotErr, controller.ErrNotLinked) {
		t.Fatalf("expected ErrNotLinked; got %v", gotErr)
	}

	// No prompt sent to agent.
	if ans := be.Answered(); len(ans) != 0 {
		t.Errorf("expected no Answer calls; got %v", ans)
	}

	// "warning" reaction posted.
	reactions := ch.Reactions(ref)
	found := false
	for _, r := range reactions {
		if r == "warning" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning reaction; got %v", reactions)
	}
}

// TestOnReplyDifferentUserRefused: reply from a user other than the thread owner
// is refused with ErrNotOwner; no prompt is sent.
func TestOnReplyDifferentUserRefused(t *testing.T) {
	ctx := context.Background()
	d, ch, st, be := newBlockedDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-notowner")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "OWNER",
		LastAuthor:     "OWNER",
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "INTRUDER", Text: "hijack"}
	gotErr := c.OnReply(ctx, task, ev)
	if !errors.Is(gotErr, controller.ErrNotOwner) {
		t.Fatalf("expected ErrNotOwner; got %v", gotErr)
	}

	// No prompt or answer sent to agent.
	if ans := be.Answered(); len(ans) != 0 {
		t.Errorf("expected no Answer calls; got %v", ans)
	}

	// Refusal message mentions the owner.
	posts := ch.Posts(ref)
	msg := strings.Join(posts, " ")
	if !strings.Contains(msg, "OWNER") {
		t.Errorf("expected owner mentioned in refusal; posts: %v", posts)
	}
}

// errObserveBackend is an error injected to simulate a backend failure in Observe.
var errObserveBackend = errors.New("backendtest: injected observe error")

// observeErrBackend wraps a real backend but fails on Observe after a given number of calls.
type observeErrBackend struct {
	controller.AgentBackend
	observeCallsBeforeErr int
	mu                    int
}

func (b *observeErrBackend) Observe(ctx context.Context, agentRef string, wait bool) (herdragent.State, error) {
	b.mu++
	if b.mu > b.observeCallsBeforeErr {
		return herdragent.State{}, errObserveBackend
	}
	return b.AgentBackend.Observe(ctx, agentRef, wait)
}

// TestWaitNotBlockedBackendErrorPropagates: when Observe returns an error inside
// waitNotBlocked, handleBlockedReply propagates it (and reacts "warning").
func TestWaitNotBlockedBackendErrorPropagates(t *testing.T) {
	ctx := context.Background()
	ch := chattest.New()
	st := storetest.New()
	inner := backendtest.New()
	// Wrap: after 1 Observe call (from waitNotBlocked), inject error.
	be := &observeErrBackend{AgentBackend: inner, observeCallsBeforeErr: 0}
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: &fakeLc{},
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "myproject"},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-be-err")
	sbID, agRef, err := inner.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	// No script: Answer() on inner succeeds, then waitNotBlocked calls Observe on be -> error.
	_ = sbID

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "yes"}
	gotErr := c.OnReply(ctx, task, ev)
	if gotErr == nil {
		t.Fatal("expected error from waitNotBlocked; got nil")
	}
	if !errors.Is(gotErr, errObserveBackend) {
		t.Fatalf("expected errObserveBackend wrapped in error; got %v", gotErr)
	}

	// "warning" reaction posted.
	reactions := ch.Reactions(ref)
	found := false
	for _, r := range reactions {
		if r == "warning" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning reaction; got %v", reactions)
	}
}

// TestWaitNotBlockedDeadline: when the agent stays StatusBlocked through the
// timeout, waitNotBlocked returns a deadline-exceeded error.
func TestWaitNotBlockedDeadline(t *testing.T) {
	// Override timeout so the test finishes in milliseconds.
	old := controller.WaitNotBlockedTimeout
	controller.WaitNotBlockedTimeout = 80 * time.Millisecond
	t.Cleanup(func() { controller.WaitNotBlockedTimeout = old })

	ctx := context.Background()
	d, ch, st, be := newBlockedDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-deadline")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	// Script: always blocked (last sticks).
	be.Script(agRef,
		herdragent.State{Status: herdragent.StatusBlocked, Settled: true, Question: "stuck?"},
	)

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "continue"}
	gotErr := c.OnReply(ctx, task, ev)
	if gotErr == nil {
		t.Fatal("expected deadline error; got nil")
	}
	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded wrapped; got %v", gotErr)
	}

	// "warning" reaction posted.
	reactions := ch.Reactions(ref)
	found := false
	for _, r := range reactions {
		if r == "warning" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning reaction; got %v", reactions)
	}
	_ = ch
}
