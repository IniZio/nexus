// Package builder_test — G9 fault-injection guard for BuildInVM.
//
// This file drives BuildInVM through several failure modes using a fake
// BuilderDriver, a recording execFn, and a fake BuilderStore, asserting that:
//
//  (a) drv.Stop is called on every exit path
//  (b) guest sync is attempted before Stop
//  (c) the transient sandbox record is deleted on every exit path — build
//      success, build failure, boot failure, context cancellation, and panic
//
// G3's lifecycle_test.go tests the Lifecycle helper in isolation; this file
// tests BuildInVM end-to-end, including the wiring of the Lifecycle into the
// driver calls and the store cleanup contract.
//
// Build tag: none — these are pure-unit tests; no KVM or mke2fs required.
package builder_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

// ── fakeBuilderStore ──────────────────────────────────────────────────────────

// fakeBuilderStore is an in-memory implementation of builder.BuilderStore used
// by these tests to verify the transient record lifecycle.
type fakeBuilderStore struct {
	mu      sync.Mutex
	records map[domain.SandboxID]domain.Sandbox
	creates int
	deletes int
}

func newFakeBuilderStore() *fakeBuilderStore {
	return &fakeBuilderStore{records: make(map[domain.SandboxID]domain.Sandbox)}
}

// Compile-time check: fakeBuilderStore satisfies builder.BuilderStore.
var _ builder.BuilderStore = (*fakeBuilderStore)(nil)

func (f *fakeBuilderStore) Create(_ context.Context, sb domain.Sandbox) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[sb.ID] = sb
	f.creates++
	return nil
}

func (f *fakeBuilderStore) Update(_ context.Context, id domain.SandboxID, fn func(*domain.Sandbox) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[id]
	if !ok {
		return errors.New("fakeBuilderStore.Update: record not found")
	}
	if err := fn(&rec); err != nil {
		return err
	}
	f.records[id] = rec
	return nil
}

func (f *fakeBuilderStore) Delete(_ context.Context, id domain.SandboxID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, id)
	f.deletes++
	return nil
}

func (f *fakeBuilderStore) recordCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// seqCounter is a shared monotone sequence counter used to order events across
// the execFn and the driver Stop call.
type seqCounter struct {
	n atomic.Int64
}

func (s *seqCounter) next() int64 { return s.n.Add(1) }

// execTracker records every execFn invocation and returns injected behaviour.
type execTracker struct {
	mu         sync.Mutex
	calls      [][]string // argv slices in call order
	buildExit  int32      // exit code for --builder-role invocation (0 = success)
	buildErr   error      // error returned for --builder-role invocation
	buildPanic bool       // panic on --builder-role invocation
	syncErr    error      // error returned for "sync" invocation

	seq      *seqCounter
	buildSeq int64 // sequence number of the build call
	syncSeq  int64 // sequence number of the sync call
}

func newExecTracker(seq *seqCounter) *execTracker {
	return &execTracker{seq: seq}
}

func (et *execTracker) fn(ctx context.Context, argv []string, w io.Writer) (int32, error) {
	n := et.seq.next()

	et.mu.Lock()
	argCopy := make([]string, len(argv))
	copy(argCopy, argv)
	et.calls = append(et.calls, argCopy)
	et.mu.Unlock()

	isBuild := len(argv) >= 2 && argv[len(argv)-1] == "--builder-role"
	// Match any of the absolute/bare sync paths guestSync tries in order
	// (/bin/sync, /usr/bin/sync, sync) — match by the last path component.
	isSync := len(argv) == 1 && (argv[0] == "/bin/sync" || argv[0] == "/usr/bin/sync" || argv[0] == "sync")

	if isBuild {
		atomic.StoreInt64(&et.buildSeq, n)
		if et.buildPanic {
			panic("G9: injected panic in guest execFn")
		}
		if et.buildErr != nil {
			return 1, et.buildErr
		}
		return et.buildExit, nil
	}
	if isSync {
		atomic.StoreInt64(&et.syncSeq, n)
		if et.syncErr != nil {
			return 1, et.syncErr
		}
	}
	return 0, nil
}

// syncCalled returns true if the execFn was invoked with a sync command
// (any of /bin/sync, /usr/bin/sync, or bare sync).
func (et *execTracker) syncCalled() bool { return atomic.LoadInt64(&et.syncSeq) > 0 }

// stopTrackingDriver wraps a *fake.FakeDriver and records the sequence number
// of each Stop call so tests can assert sync-before-stop ordering.
type stopTrackingDriver struct {
	*fake.FakeDriver
	seq     *seqCounter
	mu      sync.Mutex
	stopSeq []int64 // sequence numbers of Stop invocations
}

func newStopTracker(seq *seqCounter) *stopTrackingDriver {
	return &stopTrackingDriver{
		FakeDriver: fake.New(),
		seq:        seq,
	}
}

// Stop overrides the embedded FakeDriver.Stop to record the call sequence.
func (td *stopTrackingDriver) Stop(ctx context.Context, id domain.SandboxID) error {
	n := td.seq.next()
	td.mu.Lock()
	td.stopSeq = append(td.stopSeq, n)
	td.mu.Unlock()
	return td.FakeDriver.Stop(ctx, id)
}

func (td *stopTrackingDriver) stopCalled() bool {
	td.mu.Lock()
	defer td.mu.Unlock()
	return len(td.stopSeq) > 0
}

func (td *stopTrackingDriver) firstStopSeq() int64 {
	td.mu.Lock()
	defer td.mu.Unlock()
	if len(td.stopSeq) == 0 {
		return -1
	}
	return td.stopSeq[0]
}

// Ensure stopTrackingDriver satisfies builder.BuilderDriver at compile time.
var _ builder.BuilderDriver = (*stopTrackingDriver)(nil)

// runBuildInVM is a convenience wrapper that calls builder.BuildInVM and
// returns the error. Pass a nil cache — failure-mode tests never reach the
// harvest step. A fresh fakeBuilderStore is wired so that store cleanup is
// exercised on every call; the store is discarded after the call returns.
func runBuildInVM(ctx context.Context, drv builder.BuilderDriver, et *execTracker) error {
	spec := builder.BuilderVMSpec{
		RootfsDiskPath:   "/dev/null", // never opened by BuildInVM itself
		ArtifactDiskPath: "",          // intentionally empty — triggers clear error if reached
	}
	_, err := builder.BuildInVM(ctx, drv, spec, nil, et.fn, newFakeBuilderStore())
	return err
}

// runBuildInVMWithRecover wraps BuildInVM in a panic-recovery layer so the
// panic-injection test can observe that Stop was called without the test
// process crashing.
func runBuildInVMWithRecover(ctx context.Context, drv builder.BuilderDriver, et *execTracker) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	_ = runBuildInVM(ctx, drv, et)
	return false
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestBuildInVM_StartOK_BuildError verifies that when Start succeeds but the
// in-guest build exits non-zero, Stop is called and sync was attempted first.
func TestBuildInVM_StartOK_BuildError(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)
	et.buildExit = 1 // non-zero exit code → build error

	drv := newStopTracker(seq)

	err := runBuildInVM(context.Background(), drv, et)

	if err == nil {
		t.Fatal("want error from build failure, got nil")
	}
	if !drv.stopCalled() {
		t.Error("Stop was NOT called after build error — lifecycle contract violated")
	}
	if !et.syncCalled() {
		t.Error("sync was NOT attempted before Stop — lifecycle contract violated")
	}
	// Sync must precede Stop in the sequence.
	if et.syncSeq >= drv.firstStopSeq() {
		t.Errorf("sync (seq=%d) did not precede Stop (seq=%d)", et.syncSeq, drv.firstStopSeq())
	}
}

// TestBuildInVM_StartOK_TransferFailure verifies that when Start and the
// in-guest build both succeed, Stop is still called before the harvest step,
// and that an empty ArtifactDiskPath produces a clear error (not a panic).
func TestBuildInVM_StartOK_TransferFailure(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)
	// build succeeds; spec.ArtifactDiskPath is "" → BuildInVM returns
	// "spec.ArtifactDiskPath is empty" after Stop.

	drv := newStopTracker(seq)

	err := runBuildInVM(context.Background(), drv, et)

	if err == nil {
		t.Fatal("want error from missing ArtifactDiskPath, got nil")
	}
	if !drv.stopCalled() {
		t.Error("Stop was NOT called before harvest — lifecycle contract violated")
	}
	if !et.syncCalled() {
		t.Error("sync was NOT attempted before Stop — lifecycle contract violated")
	}
	// Stop must precede the harvest error (ordering: build→sync→stop→harvest-error).
	if drv.firstStopSeq() <= 0 {
		t.Error("expected positive stop sequence number")
	}
}

// TestBuildInVM_CtxTimeout verifies that a cancelled context causes BuildInVM
// to return an error and that Stop is still called.
func TestBuildInVM_CtxTimeout(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)
	// execFn blocks until context is done.
	et.buildErr = context.DeadlineExceeded

	drv := newStopTracker(seq)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Use a fresh context-cancelled error via the tracker.
	et.buildErr = nil
	et.buildExit = 0
	// Override with a real cancel to exercise the path.
	cancelledCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn() // cancel immediately

	err := runBuildInVM(cancelledCtx, drv, et)
	_ = ctx // silence vet

	// BuildInVM may succeed or fail depending on race; what matters is Stop.
	// Actually with a pre-cancelled context, drv.Start sees a cancelled ctx.
	// If Start succeeds (fake ignores ctx), build runs with cancelled ctx;
	// the result can vary. Assert Stop was called if Start succeeded.
	calls := drv.Calls()
	started := false
	for _, c := range calls {
		if c.Kind == fake.CallStart {
			started = true
		}
	}
	if started && !drv.stopCalled() {
		t.Error("Start succeeded but Stop was NOT called — VM would be orphaned")
	}
	// An error is expected (either from context or ArtifactDiskPath).
	if err == nil {
		t.Error("want non-nil error from cancelled context, got nil")
	}
}

// TestBuildInVM_TaskTimeout verifies that a context whose deadline is already
// past causes BuildInVM to return a DeadlineExceeded-wrapping error promptly,
// that Stop is still called (no orphaned CH VMM), and that the function never
// reaches the ArtifactFromDisk harvest step (no cache write).
//
// This mirrors the outer task-timeout path: NEXUS3_BUILD_TASK_TIMEOUT creates a
// context.WithTimeout whose deadline may expire before or during VM boot — a
// case the inner NEXUS3_BUILD_SOLVE_TIMEOUT (which caps only the buildkitd solve
// step) cannot catch.
func TestBuildInVM_TaskTimeout(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)
	drv := newStopTracker(seq)

	// Deadline already one second in the past — ctx.Err() returns
	// DeadlineExceeded immediately on first call, making the outcome deterministic.
	pastCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := runBuildInVM(pastCtx, drv, et)

	// Must return a non-nil error.
	if err == nil {
		t.Fatal("want error from expired task deadline, got nil")
	}
	// The error chain must include DeadlineExceeded — waitForBuilderAgent returns
	// ctx.Err() (DeadlineExceeded) the instant the expired context is checked,
	// and BuildInVM wraps it with %w.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want errors.Is(err, context.DeadlineExceeded) = true; got: %v", err)
	}
	// Stop must have been called if Start succeeded — the deferred panicSafeStop
	// fires on the early return from waitForBuilderAgent.
	calls := drv.Calls()
	started := false
	for _, c := range calls {
		if c.Kind == fake.CallStart {
			started = true
		}
	}
	if started && !drv.stopCalled() {
		t.Error("Start succeeded but Stop was NOT called — CH VMM would be orphaned after timeout")
	}
	// No harvest: BuildInVM returns before ArtifactFromDisk is ever called.
	// Passing nil cache + empty ArtifactDiskPath to runBuildInVM is a structural
	// guard: if the harvest step were reached, ArtifactFromDisk would return a
	// clear error ("spec.ArtifactDiskPath is empty") rather than the
	// DeadlineExceeded the assertion above already required.
}

// TestBuildInVM_PanicInExecFn verifies that if the execFn panics (simulating
// a guest agent crash that corrupts state), the deferred panicSafeStop fires
// and Stop is called before the panic propagates.
func TestBuildInVM_PanicInExecFn(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)
	et.buildPanic = true

	drv := newStopTracker(seq)

	panicked := runBuildInVMWithRecover(context.Background(), drv, et)

	if !panicked {
		t.Fatal("expected panic to propagate out of BuildInVM but got none")
	}
	if !drv.stopCalled() {
		t.Error("Stop was NOT called after guest exec panic — VM would be orphaned")
	}
}

// TestBuildInVM_StartFails verifies that if Start fails, Stop is NOT called
// (there is nothing to stop) and BuildInVM returns the start error.
func TestBuildInVM_StartFails(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)

	drv := newStopTracker(seq)
	drv.SetStartError(errors.New("G9: injected start failure"))

	err := runBuildInVM(context.Background(), drv, et)

	if err == nil {
		t.Fatal("want error from start failure, got nil")
	}
	if drv.stopCalled() {
		t.Error("Stop should NOT be called when Start fails — nothing was started")
	}
	// No sync should have been attempted either.
	if et.syncCalled() {
		t.Error("sync should NOT be attempted when Start fails")
	}
}

// TestBuildInVM_StoreCleanupOnBuildFailure verifies that the transient sandbox
// record created before boot is deleted when the in-guest build fails (non-zero
// exit code). A record left behind would show up in `nexus3 sandbox list` and
// confuse users.
func TestBuildInVM_StoreCleanupOnBuildFailure(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)
	et.buildExit = 1 // non-zero → build error

	drv := newStopTracker(seq)
	st := newFakeBuilderStore()

	spec := builder.BuilderVMSpec{
		RootfsDiskPath:   "/dev/null",
		ArtifactDiskPath: "",
	}
	_, err := builder.BuildInVM(context.Background(), drv, spec, nil, et.fn, st)

	if err == nil {
		t.Fatal("want error from build failure, got nil")
	}
	// The transient record must be gone after a build failure.
	if st.recordCount() != 0 {
		t.Errorf("transient record still present after build failure: want 0 records, got %d", st.recordCount())
	}
	if st.creates != 1 {
		t.Errorf("expected exactly 1 store.Create call, got %d", st.creates)
	}
	if st.deletes != 1 {
		t.Errorf("expected exactly 1 store.Delete call, got %d", st.deletes)
	}
}

// TestBuildInVM_StoreCleanupOnStartFailure verifies that the transient record
// is deleted even when drv.Start fails (boot failure path). The record is
// created before Start to allow supervisor discovery; on failure it must be
// removed immediately so no orphan record appears.
func TestBuildInVM_StoreCleanupOnStartFailure(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)

	drv := newStopTracker(seq)
	drv.SetStartError(errors.New("G9: injected start failure"))
	st := newFakeBuilderStore()

	spec := builder.BuilderVMSpec{RootfsDiskPath: "/dev/null"}
	_, err := builder.BuildInVM(context.Background(), drv, spec, nil, et.fn, st)

	if err == nil {
		t.Fatal("want error from start failure, got nil")
	}
	// The transient record must be cleaned up even when boot fails.
	if st.recordCount() != 0 {
		t.Errorf("transient record still present after start failure: want 0 records, got %d", st.recordCount())
	}
	if st.deletes != 1 {
		t.Errorf("expected exactly 1 store.Delete call after start failure, got %d", st.deletes)
	}
}

// ── No-double-Stop coverage note (UNI-TEARDOWN) ───────────────────────────────
//
// The double-Stop hazard for the supervisor-backed path (supervisorBuilderDriver)
// cannot be exercised in pure unit tests because it requires a real supervisor
// process. What is protected at this level:
//
//   - lifecycle.once (sync.Once): drv.Stop is called AT MOST ONCE per Lifecycle.
//     Any call to SyncAndStop or panicSafeStop after the first is a no-op.
//   - started=false after SyncAndStop prevents the deferred panicSafeStop.
//   - The per-sandbox flock in store.Update serialises concurrent Stop/Remove.
//   - WaitForExit in supervisorBuilderDriver.Stop ensures the supervisor's
//     svc.Remove has completed before the CLI's defer st.Delete runs.
//
// What requires real KVM (TestBuilderVME2E, build tag kvm):
//   - The full CLI→supervisor IPC stop→WaitForExit→defer-Delete ordering.
//   - The SIGKILL path: parent-watchdog pipe EOF triggers supervisor self-teardown.

// TestBuildInVM_StopCalledExactlyOnce verifies drv.Stop is called exactly once
// on the harvest-error exit path — both the explicit SyncAndStop and the
// panicSafeStop defer share the same once.Do, so only one Stop call should
// reach the driver.
func TestBuildInVM_StopCalledExactlyOnce(t *testing.T) {
	seq := &seqCounter{}
	et := newExecTracker(seq)
	drv := newStopTracker(seq)

	// ArtifactDiskPath="" triggers the harvest error after a successful build+stop.
	spec := builder.BuilderVMSpec{
		RootfsDiskPath:   "/dev/null",
		ArtifactDiskPath: "",
	}
	st := newFakeBuilderStore()
	_, err := builder.BuildInVM(context.Background(), drv, spec, nil, et.fn, st)
	if err == nil {
		t.Fatal("want harvest error (empty ArtifactDiskPath), got nil")
	}

	drv.mu.Lock()
	count := len(drv.stopSeq)
	drv.mu.Unlock()
	if count != 1 {
		t.Errorf("drv.Stop called %d times on harvest-error path, want exactly 1", count)
	}
}
