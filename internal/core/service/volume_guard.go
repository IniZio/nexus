package service

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// checkRWAttach implements the §4.1 concurrency guard from shadow-disk-v2.md
// for kind=disk rw attach. It acquires the per-volume advisory lock at
// volumes/<name>/lock, applies the five-row verdict table (probe-then-list,
// matching reap.go's N-AC2 discipline), prunes stale entries in the write-back,
// and records the new attachment atomically.
//
// On success the returned *store.Lock is held (exclusive flock, NOT yet
// unlocked).  The caller MUST release it by calling lk.Unlock()+lk.Close() or
// by closing it after the sandbox record is committed to the store.  Holding
// the lock across store.Create prevents Prune from treating the volume as
// "detached" in the window between AttachAndPrune writing meta.json and
// store.Create writing the sandbox record (D2).
//
// On error the lock is always released before returning; the caller receives
// (nil, err) and must not attempt to release the lock.
//
// ctx MUST carry a deadline (RISK-SD2-1): store.Lock.TryExclusive retries
// with a 5 ms backoff and respects ctx cancellation so a wedged peer surfaces
// as a context-deadline error, not a hung CLI.
//
// diskDir is the per-sandbox disk directory used to derive intent file paths
// for other sandboxes via filepath.Join(diskDir, sandboxID+".create-intent.json").
// This is the same disk directory that writeCreateIntent uses (step 3.6 of
// CreateAndBoot), so the intent file for any in-flight create is discoverable.
func checkRWAttach(ctx context.Context, vs *volumestore.VolumeStore, st store.Store, diskDir, name, sandboxID string) (*store.Lock, error) {
	lk, err := store.OpenLock(vs.LockPath(name))
	if err != nil {
		return nil, fmt.Errorf("volume %s: open lock: %w", name, err)
	}

	if err := lk.TryExclusive(ctx); err != nil {
		_ = lk.Close()
		return nil, fmt.Errorf("volume %s: acquire lock: %w", name, err)
	}

	if err := applyRWVerdictTable(ctx, vs, st, diskDir, name, sandboxID); err != nil {
		_ = lk.Unlock()
		_ = lk.Close()
		return nil, err
	}

	// Return with the lock held — caller releases after committing the sandbox
	// record (D2 fix).
	return lk, nil
}

// evalVerdictRow is the verdict produced by evalVerdictRows14 for a single
// attachment entry: a conflict error (rows 1, 3, 4), nil (row 2), or
// nil+stale=true (row 5 = leaseFree, caller decides whether to prune).
//
// Both applyRWVerdictTable (locked, with write-back) and
// preCheckRWAttachUnlocked (unlocked, no write-back) call this so the row
// evaluation and error strings live in exactly one place. Two copies that must
// agree is the drift that produced multiple defects.
func evalVerdictRows14(name, diskDir, attSandboxID string, sb domain.Sandbox, sbExists bool) (conflict error, stale bool) {
	if sbExists {
		if isVolumeLiveRecord(sb) {
			// Row 1: record exists; sandbox live → CONFLICT.
			return fmt.Errorf(
				"volume %s: rw conflict: sandbox %s holds this volume (state: %s)",
				name, attSandboxID, sb.State), false
		}
		// Row 2: sandbox dead — no conflict; leave entry for Detach to clean.
		return nil, false
	}
	// Record absent. Probe intent lease (matching reap.go:132).
	intentFilePath := filepath.Join(diskDir, attSandboxID+".create-intent.json")
	switch probeIntentLease(intentFilePath) {
	case leaseHeld:
		// Row 3: create in flight — CONFLICT.
		return fmt.Errorf(
			"volume %s: rw conflict: sandbox %s create is in flight (intent lease held)",
			name, attSandboxID), false
	case leaseUnknown:
		// Row 4: cannot rule out in-flight create — CONFLICT (distinct error, RISK-SD2-2).
		return fmt.Errorf(
			"volume %s: rw conflict: sandbox %s intent file unreadable — cannot rule out an in-flight create (check file permissions; this state does not self-resolve)",
			name, attSandboxID), false
	}
	// Row 5: leaseFree → stale entry; caller decides whether to prune.
	return nil, true
}

// applyRWVerdictTable reads meta.json and the sandbox store, then applies the
// §4.1 five-row verdict table for each existing rw attachment entry.
//
// Verdict table (probe-then-list order, matching reap.go:132 before :142-149):
//
//	Row 1: record exists; sandbox live (Running or Paused)      → CONFLICT
//	Row 2: record exists; sandbox dead (Stopped/Created/Error)  → no conflict
//	Row 3: record absent; probeIntentLease = leaseHeld          → CONFLICT ("create in flight")
//	Row 4: record absent; probeIntentLease = leaseUnknown       → CONFLICT (distinct error, RISK-SD2-2)
//	Row 5: record absent; probeIntentLease = leaseFree          → no conflict; entry is stale → PRUNE
//
// Row 3 is load-bearing: it covers the window where create.go publishes the
// intent at line 401 but the store.Create call (line 530) has not yet
// committed the record. Without Row 3 the lock is decorative in that window.
//
// Row 4 carries a DISTINCT error string from Row 3 (RISK-SD2-2) so an operator
// reading the output can distinguish a permissions problem from a live create.
//
// Must be called while the per-volume lock is held (checkRWAttach does this).
func applyRWVerdictTable(ctx context.Context, vs *volumestore.VolumeStore, st store.Store, diskDir, name, sandboxID string) error {
	rec, err := vs.Get(name)
	if err != nil {
		return fmt.Errorf("volume %s: read record for guard: %w", name, err)
	}

	// Build sandbox record map once for all attachment checks.
	allSandboxes, err := st.List(ctx)
	if err != nil {
		return fmt.Errorf("volume %s: list sandboxes for guard: %w", name, err)
	}
	sbMap := make(map[string]domain.Sandbox, len(allSandboxes))
	for _, sb := range allSandboxes {
		sbMap[sb.ID.String()] = sb
	}

	var staleIDs []string
	for _, att := range rec.Attachments {
		if att.SandboxID == sandboxID {
			continue // already attached — idempotent; AttachAndPrune is a no-op
		}
		sb, sbExists := sbMap[att.SandboxID]
		conflict, stale := evalVerdictRows14(name, diskDir, att.SandboxID, sb, sbExists)
		if conflict != nil {
			return conflict
		}
		if stale {
			// Row 5: record absent + leaseFree → stale entry; MUST be pruned.
			staleIDs = append(staleIDs, att.SandboxID)
		}
	}

	// Write-back: prune stale entries and record the new attachment.
	return vs.AttachAndPrune(name, sandboxID, staleIDs)
}

// preCheckRWAttachUnlocked is a best-effort, UNLOCKED pre-check for rw
// kind=disk volumes. It applies rows 1–4 of the §4.1 verdict table without
// acquiring the per-volume flock, so the result may be stale. This carries
// NO correctness weight — the authoritative locked check is checkRWAttach
// (step 4.7). Its sole purpose is to fail fast before workspace capture so
// a user on the error path does not wait for a 28–40 s capture to complete.
//
// On any error reading the volume record or listing sandboxes, the pre-check
// returns nil (no error) rather than blocking the create — correctness is the
// responsibility of the locked path.
func preCheckRWAttachUnlocked(ctx context.Context, vs *volumestore.VolumeStore, st store.Store, diskDir, name, sandboxID string) error {
	rec, err := vs.Get(name)
	if err != nil {
		return nil // volume may not exist yet; vs.Create handles it at step 4.7
	}
	allSandboxes, err := st.List(ctx)
	if err != nil {
		return nil // don't block the create on a transient List error
	}
	sbMap := make(map[string]domain.Sandbox, len(allSandboxes))
	for _, sb := range allSandboxes {
		sbMap[sb.ID.String()] = sb
	}
	for _, att := range rec.Attachments {
		if att.SandboxID == sandboxID {
			continue // idempotent — already our own attachment
		}
		sb, sbExists := sbMap[att.SandboxID]
		conflict, _ := evalVerdictRows14(name, diskDir, att.SandboxID, sb, sbExists)
		if conflict != nil {
			return conflict
		}
		// Row 5 (stale=true): leaseFree → the locked path will prune it.
	}
	return nil
}

// detachVolumeLocked removes sandboxID from the volume's attachment list.
// The per-volume flock is acquired inside vs.Detach (VOL-LOCK); ctx's deadline
// bounds the acquisition so a long-held lock surfaces as a context-deadline
// error rather than a hung CLI (RISK-SD2-1).
//
// Non-fatal if the volume record is absent; prune handles orphans.
func detachVolumeLocked(ctx context.Context, vs *volumestore.VolumeStore, name, sandboxID string) error {
	return vs.Detach(ctx, name, sandboxID)
}

// isVolumeLiveRecord returns true when the sandbox is actively running and
// could hold the volume in active use.
func isVolumeLiveRecord(sb domain.Sandbox) bool {
	return sb.State == domain.Running || sb.State == domain.Paused
}
