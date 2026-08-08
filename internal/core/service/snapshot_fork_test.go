package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
)

// ── brokenSnapshotter ─────────────────────────────────────────────────────────
//
// brokenSnapshotter wraps FakeDriver and overrides TakeSnapshot to return a
// snapshot with an empty CommitMarker, which causes snap.Validate() to fail
// with "missing commit marker". Used to exercise the integrity-reject path.

type brokenSnapshotter struct {
	*fake.FakeDriver
}

func (b *brokenSnapshotter) TakeSnapshot(_ context.Context, id domain.SandboxID, kind artifact.SnapshotKind) (artifact.Snapshot, error) {
	return artifact.Snapshot{
		ID:           "broken-snap",
		SandboxID:    id,
		Kind:         kind,
		Size:         0,
		CommitMarker: "", // empty → Validate() returns "missing commit marker"
		CreatedAt:    time.Now(),
	}, nil
}

func newBrokenSvc(t *testing.T) *service.Service {
	t.Helper()
	return service.New(newFileStore(t), &brokenSnapshotter{fake.New()}, lifecycle.New())
}

// ── Snapshot: happy path (Running) ────────────────────────────────────────────

func TestSnapshot_HappyPath_Running(t *testing.T) {
	svc := newSvc(t)
	c := ctx()

	sb, err := svc.Create(c, "proj", "snap-happy-running", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	snap, err := svc.Snapshot(c, sb.ID.String())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.ID == "" {
		t.Error("expected non-empty snap.ID")
	}
	if snap.SandboxID != sb.ID {
		t.Errorf("SandboxID: got %s, want %s", snap.SandboxID, sb.ID)
	}
	if err := snap.Validate(); err != nil {
		t.Errorf("snap.Validate: %v", err)
	}

	// Self-edge: sandbox must remain Running after the snapshot.
	all, err := svc.List(c)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *domain.Sandbox
	for i := range all {
		if all[i].ID == sb.ID {
			found = &all[i]
			break
		}
	}
	if found == nil {
		t.Fatal("sandbox not found in List after Snapshot")
	}
	if found.State != domain.Running {
		t.Errorf("state after snapshot: got %v, want Running", found.State)
	}
}

// ── Snapshot: happy path (Stopped) ───────────────────────────────────────────

func TestSnapshot_HappyPath_Stopped(t *testing.T) {
	svc := newSvc(t)
	c := ctx()

	sb, err := svc.Create(c, "proj", "snap-happy-stopped", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := svc.Stop(c, sb.ID.String()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	snap, err := svc.Snapshot(c, sb.ID.String())
	if err != nil {
		t.Fatalf("Snapshot from Stopped: %v", err)
	}
	if err := snap.Validate(); err != nil {
		t.Errorf("snap.Validate: %v", err)
	}
}

// ── Snapshot: illegal state (Created → rejected) ──────────────────────────────

func TestSnapshot_IllegalState_Created(t *testing.T) {
	svc := newSvc(t)
	c := ctx()

	sb, err := svc.Create(c, "proj", "snap-illegal", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.Snapshot(c, sb.ID.String())
	if err == nil {
		t.Fatal("Snapshot of Created sandbox: expected error, got nil")
	}
	var illegalT *lifecycle.IllegalTransitionError
	if !errors.As(err, &illegalT) {
		t.Errorf("expected *lifecycle.IllegalTransitionError in error chain, got: %v", err)
	}
}

// ── Snapshot: integrity-reject (driver returns invalid snapshot) ──────────────

func TestSnapshot_IntegrityReject(t *testing.T) {
	svc := newBrokenSvc(t)
	c := ctx()

	sb, err := svc.Create(c, "proj", "snap-integrity", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = svc.Snapshot(c, sb.ID.String())
	if err == nil {
		t.Fatal("Snapshot with broken driver: expected error, got nil")
	}
	// Error must mention the torn-write / commit marker failure.
	if !strings.Contains(err.Error(), "commit marker") {
		t.Errorf("expected 'commit marker' in error, got: %v", err)
	}
}

// ── Snapshot: S0-AC5 no placeholder written to service artifact store ─────────
//
// S0-AC5: Exactly ONE store record per snapshot create (no duplicate real+placeholder pair).
// After S0 the service no longer writes a zero-filled placeholder to s.artifacts;
// persistence is exclusively the driver's responsibility. This test verifies
// that the service-attached artifact store remains empty after a successful
// Snapshot call.

func TestSnapshot_S0AC5_NoPlaceholderWrittenToServiceStore(t *testing.T) {
	aStore, err := artifact.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("artifact.NewStore: %v", err)
	}
	svc := newSvc(t).WithArtifacts(aStore)
	c := ctx()

	sb, err := svc.Create(c, "proj", "snap-noplacehold", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := svc.Snapshot(c, sb.ID.String()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// The service-attached artifact store must be empty (no placeholder written).
	snaps, err := aStore.List()
	if err != nil {
		t.Fatalf("aStore.List: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("S0-AC5: expected aStore to be empty after Snapshot, got %d record(s)", len(snaps))
	}
}

// ── Fork: count=3 mints 3 unique children with Provenance ────────────────────

func TestFork_Count3_UniqueChildrenWithProvenance(t *testing.T) {
	svc := newSvc(t)
	c := ctx()

	parent, err := svc.Create(c, "proj", "fork-parent", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, parent.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	children, err := svc.Fork(c, parent.ID.String(), 3)
	if err != nil {
		t.Fatalf("Fork(count=3): %v", err)
	}
	if len(children) != 3 {
		t.Fatalf("Fork returned %d children, want 3", len(children))
	}

	seenIDs := make(map[domain.SandboxID]bool)
	seenNames := make(map[string]bool)
	for _, ch := range children {
		// Unique IDs.
		if seenIDs[ch.ID] {
			t.Errorf("duplicate child ID: %s", ch.ID)
		}
		seenIDs[ch.ID] = true

		// Unique names.
		if seenNames[ch.Name] {
			t.Errorf("duplicate child name: %q", ch.Name)
		}
		seenNames[ch.Name] = true

		// All children Running.
		if ch.State != domain.Running {
			t.Errorf("child %s: state %v, want Running", ch.ID, ch.State)
		}

		// Provenance set and correct.
		if ch.Provenance == nil {
			t.Errorf("child %s: Provenance is nil", ch.ID)
			continue
		}
		if ch.Provenance.ParentID != parent.ID {
			t.Errorf("child %s: Provenance.ParentID %s, want %s",
				ch.ID, ch.Provenance.ParentID, parent.ID)
		}
		if ch.Provenance.SourceSnapshot == "" {
			t.Errorf("child %s: Provenance.SourceSnapshot is empty", ch.ID)
		}
	}

	// All three children must share the same SourceSnapshot (same fork batch).
	snap0 := children[0].Provenance.SourceSnapshot
	for _, ch := range children[1:] {
		if ch.Provenance.SourceSnapshot != snap0 {
			t.Errorf("children disagree on SourceSnapshot: %q vs %q",
				snap0, ch.Provenance.SourceSnapshot)
		}
	}
}

// ── Fork: count < 1 is rejected immediately ───────────────────────────────────

func TestFork_InvalidCount(t *testing.T) {
	svc := newSvc(t)
	c := ctx()

	sb, err := svc.Create(c, "proj", "fork-count0", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.Fork(c, sb.ID.String(), 0); err == nil {
		t.Fatal("Fork(count=0): expected error, got nil")
	}
	if _, err := svc.Fork(c, sb.ID.String(), -1); err == nil {
		t.Fatal("Fork(count=-1): expected error, got nil")
	}
}

// ── Fork: illegal parent state (Created → rejected) ───────────────────────────

func TestFork_IllegalParentState_Created(t *testing.T) {
	svc := newSvc(t)
	c := ctx()

	sb, err := svc.Create(c, "proj", "fork-illegal-parent", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Parent is in Created state — cannot be snapshotted, so Fork must be rejected.
	_, err = svc.Fork(c, sb.ID.String(), 1)
	if err == nil {
		t.Fatal("Fork of Created sandbox: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "snapshotable") && !strings.Contains(err.Error(), "illegal") {
		t.Errorf("unexpected error for illegal fork: %v", err)
	}
}
