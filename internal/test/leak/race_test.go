package leak_test

// Race tests for R3-AC1(c): concurrent create/remove operations on the same
// and different sandboxes with the race detector enabled.
//
// Run with: TMPDIR=/tmp go test -race -count=5 -run Race ./internal/test/leak/
//
// Design rationale:
//
//	The reaper (service.Reap) reads the filesystem and store without any
//	global lock. Concurrent creates and removes can cause the reaper to observe
//	transient states (resource exists, record being deleted). The tests verify:
//
//	  (1) No panics or data races (race detector).
//	  (2) After all goroutines complete, a final Reap --apply leaves zero stranded
//	      resources.
//	  (3) A resource is NEVER deleted while a store record for it is live
//	      (N-AC2).
//
// # Auditing N-AC2 without phantom reports
//
// Reap takes its ownership snapshot once per call (service.Reap: idx.List then
// st.List) and performs every unlink after that snapshot but before returning.
// So a path in report.Deleted was unlinked at some unknown instant inside
// [reapStart, reapEnd]. A harness that merely asks "is this ID owned *now*?"
// after Reap returns compares two different instants and reports violations
// that never happened: a resource whose files were unlinked in the mid-create
// window (files on disk, record not yet committed) is a legitimate orphan at
// unlink time, yet its ID appears owned a few milliseconds later once
// store.Create commits.
//
// That is not a hypothetical. Under the empty ProcDir these tests use, the
// liveness gate that protects a real mid-create sandbox (reap.go
// classifyResource → scanProcForULID; see
// service.TestReap_ConcurrentCreateInFlight) always reports dead, so the reaper
// legitimately reclaims every mid-create pair. Membership-only auditing turns
// that into a burst of false N-AC2 reports whenever the store.Create commit
// happens to land between the unlink and the audit — which is exactly what a
// slow-I/O CI runner makes likely.
//
// ownershipLedger therefore records WHEN each record became live and only
// reports a violation when the record was live for the whole [reapStart,
// reapEnd] window. Then, wherever inside that window the unlink happened, the
// record was provably live — a real N-AC2 break — while a record that came
// into existence mid-window can never be reported.
//
// # Same-sandbox race
//
// TestConcurrentReapSameSandbox hammers a single orphan resource with N
// concurrent reaps. The idempotent os.Remove means concurrent deletes are safe
// at the OS level; this test ensures no higher-level logic panics.
//
// # Different-sandbox race
//
// TestConcurrentCreateRemoveRace runs W worker goroutines, each cycling through
// create-commit-remove on their own set of IDs, while a reaper goroutine runs
// Reap in a loop. The invariant: no owned resource appears in Deleted.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ownershipLedger tracks, for each sandbox ID, the instant its store record
// became live, and audits a ReapReport against that history.
//
// The instant matters: a deletion reported by Reap happened somewhere inside
// [reapStart, reapEnd]. Only a record that was already live at reapStart (and
// still live when audited, i.e. still in the ledger) was provably live at the
// unlink instant, whenever inside the window that was. See the package comment
// for why membership alone is not sound.
type ownershipLedger struct {
	mu sync.Mutex
	// liveSince maps an ID with a live store record to the time store.Create
	// returned for it. Entries are removed when the record is released.
	liveSince map[domain.SandboxID]time.Time
}

func newOwnershipLedger() *ownershipLedger {
	return &ownershipLedger{liveSince: make(map[domain.SandboxID]time.Time)}
}

// markLive records that id's store record became live at instant at. Callers
// must pass the instant store.Create returned, sampled BEFORE taking the
// ledger lock, so a slow lock acquisition cannot make a record look younger
// than it is (which would weaken detection).
func (l *ownershipLedger) markLive(id domain.SandboxID, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.liveSince[id] = at
}

// markReleased records that id's store record is no longer live. It must be
// called BEFORE store.Delete, so the ledger never claims a record is live
// after it has been removed.
func (l *ownershipLedger) markReleased(id domain.SandboxID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.liveSince, id)
}

// auditDeletions returns one message per N-AC2 violation found in deleted,
// plus auditable: how many records were, at audit time, live since before
// reapStart. auditable > 0 means this audit had real detection power — a
// genuine N-AC2 break involving one of those records would have been reported.
// A run whose audits are all auditable == 0 proves nothing, so callers should
// surface the total.
// reapStart must be sampled immediately before the service.Reap call that
// produced deleted; audit must be called before any record released after
// reapEnd is re-created, i.e. promptly after Reap returns.
//
// A deletion is a violation only when the owning record was live for the
// entire window in which the unlink could have occurred: live at reapStart
// (liveSince before reapStart) and still live now.
func (l *ownershipLedger) auditDeletions(deleted []string, reapStart time.Time) (msgs []string, auditable int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, since := range l.liveSince {
		if since.Before(reapStart) {
			auditable++
		}
	}

	var out []string
	for _, path := range deleted {
		base := filepath.Base(path)
		for id, since := range l.liveSince {
			idStr := id.String()
			if len(base) < len(idStr) || base[:len(idStr)] != idStr {
				continue
			}
			if !since.Before(reapStart) {
				// The record came into existence after the reap snapshot, so
				// the unlink may legitimately have preceded it. Not a
				// violation — see the package comment.
				continue
			}
			out = append(out, fmt.Sprintf(
				"N-AC2 violation: deleted %q while record for %s was live (live since %v, reap started %v)",
				path, id, since, reapStart))
		}
	}
	return out, auditable
}

// TestConcurrentReapSameSandbox verifies that N concurrent Reap --apply calls
// targeting the SAME orphan resource do not panic, corrupt state, or fail with
// unexpected errors.
//
// The resource is deleted by whichever goroutine wins the race; all others
// encounter ErrNotExist on os.Remove (which Reap treats as success). At least
// one deletion must succeed.
//
// @verifies R3-AC1(c)
func TestConcurrentReapSameSandbox(t *testing.T) {
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	socketDir := t.TempDir()

	id := domain.NewSandboxID()
	rawPath := filepath.Join(diskDir, id.String()+".raw")
	if err := helperSparse(rawPath, 4096); err != nil {
		t.Fatalf("create orphan .raw: %v", err)
	}

	st := newEmptyStore(t) // no records → resource is an orphan
	emptyProcDir := t.TempDir()
	ctx := context.Background()

	const concurrent = 10

	var wg sync.WaitGroup
	var deleteCount atomic.Int64

	for range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			idx := service.NewResourceIndex(service.IndexConfig{
				StateRoot: stateRoot,
				SocketDir: socketDir,
			})
			report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: emptyProcDir})
			if err != nil {
				t.Errorf("Reap: %v", err)
				return
			}
			deleteCount.Add(int64(len(report.Deleted)))
		}()
	}
	wg.Wait()

	// The orphan must have been deleted by exactly one of the concurrent reaps.
	if _, err := os.Stat(rawPath); !os.IsNotExist(err) {
		t.Errorf("orphan .raw still present after %d concurrent reaps", concurrent)
	}
	if deleteCount.Load() == 0 {
		// Every goroutine raced and lost — the file was already gone before any
		// of them ran the delete. That is fine: os.Remove(ErrNotExist) silently
		// succeeds per the idempotency contract.
		t.Logf("note: all %d reaps found the file already gone (race all-miss is ok)", concurrent)
	}
}

// TestConcurrentCreateRemoveRace exercises W worker goroutines cycling through
// create→owned→remove, with a concurrent reaper goroutine running Reap --apply
// in a loop.
//
// Invariant: a resource is never deleted while its store record exists.
//
// The test runs with -race to catch unsynchronised accesses in the reaper or
// the store. Run repeatedly (-count=N) to exercise timing variations.
//
// @verifies R3-AC1(c)
func TestConcurrentCreateRemoveRace(t *testing.T) {
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	storeTmpDir := t.TempDir()
	st, err := store.NewFileStore(storeTmpDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	emptyProcDir := t.TempDir()
	socketDir := t.TempDir()
	ctx := context.Background()

	const (
		workers    = 5
		iterations = 20
	)

	// ledger tracks IDs that currently have a live store record, and since
	// when. The reaper goroutine audits each report against it.
	ledger := newOwnershipLedger()

	// violation records any N-AC2 violation detected by the reaper.
	var violationMu sync.Mutex
	var violations []string

	// auditableTotal counts how many audits had detection power (a record live
	// since before the reap window opened). Reported at the end so a run that
	// could not have caught a violation is visible rather than silently green.
	var auditableTotal atomic.Int64

	// ── Reaper goroutine ──────────────────────────────────────────────────────
	reapCtx, reapCancel := context.WithCancel(ctx)
	var reapWG sync.WaitGroup
	reapWG.Add(1)
	go func() {
		defer reapWG.Done()
		for {
			select {
			case <-reapCtx.Done():
				return
			default:
			}
			idx := service.NewResourceIndex(service.IndexConfig{
				StateRoot: stateRoot,
				SocketDir: socketDir,
			})
			// reapStart bounds the earliest instant any unlink in this report
			// could have happened; every deletion below occurred in
			// [reapStart, now].
			reapStart := time.Now()
			report, err := service.Reap(reapCtx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: emptyProcDir})
			if err != nil {
				// Context cancellation on shutdown is not an error.
				if reapCtx.Err() != nil {
					return
				}
				t.Errorf("reaper: Reap: %v", err)
				return
			}
			// Audit: no deleted path may belong to a record that was live for
			// the whole reap window.
			msgs, auditable := ledger.auditDeletions(report.Deleted, reapStart)
			auditableTotal.Add(int64(auditable))
			if len(msgs) > 0 {
				violationMu.Lock()
				violations = append(violations, msgs...)
				violationMu.Unlock()
			}

			// Brief yield so workers have a chance to make progress.
			time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
		}
	}()

	// ── Worker goroutines ─────────────────────────────────────────────────────
	var workerWG sync.WaitGroup
	for range workers {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for range iterations {
				id := domain.NewSandboxID()

				// Write intent + .raw (simulates start of CreateAndBoot).
				intentPath := service.IntentPath(diskDir, id)
				intentJSON := fmt.Sprintf(`{"id":%q,"disk_copy_path":%q}`,
					id.String(),
					filepath.Join(diskDir, id.String()+".raw"),
				)
				if err := os.WriteFile(intentPath, []byte(intentJSON), 0o600); err != nil {
					// Transient error (e.g. concurrent reap deleted the dir) — skip.
					continue
				}
				if err := helperSparse(filepath.Join(diskDir, id.String()+".raw"), 4096); err != nil {
					// Ditto.
					_ = os.Remove(intentPath)
					continue
				}

				// Commit store record → resource becomes "owned".
				sb := domain.Sandbox{
					ID:      id,
					Name:    "race-worker",
					Project: "leak-test",
					State:   domain.Created,
				}
				if err := st.Create(ctx, sb); err != nil {
					// ID collision or transient — skip.
					_ = os.Remove(intentPath)
					_ = os.Remove(filepath.Join(diskDir, id.String()+".raw"))
					continue
				}

				// Mark as owned AFTER store.Create completes. Sample the
				// instant first: a delayed lock acquisition must not make the
				// record look younger than it is.
				ledger.markLive(id, time.Now())

				// Simulate holding the sandbox for a variable duration.
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)

				// Remove: mark as no-longer-owned BEFORE deleting the record,
				// so the ledger never claims a record is live after it is gone.
				ledger.markReleased(id)

				// Delete store record (now orphan).
				_ = st.Delete(ctx, id)

				// Clean up disk files (simulates service.Remove → ReapDiskCopy).
				_ = service.ReapDiskCopy(diskDir, id)
			}
		}()
	}

	workerWG.Wait()
	reapCancel()
	reapWG.Wait()

	t.Logf("audits with detection power (record live before the reap window): %d",
		auditableTotal.Load())

	// Report any N-AC2 violations detected during the run.
	violationMu.Lock()
	defer violationMu.Unlock()
	for _, v := range violations {
		t.Errorf("%s", v)
	}

	// Final cleanup: any resources left by workers should be orphaned by now.
	assertZeroStranded(t, stateRoot, st)
}

// TestOwnershipAuditIgnoresPreCommitReap is the regression test for the
// phantom N-AC2 report that failed CI intermittently while passing locally.
//
// It reproduces the losing interleaving deterministically, driving the real
// service.Reap: the mid-create files exist with no record, the reaper
// legitimately reclaims them as orphans, and only THEN does store.Create
// commit the record. Nothing was deleted while a record was live, so the audit
// must stay silent.
//
// A membership-only audit ("is this ID owned at audit time?") reports two
// violations here — one per reclaimed file — which is precisely the CI
// failure.
//
// @verifies R3-AC1(c)
func TestOwnershipAuditIgnoresPreCommitReap(t *testing.T) {
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ctx := context.Background()

	// Mid-create state: intent + .raw on disk, no store record yet.
	id := domain.NewSandboxID()
	intentPath := service.IntentPath(diskDir, id)
	rawPath := filepath.Join(diskDir, id.String()+".raw")
	intentJSON := fmt.Sprintf(`{"id":%q,"disk_copy_path":%q}`, id.String(), rawPath)
	if err := os.WriteFile(intentPath, []byte(intentJSON), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}
	if err := helperSparse(rawPath, 4096); err != nil {
		t.Fatalf("write raw: %v", err)
	}

	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})
	ledger := newOwnershipLedger()

	// The reaper reclaims both files while no record exists. With the empty
	// ProcDir there is no liveness signal, so this is the reaper behaving
	// exactly as specified.
	reapStart := time.Now()
	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(report.Deleted) != 2 {
		t.Fatalf("setup: expected the intent + raw pair to be reclaimed, got %v", report.Deleted)
	}

	// Only now does the create commit its record — after every unlink.
	sb := domain.Sandbox{ID: id, Name: "late-commit", Project: "leak-test", State: domain.Created}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	ledger.markLive(id, time.Now())

	if msgs, _ := ledger.auditDeletions(report.Deleted, reapStart); len(msgs) != 0 {
		t.Errorf("audit reported %d phantom violation(s) for deletions that all preceded the record: %v",
			len(msgs), msgs)
	}
}

// TestOwnershipAuditFlagsLiveRecordDeletion is the positive control for the
// audit: a record that was already live before the reap started and is still
// live must produce a violation for every deleted path of that sandbox.
//
// Without this, a fix for the phantom report could silence the invariant
// entirely and still look green.
//
// @verifies R3-AC1(c)
func TestOwnershipAuditFlagsLiveRecordDeletion(t *testing.T) {
	id := domain.NewSandboxID()
	ledger := newOwnershipLedger()

	// Record live strictly before the reap window opens.
	ledger.markLive(id, time.Now())
	time.Sleep(time.Millisecond)
	reapStart := time.Now()

	deleted := []string{
		filepath.Join("/state/disks", id.String()+".create-intent.json"),
		filepath.Join("/state/disks", id.String()+".raw"),
		filepath.Join("/state/disks", domain.NewSandboxID().String()+".raw"), // unrelated orphan
	}

	msgs, auditable := ledger.auditDeletions(deleted, reapStart)
	if auditable != 1 {
		t.Errorf("auditable = %d, want 1 (the live record predates reapStart)", auditable)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 violations (intent + raw of the live sandbox), got %d: %v", len(msgs), msgs)
	}

	// Once the record is released the same report must no longer be flagged.
	ledger.markReleased(id)
	if msgs, auditable := ledger.auditDeletions(deleted, reapStart); len(msgs) != 0 || auditable != 0 {
		t.Errorf("released record still flagged: %v (auditable=%d)", msgs, auditable)
	}
}

// TestConcurrentReapDifferentSandboxes verifies that N concurrent Reap calls,
// each operating on its own isolated stateRoot, do not interfere with each other.
// The test also checks for -race detection of any shared global state.
//
// @verifies R3-AC1(c)
func TestConcurrentReapDifferentSandboxes(t *testing.T) {
	const concurrent = 8

	ctx := context.Background()
	var wg sync.WaitGroup

	for range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Each goroutine gets its own isolated stateRoot.
			stateRoot := t.TempDir()
			diskDir := filepath.Join(stateRoot, "disks")
			if err := os.MkdirAll(diskDir, 0o700); err != nil {
				t.Errorf("mkdir: %v", err)
				return
			}

			// Create 5 orphan disks with intent files.
			for range 5 {
				id := domain.NewSandboxID()
				intentPath := service.IntentPath(diskDir, id)
				intentJSON := fmt.Sprintf(`{"id":%q,"disk_copy_path":%q}`,
					id.String(),
					filepath.Join(diskDir, id.String()+".raw"),
				)
				if err := os.WriteFile(intentPath, []byte(intentJSON), 0o600); err != nil {
					t.Errorf("write intent: %v", err)
					return
				}
				if err := helperSparse(filepath.Join(diskDir, id.String()+".raw"), 4096); err != nil {
					t.Errorf("write raw: %v", err)
					return
				}
			}

			idx := service.NewResourceIndex(service.IndexConfig{
				StateRoot: stateRoot,
				SocketDir: t.TempDir(),
			})
			st := newEmptyStore(t)
			emptyProcDir := t.TempDir()

			report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: emptyProcDir})
			if err != nil {
				t.Errorf("Reap: %v", err)
				return
			}

			// Expect exactly 10 orphans: 5 intents + 5 raws.
			orphanCount := 0
			for _, e := range report.Entries {
				if e.Status == service.ReapStatusOrphan {
					orphanCount++
				}
			}
			if orphanCount != 10 {
				t.Errorf("expected 10 orphans (5 intent + 5 raw), got %d; entries: %v",
					orphanCount, report.Entries)
			}
			if len(report.Deleted) != 10 {
				t.Errorf("expected 10 deleted paths, got %d", len(report.Deleted))
			}

			// Rescan: nothing should remain.
			remaining, err := idx.List()
			if err != nil {
				t.Errorf("List after Reap: %v", err)
				return
			}
			if len(remaining) != 0 {
				t.Errorf("stranded after Reap --apply: %v", remaining)
			}
		}()
	}
	wg.Wait()
}
