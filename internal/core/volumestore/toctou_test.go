// Package volumestore_test — TOCTOU regression tests for D1, D2, D3.
//
// Each test targets one of the three confirmed TOCTOU windows (VOL-LOCK):
//
//   - D1: Prune deletes an in-flight Create stub because the per-volume flock
//     was not held across the meta-write + materialise window.
//   - D2: Prune deletes a volume whose attachment has been written to meta.json
//     but whose sandbox record has not yet been committed to the store.
//   - D3: Rm races with a concurrent Attach: Rm reads zero attachments, Attach
//     writes a new attachment, Rm deletes the volume — attachment is silently lost.
//
// Tests are written to FAIL against the unfixed code and PASS after the fix.
// Deterministic synchronisation (channels, hooks) is used throughout; time.Sleep
// is used only as a secondary safeguard in D3 to ensure the goroutine is
// scheduled before Rm completes.
package volumestore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// ── D2b: service else-branch (kind=dir / ro-disk) must hold lock across store.Create ──
//
// Without the fix: the service else-branch calls vs.Attach, which releases its
// flock on return.  No lease reaches volumeLeases, so in the window between
// meta.json write and svc.store.Create the volume lock is free.  Prune acquires
// it cleanly, finds no matching sandbox record, and (with IncludeDetached) runs
// os.RemoveAll — destroying the backing data/ directory or disk.ext4.
//
// With the fix: the service calls vs.AttachLocked, which returns the flock
// still held.  Prune probes the lock, sees EWOULDBLOCK, and keeps the volume.
//
// Test contract: the test drives AttachLocked directly to verify the API
// delivers the held lock.  Before AttachLocked is defined the file does not
// compile (a compile failure IS a test failure).  After the fix: lock is held
// → Prune skips → DetachedDeleted is empty → volume survives.
func TestD2_ServiceElseBranch_DirVolumeHoldsLock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "d2dir", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("D2-else: Create failed: %v", err)
	}

	// Simulate the service else-branch after the fix: AttachLocked returns with
	// the exclusive flock still held, bridging the window between meta.json write
	// and svc.store.Create.  Without this held lock Prune would delete the volume.
	lk, err := s.AttachLocked(ctx, "d2dir", "sb-inflight")
	if err != nil {
		t.Fatalf("D2-else: AttachLocked: %v", err)
	}

	// No sandbox records → isLive(d2dir)=false → detached candidate without fix.
	res, pruneErr := s.Prune(ctx, &mockSandboxLister{}, volumestore.PruneOptions{
		Apply:           true,
		IncludeDetached: true,
	})
	_ = lk.Unlock()
	_ = lk.Close()

	if pruneErr != nil {
		t.Fatalf("D2-else: Prune error: %v", pruneErr)
	}
	if len(res.DetachedDeleted) > 0 {
		t.Errorf("D2-else TOCTOU: Prune deleted dir volume with held lock: %v", res.DetachedDeleted)
	}
	if _, err := s.Get("d2dir"); err != nil {
		t.Errorf("D2-else: dir volume missing after Prune: %v", err)
	}
}

// ── D1: Prune must KEEP an in-flight Create stub ──────────────────────────────
//
// Without the fix: Prune classifies "meta.json present, backing absent" as a
// crash stub and deletes it while Create still holds the volume lock.
// With the fix: Prune probes the per-volume flock, sees EWOULDBLOCK (Create
// holds it), and skips the entry — ambiguity resolves to KEEP.
//
// Synchronisation: the testHookAfterMetaWrite fires while Create holds the
// lock.  Prune is called synchronously from within the hook (same goroutine,
// no sleep needed).  The hook returns without error so Create proceeds to
// materialise, leaving a fully intact volume.
func TestD1_PruneKeepsInFlightCreateStub(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	volumestore.SetTestHookAfterMetaWrite(s, func() error {
		// Create holds the per-volume flock here.  A non-blocking Prune probe
		// of the same lock must see EWOULDBLOCK and KEEP the entry.
		res, err := s.Prune(ctx, &mockSandboxLister{}, volumestore.PruneOptions{Apply: true})
		if err != nil {
			t.Errorf("D1: Prune error: %v", err)
			return nil
		}
		if len(res.StubsDeleted) > 0 {
			t.Errorf("D1 TOCTOU: Prune deleted in-flight Create stub: %v", res.StubsDeleted)
		}
		return nil
	})

	if _, err := s.Create(ctx, "d1vol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("D1: Create failed: %v", err)
	}

	// Volume must still exist after the hook-triggered Prune run.
	if _, err := s.Get("d1vol"); err != nil {
		t.Errorf("D1: volume missing after Create+Prune: %v", err)
	}
}

// ── D2: Prune must KEEP a volume with a held lock (simulates service window) ──
//
// Without the fix: Prune classifies the volume as "detached" (sandbox record
// not yet committed, no live IDs match) and deletes it.
// With the fix: Prune probes the per-volume flock, sees EWOULDBLOCK, and KEEPS
// the volume.
//
// Synchronisation: HoldVolumeLockForTest simulates the service-layer window
// (checkRWAttach wrote the attachment → lock held → svc.store.Create not yet
// committed).  Prune runs synchronously while the lock is held; the test
// releases the lock after Prune returns.
func TestD2_PruneKeepsVolumeWithHeldLock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "d2vol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("D2: Create failed: %v", err)
	}
	if err := s.Attach(ctx, "d2vol", "sb-inflight"); err != nil {
		t.Fatalf("D2: Attach failed: %v", err)
	}

	// Simulate the service-layer window: attachment is in meta.json, but the
	// sandbox record ("sb-inflight") does not exist in the store.  The volume
	// lock is held by the caller, exactly as service/create.go will do after
	// the D2 fix lands there.
	release, err := volumestore.HoldVolumeLockForTest(s, "d2vol")
	if err != nil {
		t.Fatalf("D2: HoldVolumeLockForTest: %v", err)
	}

	// No sandbox records → isLive(d2vol)=false → detached candidate without fix.
	res, pruneErr := s.Prune(ctx, &mockSandboxLister{}, volumestore.PruneOptions{
		Apply:           true,
		IncludeDetached: true,
	})
	release() // always release, even if test fails

	if pruneErr != nil {
		t.Fatalf("D2: Prune error: %v", pruneErr)
	}
	if len(res.DetachedDeleted) > 0 {
		t.Errorf("D2 TOCTOU: Prune deleted volume with held lock: %v", res.DetachedDeleted)
	}

	// Volume must still exist.
	if _, err := s.Get("d2vol"); err != nil {
		t.Errorf("D2: volume missing after Prune: %v", err)
	}
}

// ── D3: Rm and concurrent Attach must be serialised by the flock ─────────────
//
// Without the fix: Rm reads zero attachments (no lock held), Attach writes a
// new attachment in the gap, Rm deletes the volume — both return nil and the
// attachment is silently lost.
// With the fix: Attach blocks on the per-volume flock while Rm holds it.
// After Rm finishes and removes the volume directory, Attach opens the lock file
// (ENOENT — directory gone) and returns a "not found" error.  Exactly ONE of
// {Rm, Attach} must return a non-nil error.
//
// Synchronisation: rmRead/unblockRm channels gate the hook so Attach is
// definitely issued while Rm holds the lock.  A 10 ms sleep is a secondary
// measure to ensure the Attach goroutine is scheduled before we release Rm.
func TestD3_RmAttachSerialisedByFlock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Use a KindDir volume so Create does not need mkfs.ext4.
	dataPath := filepath.Join(t.TempDir(), "d3data")
	if _, err := s.Create(ctx, "d3vol", volumestore.KindDir, 0, dataPath); err != nil {
		t.Fatalf("D3: Create failed: %v", err)
	}

	rmRead := make(chan struct{})    // Rm signals: it has read the record (lock held)
	unblockRm := make(chan struct{}) // test signals: Rm may proceed with delete

	volumestore.SetTestHookAfterRmRead(s, func() error {
		close(rmRead)   // signal: Attach may now be issued
		<-unblockRm    // wait: hold lock until test says go
		return nil
	})

	rmDone := make(chan error, 1)
	go func() { rmDone <- s.Rm(ctx, "d3vol") }()

	// Wait for Rm to read the record and hold the lock.
	<-rmRead

	attachDone := make(chan error, 1)
	go func() { attachDone <- s.Attach(ctx, "d3vol", "sb-concurrent") }()

	// Give the Attach goroutine time to reach the flock acquisition and block.
	time.Sleep(10 * time.Millisecond)

	// Let Rm proceed — it finishes delete, releases lock; Attach unblocks.
	close(unblockRm)

	rmErr := <-rmDone
	attachErr := <-attachDone

	if rmErr == nil && attachErr == nil {
		t.Error("D3 TOCTOU: both Rm and Attach returned nil — concurrent attachment silently lost")
	}
	// With the fix: rmErr==nil (Rm wins the lock, deletes), attachErr!=nil
	// (Attach sees ENOENT after Rm removes the volume directory).
	t.Logf("D3: Rm=%v  Attach=%v", rmErr, attachErr)
}
