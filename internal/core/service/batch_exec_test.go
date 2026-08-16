package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

// seedBatchSandbox stores a domain.Sandbox with the given motive in svc's
// store and returns it.
func seedBatchSandbox(t *testing.T, ctx context.Context, svc *Service, motiveID string) domain.Sandbox {
	t.Helper()
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "batch-" + domain.NewSandboxID().String(),
		Project: "testproj",
		Labels:  map[string]string{"motive": motiveID},
		State:   domain.Created,
	}
	if err := svc.store.Create(ctx, sb); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return sb
}

// makeSuccessExecFn returns an execFn that always succeeds, recording the
// sandbox IDs it was called with.
func makeSuccessExecFn() (batchExecOneFn, func() []domain.SandboxID) {
	var mu sync.Mutex
	var called []domain.SandboxID
	fn := func(_ context.Context, sb domain.Sandbox, _ []string) (int32, []byte, []byte, error) {
		mu.Lock()
		called = append(called, sb.ID)
		mu.Unlock()
		return 0, []byte("ok-" + sb.ID.String()), nil, nil
	}
	return fn, func() []domain.SandboxID {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]domain.SandboxID, len(called))
		copy(cp, called)
		return cp
	}
}

// TestBatchExec_AllSucceed verifies that all sandboxes are exec'd and outcomes
// are all zero-exit.
func TestBatchExec_AllSucceed(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	const motive = "batch-all-ok"
	sb1 := seedBatchSandbox(t, ctx, svc, motive)
	sb2 := seedBatchSandbox(t, ctx, svc, motive)
	sb3 := seedBatchSandbox(t, ctx, svc, motive)

	execFn, getCalled := makeSuccessExecFn()
	result, err := svc.batchExecWith(ctx, motive, BatchExecOptions{
		Argv:     []string{"true"},
		Parallel: 2,
	}, execFn)
	if err != nil {
		t.Fatalf("batchExecWith returned unexpected error: %v", err)
	}

	if len(result.Outcomes) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(result.Outcomes))
	}
	for _, o := range result.Outcomes {
		if o.Err != nil {
			t.Errorf("sandbox %s: unexpected error: %v", o.SandboxID, o.Err)
		}
		if o.ExitCode != 0 {
			t.Errorf("sandbox %s: exit %d, want 0", o.SandboxID, o.ExitCode)
		}
	}

	// Verify all three sandboxes were exec'd.
	called := getCalled()
	wantIDs := map[domain.SandboxID]bool{sb1.ID: true, sb2.ID: true, sb3.ID: true}
	for _, id := range called {
		if !wantIDs[id] {
			t.Errorf("execFn called for unexpected sandbox %s", id)
		}
		delete(wantIDs, id)
	}
	for id := range wantIDs {
		t.Errorf("sandbox %s was never exec'd", id)
	}

	if result.HasFailures() {
		t.Error("HasFailures() = true, want false")
	}
}

// TestBatchExec_PartialFailure_SiblingsComplete is the critical M2-AC1 test.
// It verifies that:
//  1. A failing sandbox does not abort its siblings — every sandbox runs.
//  2. Per-sandbox exit codes are reported in the result.
//  3. The aggregate error is non-nil.
func TestBatchExec_PartialFailure_SiblingsComplete(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	const motive = "batch-partial-fail"
	sb1 := seedBatchSandbox(t, ctx, svc, motive)
	sb2 := seedBatchSandbox(t, ctx, svc, motive)
	sb3 := seedBatchSandbox(t, ctx, svc, motive)

	// sb2 exits non-zero; sb1 and sb3 succeed.
	failID := sb2.ID
	var mu sync.Mutex
	execd := make(map[domain.SandboxID]bool)
	execFn := func(_ context.Context, sb domain.Sandbox, _ []string) (int32, []byte, []byte, error) {
		mu.Lock()
		execd[sb.ID] = true
		mu.Unlock()
		if sb.ID == failID {
			return 1, nil, []byte("intentional failure"), nil
		}
		return 0, []byte("ok"), nil, nil
	}

	result, aggErr := svc.batchExecWith(ctx, motive, BatchExecOptions{
		Argv:     []string{"test-cmd"},
		Parallel: 2,
	}, execFn)

	// Aggregate error must be non-nil.
	if aggErr == nil {
		t.Fatal("expected non-nil aggregate error when a sandbox fails, got nil")
	}

	// All three sandboxes must have been exec'd.
	mu.Lock()
	defer mu.Unlock()
	for _, sb := range []domain.Sandbox{sb1, sb2, sb3} {
		if !execd[sb.ID] {
			t.Errorf("sandbox %s was not exec'd (sibling abort detected)", sb.ID)
		}
	}

	// Result must contain all three outcomes.
	if len(result.Outcomes) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(result.Outcomes))
	}

	// The failing sandbox must have exit code 1.
	var foundFail bool
	for _, o := range result.Outcomes {
		if o.SandboxID == failID {
			foundFail = true
			if o.ExitCode != 1 {
				t.Errorf("failing sandbox: exit %d, want 1", o.ExitCode)
			}
			if o.Err != nil {
				t.Errorf("failing sandbox: Err = %v, want nil (non-zero exit is not an Err)", o.Err)
			}
		} else {
			if o.ExitCode != 0 {
				t.Errorf("sibling sandbox %s: exit %d, want 0", o.SandboxID, o.ExitCode)
			}
		}
	}
	if !foundFail {
		t.Error("failing sandbox not found in outcomes")
	}

	if !result.HasFailures() {
		t.Error("HasFailures() = false, want true")
	}
}

// TestBatchExec_PartialFailure_ExecError verifies that a transport-level exec
// error (Err != nil) is also treated as a failure without aborting siblings.
func TestBatchExec_PartialFailure_ExecError(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	const motive = "batch-exec-err"
	sb1 := seedBatchSandbox(t, ctx, svc, motive)
	sb2 := seedBatchSandbox(t, ctx, svc, motive)

	failID := sb1.ID
	var mu sync.Mutex
	execd := make(map[domain.SandboxID]bool)
	execFn := func(_ context.Context, sb domain.Sandbox, _ []string) (int32, []byte, []byte, error) {
		mu.Lock()
		execd[sb.ID] = true
		mu.Unlock()
		if sb.ID == failID {
			return 0, nil, nil, errors.New("injected dial error")
		}
		return 0, []byte("ok"), nil, nil
	}

	result, aggErr := svc.batchExecWith(ctx, motive, BatchExecOptions{
		Argv: []string{"cmd"},
	}, execFn)

	if aggErr == nil {
		t.Fatal("expected aggregate error, got nil")
	}

	// Both sandboxes must have been exec'd.
	mu.Lock()
	defer mu.Unlock()
	for _, sb := range []domain.Sandbox{sb1, sb2} {
		if !execd[sb.ID] {
			t.Errorf("sandbox %s was not exec'd", sb.ID)
		}
	}

	// Failing sandbox must have Err set.
	for _, o := range result.Outcomes {
		if o.SandboxID == failID && o.Err == nil {
			t.Error("failing sandbox: Err == nil, want injected error")
		}
	}
}

// TestBatchExec_BoundedParallelism is the M2-AC1 proof that at most N
// sandboxes exec concurrently. With parallel=2 and 4 sandboxes, the test:
//  1. Waits until exactly 2 goroutines are active (started but not released).
//  2. Confirms no more than 2 are running at that instant.
//  3. Releases them and repeats — confirming the remaining 2 run in a second wave.
func TestBatchExec_BoundedParallelism(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	const (
		motive     = "batch-bounded"
		numSB      = 4
		parallel   = 2
	)
	for i := 0; i < numSB; i++ {
		seedBatchSandbox(t, ctx, svc, motive)
	}

	// inFlight tracks concurrent active executions.
	var inFlight atomic.Int32
	// maxSeen records the peak concurrent count.
	var maxSeen atomic.Int32

	// gate controls when each exec completes. A buffered channel with capacity
	// numSB is used so senders never block; we drain it to release goroutines.
	gate := make(chan struct{}, numSB)
	// started signals when each goroutine has incremented inFlight.
	started := make(chan struct{}, numSB)

	execFn := func(_ context.Context, sb domain.Sandbox, _ []string) (int32, []byte, []byte, error) {
		cur := inFlight.Add(1)
		// Record the peak.
		for {
			old := maxSeen.Load()
			if cur <= old || maxSeen.CompareAndSwap(old, cur) {
				break
			}
		}
		started <- struct{}{} // signal: this goroutine is now running
		<-gate                // wait until released by the test
		inFlight.Add(-1)
		return 0, nil, nil, nil
	}

	// Run batchExecWith in the background so the test can interleave.
	done := make(chan struct{})
	var batchErr error
	go func() {
		defer close(done)
		_, batchErr = svc.batchExecWith(ctx, motive, BatchExecOptions{
			Argv:     []string{"sleep"},
			Parallel: parallel,
		}, execFn)
	}()

	// Wait for exactly `parallel` goroutines to have started. The semaphore
	// prevents more than `parallel` from proceeding concurrently, so the
	// remaining numSB-parallel goroutines are blocked on the semaphore.
	for i := 0; i < parallel; i++ {
		select {
		case <-started:
		case <-done:
			t.Fatal("batch exec finished before all goroutines started (unexpected)")
		}
	}

	// At this exact moment, `parallel` goroutines are running and
	// numSB-parallel are blocked waiting for a semaphore slot.
	cur := inFlight.Load()
	if int(cur) > parallel {
		t.Errorf("parallelism violated: %d goroutines in flight, limit is %d", cur, parallel)
	}

	// Release the first wave to allow the second wave to start.
	for i := 0; i < parallel; i++ {
		gate <- struct{}{}
	}

	// Wait for the second wave to start.
	for i := 0; i < numSB-parallel; i++ {
		select {
		case <-started:
		case <-done:
			t.Fatal("batch exec finished before second wave started")
		}
	}

	// Release the second wave.
	for i := 0; i < numSB-parallel; i++ {
		gate <- struct{}{}
	}

	<-done
	if batchErr != nil {
		t.Fatalf("batchExecWith returned unexpected error: %v", batchErr)
	}

	// The global peak must never exceed parallel.
	if peak := maxSeen.Load(); int(peak) > parallel {
		t.Errorf("peak concurrent execs = %d, limit = %d", peak, parallel)
	}
}

// TestBatchExec_EmptyMotive verifies that an unknown motive returns an empty
// result and nil error (the store contract for unknown motives).
func TestBatchExec_EmptyMotive(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	execFn := func(_ context.Context, _ domain.Sandbox, _ []string) (int32, []byte, []byte, error) {
		t.Error("execFn called for empty motive")
		return 0, nil, nil, nil
	}

	result, err := svc.batchExecWith(ctx, "no-such-motive", BatchExecOptions{
		Argv: []string{"ls"},
	}, execFn)
	if err != nil {
		t.Fatalf("empty motive: unexpected error: %v", err)
	}
	if len(result.Outcomes) != 0 {
		t.Errorf("empty motive: got %d outcomes, want 0", len(result.Outcomes))
	}
}

// TestBatchExec_DefaultParallel verifies that Parallel=0 defaults to
// DefaultBatchParallel (not an unbounded zero-capacity semaphore, which would
// deadlock).
func TestBatchExec_DefaultParallel(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	const motive = "batch-default-parallel"
	for i := 0; i < 3; i++ {
		seedBatchSandbox(t, ctx, svc, motive)
	}

	execFn := func(_ context.Context, _ domain.Sandbox, _ []string) (int32, []byte, []byte, error) {
		return 0, nil, nil, nil
	}

	// Parallel=0 must not deadlock.
	result, err := svc.batchExecWith(ctx, motive, BatchExecOptions{
		Argv:     []string{"true"},
		Parallel: 0, // must default to DefaultBatchParallel
	}, execFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Outcomes) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(result.Outcomes))
	}
}
