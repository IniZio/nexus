package volumestore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// AttachAndPrune tests

// TestAttachAndPrune_first_attach tests attaching a sandbox to a volume for
// the first time.
func TestAttachAndPrune_first_attach(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)

	// Create a test volume.
	_, err := vs.Create(ctx, "test-vol", volumestore.KindDisk, 0, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First attach should succeed.
	sandboxID := "sb-first-123"
	err = vs.AttachAndPrune("test-vol", sandboxID, nil)
	if err != nil {
		t.Fatalf("AttachAndPrune: %v", err)
	}

	// Verify the attachment was recorded.
	rec, err := vs.Get("test-vol")
	if err != nil {
		t.Fatalf("Get after attach: %v", err)
	}
	if len(rec.Attachments) != 1 {
		t.Errorf("Attachments count: got %d, want 1", len(rec.Attachments))
	}
	if rec.Attachments[0].SandboxID != sandboxID {
		t.Errorf("Attachment SandboxID: got %q, want %q",
			rec.Attachments[0].SandboxID, sandboxID)
	}
}

// TestAttachAndPrune_multiple_attachments tests attaching multiple sandboxes
// to the same volume.
func TestAttachAndPrune_multiple_attachments(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)

	_, err := vs.Create(ctx, "test-vol", volumestore.KindDisk, 0, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Attach first sandbox.
	err = vs.AttachAndPrune("test-vol", "sb-first", nil)
	if err != nil {
		t.Fatalf("AttachAndPrune sb-first: %v", err)
	}

	// Attach second sandbox.
	err = vs.AttachAndPrune("test-vol", "sb-second", nil)
	if err != nil {
		t.Fatalf("AttachAndPrune sb-second: %v", err)
	}

	// Verify both attachments are present.
	rec, err := vs.Get("test-vol")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(rec.Attachments) != 2 {
		t.Errorf("Attachments count: got %d, want 2", len(rec.Attachments))
	}

	// Verify both sandbox IDs are present.
	sandboxIDs := make(map[string]bool)
	for _, att := range rec.Attachments {
		sandboxIDs[att.SandboxID] = true
	}
	if !sandboxIDs["sb-first"] {
		t.Errorf("sb-first not found in attachments")
	}
	if !sandboxIDs["sb-second"] {
		t.Errorf("sb-second not found in attachments")
	}
}

// TestAttachAndPrune_idempotent tests that attaching the same sandbox twice
// is idempotent (no duplicate entries).
func TestAttachAndPrune_idempotent(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)

	_, err := vs.Create(ctx, "test-vol", volumestore.KindDisk, 0, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sandboxID := "sb-idempotent"

	// Attach the sandbox.
	err = vs.AttachAndPrune("test-vol", sandboxID, nil)
	if err != nil {
		t.Fatalf("AttachAndPrune first: %v", err)
	}

	// Attach the same sandbox again.
	err = vs.AttachAndPrune("test-vol", sandboxID, nil)
	if err != nil {
		t.Fatalf("AttachAndPrune second: %v", err)
	}

	// Verify no duplicates were added.
	rec, err := vs.Get("test-vol")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(rec.Attachments) != 1 {
		t.Errorf("Attachments count: got %d, want 1 (idempotent)",
			len(rec.Attachments))
	}
}

// TestAttachAndPrune_prune_stale_entries tests that AttachAndPrune removes
// the specified sandboxes from the attachment list.
func TestAttachAndPrune_prune_stale(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)

	_, err := vs.Create(ctx, "test-vol", volumestore.KindDisk, 0, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Attach three sandboxes.
	for _, id := range []string{"sb-keep1", "sb-prune", "sb-keep2"} {
		err = vs.AttachAndPrune("test-vol", id, nil)
		if err != nil {
			t.Fatalf("AttachAndPrune %s: %v", id, err)
		}
	}

	// Verify three attachments exist.
	rec, err := vs.Get("test-vol")
	if err != nil {
		t.Fatalf("Get before prune: %v", err)
	}
	if len(rec.Attachments) != 3 {
		t.Errorf("Attachments before prune: got %d, want 3", len(rec.Attachments))
	}

	// Now attach a fourth while pruning the second.
	err = vs.AttachAndPrune("test-vol", "sb-new", []string{"sb-prune"})
	if err != nil {
		t.Fatalf("AttachAndPrune with prune: %v", err)
	}

	// Verify: sb-prune is gone, sb-keep1 and sb-keep2 remain, sb-new is added.
	rec, err = vs.Get("test-vol")
	if err != nil {
		t.Fatalf("Get after prune: %v", err)
	}
	if len(rec.Attachments) != 3 {
		t.Errorf("Attachments after prune: got %d, want 3", len(rec.Attachments))
	}

	sandboxIDs := make(map[string]bool)
	for _, att := range rec.Attachments {
		sandboxIDs[att.SandboxID] = true
	}

	if sandboxIDs["sb-prune"] {
		t.Errorf("sb-prune was not pruned")
	}
	if !sandboxIDs["sb-keep1"] {
		t.Errorf("sb-keep1 was not kept")
	}
	if !sandboxIDs["sb-keep2"] {
		t.Errorf("sb-keep2 was not kept")
	}
	if !sandboxIDs["sb-new"] {
		t.Errorf("sb-new was not added")
	}
}

// TestAttachAndPrune_prune_multiple tests pruning multiple stale entries
// in a single call.
func TestAttachAndPrune_prune_multiple(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)

	_, err := vs.Create(ctx, "test-vol", volumestore.KindDisk, 0, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Attach five sandboxes.
	for i := 1; i <= 5; i++ {
		id := "sb-" + string(rune(48+i))
		err = vs.AttachAndPrune("test-vol", id, nil)
		if err != nil {
			t.Fatalf("AttachAndPrune %s: %v", id, err)
		}
	}

	// Prune three, keep two, add one new.
	err = vs.AttachAndPrune("test-vol", "sb-new", []string{"sb-1", "sb-2", "sb-3"})
	if err != nil {
		t.Fatalf("AttachAndPrune with multiple prune: %v", err)
	}

	rec, err := vs.Get("test-vol")
	if err != nil {
		t.Fatalf("Get after prune: %v", err)
	}

	// Should have 3 remaining (sb-4, sb-5, sb-new).
	if len(rec.Attachments) != 3 {
		t.Errorf("Attachments count: got %d, want 3", len(rec.Attachments))
	}

	sandboxIDs := make(map[string]bool)
	for _, att := range rec.Attachments {
		sandboxIDs[att.SandboxID] = true
	}

	// sb-1, sb-2, sb-3 should be gone.
	if sandboxIDs["sb-1"] || sandboxIDs["sb-2"] || sandboxIDs["sb-3"] {
		t.Errorf("Some pruned entries remain")
	}

	// sb-4, sb-5, sb-new should be present.
	if !sandboxIDs["sb-4"] || !sandboxIDs["sb-5"] || !sandboxIDs["sb-new"] {
		t.Errorf("Some kept/new entries are missing")
	}
}

// TestAttachAndPrune_attach_time_recorded tests that AttachedAt timestamp
// is recorded when an attachment is created.
func TestAttachAndPrune_attach_time_recorded(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)

	_, err := vs.Create(ctx, "test-vol", volumestore.KindDisk, 0, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	beforeAttach := time.Now().UTC()
	err = vs.AttachAndPrune("test-vol", "sb-time-test", nil)
	if err != nil {
		t.Fatalf("AttachAndPrune: %v", err)
	}
	afterAttach := time.Now().UTC()

	rec, err := vs.Get("test-vol")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(rec.Attachments) != 1 {
		t.Fatalf("Expected 1 attachment, got %d", len(rec.Attachments))
	}

	attachedAt := rec.Attachments[0].AttachedAt
	if attachedAt.Before(beforeAttach) || attachedAt.After(afterAttach) {
		t.Errorf("AttachedAt timestamp %v not in range [%v, %v]",
			attachedAt, beforeAttach, afterAttach)
	}
}

// TestAttachAndPrune_not_found tests that AttachAndPrune returns an error
// when the volume does not exist.
func TestAttachAndPrune_not_found(t *testing.T) {
	vs := newStore(t)

	err := vs.AttachAndPrune("nonexistent-vol", "sb-1", nil)
	if err == nil {
		t.Fatal("AttachAndPrune on nonexistent volume: expected error, got nil")
	}
}

// TestLockPath_creates_deterministic_path tests that LockPath returns a
// consistent, deterministic path for a given volume name.
func TestLockPath_deterministic(t *testing.T) {
	vs := newStore(t)

	path1 := vs.LockPath("test-vol")
	path2 := vs.LockPath("test-vol")

	if path1 != path2 {
		t.Errorf("LockPath not deterministic: %q != %q", path1, path2)
	}
}

// TestLockPath_unique_per_volume tests that different volumes have different
// lock paths.
func TestLockPath_unique_per_volume(t *testing.T) {
	vs := newStore(t)

	path1 := vs.LockPath("vol1")
	path2 := vs.LockPath("vol2")

	if path1 == path2 {
		t.Errorf("Different volumes have same lock path: %q", path1)
	}
}

// TestLockPath_contains_volume_name tests that LockPath contains the volume
// name in the returned path.
func TestLockPath_contains_name(t *testing.T) {
	vs := newStore(t)

	path := vs.LockPath("my-volume")

	if !containsPath(path, "my-volume") {
		t.Errorf("LockPath %q does not contain volume name 'my-volume'", path)
	}
}

// TestLockPath_ends_with_lock tests that LockPath ends with "lock".
func TestLockPath_ends_with_lock(t *testing.T) {
	vs := newStore(t)

	path := vs.LockPath("test-vol")

	if filepath.Base(path) != "lock" {
		t.Errorf("LockPath base: got %q, want 'lock'", filepath.Base(path))
	}
}

// containsPath is a helper to check if a path string contains a component.
func containsPath(path, component string) bool {
	return filepath.Base(filepath.Dir(path)) == component ||
		filepath.Base(filepath.Dir(filepath.Dir(path))) == component
}
