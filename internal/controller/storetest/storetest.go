package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
)

// Fake is an in-memory TaskStore for use in tests.
type Fake struct {
	mu    sync.Mutex
	tasks map[controller.ThreadRef]controller.Task
	now   func() time.Time
}

// New returns an empty Fake backed by time.Now.
func New() *Fake {
	return &Fake{
		tasks: make(map[controller.ThreadRef]controller.Task),
		now:   time.Now,
	}
}

// SetClock replaces the clock used by TouchActivity.
func (f *Fake) SetClock(now func() time.Time) { f.now = now }

func (f *Fake) Get(_ context.Context, ref controller.ThreadRef) (controller.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[ref]
	if !ok {
		return controller.Task{}, controller.ErrNotFound
	}
	return t, nil
}

func (f *Fake) Upsert(_ context.Context, t controller.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[t.ThreadRef] = t
	return nil
}

func (f *Fake) Transition(_ context.Context, ref controller.ThreadRef, from, to controller.Status, seq uint64) error {
	if !controller.ValidTransition(from, to) {
		return controller.ErrInvalidTransition
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[ref]
	if !ok {
		return controller.ErrNotFound
	}
	if t.Status != from {
		return controller.ErrConflict
	}
	t.Status = to
	t.StateChangeSeq = seq
	f.tasks[ref] = t
	return nil
}

func (f *Fake) ListIdle(_ context.Context, before time.Time) ([]controller.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []controller.Task
	for _, t := range f.tasks {
		switch t.Status {
		case controller.StatusIdle, controller.StatusWaitingOnUser, controller.StatusPaused:
			if t.LastActivityAt.Before(before) {
				cp := t
				out = append(out, cp)
			}
		}
	}
	return out, nil
}

func (f *Fake) TouchActivity(_ context.Context, ref controller.ThreadRef, author string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[ref]
	if !ok {
		return controller.ErrNotFound
	}
	t.LastAuthor = author
	t.LastActivityAt = f.now()
	f.tasks[ref] = t
	return nil
}

// RunTaskStoreContract runs the canonical contract suite against any TaskStore.
// Each subtest receives a fresh store from newStore.
func RunTaskStoreContract(t *testing.T, newStore func(t *testing.T) controller.TaskStore) {
	t.Helper()
	ctx := context.Background()

	ref := func(suffix string) controller.ThreadRef {
		return controller.NewThreadRef("T1", "C1", suffix)
	}

	// truncMs strips monotonic clock and sub-ms precision, matching SQLite storage.
	truncMs := func(tm time.Time) time.Time {
		return tm.UTC().Truncate(time.Millisecond)
	}

	baseTask := func(r controller.ThreadRef) controller.Task {
		now := truncMs(time.Now())
		return controller.Task{
			ThreadRef:      r,
			Project:        "proj",
			Owner:          "owner",
			LastAuthor:     "author",
			SandboxID:      "sb-1",
			HerdrAgent:     "ha-1",
			AgentSessionID: "sess-1",
			Status:         controller.StatusIdle,
			TurnID:         "t1",
			StateChangeSeq: 1,
			CreatedAt:      now,
			LastActivityAt: now,
			PreviewSlot:    "ps-1",
		}
	}

	t.Run("GetMissing", func(t *testing.T) {
		s := newStore(t)
		_, err := s.Get(ctx, ref("miss"))
		if err == nil || !isErrNotFound(err) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("UpsertGetRoundtrip", func(t *testing.T) {
		s := newStore(t)
		r := ref("rtrip")
		task := baseTask(r)
		if err := s.Upsert(ctx, task); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := s.Get(ctx, r)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		checkTaskEqual(t, task, got)
	})

	t.Run("UpsertOverwrites", func(t *testing.T) {
		s := newStore(t)
		r := ref("overw")
		task := baseTask(r)
		if err := s.Upsert(ctx, task); err != nil {
			t.Fatal(err)
		}
		task.Owner = "new-owner"
		task.Status = controller.StatusWorking
		if err := s.Upsert(ctx, task); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if got.Owner != "new-owner" || got.Status != controller.StatusWorking {
			t.Fatalf("overwrite not reflected: %+v", got)
		}
	})

	t.Run("TransitionValid", func(t *testing.T) {
		s := newStore(t)
		r := ref("tv")
		task := baseTask(r)
		task.Status = controller.StatusIdle
		task.StateChangeSeq = 1
		if err := s.Upsert(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := s.Transition(ctx, r, controller.StatusIdle, controller.StatusWorking, 2); err != nil {
			t.Fatalf("Transition: %v", err)
		}
		got, err := s.Get(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != controller.StatusWorking {
			t.Fatalf("expected StatusWorking, got %v", got.Status)
		}
		if got.StateChangeSeq != 2 {
			t.Fatalf("expected seq=2, got %v", got.StateChangeSeq)
		}
	})

	t.Run("TransitionStaleFrom", func(t *testing.T) {
		s := newStore(t)
		r := ref("tsf")
		task := baseTask(r)
		task.Status = controller.StatusWorking
		if err := s.Upsert(ctx, task); err != nil {
			t.Fatal(err)
		}
		err := s.Transition(ctx, r, controller.StatusIdle, controller.StatusWorking, 2)
		if !isErrConflict(err) {
			t.Fatalf("expected ErrConflict, got %v", err)
		}
		got, _ := s.Get(ctx, r)
		if got.Status != controller.StatusWorking {
			t.Fatalf("record must be unchanged after conflict")
		}
	})

	t.Run("TransitionInvalidPair", func(t *testing.T) {
		s := newStore(t)
		r := ref("tip")
		task := baseTask(r)
		task.Status = controller.StatusClosed
		if err := s.Upsert(ctx, task); err != nil {
			t.Fatal(err)
		}
		err := s.Transition(ctx, r, controller.StatusClosed, controller.StatusWorking, 2)
		if !isErrInvalidTransition(err) {
			t.Fatalf("expected ErrInvalidTransition, got %v", err)
		}
	})

	t.Run("TransitionMissing", func(t *testing.T) {
		s := newStore(t)
		err := s.Transition(ctx, ref("miss"), controller.StatusIdle, controller.StatusWorking, 1)
		if !isErrNotFound(err) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("TransitionConcurrentRace", func(t *testing.T) {
		s := newStore(t)
		r := ref("race")
		task := baseTask(r)
		task.Status = controller.StatusIdle
		if err := s.Upsert(ctx, task); err != nil {
			t.Fatal(err)
		}
		const n = 20
		errs := make([]error, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i := range n {
			go func(i int) {
				defer wg.Done()
				errs[i] = s.Transition(ctx, r, controller.StatusIdle, controller.StatusWorking, uint64(i+2))
			}(i)
		}
		wg.Wait()
		var nils int
		for _, err := range errs {
			if err == nil {
				nils++
			} else if !isErrConflict(err) && !isErrInvalidTransition(err) {
				t.Errorf("unexpected error: %v", err)
			}
		}
		if nils != 1 {
			t.Fatalf("expected exactly 1 nil, got %d", nils)
		}
	})

	t.Run("ListIdle", func(t *testing.T) {
		s := newStore(t)
		cutoff := truncMs(time.Now())
		old := cutoff.Add(-time.Hour)
		recent := cutoff.Add(time.Hour)

		seed := func(suffix string, status controller.Status, actAt time.Time) {
			task := baseTask(ref(suffix))
			task.Status = status
			task.LastActivityAt = actAt
			if err := s.Upsert(ctx, task); err != nil {
				t.Fatal(err)
			}
		}

		seed("idle-old", controller.StatusIdle, old)
		seed("wait-old", controller.StatusWaitingOnUser, old)
		seed("paused-old", controller.StatusPaused, old)
		seed("working", controller.StatusWorking, old)
		seed("closed", controller.StatusClosed, old)
		seed("idle-recent", controller.StatusIdle, recent)

		list, err := s.ListIdle(ctx, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		got := make(map[controller.ThreadRef]bool)
		for _, task := range list {
			got[task.ThreadRef] = true
		}
		want := []controller.ThreadRef{ref("idle-old"), ref("wait-old"), ref("paused-old")}
		excluded := []controller.ThreadRef{ref("working"), ref("closed"), ref("idle-recent")}
		for _, r := range want {
			if !got[r] {
				t.Errorf("expected %v in ListIdle result", r)
			}
		}
		for _, r := range excluded {
			if got[r] {
				t.Errorf("unexpected %v in ListIdle result", r)
			}
		}
	})

	t.Run("TouchActivity", func(t *testing.T) {
		s := newStore(t)
		r := ref("touch")
		task := baseTask(r)
		task.LastActivityAt = truncMs(time.Now().Add(-time.Hour))
		if err := s.Upsert(ctx, task); err != nil {
			t.Fatal(err)
		}
		before := time.Now().Add(-time.Millisecond)
		if err := s.TouchActivity(ctx, r, "new-author"); err != nil {
			t.Fatalf("TouchActivity: %v", err)
		}
		got, err := s.Get(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastAuthor != "new-author" {
			t.Fatalf("LastAuthor not updated: %v", got.LastAuthor)
		}
		if got.LastActivityAt.Before(before.Add(-time.Millisecond)) {
			t.Fatalf("LastActivityAt not advanced: %v (before=%v)", got.LastActivityAt, before)
		}
	})

	t.Run("TouchActivityMissing", func(t *testing.T) {
		s := newStore(t)
		err := s.TouchActivity(ctx, ref("miss"), "author")
		if !isErrNotFound(err) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
}

func isErrNotFound(err error) bool          { return errors.Is(err, controller.ErrNotFound) }
func isErrConflict(err error) bool          { return errors.Is(err, controller.ErrConflict) }
func isErrInvalidTransition(err error) bool { return errors.Is(err, controller.ErrInvalidTransition) }

func checkTaskEqual(t *testing.T, want, got controller.Task) {
	t.Helper()
	if want.ThreadRef != got.ThreadRef {
		t.Errorf("ThreadRef: want %v got %v", want.ThreadRef, got.ThreadRef)
	}
	if want.Project != got.Project {
		t.Errorf("Project: want %v got %v", want.Project, got.Project)
	}
	if want.Owner != got.Owner {
		t.Errorf("Owner: want %v got %v", want.Owner, got.Owner)
	}
	if want.LastAuthor != got.LastAuthor {
		t.Errorf("LastAuthor: want %v got %v", want.LastAuthor, got.LastAuthor)
	}
	if want.SandboxID != got.SandboxID {
		t.Errorf("SandboxID: want %v got %v", want.SandboxID, got.SandboxID)
	}
	if want.HerdrAgent != got.HerdrAgent {
		t.Errorf("HerdrAgent: want %v got %v", want.HerdrAgent, got.HerdrAgent)
	}
	if want.AgentSessionID != got.AgentSessionID {
		t.Errorf("AgentSessionID: want %v got %v", want.AgentSessionID, got.AgentSessionID)
	}
	if want.Status != got.Status {
		t.Errorf("Status: want %v got %v", want.Status, got.Status)
	}
	if want.TurnID != got.TurnID {
		t.Errorf("TurnID: want %v got %v", want.TurnID, got.TurnID)
	}
	if want.StateChangeSeq != got.StateChangeSeq {
		t.Errorf("StateChangeSeq: want %v got %v", want.StateChangeSeq, got.StateChangeSeq)
	}
	wc := want.CreatedAt.UTC().Truncate(time.Millisecond)
	gc := got.CreatedAt.UTC().Truncate(time.Millisecond)
	if !wc.Equal(gc) {
		t.Errorf("CreatedAt: want %v got %v", wc, gc)
	}
	wl := want.LastActivityAt.UTC().Truncate(time.Millisecond)
	gl := got.LastActivityAt.UTC().Truncate(time.Millisecond)
	if !wl.Equal(gl) {
		t.Errorf("LastActivityAt: want %v got %v", wl, gl)
	}
	if want.PreviewSlot != got.PreviewSlot {
		t.Errorf("PreviewSlot: want %v got %v", want.PreviewSlot, got.PreviewSlot)
	}
}

// ensure compile-time check that Fake satisfies TaskStore
var _ controller.TaskStore = (*Fake)(nil)
