package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

// newStore creates a FileStore rooted at a fresh temporary directory.
func newStore(t *testing.T) *store.FileStore {
	t.Helper()
	fs, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return fs
}

// makeSandbox returns a minimal valid Sandbox for use in tests.
func makeSandbox(name, project string) domain.Sandbox {
	return domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    name,
		Project: project,
		State:   domain.Created,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:abc123",
		},
		InstanceID:    "inst-0",
		RemoveOnExit:  false,
		RemovalMarker: false,
	}
}

// TestCreateGetRoundTrip verifies that all durable fields survive a Create/Get
// round-trip without loss or corruption.
func TestCreateGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	sb := makeSandbox("mybox", "myproject")
	sb.RemoveOnExit = true
	sb.InstanceID = "instance-xyz"
	sb.Envelope = domain.Envelope{ImageDigest: "sha256:deadbeef"}

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ID != sb.ID {
		t.Errorf("ID mismatch: got %v, want %v", got.ID, sb.ID)
	}
	if got.Name != sb.Name {
		t.Errorf("Name mismatch: got %q, want %q", got.Name, sb.Name)
	}
	if got.Project != sb.Project {
		t.Errorf("Project mismatch: got %q, want %q", got.Project, sb.Project)
	}
	if got.State != sb.State {
		t.Errorf("State mismatch: got %v, want %v", got.State, sb.State)
	}
	if got.Envelope.ImageDigest != sb.Envelope.ImageDigest {
		t.Errorf("Envelope.ImageDigest mismatch: got %q, want %q",
			got.Envelope.ImageDigest, sb.Envelope.ImageDigest)
	}
	if got.InstanceID != sb.InstanceID {
		t.Errorf("InstanceID mismatch: got %q, want %q", got.InstanceID, sb.InstanceID)
	}
	if got.RemoveOnExit != sb.RemoveOnExit {
		t.Errorf("RemoveOnExit mismatch: got %v, want %v", got.RemoveOnExit, sb.RemoveOnExit)
	}
	if got.RemovalMarker != sb.RemovalMarker {
		t.Errorf("RemovalMarker mismatch: got %v, want %v", got.RemovalMarker, sb.RemovalMarker)
	}
}

// TestDurableFieldSetExact verifies that the JSON record on disk contains
// EXACTLY the expected set of top-level keys — no more, no less. This prevents
// future domain fields from silently landing on disk.
func TestDurableFieldSetExact(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := makeSandbox("exact", "testproject")
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Find the record file on disk.
	recordPath := filepath.Join(root, "sandboxes", sb.ID.String(), "record.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}

	want := map[string]bool{
		"schema_version": true,
		"id":             true,
		"name":           true,
		"project":        true,
		"state":          true,
		"envelope":       true,
		"instance_id":    true,
		"remove_on_exit": true,
		"removal_marker": true,
	}
	for key := range raw {
		if !want[key] {
			t.Errorf("unexpected top-level key in stored record: %q", key)
		}
	}
	for key := range want {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing expected top-level key in stored record: %q", key)
		}
	}
}

// TestCreateAlreadyExists verifies that Create returns ErrAlreadyExists on a
// duplicate ID.
func TestCreateAlreadyExists(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	sb := makeSandbox("dup", "proj")

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	err := st.Create(ctx, sb)
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists on duplicate Create, got %v", err)
	}
}

// TestGetNotFound verifies that Get returns ErrNotFound for an unknown ID.
func TestGetNotFound(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, err := st.Get(ctx, domain.NewSandboxID())
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestListEmpty verifies that List on an empty store returns a non-error empty
// slice.
func TestListEmpty(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	got, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %d entries", len(got))
	}
}

// TestListMultiple verifies that all created sandboxes appear in List.
func TestListMultiple(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	names := []string{"a", "b", "c"}
	for _, n := range names {
		if err := st.Create(ctx, makeSandbox(n, "proj")); err != nil {
			t.Fatalf("Create %q: %v", n, err)
		}
	}
	all, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != len(names) {
		t.Errorf("expected %d sandboxes, got %d", len(names), len(all))
	}
}

// TestDeleteRoundTrip verifies that Delete removes a sandbox and that Get
// subsequently returns ErrNotFound.
func TestDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	sb := makeSandbox("todelete", "proj")

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.Delete(ctx, sb.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, sb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after Delete, got %v", err)
	}
}

// TestDeleteNotFound verifies that Delete returns ErrNotFound for unknown IDs.
func TestDeleteNotFound(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	if err := st.Delete(ctx, domain.NewSandboxID()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestUpdateModifiesRecord verifies that Update can change a field and the
// change is visible on a subsequent Get.
func TestUpdateModifiesRecord(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	sb := makeSandbox("updateme", "proj")

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := st.Update(ctx, sb.ID, func(s *domain.Sandbox) error {
		s.State = domain.Running
		s.InstanceID = "new-instance"
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.State != domain.Running {
		t.Errorf("State: got %v, want Running", got.State)
	}
	if got.InstanceID != "new-instance" {
		t.Errorf("InstanceID: got %q, want %q", got.InstanceID, "new-instance")
	}
}

// TestUpdateNotFound verifies Update returns ErrNotFound for unknown IDs.
func TestUpdateNotFound(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	err := st.Update(ctx, domain.NewSandboxID(), func(s *domain.Sandbox) error {
		return nil
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestConcurrentUpdate verifies that N goroutines calling Update to increment
// InstanceID (as a decimal counter) all land — no lost updates.
//
// InstanceID is used as the counter because domain.Sandbox has no numeric field
// and the domain type must not be modified.
func TestConcurrentUpdate(t *testing.T) {
	const n = 50
	ctx := context.Background()
	st := newStore(t)

	sb := makeSandbox("concurrent", "proj")
	sb.InstanceID = "0"
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			err := st.Update(ctx, sb.ID, func(s *domain.Sandbox) error {
				cur, err := strconv.Atoi(s.InstanceID)
				if err != nil {
					return fmt.Errorf("parse InstanceID %q: %w", s.InstanceID, err)
				}
				s.InstanceID = strconv.Itoa(cur + 1)
				return nil
			})
			if err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Update error: %v", err)
	}

	got, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after concurrent updates: %v", err)
	}
	val, err := strconv.Atoi(got.InstanceID)
	if err != nil {
		t.Fatalf("parse final InstanceID: %v", err)
	}
	if val != n {
		t.Errorf("expected InstanceID=%d after %d concurrent increments, got %d", n, n, val)
	}
}

// TestCrashSafety verifies that a deliberately truncated temp file in the
// sandbox directory does not corrupt a subsequent read — the old record is
// still visible.
func TestCrashSafety(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	sb := makeSandbox("crash", "proj")
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate a crash mid-write: drop a truncated temp file in the sandbox dir.
	sandboxDir := filepath.Join(root, "sandboxes", sb.ID.String())
	tmpFile := filepath.Join(sandboxDir, ".record-CRASH.tmp")
	if err := os.WriteFile(tmpFile, []byte(`{"schema_version":1,"id":"sb-`), 0600); err != nil {
		t.Fatalf("write truncated temp: %v", err)
	}

	// The existing record must still be readable.
	got, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after crash: %v", err)
	}
	if got.Name != sb.Name {
		t.Errorf("Name mismatch after crash: got %q, want %q", got.Name, sb.Name)
	}

	// Garbage in the directory must not break List either.
	all, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List after crash: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 sandbox in List, got %d", len(all))
	}
}

// TestRemovalMarkerRoundTrip verifies the write-ahead removal marker: it
// survives across separate Get calls (simulating re-opens), and ClearRemovalMarker
// removes it.
func TestRemovalMarkerRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	sb := makeSandbox("marked", "proj")

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Marker is not set initially.
	got, _ := st.Get(ctx, sb.ID)
	if got.RemovalMarker {
		t.Error("RemovalMarker should be false after Create")
	}

	if err := st.SetRemovalMarker(ctx, sb.ID); err != nil {
		t.Fatalf("SetRemovalMarker: %v", err)
	}
	got, _ = st.Get(ctx, sb.ID)
	if !got.RemovalMarker {
		t.Error("RemovalMarker should be true after SetRemovalMarker")
	}

	// Clear the marker.
	if err := st.ClearRemovalMarker(ctx, sb.ID); err != nil {
		t.Fatalf("ClearRemovalMarker: %v", err)
	}
	got, _ = st.Get(ctx, sb.ID)
	if got.RemovalMarker {
		t.Error("RemovalMarker should be false after ClearRemovalMarker")
	}
}

// TestRemovalMarkerSurvivesReopen verifies that a set marker is visible when
// the store is reopened with a new FileStore pointing to the same root —
// simulating the process crash scenario.
func TestRemovalMarkerSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	st1, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := makeSandbox("reopen", "proj")
	if err := st1.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st1.SetRemovalMarker(ctx, sb.ID); err != nil {
		t.Fatalf("SetRemovalMarker: %v", err)
	}

	// Reopen with a fresh FileStore instance (simulates process restart).
	st2, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	got, err := st2.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !got.RemovalMarker {
		t.Error("RemovalMarker must survive a process restart (reopen)")
	}
}

// TestSchemaVersionFutureRejected verifies that a record whose schema_version
// is higher than the current binary supports is rejected with a clear error
// message rather than silently decoded.
func TestSchemaVersionFutureRejected(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	sb := makeSandbox("futurever", "proj")
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Overwrite the record with a future schema version.
	recordPath := filepath.Join(root, "sandboxes", sb.ID.String(), "record.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw["schema_version"] = json.RawMessage("9999")
	modified, _ := json.Marshal(raw)
	if err := os.WriteFile(recordPath, modified, 0600); err != nil {
		t.Fatalf("write modified record: %v", err)
	}

	_, err = st.Get(ctx, sb.ID)
	if err == nil {
		t.Fatal("expected error on future schema version, got nil")
	}
	// The error message must mention the version and upgrading nexus3.
	msg := err.Error()
	if msg == "" {
		t.Error("error message must not be empty")
	}
	// Sanity-check the error propagates an ErrSchemaTooNew somewhere in the chain.
	var schemaErr *store.ErrSchemaTooNew
	if !errors.As(err, &schemaErr) {
		t.Errorf("expected ErrSchemaTooNew in error chain, got: %v", err)
	}
	if schemaErr.Found != 9999 {
		t.Errorf("ErrSchemaTooNew.Found: got %d, want 9999", schemaErr.Found)
	}

	// List must skip the future-version record without error.
	all, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List with future-version record: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("List should skip future-version record, got %d entries", len(all))
	}
}

// TestListSkipsCorruptRecord verifies that a garbage record file in the sandbox
// directory does not cause List to return an error.
func TestListSkipsCorruptRecord(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Create a valid sandbox.
	good := makeSandbox("good", "proj")
	if err := st.Create(ctx, good); err != nil {
		t.Fatalf("Create good: %v", err)
	}

	// Plant a corrupt record as if a different sandbox's directory was trashed.
	corruptDir := filepath.Join(root, "sandboxes", "sb-XXXXXXXXXXXXXXXXXXXXXXXX")
	if err := os.MkdirAll(corruptDir, 0700); err != nil {
		t.Fatalf("mkdir corrupt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "record.json"), []byte("this is not json"), 0600); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	all, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Only the valid sandbox appears.
	if len(all) != 1 || all[0].ID != good.ID {
		t.Errorf("expected only the good sandbox, got %d entries", len(all))
	}
}

// TestResolveByPrefix verifies prefix resolution with ErrNoMatch and
// ErrAmbiguous propagated through the error chain.
func TestResolveByPrefix(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	sb := makeSandbox("prefixed", "proj")
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Full ID prefix resolves to the sandbox.
	got, err := st.ResolveByPrefix(ctx, sb.ID.String())
	if err != nil {
		t.Fatalf("ResolveByPrefix (full): %v", err)
	}
	if got.ID != sb.ID {
		t.Errorf("ID mismatch: got %v, want %v", got.ID, sb.ID)
	}

	// Unknown prefix returns ErrNoMatch.
	_, err = st.ResolveByPrefix(ctx, "sb-ZZZZZZ")
	var noMatch *domain.ErrNoMatch
	if !errors.As(err, &noMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

// TestResolveByHandle verifies handle-based resolution.
func TestResolveByHandle(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	sb := makeSandbox("mybox", "myproject")
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := st.ResolveByHandle(ctx, "myproject/mybox")
	if err != nil {
		t.Fatalf("ResolveByHandle: %v", err)
	}
	if got.ID != sb.ID {
		t.Errorf("ID mismatch: got %v, want %v", got.ID, sb.ID)
	}

	_, err = st.ResolveByHandle(ctx, "myproject/nonexistent")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing handle, got %v", err)
	}
}

// TestDefaultRoot verifies that DefaultRoot returns a non-empty path.
func TestDefaultRoot(t *testing.T) {
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	if root == "" {
		t.Error("DefaultRoot returned empty string")
	}
}

// TestAllStatesPersistAndReload verifies every durable state survives
// a round-trip through the store.
func TestAllStatesPersistAndReload(t *testing.T) {
	ctx := context.Background()
	for _, state := range domain.AllStates() {
		t.Run(state.String(), func(t *testing.T) {
			st := newStore(t)
			sb := makeSandbox("statebox", "proj")
			sb.State = state
			if err := st.Create(ctx, sb); err != nil {
				t.Fatalf("Create: %v", err)
			}
			got, err := st.Get(ctx, sb.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.State != state {
				t.Errorf("State mismatch: got %v, want %v", got.State, state)
			}
		})
	}
}

// TestDurabilityReopen simulates a process restart by writing to one FileStore
// instance and reading back via a fresh instance pointed at the same root.
//
// What this proves: data written and synced in one FileStore instance is visible
// to a new instance on the same directory — the OS did not silently discard
// buffered pages between the two opens. All durable fields, including the WAL
// removal marker, must survive. The marker guarantee is critical: if it can be
// lost the store's removal-idempotency contract breaks.
//
// What this does NOT prove: durability against power loss. A unit test cannot
// induce a hard power cut or confirm that the kernel actually flushed pages to
// stable storage. The directory fsync added to writeRecord and to Create/Delete
// provides the OS-level guarantee for power-loss durability; its correctness can
// only be confirmed by controlled hardware fault-injection testing.
func TestDurabilityReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Write phase.
	st1, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := makeSandbox("durable", "proj")
	sb.RemoveOnExit = true
	sb.InstanceID = "inst-42"
	sb.Envelope = domain.Envelope{ImageDigest: "sha256:durability"}

	if err := st1.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st1.SetRemovalMarker(ctx, sb.ID); err != nil {
		t.Fatalf("SetRemovalMarker: %v", err)
	}

	// Reopen phase: fresh FileStore instance, same root — simulates process restart.
	st2, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	got, err := st2.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}

	// Every durable field must be bit-for-bit identical.
	if got.ID != sb.ID {
		t.Errorf("ID: got %v, want %v", got.ID, sb.ID)
	}
	if got.Name != sb.Name {
		t.Errorf("Name: got %q, want %q", got.Name, sb.Name)
	}
	if got.Project != sb.Project {
		t.Errorf("Project: got %q, want %q", got.Project, sb.Project)
	}
	if got.State != sb.State {
		t.Errorf("State: got %v, want %v", got.State, sb.State)
	}
	if got.Envelope.ImageDigest != sb.Envelope.ImageDigest {
		t.Errorf("Envelope.ImageDigest: got %q, want %q", got.Envelope.ImageDigest, sb.Envelope.ImageDigest)
	}
	if got.InstanceID != sb.InstanceID {
		t.Errorf("InstanceID: got %q, want %q", got.InstanceID, sb.InstanceID)
	}
	if got.RemoveOnExit != sb.RemoveOnExit {
		t.Errorf("RemoveOnExit: got %v, want %v", got.RemoveOnExit, sb.RemoveOnExit)
	}
	if !got.RemovalMarker {
		t.Error("RemovalMarker must survive a process restart (reopen)")
	}
}

// TestProvenanceRoundTrip verifies that Provenance (ParentID + SourceSnapshot)
// survives a Create/List cycle across a fresh FileStore instance.  This
// exercises the filestore serialization path for fork children; without the
// provenanceRecord fix the fields would be silently dropped.
func TestProvenanceRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Build a parent sandbox and write it.
	st1, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	parent := makeSandbox("parent", "proj")
	if err := st1.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	// Write a child sandbox with Provenance referencing the parent and a snapshot.
	child := makeSandbox("child", "proj")
	child.Provenance = &domain.Provenance{
		ParentID:       parent.ID,
		SourceSnapshot: "snap-abc123",
	}
	if err := st1.Create(ctx, child); err != nil {
		t.Fatalf("Create child: %v", err)
	}

	// Re-open the store from disk — this proves that serialization, not just
	// in-memory state, carries provenance.
	st2, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}

	got, err := st2.Get(ctx, child.ID)
	if err != nil {
		t.Fatalf("Get child after reopen: %v", err)
	}
	if got.Provenance == nil {
		t.Fatal("Provenance is nil after round-trip; filestore did not persist it")
	}
	if got.Provenance.ParentID != parent.ID {
		t.Errorf("ParentID: got %v, want %v", got.Provenance.ParentID, parent.ID)
	}
	if got.Provenance.SourceSnapshot != "snap-abc123" {
		t.Errorf("SourceSnapshot: got %q, want %q", got.Provenance.SourceSnapshot, "snap-abc123")
	}

	// Also verify via List — that path deserializes separately.
	all, err := st2.List(ctx)
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	var found *domain.Sandbox
	for i := range all {
		if all[i].ID == child.ID {
			found = &all[i]
			break
		}
	}
	if found == nil {
		t.Fatal("child not found in List after reopen")
	}
	if found.Provenance == nil {
		t.Fatal("Provenance is nil in List result after round-trip")
	}
	if found.Provenance.SourceSnapshot != "snap-abc123" {
		t.Errorf("List: SourceSnapshot: got %q, want %q", found.Provenance.SourceSnapshot, "snap-abc123")
	}
}

// TestLabelsRoundTrip verifies that Labels (including the motive key) survive a
// Create/Get round-trip without loss.
func TestLabelsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	sb := makeSandbox("label-box", "label-project")
	sb.Labels = map[string]string{"motive": "motive-abc", "env": "ci"}

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Labels["motive"] != sb.Labels["motive"] {
		t.Errorf("Labels[motive]: got %q, want %q", got.Labels["motive"], sb.Labels["motive"])
	}
	if got.Labels["env"] != sb.Labels["env"] {
		t.Errorf("Labels[env]: got %q, want %q", got.Labels["env"], sb.Labels["env"])
	}
}

// TestGetByMotive verifies that GetByMotive returns exactly the sandboxes
// matching the given motive ID when several sandboxes with mixed/empty
// MotiveIDs exist.
func TestGetByMotive(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Two sandboxes in motive-A, one in motive-B, one unassociated.
	a1 := makeSandbox("a1", "proj")
	a1.Labels = map[string]string{"motive": "motive-A"}
	a2 := makeSandbox("a2", "proj")
	a2.Labels = map[string]string{"motive": "motive-A"}
	b1 := makeSandbox("b1", "proj")
	b1.Labels = map[string]string{"motive": "motive-B"}
	none := makeSandbox("none", "proj")
	// none.Labels is nil — unassociated

	for _, sb := range []domain.Sandbox{a1, a2, b1, none} {
		if err := st.Create(ctx, sb); err != nil {
			t.Fatalf("Create %s: %v", sb.Name, err)
		}
	}

	got, err := st.GetByMotive(ctx, "motive-A")
	if err != nil {
		t.Fatalf("GetByMotive: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetByMotive(motive-A): got %d sandboxes, want 2", len(got))
	}
	for _, sb := range got {
		if sb.Labels["motive"] != "motive-A" {
			t.Errorf("unexpected sandbox in result: Labels[motive]=%q", sb.Labels["motive"])
		}
	}

	// Confirm motive-B returns only b1.
	gotB, err := st.GetByMotive(ctx, "motive-B")
	if err != nil {
		t.Fatalf("GetByMotive(motive-B): %v", err)
	}
	if len(gotB) != 1 || gotB[0].ID != b1.ID {
		t.Errorf("GetByMotive(motive-B): got %v, want [b1]", gotB)
	}
}

// TestGetByMotive_UnknownMotive verifies that an unknown motive ID returns an
// empty (non-nil) slice and nil error.
func TestGetByMotive_UnknownMotive(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	sb := makeSandbox("box", "proj")
	sb.Labels = map[string]string{"motive": "motive-X"}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := st.GetByMotive(ctx, "motive-does-not-exist")
	if err != nil {
		t.Fatalf("GetByMotive: unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("GetByMotive: expected non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("GetByMotive: expected empty slice, got %d sandboxes", len(got))
	}
}

// TestLegacyMotiveIDMigration verifies that a record written with the old
// motive_id JSON field (before the Labels field existed) is loaded correctly:
// the MotiveID value must appear in Labels["motive"] and the sandbox must be
// reachable by GetByMotive. No data must be lost or cause a crash.
func TestLegacyMotiveIDMigration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Write a raw JSON record that carries motive_id but no labels field —
	// exactly what an old nexus3 binary would have written to disk.
	legacyID := domain.NewSandboxID()
	legacyJSON := `{
		"schema_version": 1,
		"id": "` + legacyID.String() + `",
		"name": "legacy-box",
		"project": "proj",
		"motive_id": "old-motive-42",
		"state": "stopped",
		"envelope": {},
		"instance_id": "",
		"remove_on_exit": false,
		"removal_marker": false
	}`

	// Write the record directly to disk, bypassing the store API
	// (the same path convention the store uses: root/sandboxes/<id>/record.json).
	sandboxDir := filepath.Join(root, "sandboxes", legacyID.String())
	if err := os.MkdirAll(sandboxDir, 0700); err != nil {
		t.Fatalf("mkdir sandbox dir: %v", err)
	}
	recordPath := filepath.Join(sandboxDir, "record.json")
	if err := os.WriteFile(recordPath, []byte(legacyJSON), 0600); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}

	// Get: the sandbox must load without error.
	sb, err := st.Get(ctx, legacyID)
	if err != nil {
		t.Fatalf("Get legacy sandbox: %v", err)
	}

	// The MotiveID value must be accessible as Labels["motive"].
	if sb.Labels["motive"] != "old-motive-42" {
		t.Errorf("Labels[motive]: got %q, want %q", sb.Labels["motive"], "old-motive-42")
	}

	// GetByMotive must find the sandbox by its legacy motive value.
	found, err := st.GetByMotive(ctx, "old-motive-42")
	if err != nil {
		t.Fatalf("GetByMotive on legacy record: %v", err)
	}
	if len(found) != 1 || found[0].ID != legacyID {
		t.Errorf("GetByMotive: got %d sandboxes, want 1 with ID %s", len(found), legacyID)
	}
}

// TestGetByLabels_ANDMatch verifies that GetByLabels with multiple labels
// performs AND-matching: a sandbox must carry ALL specified labels to appear
// in the result. A sandbox with only a subset of the labels is excluded.
func TestGetByLabels_ANDMatch(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// both: has app=engine AND tier=web — must appear in AND-query.
	both := makeSandbox("both", "proj")
	both.Labels = map[string]string{"app": "engine", "tier": "web"}

	// appOnly: only has app=engine — excluded from AND-query requiring tier=web.
	appOnly := makeSandbox("app-only", "proj")
	appOnly.Labels = map[string]string{"app": "engine"}

	// tierOnly: only has tier=web — excluded from AND-query requiring app=engine.
	tierOnly := makeSandbox("tier-only", "proj")
	tierOnly.Labels = map[string]string{"tier": "web"}

	// none: no labels at all.
	none := makeSandbox("none", "proj")

	for _, sb := range []domain.Sandbox{both, appOnly, tierOnly, none} {
		if err := st.Create(ctx, sb); err != nil {
			t.Fatalf("Create %s: %v", sb.Name, err)
		}
	}

	// AND-query: must return only "both".
	got, err := st.GetByLabels(ctx, map[string]string{"app": "engine", "tier": "web"})
	if err != nil {
		t.Fatalf("GetByLabels AND: %v", err)
	}
	if len(got) != 1 || got[0].ID != both.ID {
		t.Errorf("AND-match: got %d results (want 1 with ID %s)", len(got), both.ID)
	}

	// Single-label query: must return "both" and "appOnly".
	gotApp, err := st.GetByLabels(ctx, map[string]string{"app": "engine"})
	if err != nil {
		t.Fatalf("GetByLabels single: %v", err)
	}
	if len(gotApp) != 2 {
		t.Errorf("single-label (app=engine): got %d results, want 2", len(gotApp))
	}
}
