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
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/herdragent"
)

func newTurnDeps(t *testing.T) (controller.Deps, *chattest.Fake, *storetest.Fake, *backendtest.Fake) {
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

func TestTurnMentionProvisionsPromptsAndPostsAnswer(t *testing.T) {
	ctx := context.Background()
	d, ch, st, _ := newTurnDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts1")
	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusStarting,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "hello"}
	if err := c.OnMention(ctx, task, ev); err != nil {
		t.Fatalf("OnMention: %v", err)
	}

	posts := ch.Posts(ref)
	if len(posts) == 0 {
		t.Fatal("no posts; expected answer")
	}
	reactions := ch.Reactions(ref)
	found := false
	for _, r := range reactions {
		if r == "white_check_mark" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no white_check_mark reaction; reactions=%v", reactions)
	}
}

func TestTurnDoneFlashThenBlockedDefersToBlocked(t *testing.T) {
	ctx := context.Background()
	d, ch, st, be := newTurnDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts2")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	be.Script(agRef,
		herdragent.State{Status: herdragent.StatusDone, Settled: true, Seq: 1},
		herdragent.State{Status: herdragent.StatusBlocked, Settled: true, Seq: 2, Question: "which branch?"},
	)

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusStarting,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "go"}
	if err := c.OnMention(ctx, task, ev); err != nil {
		t.Fatalf("OnMention: %v", err)
	}

	for _, r := range ch.Reactions(ref) {
		if r == "white_check_mark" {
			t.Fatal("got white_check_mark; expected blocked handling instead")
		}
	}

	posts := ch.Posts(ref)
	found := false
	for _, p := range posts {
		if strings.Contains(p, "which branch?") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blocked question not posted; posts=%v", posts)
	}

	got, err := st.Get(ctx, ref)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.Status != controller.StatusWaitingOnUser {
		t.Fatalf("status=%v; want waiting_on_user", got.Status)
	}
}

func TestTurnProvisionCarriesLinkedPrincipal(t *testing.T) {
	ctx := context.Background()
	d, _, st, be := newTurnDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts3")
	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusStarting,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "do it"}
	if err := c.OnMention(ctx, task, ev); err != nil {
		t.Fatalf("OnMention: %v", err)
	}

	provisions := be.Provisioned()
	if len(provisions) == 0 {
		t.Fatal("no Provision calls recorded")
	}
	want := vault.SlackPrincipal("T1", "U1")
	if provisions[0].Principal != want {
		t.Fatalf("principal=%q; want %q", provisions[0].Principal, want)
	}
}

func TestTurnFailurePostsWarningReaction(t *testing.T) {
	ctx := context.Background()
	d, ch, st, _ := newTurnDeps(t)
	d.Linker = &fakeLinker{err: controller.ErrNotLinked}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts4")
	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusStarting,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "hi"}
	if err := c.OnMention(ctx, task, ev); err == nil {
		t.Fatal("expected error from OnMention; got nil")
	}

	found := false
	for _, r := range ch.Reactions(ref) {
		if r == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning reaction; reactions=%v", ch.Reactions(ref))
	}
}
