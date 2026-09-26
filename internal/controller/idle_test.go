package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/backendtest"
	"github.com/IniZio/nexus/internal/controller/chattest"
	"github.com/IniZio/nexus/internal/controller/storetest"
)

func newIdleDeps(t *testing.T, lc controller.SandboxLifecycle) (controller.Deps, *chattest.Fake, *storetest.Fake, *backendtest.Fake) {
	t.Helper()
	ch := chattest.New()
	st := storetest.New()
	be := backendtest.New()
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: lc,
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "myproject"},
		IdlePause: 30 * time.Minute,
		IdleStop:  2 * time.Hour,
	}
	return d, ch, st, be
}

func TestIdleSweepPausesAfterThreshold(t *testing.T) {
	ctx := context.Background()
	lc := &fakeLc{}
	d, _, st, be := newIdleDeps(t, lc)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts1")
	sbID, _, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	past := time.Now().Add(-1 * time.Hour)
	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		Status:         controller.StatusIdle,
		SandboxID:      sbID,
		CreatedAt:      past,
		LastActivityAt: past,
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := c.OnTick(ctx, task, time.Now()); err != nil {
		t.Fatalf("OnTick: %v", err)
	}

	if len(lc.paused) == 0 || lc.paused[0] != sbID {
		t.Fatalf("Pause not called with sandboxID=%q; paused=%v", sbID, lc.paused)
	}
	got, _ := st.Get(ctx, ref)
	if got.Status != controller.StatusPaused {
		t.Fatalf("status=%v; want paused", got.Status)
	}
}

func TestIdleSweepStopsAfterLongIdle(t *testing.T) {
	ctx := context.Background()
	lc := &fakeLc{}
	d, _, st, be := newIdleDeps(t, lc)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts2")
	sbID, _, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	past := time.Now().Add(-3 * time.Hour)
	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		Status:         controller.StatusPaused,
		SandboxID:      sbID,
		CreatedAt:      past,
		LastActivityAt: past,
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := c.OnTick(ctx, task, time.Now()); err != nil {
		t.Fatalf("OnTick: %v", err)
	}

	if len(lc.stopped) == 0 || lc.stopped[0] != sbID {
		t.Fatalf("Stop not called with sandboxID=%q; stopped=%v", sbID, lc.stopped)
	}
	got, _ := st.Get(ctx, ref)
	if got.Status != controller.StatusClosed {
		t.Fatalf("status=%v; want closed", got.Status)
	}
}

func TestReplyOnPausedResumesThenTurns(t *testing.T) {
	ctx := context.Background()
	lc := &fakeLc{}
	d, ch, st, be := newIdleDeps(t, lc)
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
		Status:         controller.StatusPaused,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "continue"}
	if err := c.OnReply(ctx, task, ev); err != nil {
		t.Fatalf("OnReply: %v", err)
	}

	if len(lc.resumed) == 0 || lc.resumed[0] != sbID {
		t.Fatalf("Resume not called; resumed=%v", lc.resumed)
	}
	if len(ch.Posts(ref)) == 0 {
		t.Fatal("no answer posted after resume")
	}
}
