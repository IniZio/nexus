package controller_test

import (
	"context"
	"errors"
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
		IdleFor: func(string) controller.IdleThresholds {
			return controller.IdleThresholds{Pause: 30 * time.Minute, Stop: 2 * time.Hour}
		},
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
	if got.Status != controller.StatusStopped {
		t.Fatalf("status=%v; want stopped", got.Status)
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

// TestIdleZeroPauseDisabled: zero Pause threshold means never pause.
func TestIdleZeroPauseDisabled(t *testing.T) {
	ctx := context.Background()
	lc := &fakeLc{}
	ch := chattest.New()
	st := storetest.New()
	be := backendtest.New()
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: lc,
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "proj"},
		IdleFor: func(string) controller.IdleThresholds {
			return controller.IdleThresholds{Pause: 0, Stop: 4 * time.Hour}
		},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "zp1")
	sbID, _, err := be.Provision(ctx, "proj", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	past := time.Now().Add(-2 * time.Hour)
	task := controller.Task{
		ThreadRef:      ref,
		Status:         controller.StatusIdle,
		SandboxID:      sbID,
		LastActivityAt: past,
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := c.OnTick(ctx, task, time.Now()); err != nil {
		t.Fatalf("OnTick: %v", err)
	}
	if len(lc.paused) != 0 {
		t.Fatalf("expected no Pause call with zero Pause threshold; got %v", lc.paused)
	}
}

// TestIdleZeroStopDisabled: zero Stop threshold means never stop.
func TestIdleZeroStopDisabled(t *testing.T) {
	ctx := context.Background()
	lc := &fakeLc{}
	ch := chattest.New()
	st := storetest.New()
	be := backendtest.New()
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: lc,
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "proj"},
		IdleFor: func(string) controller.IdleThresholds {
			return controller.IdleThresholds{Pause: 30 * time.Minute, Stop: 0}
		},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "zs1")
	sbID, _, err := be.Provision(ctx, "proj", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	past := time.Now().Add(-10 * time.Hour)
	task := controller.Task{
		ThreadRef:      ref,
		Status:         controller.StatusPaused,
		SandboxID:      sbID,
		LastActivityAt: past,
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := c.OnTick(ctx, task, time.Now()); err != nil {
		t.Fatalf("OnTick: %v", err)
	}
	if len(lc.stopped) != 0 {
		t.Fatalf("expected no Stop call with zero Stop threshold; got %v", lc.stopped)
	}
}

// TestIdlePerChannelThresholds: channel with 2h pause - at 1h idle must NOT pause; at 2h+ must.
func TestIdlePerChannelThresholds(t *testing.T) {
	ctx := context.Background()

	newDepsForChannel := func(t *testing.T, lc *fakeLc) (*controller.Controller, *storetest.Fake, *backendtest.Fake) {
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
			Projects:  &fakeProjects{project: "proj"},
			IdleFor: func(channel string) controller.IdleThresholds {
				if channel == "CLONG" {
					return controller.IdleThresholds{Pause: 2 * time.Hour, Stop: 8 * time.Hour}
				}
				return controller.IdleThresholds{Pause: 30 * time.Minute, Stop: 4 * time.Hour}
			},
		}
		return controller.New(d), st, be
	}

	t.Run("at 1h idle does not pause 2h-threshold channel", func(t *testing.T) {
		lc := &fakeLc{}
		c, st, be := newDepsForChannel(t, lc)
		ref := controller.NewThreadRef("T1", "CLONG", "pc1")
		sbID, _, err := be.Provision(ctx, "proj", ref, "p")
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		past := time.Now().Add(-1 * time.Hour)
		task := controller.Task{
			ThreadRef:      ref,
			Status:         controller.StatusIdle,
			SandboxID:      sbID,
			LastActivityAt: past,
		}
		if err := st.Upsert(ctx, task); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := c.OnTick(ctx, task, time.Now()); err != nil {
			t.Fatalf("OnTick: %v", err)
		}
		if len(lc.paused) != 0 {
			t.Fatalf("should not pause at 1h with 2h threshold; paused=%v", lc.paused)
		}
	})

	t.Run("at 2h+ idle pauses 2h-threshold channel", func(t *testing.T) {
		lc := &fakeLc{}
		c, st, be := newDepsForChannel(t, lc)
		ref := controller.NewThreadRef("T1", "CLONG", "pc2")
		sbID, _, err := be.Provision(ctx, "proj", ref, "p")
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		past := time.Now().Add(-2*time.Hour - time.Second)
		task := controller.Task{
			ThreadRef:      ref,
			Status:         controller.StatusIdle,
			SandboxID:      sbID,
			LastActivityAt: past,
		}
		if err := st.Upsert(ctx, task); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := c.OnTick(ctx, task, time.Now()); err != nil {
			t.Fatalf("OnTick: %v", err)
		}
		if len(lc.paused) == 0 || lc.paused[0] != sbID {
			t.Fatalf("expected Pause at 2h+ idle; paused=%v", lc.paused)
		}
		got, _ := st.Get(ctx, ref)
		if got.Status != controller.StatusPaused {
			t.Fatalf("status=%v; want paused", got.Status)
		}
	})
}

// TestIdleNilIdleForUsesDefault: nil IdleFor falls back to DefaultIdle (30m/4h).
func TestIdleNilIdleForUsesDefault(t *testing.T) {
	ctx := context.Background()
	lc := &fakeLc{}
	ch := chattest.New()
	st := storetest.New()
	be := backendtest.New()
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: lc,
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "proj"},
		IdleFor:   nil,
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "def1")
	sbID, _, err := be.Provision(ctx, "proj", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	past := time.Now().Add(-35 * time.Minute)
	task := controller.Task{
		ThreadRef:      ref,
		Status:         controller.StatusIdle,
		SandboxID:      sbID,
		LastActivityAt: past,
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := c.OnTick(ctx, task, time.Now()); err != nil {
		t.Fatalf("OnTick: %v", err)
	}
	if len(lc.paused) == 0 || lc.paused[0] != sbID {
		t.Fatalf("expected Pause using DefaultIdle(30m); paused=%v", lc.paused)
	}
}

// TestIdleWaitingOnUserPauses: WaitingOnUser -> Paused when idle >= Pause.
func TestIdleWaitingOnUserPauses(t *testing.T) {
	ctx := context.Background()
	lc := &fakeLc{}
	d, _, st, be := newIdleDeps(t, lc)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "wou1")
	sbID, _, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	past := time.Now().Add(-1 * time.Hour)
	task := controller.Task{
		ThreadRef:      ref,
		Status:         controller.StatusWaitingOnUser,
		SandboxID:      sbID,
		LastActivityAt: past,
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := c.OnTick(ctx, task, time.Now()); err != nil {
		t.Fatalf("OnTick: %v", err)
	}
	if len(lc.paused) == 0 || lc.paused[0] != sbID {
		t.Fatalf("expected Pause for WaitingOnUser; paused=%v", lc.paused)
	}
	got, _ := st.Get(ctx, ref)
	if got.Status != controller.StatusPaused {
		t.Fatalf("status=%v; want paused", got.Status)
	}
}

// TestStoppedReplyStartsRestartPrompts: stopped reply calls Start, Restart, Prompt and ends idle.
// Tested through OnReply; skipped if blocked.go has not yet dispatched StatusStopped.
func TestStoppedReplyStartsRestartPrompts(t *testing.T) {
	ctx := context.Background()
	lc := &fakeLc{}
	d, ch, st, be := newIdleDeps(t, lc)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "sr1")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusStopped,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "wake up"}
	replyErr := c.OnReply(ctx, task, ev)

	if len(lc.started) == 0 {
		t.Fatalf("Start not called; OnReply err=%v", replyErr)
	}

	if replyErr != nil {
		t.Fatalf("OnReply: %v", replyErr)
	}
	if lc.started[0] != sbID {
		t.Fatalf("Start called with wrong id: %v", lc.started)
	}
	if len(ch.Posts(ref)) == 0 {
		t.Fatal("no answer posted after stopped resume")
	}
	got, _ := st.Get(ctx, ref)
	if got.Status != controller.StatusIdle {
		t.Fatalf("final status=%v; want idle", got.Status)
	}
}

// TestStoppedReplyStartErrorWarnsAndReturnsError: Start error -> warning reaction, error returned, no Prompt.
// Tested through OnReply; skipped if blocked.go has not yet dispatched StatusStopped.
func TestStoppedReplyStartErrorWarnsAndReturnsError(t *testing.T) {
	ctx := context.Background()

	errStart := errors.New("start failed")
	lc := &fakeLcStartErr{fakeLc: &fakeLc{}, startErr: errStart}

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
		IdleFor: func(string) controller.IdleThresholds {
			return controller.IdleThresholds{Pause: 30 * time.Minute, Stop: 2 * time.Hour}
		},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "se1")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		Status:         controller.StatusStopped,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventReply, ThreadRef: ref, User: "U1", Text: "go"}
	replyErr := c.OnReply(ctx, task, ev)

	if lc.fakeLc.started == nil {
		t.Fatalf("Start not called; OnReply err=%v", replyErr)
	}

	if replyErr == nil {
		t.Fatal("expected error from OnReply on Start failure")
	}
	if !errors.Is(replyErr, errStart) {
		t.Fatalf("error=%v; want errStart", replyErr)
	}
	reactions := ch.Reactions(ref)
	hasWarning := false
	for _, r := range reactions {
		if r == "warning" {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Fatalf("expected 'warning' reaction; got %v", reactions)
	}
}

// fakeLcStartErr overrides Start to return an error; other lifecycle ops use fakeLc.
type fakeLcStartErr struct {
	*fakeLc
	startErr error
}

func (f *fakeLcStartErr) Start(_ context.Context, id string) error {
	f.fakeLc.started = append(f.fakeLc.started, id)
	return f.startErr
}
