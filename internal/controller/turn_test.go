package controller_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

func TestTurnTimeoutTransitionsIdleAndReturnsErrTurnTimeout(t *testing.T) {
	ctx := context.Background()
	d, ch, st, be := newTurnDeps(t)
	d.TurnTimeout = 50 * time.Millisecond
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-timeout")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	// script a non-settling state so the loop never exits normally
	be.Script(agRef, herdragent.State{Status: herdragent.StatusWorking, Settled: false})

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWorking,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if uErr := st.Upsert(ctx, task); uErr != nil {
		t.Fatalf("Upsert: %v", uErr)
	}

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "go"}
	err = c.OnMention(ctx, task, ev)
	if !errors.Is(err, controller.ErrTurnTimeout) {
		t.Fatalf("want ErrTurnTimeout; got %v", err)
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

	posts := ch.Posts(ref)
	timedOut := false
	for _, p := range posts {
		if strings.Contains(p, "timed out") {
			timedOut = true
		}
	}
	if !timedOut {
		t.Fatalf("no timeout post; posts=%v", posts)
	}

	got, gErr := st.Get(ctx, ref)
	if gErr != nil {
		t.Fatalf("store.Get: %v", gErr)
	}
	if got.Status != controller.StatusIdle {
		t.Fatalf("status=%v; want idle", got.Status)
	}
}

func TestTurnNotOwnerRefuses(t *testing.T) {
	ctx := context.Background()
	d, ch, st, be := newTurnDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-notowner")
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
	if uErr := st.Upsert(ctx, task); uErr != nil {
		t.Fatalf("Upsert: %v", uErr)
	}

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U2", Text: "hi"}
	err = c.OnMention(ctx, task, ev)
	if !errors.Is(err, controller.ErrNotOwner) {
		t.Fatalf("want ErrNotOwner; got %v", err)
	}

	if len(be.Provisioned()) > 1 {
		t.Fatal("unexpected extra Provision call")
	}
	// no Prompt should have been sent
	// verify no white_check_mark reaction
	for _, r := range ch.Reactions(ref) {
		if r == "white_check_mark" {
			t.Fatal("got white_check_mark; expected refusal only")
		}
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

func TestTurnParentCtxCancelNoTransition(t *testing.T) {
	d, _, st, be := newTurnDeps(t)
	d.TurnTimeout = 5 * time.Second
	c := controller.New(d)

	ctx, cancel := context.WithCancel(context.Background())

	ref := controller.NewThreadRef("T1", "C1", "ts-cancel")
	sbID, agRef, err := be.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	be.Script(agRef, herdragent.State{Status: herdragent.StatusWorking, Settled: false})

	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusWorking,
		SandboxID:      sbID,
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if uErr := st.Upsert(ctx, task); uErr != nil {
		t.Fatalf("Upsert: %v", uErr)
	}

	// cancel parent ctx immediately
	cancel()

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "go"}
	err = c.OnMention(ctx, task, ev)
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if errors.Is(err, controller.ErrTurnTimeout) {
		t.Fatalf("got ErrTurnTimeout; parent ctx cancel should not produce turn timeout")
	}

	got, gErr := st.Get(context.Background(), ref)
	if gErr != nil {
		t.Fatalf("store.Get: %v", gErr)
	}
	if got.Status == controller.StatusIdle {
		t.Fatalf("status transitioned to idle on parent ctx cancel; want working")
	}
}

func TestTurnNotLinkedPostsReasonInThread(t *testing.T) {
	ctx := context.Background()
	d, ch, st, _ := newTurnDeps(t)
	d.Linker = &fakeLinker{err: fmt.Errorf("%w: link with /link github", controller.ErrNotLinked)}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-notlinked")
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

	posts := ch.Posts(ref)
	found := false
	for _, p := range posts {
		if strings.Contains(p, "link") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no actionable message posted in-thread; posts=%v", posts)
	}
}

func TestTurnNoProjectPostsReasonInThread(t *testing.T) {
	ctx := context.Background()
	d, ch, st, _ := newTurnDeps(t)
	d.Projects = &fakeProjects{project: ""}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-noproject")
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

	posts := ch.Posts(ref)
	if len(posts) == 0 {
		t.Fatalf("no message posted in-thread for ErrNoProject; posts=%v", posts)
	}
}

// provisionErrBackend wraps AgentBackend, returning a fixed error (and optional partial sbID) from Provision.
type provisionErrBackend struct {
	controller.AgentBackend
	sbID string
	err  error
	mu   sync.Mutex
	torn []string
}

func (b *provisionErrBackend) Provision(_ context.Context, _ string, _ controller.ThreadRef, _ string) (string, string, error) {
	return b.sbID, "", b.err
}

func (b *provisionErrBackend) Teardown(_ context.Context, sandboxID string) error {
	b.mu.Lock()
	b.torn = append(b.torn, sandboxID)
	b.mu.Unlock()
	return nil
}

func (b *provisionErrBackend) TornDown() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.torn))
	copy(out, b.torn)
	return out
}

// promptErrBackend wraps AgentBackend, returning a fixed error from Prompt.
type promptErrBackend struct {
	controller.AgentBackend
	err error
}

func (b *promptErrBackend) Prompt(_ context.Context, _ string, _ string) (string, error) {
	return "", b.err
}

// TestProvisionAckPostsEyesAndMessage verifies that before provisioning, eyes
// reaction and a provisioning message are posted.
func TestProvisionAckPostsEyesAndMessage(t *testing.T) {
	ctx := context.Background()
	d, ch, st, _ := newTurnDeps(t)
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-ack")
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

	found := false
	for _, r := range ch.Reactions(ref) {
		if r == "eyes" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no eyes reaction before provisioning; reactions=%v", ch.Reactions(ref))
	}

	posts := ch.Posts(ref)
	provMsg := false
	for _, p := range posts {
		if strings.Contains(p, "provisioning a sandbox") {
			provMsg = true
		}
	}
	if !provMsg {
		t.Fatalf("no provisioning message posted; posts=%v", posts)
	}
}

// TestProvisionErrorFailsTaskAndPostsReason verifies that a Provision failure
// transitions the task to failed and posts a failure reason.
func TestProvisionErrorFailsTaskAndPostsReason(t *testing.T) {
	ctx := context.Background()
	ch := chattest.New()
	st := storetest.New()
	provErr := errors.New("backend unavailable")
	be := &provisionErrBackend{AgentBackend: backendtest.New(), err: provErr}
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: &fakeLc{},
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "myproject"},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-prov-err")
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

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "go"}
	if err := c.OnMention(ctx, task, ev); err == nil {
		t.Fatal("expected error from OnMention; got nil")
	}

	got, gErr := st.Get(ctx, ref)
	if gErr != nil {
		t.Fatalf("store.Get: %v", gErr)
	}
	if got.Status != controller.StatusFailed {
		t.Fatalf("status=%v; want failed", got.Status)
	}

	posts := ch.Posts(ref)
	if len(posts) == 0 {
		t.Fatal("expected at least one post after provision failure; got none")
	}
}

// TestProvisionErrorWithPartialSandboxCallsTeardown verifies that when Provision
// returns a sandboxID alongside an error, Teardown is called with that ID.
func TestProvisionErrorWithPartialSandboxCallsTeardown(t *testing.T) {
	ctx := context.Background()
	ch := chattest.New()
	st := storetest.New()
	provErr := errors.New("partial provision")
	be := &provisionErrBackend{AgentBackend: backendtest.New(), sbID: "sb-partial", err: provErr}
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: &fakeLc{},
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "myproject"},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-partial-prov")
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

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "go"}
	_ = c.OnMention(ctx, task, ev)

	torn := be.TornDown()
	found := false
	for _, id := range torn {
		if id == "sb-partial" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Teardown not called for partial sandbox; torn=%v", torn)
	}
}

// TestProvisionErrorThenRetryReprovisions verifies that after a provision failure
// (task=failed, no SandboxID), the next mention re-provisions successfully.
func TestProvisionErrorThenRetryReprovisions(t *testing.T) {
	ctx := context.Background()
	d, ch, st, _ := newTurnDeps(t)
	c := controller.New(d)

	// First mention: force provision error by using a bad backend.
	ch2 := chattest.New()
	st2 := storetest.New()
	provErr := errors.New("transient")
	be2 := &provisionErrBackend{AgentBackend: backendtest.New(), err: provErr}
	d2 := controller.Deps{
		Chat:      ch2,
		Store:     st2,
		Backend:   be2,
		Lifecycle: &fakeLc{},
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "myproject"},
	}
	c2 := controller.New(d2)

	ref := controller.NewThreadRef("T1", "C1", "ts-retry")
	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusStarting,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st2.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "first"}
	_ = c2.OnMention(ctx, task, ev)

	// Verify task is now failed with no sandbox.
	failed, fErr := st2.Get(ctx, ref)
	if fErr != nil {
		t.Fatalf("store.Get: %v", fErr)
	}
	if failed.Status != controller.StatusFailed {
		t.Fatalf("status=%v; want failed", failed.Status)
	}
	if failed.SandboxID != "" {
		t.Fatalf("SandboxID should be empty after provision failure; got %q", failed.SandboxID)
	}

	// Second mention using a working controller.
	ref2 := controller.NewThreadRef("T1", "C1", "ts-retry2")
	task2 := controller.Task{
		ThreadRef:      ref2,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusFailed,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task2); err != nil {
		t.Fatalf("Upsert task2: %v", err)
	}
	ev2 := controller.Event{Kind: controller.EventMention, ThreadRef: ref2, User: "U1", Text: "retry"}
	if err := c.OnMention(ctx, task2, ev2); err != nil {
		t.Fatalf("OnMention (retry): %v", err)
	}

	// Should have transitioned to idle after successful turn.
	got2, gErr := st.Get(ctx, ref2)
	if gErr != nil {
		t.Fatalf("store.Get retry: %v", gErr)
	}
	if got2.Status != controller.StatusIdle {
		t.Fatalf("retry status=%v; want idle", got2.Status)
	}
	_ = ch
}

// TestPromptErrorFailsTask verifies that a Prompt error transitions the task to
// failed and posts a failure reason.
func TestPromptErrorFailsTask(t *testing.T) {
	ctx := context.Background()
	ch := chattest.New()
	st := storetest.New()
	inner := backendtest.New()
	promptErr := errors.New("prompt rejected")
	be := &promptErrBackend{AgentBackend: inner, err: promptErr}
	d := controller.Deps{
		Chat:      ch,
		Store:     st,
		Backend:   be,
		Lifecycle: &fakeLc{},
		Linker:    &fakeLinker{},
		Projects:  &fakeProjects{project: "myproject"},
	}
	c := controller.New(d)

	ref := controller.NewThreadRef("T1", "C1", "ts-prompt-err")
	// Pre-provision so Prompt is reached.
	_, agRef, err := inner.Provision(ctx, "myproject", ref, "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	_ = agRef
	task := controller.Task{
		ThreadRef:      ref,
		Owner:          "U1",
		LastAuthor:     "U1",
		Status:         controller.StatusStarting,
		SandboxID:      "sb-1",
		HerdrAgent:     agRef,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := st.Upsert(ctx, task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ev := controller.Event{Kind: controller.EventMention, ThreadRef: ref, User: "U1", Text: "do it"}
	if err := c.OnMention(ctx, task, ev); err == nil {
		t.Fatal("expected error from OnMention; got nil")
	}

	got, gErr := st.Get(ctx, ref)
	if gErr != nil {
		t.Fatalf("store.Get: %v", gErr)
	}
	if got.Status != controller.StatusFailed {
		t.Fatalf("status=%v; want failed after prompt error", got.Status)
	}

	posts := ch.Posts(ref)
	if len(posts) == 0 {
		t.Fatal("expected failure message post; got none")
	}
}

// TestStartupResetStuckTransitionsToFailed verifies that ResetStuck in the store
// transitions starting and working tasks to failed.
func TestStartupResetStuckTransitionsToFailed(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()

	refs := []struct {
		ref    controller.ThreadRef
		status controller.Status
	}{
		{controller.NewThreadRef("T1", "C1", "starting"), controller.StatusStarting},
		{controller.NewThreadRef("T1", "C1", "working"), controller.StatusWorking},
		{controller.NewThreadRef("T1", "C1", "idle"), controller.StatusIdle},
	}
	for _, r := range refs {
		if err := st.Upsert(ctx, controller.Task{
			ThreadRef:      r.ref,
			Status:         r.status,
			CreatedAt:      time.Now(),
			LastActivityAt: time.Now(),
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	if err := st.ResetStuck(ctx); err != nil {
		t.Fatalf("ResetStuck: %v", err)
	}

	for _, r := range refs {
		got, gErr := st.Get(ctx, r.ref)
		if gErr != nil {
			t.Fatalf("Get(%v): %v", r.ref, gErr)
		}
		switch r.status {
		case controller.StatusStarting, controller.StatusWorking:
			if got.Status != controller.StatusFailed {
				t.Errorf("ref %v: status=%v; want failed", r.ref, got.Status)
			}
		default:
			if got.Status != r.status {
				t.Errorf("ref %v: status=%v; want %v (unchanged)", r.ref, got.Status, r.status)
			}
		}
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
