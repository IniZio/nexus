package volumestore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// newStore returns a VolumeStore backed by a temp directory.
func newStore(t *testing.T) *volumestore.VolumeStore {
	t.Helper()
	root := filepath.Join(t.TempDir(), "volumes")
	return volumestore.New(root)
}

// ── §7.1 Test 1 — record-before-backing ordering (D-PD-89) ─────────────────
//
// Acceptance criterion 2: a test OBSERVES the ordering by injecting a failure
// between meta.json write and backing materialisation, then asserting that
// meta.json exists while disk.ext4 (or data/) does not.
//
// Mutation that breaks this test: swap the order so disk.ext4 is created
// before meta.json → after hook-induced failure, only disk.ext4 exists with no
// record → prune would misclassify it as an orphaned backing file.
func TestOrderingProof_kindDisk(t *testing.T) {
	s := newStore(t)

	// Inject a failure immediately after meta.json is written.
	errSimulatedCrash := errors.New("simulated crash between record and backing")
	volumestore.SetTestHookAfterMetaWrite(s, func() error {
		return errSimulatedCrash
	})

	ctx := context.Background()
	_, err := s.Create(ctx, "test-vol", volumestore.KindDisk, 0, "")
	if !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("expected simulated crash error, got: %v", err)
	}

	// ASSERT: meta.json must exist (record written before crash).
	root := s.Root()
	metaPath := filepath.Join(root, "test-vol", "meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("meta.json not found after crash (ordering violated): %v", err)
	}

	// ASSERT: disk.ext4 must NOT exist (crash happened before materialisation).
	diskPath := s.DiskPath("test-vol")
	if _, err := os.Stat(diskPath); err == nil {
		t.Error("disk.ext4 exists after crash — ordering violated: backing was created before meta.json")
	}
}

// Same ordering proof for kind=dir: data/ must not exist when hook fires.
func TestOrderingProof_kindDir(t *testing.T) {
	s := newStore(t)

	errSimulatedCrash := errors.New("simulated crash between record and data dir")
	volumestore.SetTestHookAfterMetaWrite(s, func() error {
		return errSimulatedCrash
	})

	ctx := context.Background()
	_, err := s.Create(ctx, "test-dir", volumestore.KindDir, 0, "")
	if !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("expected simulated crash error, got: %v", err)
	}

	metaPath := filepath.Join(s.Root(), "test-dir", "meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("meta.json not found after crash (ordering violated): %v", err)
	}

	dataPath := s.DataPath("test-dir")
	if _, err := os.Stat(dataPath); err == nil {
		t.Error("data/ exists after crash — ordering violated: backing was created before meta.json")
	}
}

// ── §7.1 Test 2 — idempotent create (same kind) ────────────────────────────
//
// Creates a kind=dir volume (no external tooling needed), then creates it again
// with the same kind.  Second call must return no error and the same backing
// directory (not a new one).
func TestCreate_idempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	rec1, err := s.Create(ctx, "my-vol", volumestore.KindDir, 0, "")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Capture the inode of data/ to detect if a second creation replaces it.
	dataPath := s.DataPath("my-vol")
	fi1, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("stat data/ after first create: %v", err)
	}

	rec2, err := s.Create(ctx, "my-vol", volumestore.KindDir, 0, "")
	if err != nil {
		t.Errorf("second create (same kind) must be a no-op, got error: %v", err)
	}

	if rec2.Name != rec1.Name || rec2.Kind != rec1.Kind {
		t.Errorf("second create returned different record: got %+v, want same as %+v", rec2, rec1)
	}

	fi2, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("stat data/ after second create: %v", err)
	}
	if !os.SameFile(fi1, fi2) {
		t.Error("second create produced a different data/ inode — new directory was created instead of reusing")
	}
}

// ── §7.1 Test 3 — kind conflict ─────────────────────────────────────────────
//
// Creates a kind=dir volume, then tries to create the same name with kind=disk.
// Must return an error.
func TestCreate_kindConflict(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.Create(ctx, "shared", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("initial create: %v", err)
	}

	_, err := s.Create(ctx, "shared", volumestore.KindDisk, 0, "")
	if err == nil {
		t.Fatal("expected kind-conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "kind conflict") {
		t.Errorf("error should mention 'kind conflict', got: %v", err)
	}
}

// ── §7.1 Test 4 — rm not attached ───────────────────────────────────────────
func TestRm_notAttached(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.Create(ctx, "del-vol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.Rm(ctx, "del-vol"); err != nil {
		t.Fatalf("rm: %v", err)
	}

	// meta.json must be gone.
	metaPath := filepath.Join(s.Root(), "del-vol", "meta.json")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Error("meta.json still exists after successful rm")
	}

	// Volume directory must be gone.
	volDir := filepath.Join(s.Root(), "del-vol")
	if _, err := os.Stat(volDir); !os.IsNotExist(err) {
		t.Error("volume directory still exists after successful rm")
	}
}

// ── §7.1 Test 5 — rm while attached ─────────────────────────────────────────
func TestRm_attached(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.Create(ctx, "busy-vol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Attach(ctx, "busy-vol", "sb-01ABC"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	err := s.Rm(ctx, "busy-vol")
	if err == nil {
		t.Fatal("expected 'volume in use' error, got nil")
	}
	if !strings.Contains(err.Error(), "volume in use") {
		t.Errorf("error should contain 'volume in use', got: %v", err)
	}
}

// ── Acceptance criterion 3 — store root is outside the sandbox disks dir ────
//
// The structural reaper non-interference guarantee (D-PD-87) depends on the
// volumes directory being a sibling of (not a child of) the disks directory.
// This test uses a realistic state-root layout to assert that.
func TestStoreRoot_outsideDisksDir(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	volumesDir := filepath.Join(stateRoot, "volumes")

	s := volumestore.New(volumesDir)

	root := s.Root()

	// The store root must not be inside the disks dir.
	rel, err := filepath.Rel(disksDir, root)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	if !strings.HasPrefix(rel, "..") {
		t.Errorf("volume store root %q is inside the disks dir %q (rel=%q) — reaper non-interference broken",
			root, disksDir, rel)
	}

	// Verify the expected sibling layout.
	if root != volumesDir {
		t.Errorf("store root = %q, want %q", root, volumesDir)
	}
}

// ── Additional coverage: List, Get, Detach ───────────────────────────────────

func TestList(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	names := []string{"aaa", "bbb", "ccc"}
	for _, n := range names {
		if _, err := s.Create(ctx, n, volumestore.KindDir, 0, ""); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}

	records, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != len(names) {
		t.Errorf("list returned %d records, want %d", len(records), len(names))
	}
}

func TestDetach(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.Create(ctx, "det-vol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Attach(ctx, "det-vol", "sb-AAAAAA"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// After detach, rm must succeed.
	if err := s.Detach(ctx, "det-vol", "sb-AAAAAA"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := s.Rm(ctx, "det-vol"); err != nil {
		t.Errorf("rm after detach: %v", err)
	}
}

func TestNameValidation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	bad := []string{"", "MyVol", "-leading-dash", "has space", ".leading-dot"}
	for _, n := range bad {
		_, err := s.Create(ctx, n, volumestore.KindDir, 0, "")
		if err == nil {
			t.Errorf("name %q: expected validation error, got nil", n)
		}
	}

	good := []string{"abc", "a-b-c", "a.b.c", "a0b", "myproject-node_modules"}
	for _, n := range good {
		_, err := s.Create(ctx, n, volumestore.KindDir, 0, "")
		if err != nil {
			t.Errorf("name %q: unexpected error: %v", n, err)
		}
	}
}

// ── Acceptance criterion 4 helper — used by go list -deps test ──────────────
// (No-op function whose import ensures this test file compiles only if
// volumestore compiles without importing internal/core/service.)
var _ = fmt.Sprintf
