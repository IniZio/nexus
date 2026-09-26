package controller

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrRouterClosed is returned by Handle/Tick after Close is called.
var ErrRouterClosed = errors.New("controller: router closed")

type RouterOption func(*Router)

func WithCommandHandler(h Handler) RouterOption {
	return func(r *Router) { r.commandHandler = h }
}

func WithClock(now func() time.Time) RouterOption {
	return func(r *Router) { r.clock = now }
}

// threadQueue is a FIFO queue per thread; at most one goroutine drains it (serial per thread).
type threadQueue struct {
	mu      sync.Mutex
	jobs    []func(context.Context)
	running bool
}

// Router serializes events per ThreadRef and dispatches into Flows.
type Router struct {
	store          TaskStore
	flows          Flows
	commandHandler Handler
	clock          func() time.Time

	workerCtx context.Context

	mu      sync.Mutex
	threads map[ThreadRef]*threadQueue
	closed  bool

	wg sync.WaitGroup
}

// NewRouter constructs a Router; workers use context.Background so a cancelled Handle ctx does not abort in-flight work.
func NewRouter(store TaskStore, flows Flows, opts ...RouterOption) *Router {
	r := &Router{
		store:     store,
		flows:     flows,
		clock:     time.Now,
		threads:   make(map[ThreadRef]*threadQueue),
		workerCtx: context.Background(),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Handle dispatches slash commands immediately (bypassing the queue); all other events are enqueued on the thread's FIFO.
func (r *Router) Handle(ctx context.Context, ev Event) error {
	if ev.Kind == EventSlashCommand {
		if r.commandHandler == nil {
			return nil
		}
		return r.commandHandler(ctx, ev)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrRouterClosed
	}
	q := r.threads[ev.ThreadRef]
	if q == nil {
		q = &threadQueue{}
		r.threads[ev.ThreadRef] = q
	}
	r.mu.Unlock()

	r.enqueue(q, func(wctx context.Context) {
		r.processEvent(wctx, ev)
	})
	return nil
}

// Tick fetches idle/paused/waiting tasks and enqueues OnTick on each thread queue.
func (r *Router) Tick(ctx context.Context, idleBefore time.Time) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrRouterClosed
	}
	r.mu.Unlock()

	tasks, err := r.store.ListIdle(ctx, idleBefore)
	if err != nil {
		return err
	}
	now := r.clock()
	for _, t := range tasks {
		t := t
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return ErrRouterClosed
		}
		q := r.threads[t.ThreadRef]
		if q == nil {
			q = &threadQueue{}
			r.threads[t.ThreadRef] = q
		}
		r.mu.Unlock()
		r.enqueue(q, func(wctx context.Context) {
			if fErr := r.flows.OnTick(wctx, t, now); fErr != nil {
				slog.Error("router: OnTick", "ref", t.ThreadRef, "err", fErr)
			}
		})
	}
	return nil
}

// Close stops accepting new work and blocks until all in-flight queues drain.
func (r *Router) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Router) enqueue(q *threadQueue, fn func(context.Context)) {
	q.mu.Lock()
	q.jobs = append(q.jobs, fn)
	if !q.running {
		q.running = true
		r.wg.Add(1)
		go r.runQueue(q)
	}
	q.mu.Unlock()
}

func (r *Router) runQueue(q *threadQueue) {
	defer r.wg.Done()
	for {
		q.mu.Lock()
		if len(q.jobs) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		fn := q.jobs[0]
		q.jobs = q.jobs[1:]
		q.mu.Unlock()
		fn(r.workerCtx)
	}
}

func (r *Router) processEvent(ctx context.Context, ev Event) {
	task, err := r.store.Get(ctx, ev.ThreadRef)
	if errors.Is(err, ErrNotFound) {
		if ev.Kind == EventReply {
			return // no session — drop
		}
		now := r.clock()
		task = Task{
			ThreadRef:      ev.ThreadRef,
			Owner:          ev.User,
			LastAuthor:     ev.User,
			Status:         StatusStarting,
			CreatedAt:      now,
			LastActivityAt: now,
		}
		if uErr := r.store.Upsert(ctx, task); uErr != nil {
			slog.Error("router: upsert task", "ref", ev.ThreadRef, "err", uErr)
			return
		}
		if fErr := r.flows.OnMention(ctx, task, ev); fErr != nil {
			slog.Error("router: OnMention (new)", "ref", ev.ThreadRef, "err", fErr)
		}
		return
	}
	if err != nil {
		slog.Error("router: store.Get", "ref", ev.ThreadRef, "err", err)
		return
	}

	if tErr := r.store.TouchActivity(ctx, ev.ThreadRef, ev.User); tErr != nil {
		slog.Error("router: TouchActivity", "ref", ev.ThreadRef, "err", tErr)
		return
	}
	task, err = r.store.Get(ctx, ev.ThreadRef)
	if err != nil {
		slog.Error("router: store.Get (post-touch)", "ref", ev.ThreadRef, "err", err)
		return
	}

	switch task.Status {
	case StatusClosed, StatusFailed:
		if ev.Kind == EventMention {
			if fErr := r.flows.OnMention(ctx, task, ev); fErr != nil {
				slog.Error("router: OnMention (reopen)", "ref", ev.ThreadRef, "err", fErr)
			}
		}
		// Reply into closed/failed thread → drop.
	default:
		if fErr := r.flows.OnReply(ctx, task, ev); fErr != nil {
			slog.Error("router: OnReply", "ref", ev.ThreadRef, "err", fErr)
		}
	}
}

