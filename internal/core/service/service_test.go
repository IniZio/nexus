package service_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func newFileStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return st
}

func newSvc(t *testing.T) *service.Service {
	t.Helper()
	return service.New(newFileStore(t), fake.New(), lifecycle.New())
}

func ctx() context.Context { return context.Background() }

// ── callLog records interleaved store/driver calls in order ──────────────────

type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) record(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.calls))
	copy(out, l.calls)
	return out
}

// recordingStore wraps any Store and records SetRemovalMarker and Delete calls
// into a shared callLog, enabling ordering assertions across store + driver.
type recordingStore struct {
	store.Store
	log *callLog
}

func (r *recordingStore) SetRemovalMarker(ctx context.Context, id domain.SandboxID) error {
	r.log.record("SetRemovalMarker")
	return r.Store.SetRemovalMarker(ctx, id)
}

func (r *recordingStore) Delete(ctx context.Context, id domain.SandboxID) error {
	r.log.record("Delete")
	return r.Store.Delete(ctx, id)
}

// recordingDriver wraps a driver.Driver and records Stop calls into the same
// shared callLog as recordingStore, so ordering across both can be asserted.
type recordingDriver struct {
	driver.Driver
	log *callLog
}

func (d *recordingDriver) Stop(ctx context.Context, id domain.SandboxID) error {
	d.log.record("driver.Stop")
	return d.Driver.Stop(ctx, id)
}

// Note: recordingDriver does NOT implement driver.PauseResumer. This is
// intentional: the ordering test only exercises Remove, which calls Stop.

// slowDriver wraps driver.Driver and sleeps briefly inside Start to widen the
// concurrency window between the fast-path state check and the Update lock,
// making the orphaned-VM bug detectable under the race detector.
type slowDriver struct {
	driver.Driver
}

func (s *slowDriver) Start(c context.Context, req driver.StartRequest) (string, error) {
	time.Sleep(10 * time.Millisecond)
	return s.Driver.Start(c, req)
}

// slowStopDriver wraps driver.Driver and sleeps briefly inside Stop, which is
// now called inside Remove's Update callback (holding the per-sandbox lock).
// This widens the concurrency window so concurrent Start goroutines have time
// to queue up and be rejected by the RemovalMarker check.
type slowStopDriver struct {
	driver.Driver
}

func (s *slowStopDriver) Stop(c context.Context, id domain.SandboxID) error {
	time.Sleep(10 * time.Millisecond)
	return s.Driver.Stop(c, id)
}

// ── basic CRUD ───────────────────────────────────────────────────────────────

func TestCreate_RoundTrip(t *testing.T) {
	svc := newSvc(t)

	sb, err := svc.Create(ctx(), "proj", "box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if sb.Project != "proj" || sb.Name != "box" {
		t.Errorf("identity: got %q/%q, want proj/box", sb.Project, sb.Name)
	}
	if sb.State != domain.Created {
		t.Errorf("state: got %q, want Created", sb.State)
	}
	if sb.RemoveOnExit {
		t.Error("RemoveOnExit should be false by default")
	}
}

func TestCreate_DuplicateHandle(t *testing.T) {
	svc := newSvc(t)

	if _, err := svc.Create(ctx(), "proj", "dup", service.CreateOptions{}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := svc.Create(ctx(), "proj", "dup", service.CreateOptions{})
	if err == nil {
		t.Fatal("expected error on duplicate handle, got nil")
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("want errors.Is(err, store.ErrAlreadyExists), got %v", err)
	}
}

func TestCreate_RemoveOnExit_Persisted(t *testing.T) {
	svc := newSvc(t)

	sb, err := svc.Create(ctx(), "proj", "rmbox", service.CreateOptions{RemoveOnExit: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !sb.RemoveOnExit {
		t.Error("RemoveOnExit should be true")
	}

	// Verify the flag survived round-trip to the store via List.
	all, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || !all[0].RemoveOnExit {
		t.Error("RemoveOnExit not persisted after round-trip through List")
	}
}

func TestCreate_EnvelopeImmutable(t *testing.T) {
	svc := newSvc(t)

	sb, err := svc.Create(ctx(), "proj", "env", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	original := sb.Envelope

	// Start the sandbox (fake driver, state → Running).
	started, err := svc.Start(ctx(), sb.ID.String())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !reflect.DeepEqual(started.Envelope, original) {
		t.Errorf("Start mutated Envelope: got %v, want %v", started.Envelope, original)
	}

	// Verify via List too.
	all, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || !reflect.DeepEqual(all[0].Envelope, original) {
		t.Errorf("Envelope changed after round-trip: got %v, want %v", all[0].Envelope, original)
	}
}

// ── List ─────────────────────────────────────────────────────────────────────

func TestList_Empty(t *testing.T) {
	svc := newSvc(t)
	all, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if all == nil {
		t.Error("List returned nil slice; want empty non-nil slice")
	}
	if len(all) != 0 {
		t.Errorf("List returned %d items, want 0", len(all))
	}
}

func TestList_Multiple(t *testing.T) {
	svc := newSvc(t)
	for i, name := range []string{"a", "b", "c"} {
		if _, err := svc.Create(ctx(), "p", name, service.CreateOptions{}); err != nil {
			t.Fatalf("Create[%d]: %v", i, err)
		}
	}
	all, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List returned %d items, want 3", len(all))
	}
}

// TestList_FilterBuilderRecords verifies that transient __builder records are
// hidden from List() output (UNI-TEARDOWN §listing-filter).
//
// Without this filter, a stale __builder record (e.g. left by a SIGKILL'd CLI
// before the supervisor's svc.Remove ran) would appear in `nexus3 sandbox list`
// alongside user-visible sandboxes.
func TestList_FilterBuilderRecords(t *testing.T) {
	// Insert one normal sandbox and one __builder record directly into the store
	// (bypassing service.Create so we can set Project = "__builder").
	st := newFileStore(t)
	svc := service.New(st, fake.New(), lifecycle.New())

	// Normal user sandbox — must appear in List.
	if _, err := svc.Create(ctx(), "myproject", "mybox", service.CreateOptions{}); err != nil {
		t.Fatalf("Create user sandbox: %v", err)
	}

	// Transient builder record inserted directly into the store — must be hidden.
	transient := domain.Sandbox{
		ID:           domain.NewSandboxID(),
		Name:         "some-id",
		Project:      "__builder",
		State:        domain.Created,
		RemoveOnExit: true,
	}
	if err := st.Create(ctx(), transient); err != nil {
		t.Fatalf("store.Create transient: %v", err)
	}

	all, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List returned %d records, want 1 (the __builder record must be filtered)", len(all))
	}
	for _, sb := range all {
		if sb.Project == "__builder" {
			t.Errorf("List returned a __builder record (id=%s): must be filtered", sb.ID)
		}
	}
}

// ── Addressing ───────────────────────────────────────────────────────────────

func TestResolve_ExactID(t *testing.T) {
	svc := newSvc(t)
	sb, _ := svc.Create(ctx(), "proj", "box", service.CreateOptions{})

	// Use the exact ID string to start; resolves to the right sandbox.
	started, err := svc.Start(ctx(), sb.ID.String())
	if err != nil {
		t.Fatalf("Start by exact ID: %v", err)
	}
	if started.ID != sb.ID {
		t.Errorf("wrong sandbox: got %s, want %s", started.ID, sb.ID)
	}
}

func TestResolve_Handle(t *testing.T) {
	svc := newSvc(t)
	sb, _ := svc.Create(ctx(), "myproj", "mybox", service.CreateOptions{})

	// Remove by handle.
	if err := svc.Remove(ctx(), "myproj/mybox"); err != nil {
		t.Fatalf("Remove by handle: %v", err)
	}

	all, _ := svc.List(ctx())
	for _, s := range all {
		if s.ID == sb.ID {
			t.Error("sandbox still present after Remove by handle")
		}
	}
}

func TestResolve_Prefix(t *testing.T) {
	svc := newSvc(t)
	sb, _ := svc.Create(ctx(), "proj", "box", service.CreateOptions{})

	idStr := sb.ID.String()
	// Use a prefix that is unambiguously longer than "sb-".
	if len(idStr) < 6 {
		t.Skipf("ID too short for prefix test: %s", idStr)
	}
	prefix := idStr[:6] // e.g. "sb-0RQ"

	started, err := svc.Start(ctx(), prefix)
	if err != nil {
		t.Fatalf("Start by prefix: %v", err)
	}
	if started.ID != sb.ID {
		t.Errorf("wrong sandbox: got %s, want %s", started.ID, sb.ID)
	}
}

func TestResolve_AmbiguousPrefix_NamingCandidates(t *testing.T) {
	svc := newSvc(t)
	sb1, _ := svc.Create(ctx(), "proj", "one", service.CreateOptions{})
	sb2, _ := svc.Create(ctx(), "proj", "two", service.CreateOptions{})

	id1 := sb1.ID.String()
	id2 := sb2.ID.String()

	// Compute the longest common prefix of the two IDs.
	var prefixLen int
	for prefixLen < len(id1) && prefixLen < len(id2) && id1[prefixLen] == id2[prefixLen] {
		prefixLen++
	}
	if prefixLen <= len("sb-") {
		t.Skipf("IDs share no prefix beyond %q; cannot construct an ambiguous prefix (id1=%s id2=%s)",
			"sb-", id1, id2)
	}

	prefix := id1[:prefixLen] // longest common prefix — matches both

	err := svc.Remove(ctx(), prefix)
	if err == nil {
		t.Fatal("expected ambiguous-prefix error, got nil")
	}

	var ambig *domain.ErrAmbiguous
	if !errors.As(err, &ambig) {
		t.Errorf("expected *domain.ErrAmbiguous, got %T: %v", err, err)
	}

	// The error message must name the candidates.
	errStr := err.Error()
	if !containsSubstring(errStr, id1) || !containsSubstring(errStr, id2) {
		t.Errorf("error does not name both candidates; got: %s", errStr)
	}
}

// containsSubstring is a simple helper to avoid importing strings in test
// logic (the test file already imports it indirectly).
func containsSubstring(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}()
}

// ── Lifecycle transitions ─────────────────────────────────────────────────────

func TestLifecycle_StartStop(t *testing.T) {
	svc := newSvc(t)
	sb, _ := svc.Create(ctx(), "proj", "box", service.CreateOptions{})

	started, err := svc.Start(ctx(), sb.ID.String())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.State != domain.Running {
		t.Errorf("after Start: state = %q, want Running", started.State)
	}

	stopped, err := svc.Stop(ctx(), sb.ID.String())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.State != domain.Stopped {
		t.Errorf("after Stop: state = %q, want Stopped", stopped.State)
	}
}

func TestLifecycle_PauseResume(t *testing.T) {
	svc := newSvc(t)
	sb, _ := svc.Create(ctx(), "proj", "box", service.CreateOptions{})

	if _, err := svc.Start(ctx(), sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	paused, err := svc.Pause(ctx(), sb.ID.String())
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused.State != domain.Paused {
		t.Errorf("after Pause: state = %q, want Paused", paused.State)
	}

	resumed, err := svc.Resume(ctx(), sb.ID.String())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.State != domain.Running {
		t.Errorf("after Resume: state = %q, want Running", resumed.State)
	}
}

func TestLifecycle_IllegalTransition_RejectsBeforeDriver(t *testing.T) {
	// Attempting to resume a Stopped sandbox is illegal (no resume edge from Stopped).
	// The machine must reject it BEFORE any driver call is made.
	fakeDriver := fake.New()
	svc := service.New(newFileStore(t), fakeDriver, lifecycle.New())

	sb, _ := svc.Create(ctx(), "proj", "box", service.CreateOptions{})
	// Start then stop to reach Stopped state.
	if _, err := svc.Start(ctx(), sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := svc.Stop(ctx(), sb.ID.String()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	callsBefore := len(fakeDriver.Calls())
	_, err := svc.Resume(ctx(), sb.ID.String())
	if err == nil {
		t.Fatal("Resume on Stopped sandbox: expected error, got nil")
	}

	var illegalErr *lifecycle.IllegalTransitionError
	if !errors.As(err, &illegalErr) {
		t.Errorf("expected *lifecycle.IllegalTransitionError, got %T: %v", err, err)
	}

	// Error message should name valid alternatives.
	errStr := err.Error()
	if !containsSubstring(errStr, "start") {
		t.Errorf("illegal transition error does not mention legal trigger 'start': %s", errStr)
	}

	// Driver must NOT have been called for the illegal Resume.
	callsAfter := len(fakeDriver.Calls())
	if callsAfter > callsBefore {
		t.Errorf("driver was called despite illegal transition (calls before=%d, after=%d)",
			callsBefore, callsAfter)
	}
}

// ── Credential deregistrar ───────────────────────────────────────────────────

// spyDeregistrar records every Deregister call for assertion in unit tests.
type spyDeregistrar struct {
	mu  sync.Mutex
	got []domain.SandboxID
}

func (spy *spyDeregistrar) Deregister(id domain.SandboxID) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.got = append(spy.got, id)
}

func (spy *spyDeregistrar) calls() []domain.SandboxID {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	out := make([]domain.SandboxID, len(spy.got))
	copy(out, spy.got)
	return out
}

// TestRemove_CredDeregistrar_AndRevoke verifies two things added by the S4
// lifecycle fix:
//
//  1. RegisterCredDeregistrar + Remove: Deregister is called exactly once with
//     the sandbox ID when svc.Remove runs (discriminating — fails if
//     dropDeregistrar is removed from Remove).
//
//  2. broker.Revoke: after Remove, broker.SetRealToken for the removed sandbox
//     returns an error, proving the broker scope was revoked (discriminating —
//     fails if the broker.Revoke loop is removed from Remove).
//
// No KVM or cred store required; this is a pure unit test.
func TestRemove_CredDeregistrar_AndRevoke(t *testing.T) {
	// Build a store and seed a sandbox with AllowedHosts directly so the
	// Remove→broker.Revoke loop has a scope to revoke.
	underlying, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	const apiHost = "api.anthropic.com"

	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "deregtest",
		Project: "proj",
		State:   domain.Created,
		Envelope: domain.Envelope{
			AllowedHosts: []string{apiHost},
		},
	}
	if err := underlying.Create(ctx(), sb); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	broker := cred.NewBroker()
	// RegisterPlaceholder to open the scope that Revoke will close.
	if _, err := broker.RegisterPlaceholder(sb.ID, apiHost, "initial-tok"); err != nil {
		t.Fatalf("broker.RegisterPlaceholder: %v", err)
	}

	svc := service.New(underlying, fake.New(), lifecycle.New()).WithBroker(broker)

	spy := &spyDeregistrar{}
	svc.RegisterCredDeregistrar(sb.ID, spy)

	// Sanity: scope is live before Remove — SetRealToken must succeed.
	if err := broker.SetRealToken(sb.ID, apiHost, "pre-remove"); err != nil {
		t.Fatalf("broker.SetRealToken should succeed before Remove: %v", err)
	}

	if err := svc.Remove(ctx(), sb.ID.String()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Assertion 1: Deregister was called exactly once with the sandbox ID.
	got := spy.calls()
	if len(got) != 1 || got[0] != sb.ID {
		t.Errorf("Deregister: want [%s], got %v", sb.ID, got)
	}

	// Assertion 2: broker.Revoke removed the scope — SetRealToken now fails.
	if err := broker.SetRealToken(sb.ID, apiHost, "canary"); err == nil {
		t.Error("broker.SetRealToken should fail after Remove (scope revoked); got nil")
	}
}

// ── Remove write-ahead ordering ───────────────────────────────────────────────

// TestRemove_MarkerBeforeDestructiveWork asserts the write-ahead removal
// protocol: SetRemovalMarker must be recorded before both driver.Stop and
// store.Delete. This test uses a shared call log across the store and driver
// wrapper to prove the ordering, not merely the end state.
func TestRemove_MarkerBeforeDestructiveWork(t *testing.T) {
	log := &callLog{}

	underlying, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	recStore := &recordingStore{Store: underlying, log: log}
	recDriver := &recordingDriver{Driver: fake.New(), log: log}

	svc := service.New(recStore, recDriver, lifecycle.New())

	sb, err := svc.Create(ctx(), "proj", "rmbox", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	log.mu.Lock()
	log.calls = nil // reset — only capture Remove's calls
	log.mu.Unlock()

	if err := svc.Remove(ctx(), sb.ID.String()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	calls := log.snapshot()
	if len(calls) < 3 {
		t.Fatalf("expected at least 3 recorded calls (SetRemovalMarker, driver.Stop, Delete), got %d: %v",
			len(calls), calls)
	}
	if calls[0] != "SetRemovalMarker" {
		t.Errorf("first call = %q, want SetRemovalMarker (full log: %v)", calls[0], calls)
	}

	// Both destructive steps must follow the marker.
	markerIdx := 0
	stopIdx, deleteIdx := -1, -1
	for i, c := range calls {
		switch c {
		case "driver.Stop":
			stopIdx = i
		case "Delete":
			deleteIdx = i
		}
		_ = markerIdx
	}
	if stopIdx <= markerIdx {
		t.Errorf("driver.Stop (idx %d) must come after SetRemovalMarker (idx %d)", stopIdx, markerIdx)
	}
	if deleteIdx <= markerIdx {
		t.Errorf("Delete (idx %d) must come after SetRemovalMarker (idx %d)", deleteIdx, markerIdx)
	}
}

// ── Concurrency correctness ───────────────────────────────────────────────────

// TestStart_ConcurrentNoDuplicate verifies that N concurrent Start calls on
// the same sandbox produce exactly one driver.Start invocation and one
// persisted InstanceID with no orphaned VMs.
//
// The slowDriver wrapper sleeps briefly inside Start to widen the race window
// between the fast-path state check and the per-sandbox Update lock. Without
// the lock-across-substrate fix in service.Start, all N goroutines pass the
// fast-path check concurrently and each calls driver.Start — producing N VMs
// of which N-1 are orphans. With the fix the lock serialises them: the first
// goroutine calls driver.Start and commits; the rest fail at re-validation.
//
// Run with go test -race.
func TestStart_ConcurrentNoDuplicate(t *testing.T) {
	fakeDriver := fake.New()
	svc := service.New(newFileStore(t), &slowDriver{Driver: fakeDriver}, lifecycle.New())

	sb, err := svc.Create(ctx(), "proj", "concurrent", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const goroutines = 10
	release := make(chan struct{})

	type result struct {
		sb  domain.Sandbox
		err error
	}
	results := make([]result, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-release // wait for simultaneous release
			results[i].sb, results[i].err = svc.Start(ctx(), sb.ID.String())
		}()
	}
	close(release) // release all goroutines simultaneously
	wg.Wait()

	// Exactly one goroutine must have succeeded.
	var successes int
	var winner domain.Sandbox
	for _, r := range results {
		if r.err == nil {
			successes++
			winner = r.sb
		}
	}
	if successes != 1 {
		t.Errorf("want exactly 1 successful Start, got %d", successes)
	}

	// Exactly one driver.Start call — no orphans.
	var startCalls int
	for _, c := range fakeDriver.Calls() {
		if c.Kind == fake.CallStart {
			startCalls++
		}
	}
	if startCalls != 1 {
		t.Errorf("want exactly 1 driver.Start call, got %d (%d orphaned VMs)",
			startCalls, startCalls-1)
	}

	// The persisted InstanceID must be non-empty and match the winner's return.
	all, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 sandbox, got %d", len(all))
	}
	if all[0].InstanceID == "" {
		t.Error("InstanceID not persisted after concurrent Start")
	}
	if successes == 1 && all[0].InstanceID != winner.InstanceID {
		t.Errorf("persisted InstanceID %q does not match winner's return value %q",
			all[0].InstanceID, winner.InstanceID)
	}
	if all[0].State != domain.Running {
		t.Errorf("state after concurrent Start: want Running, got %q", all[0].State)
	}
}

// TestRemove_ConcurrentWithStart_Consistent verifies that one Remove and many
// concurrent Start calls on the same sandbox never produce an orphaned VM — a
// VM that is running after its record has been deleted.
//
// The slowStopDriver sleeps inside Stop, which is now called inside Remove's
// Update callback (holding the per-sandbox exclusive lock). This widens the
// window during which concurrent Start goroutines queue up. When Remove's
// Update lock releases, each Start sees RemovalMarker=true in its re-validation
// and is rejected before touching the substrate.
//
// Invariant: after all goroutines complete, the record is gone AND no VM is
// left running. A deleted record with a running VM is the orphan bug.
//
// Run with go test -race.
func TestRemove_ConcurrentWithStart_Consistent(t *testing.T) {
	fakeDriver := fake.New()
	svc := service.New(newFileStore(t), &slowStopDriver{Driver: fakeDriver}, lifecycle.New())

	sb, err := svc.Create(ctx(), "proj", "rmrace", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Sandbox starts in Created state — a valid state from which Start can succeed.

	const startGoroutines = 10
	release := make(chan struct{})

	var removeErr error
	startErrs := make([]error, startGoroutines)

	var wg sync.WaitGroup
	wg.Add(1 + startGoroutines)

	// One Remove goroutine.
	go func() {
		defer wg.Done()
		<-release
		removeErr = svc.Remove(ctx(), sb.ID.String())
	}()

	// Many concurrent Start goroutines.
	for i := 0; i < startGoroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-release
			_, startErrs[i] = svc.Start(ctx(), sb.ID.String())
		}()
	}

	close(release) // release all goroutines simultaneously
	wg.Wait()

	if removeErr != nil {
		t.Errorf("Remove failed: %v", removeErr)
	}

	// After all concurrent operations the record must be gone.
	all, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("sandbox record still exists after Remove+concurrent Starts; want gone")
	}

	// Critical invariant: no orphaned VM.
	// Count driver.Start calls — if any VMs were ever started, the driver must
	// have subsequently been asked to stop them (via Remove's driver.Stop). Verify
	// that the VM is now Absent, not Running without a record.
	var vmStarts int
	for _, c := range fakeDriver.Calls() {
		if c.Kind == fake.CallStart {
			vmStarts++
		}
	}
	if vmStarts > 0 {
		obs, _ := fakeDriver.Observe(ctx(), sb.ID)
		if obs.State == driver.Running {
			t.Errorf("orphaned VM: record is deleted but driver.Observe reports Running — "+
				"%d driver.Start call(s) were made and the VM was never stopped; "+
				"fix: driver.Stop must be called inside the per-sandbox lock (inside store.Update) "+
				"and Start must reject records with RemovalMarker=true", vmStarts)
		}
	}

	// Suppress unused-variable warning: startErrs contains the individual Start
	// outcomes; what matters is the invariant above, not the count.
	_ = startErrs
}
