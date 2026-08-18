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
// ctx MUST carry a deadline (RISK-SD2-1): store.Lock.Exclusive blocks
// unboundedly; a wedged peer must surface as a context-deadline error, not a
// hung CLI.
//
// diskDir is the per-sandbox disk directory used to derive intent file paths
// for other sandboxes via filepath.Join(diskDir, sandboxID+".create-intent.json").
// This is the same disk directory that writeCreateIntent uses (step 3.6 of
// CreateAndBoot), so the intent file for any in-flight create is discoverable.
func checkRWAttach(ctx context.Context, vs *volumestore.VolumeStore, st store.Store, diskDir, name, sandboxID string) error {
	lk, err := store.OpenLock(vs.LockPath(name))
	if err != nil {
		return fmt.Errorf("volume %s: open lock: %w", name, err)
	}
	defer lk.Close()

	if err := lk.TryExclusive(ctx); err != nil {
		return fmt.Errorf("volume %s: acquire lock: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck

	return applyRWVerdictTable(ctx, vs, st, diskDir, name, sandboxID)
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
		if sbExists {
			// Row 1 / Row 2: record exists; classify by liveness.
			if isVolumeLiveRecord(sb) {
				return fmt.Errorf(
					"volume %s: rw conflict: sandbox %s holds this volume (state: %s)",
					name, att.SandboxID, sb.State)
			}
			// Row 2: sandbox dead — no conflict. Leave entry; Detach cleans it.
			continue
		}
		// Record absent. Probe intent lease (matching reap.go:132).
		intentFilePath := filepath.Join(diskDir, att.SandboxID+".create-intent.json")
		switch probeIntentLease(intentFilePath) {
		case leaseHeld:
			// Row 3: create in flight — CONFLICT.
			return fmt.Errorf(
				"volume %s: rw conflict: sandbox %s create is in flight (intent lease held)",
				name, att.SandboxID)
		case leaseUnknown:
			// Row 4: cannot rule out in-flight create — CONFLICT with distinct error (RISK-SD2-2).
			return fmt.Errorf(
				"volume %s: rw conflict: sandbox %s intent file unreadable — cannot rule out an in-flight create (check file permissions; this state does not self-resolve)",
				name, att.SandboxID)
		case leaseFree:
			// Row 5: record absent + leaseFree → stale entry; MUST be pruned.
			staleIDs = append(staleIDs, att.SandboxID)
		}
	}

	// Write-back: prune stale entries and record the new attachment.
	return vs.AttachAndPrune(name, sandboxID, staleIDs)
}

// detachVolumeLocked removes sandboxID from the volume's attachment list
// under the per-volume advisory lock. Used by Service.Remove so that the
// detach path shares the same lock as the attach path, preventing a race
// between a concurrent attach check and the removal write-back.
//
// Non-fatal if the volume record is absent; prune handles orphans.
func detachVolumeLocked(ctx context.Context, vs *volumestore.VolumeStore, name, sandboxID string) error {
	lk, err := store.OpenLock(vs.LockPath(name))
	if err != nil {
		return fmt.Errorf("volume %s: open lock for detach: %w", name, err)
	}
	defer lk.Close()

	if err := lk.TryExclusive(ctx); err != nil {
		return fmt.Errorf("volume %s: acquire lock for detach: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck

	return vs.Detach(name, sandboxID)
}

// isVolumeLiveRecord returns true when the sandbox is actively running and
// could hold the volume in active use.
func isVolumeLiveRecord(sb domain.Sandbox) bool {
	return sb.State == domain.Running || sb.State == domain.Paused
}
