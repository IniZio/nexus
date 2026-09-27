package controller_test

import (
	"context"
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
