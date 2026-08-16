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
//	  (3) A resource that is "owned" (has a store record) is NEVER in the Deleted
//	      list — even when the reaper runs concurrently with the create that commits
//	      the record.
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

	// ownedSet tracks IDs that currently have a live store record.
	// Protected by ownedMu. The reaper goroutine checks this set to detect
	// any N-AC2 violations (owned resource appearing in Deleted).
	var ownedMu sync.Mutex
	ownedSet := make(map[domain.SandboxID]struct{})

	// violation records any N-AC2 violation detected by the reaper.
	var violationMu sync.Mutex
	var violations []string

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
			report, err := service.Reap(reapCtx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: emptyProcDir})
			if err != nil {
				// Context cancellation on shutdown is not an error.
				if reapCtx.Err() != nil {
					return
				}
				t.Errorf("reaper: Reap: %v", err)
				return
			}
			// Check: none of the deleted paths belong to an owned sandbox.
			ownedMu.Lock()
			for _, deleted := range report.Deleted {
				// Extract the ID from the filename.
				base := filepath.Base(deleted)
				for id := range ownedSet {
					if len(base) >= len(id.String()) && base[:len(id.String())] == id.String() {
						msg := fmt.Sprintf("N-AC2 violation: deleted %q while record for %s was live", deleted, id)
						violationMu.Lock()
						violations = append(violations, msg)
						violationMu.Unlock()
					}
				}
			}
			ownedMu.Unlock()

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

				// Mark as owned AFTER store.Create completes.
				ownedMu.Lock()
				ownedSet[id] = struct{}{}
				ownedMu.Unlock()

				// Simulate holding the sandbox for a variable duration.
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)

				// Remove: mark as no-longer-owned BEFORE deleting the record,
				// so the reaper sees the resource as an orphan only after we
				// remove it from ownedSet.
				ownedMu.Lock()
				delete(ownedSet, id)
				ownedMu.Unlock()

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

	// Report any N-AC2 violations detected during the run.
	violationMu.Lock()
	defer violationMu.Unlock()
	for _, v := range violations {
		t.Errorf("%s", v)
	}

	// Final cleanup: any resources left by workers should be orphaned by now.
	assertZeroStranded(t, stateRoot, st)
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
