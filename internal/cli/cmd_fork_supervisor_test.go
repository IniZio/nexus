package cli

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// netFakeDriver wraps FakeDriver and implements driver.NetnsStateProvider so
// that any sandbox (parent or child) is assigned a non-zero netns identity by
// the service layer. Without this, fork children have NetnsChildPID=0 and
// spawnForkChildSupervisors correctly skips them — which is the right
// production behaviour for netless sandboxes but defeats the AC-3 mutation test
// that needs to confirm the call site in runForkWith actually fires.
type netFakeDriver struct {
	*fake.FakeDriver
}

func (d *netFakeDriver) NetnsState(_ domain.SandboxID) (driver.NetnsIdentity, bool) {
	return driver.NetnsIdentity{
		ChildPID:       1234,
		ChildPGID:      1234,
		ChildStartTime: 1,
		GuestTap:       "tap0",
		APISocket:      "/dev/null",
		ControlSocket:  "/dev/null",
		ControlToken:   "test-token",
	}, true
}

var _ driver.NetnsStateProvider = (*netFakeDriver)(nil)

// TestForkSupervisor_WritesSpawnSpecPerChild verifies that spawnForkChildSupervisors
// calls supervisor.WriteSpawnSpec for each fork child and that each written
// spawn.json contains the child's own distinct SandboxRef (not the parent's).
//
// Mutation-proof for s46-fork-supervisor-parity AC-4 (mutation 1):
// removing the WriteSpawnSpec call from writeAndSpawnForkChild makes this RED
// because ReadSpawnSpec fails (no file) or returns the wrong SandboxRef.
func TestForkSupervisor_WritesSpawnSpecPerChild(t *testing.T) {
	storeRoot, err := os.MkdirTemp("/tmp", "n3forksuper")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(storeRoot) })

	parentID := domain.NewSandboxID()
	child1ID := domain.NewSandboxID()
	child2ID := domain.NewSandboxID()

	// Ensure the three IDs are distinct (astronomically unlikely to collide,
	// but guard it so the mutation proof is not vacuous).
	if parentID == child1ID || parentID == child2ID || child1ID == child2ID {
		t.Fatal("NewSandboxID collision — cannot run test with non-distinct IDs")
	}

	// Write a minimal parent spawn.json so ReadSpawnSpec succeeds.
	parentStateDir := supervisor.DefaultStateDir(storeRoot, parentID)
	parentCfg := supervisor.Config{
		SandboxRef: parentID.String(),
		StoreRoot:  storeRoot,
		StateDir:   parentStateDir,
		MemoryMiB:  512,
		BootVCPUs:  2,
	}
	if err := supervisor.WriteSpawnSpec(parentStateDir, parentCfg); err != nil {
		t.Fatalf("WriteSpawnSpec parent: %v", err)
	}

	// Build fork children with Provenance pointing at the parent.
	// NetnsChildPID must be non-zero so spawnForkChildSupervisors counts them
	// as networked (the new per-child guard; netless children are skipped).
	children := []domain.Sandbox{
		{ID: child1ID, NetnsChildPID: 1, Provenance: &domain.Provenance{ParentID: parentID}},
		{ID: child2ID, NetnsChildPID: 1, Provenance: &domain.Provenance{ParentID: parentID}},
	}

	// Replace doSpawnForkChildSupervisor with a no-op so no real subprocess
	// is spawned. The test only verifies that WriteSpawnSpec was called.
	orig := doSpawnForkChildSupervisor
	t.Cleanup(func() { doSpawnForkChildSupervisor = orig })
	doSpawnForkChildSupervisor = func(_ context.Context, _ *service.Service, _ domain.SandboxID, _ string) error {
		return nil
	}

	ctx := context.Background()
	if err := spawnForkChildSupervisors(ctx, nil, storeRoot, parentID, children); err != nil {
		t.Fatalf("spawnForkChildSupervisors: %v", err)
	}

	// Assert spawn.json exists for each child and carries the child's own ID,
	// not the parent's ID. Without WriteSpawnSpec in writeAndSpawnForkChild the
	// child state dirs would be empty and ReadSpawnSpec would fail.
	for _, child := range children {
		childStateDir := supervisor.DefaultStateDir(storeRoot, child.ID)
		cfg, err := supervisor.ReadSpawnSpec(childStateDir)
		if err != nil {
			t.Errorf("ReadSpawnSpec for child %s: %v (WriteSpawnSpec not called?)", child.ID, err)
			continue
		}
		if cfg.SandboxRef != child.ID.String() {
			t.Errorf("child %s spawn.json SandboxRef = %q, want %q (child identity not applied)",
				child.ID, cfg.SandboxRef, child.ID.String())
		}
		// Confirm the parent's ID was NOT left as SandboxRef.
		if cfg.SandboxRef == parentID.String() {
			t.Errorf("child %s spawn.json still has parent SandboxRef %q", child.ID, parentID)
		}
	}
}

// TestForkSupervisor_CallsSpawnForEachChild verifies that spawnForkChildSupervisors
// calls doSpawnForkChildSupervisor once per child with the correct stateDir.
//
// Mutation-proof for s46-fork-supervisor-parity AC-4 (mutation 2):
// removing the doSpawnForkChildSupervisor call from writeAndSpawnForkChild
// makes this RED because the spy records zero calls.
func TestForkSupervisor_CallsSpawnForEachChild(t *testing.T) {
	storeRoot, err := os.MkdirTemp("/tmp", "n3forksuper")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(storeRoot) })

	parentID := domain.NewSandboxID()
	child1ID := domain.NewSandboxID()
	child2ID := domain.NewSandboxID()

	// Write parent spawn.json.
	parentStateDir := supervisor.DefaultStateDir(storeRoot, parentID)
	parentCfg := supervisor.Config{
		SandboxRef: parentID.String(),
		StoreRoot:  storeRoot,
		StateDir:   parentStateDir,
		MemoryMiB:  1024,
		BootVCPUs:  4,
	}
	if err := supervisor.WriteSpawnSpec(parentStateDir, parentCfg); err != nil {
		t.Fatalf("WriteSpawnSpec parent: %v", err)
	}

	// NetnsChildPID must be non-zero so the new per-child filter passes.
	children := []domain.Sandbox{
		{ID: child1ID, NetnsChildPID: 1, Provenance: &domain.Provenance{ParentID: parentID}},
		{ID: child2ID, NetnsChildPID: 1, Provenance: &domain.Provenance{ParentID: parentID}},
	}

	// Spy that records which (id, stateDir) pairs were passed.
	type call struct {
		id       domain.SandboxID
		stateDir string
	}
	var mu sync.Mutex
	var calls []call

	orig := doSpawnForkChildSupervisor
	t.Cleanup(func() { doSpawnForkChildSupervisor = orig })
	doSpawnForkChildSupervisor = func(_ context.Context, _ *service.Service, id domain.SandboxID, stateDir string) error {
		mu.Lock()
		calls = append(calls, call{id: id, stateDir: stateDir})
		mu.Unlock()
		return nil
	}

	ctx := context.Background()
	if err := spawnForkChildSupervisors(ctx, nil, storeRoot, parentID, children); err != nil {
		t.Fatalf("spawnForkChildSupervisors: %v", err)
	}

	// Without the doSpawnForkChildSupervisor call, len(calls) == 0 → RED.
	if len(calls) != len(children) {
		t.Fatalf("doSpawnForkChildSupervisor called %d times, want %d", len(calls), len(children))
	}

	for i, child := range children {
		wantStateDir := supervisor.DefaultStateDir(storeRoot, child.ID)
		if calls[i].id != child.ID {
			t.Errorf("call[%d].id = %s, want %s", i, calls[i].id, child.ID)
		}
		if calls[i].stateDir != wantStateDir {
			t.Errorf("call[%d].stateDir = %q, want %q", i, calls[i].stateDir, wantStateDir)
		}
	}
}

// TestForkWith_NetlessParent_NoSupervisor verifies that nexus3 fork on a
// netless (vsock-only) parent succeeds and does not attempt to spawn a
// supervisor for any child (AC-1).
//
// Before the fix, spawnForkChildSupervisors was called unconditionally for all
// children, and reacquirePreflight refused NetnsChildPID=0 children — causing
// fork to fail after the children were already persisted as Running.
func TestForkWith_NetlessParent_NoSupervisor(t *testing.T) {
	// Redirect store root to a temp dir. spawnForkChildSupervisors should
	// bail before reading it (netted slice empty), but this prevents any
	// accidental side-effect on the real store.
	storeRoot := t.TempDir()
	origRoot := doStoreDefaultRoot
	t.Cleanup(func() { doStoreDefaultRoot = origRoot })
	doStoreDefaultRoot = func() (string, error) { return storeRoot, nil }

	// Spy on doSpawnForkChildSupervisor: it must NOT be called for netless children.
	var spyCalled bool
	origChild := doSpawnForkChildSupervisor
	t.Cleanup(func() { doSpawnForkChildSupervisor = origChild })
	doSpawnForkChildSupervisor = func(_ context.Context, _ *service.Service, _ domain.SandboxID, _ string) error {
		spyCalled = true
		return nil
	}

	// Default fake driver does NOT implement NetnsStateProvider → children
	// will have NetnsChildPID=0 → spawnForkChildSupervisors must skip them.
	svc := newSnapSvc(t)
	ref := startedSandbox(t, svc, "proj/fork-netless")

	out, _, _ := capture(true)
	if err := runForkWith(context.Background(), []string{ref, "--count", "1"}, out, svc); err != nil {
		t.Fatalf("runForkWith netless: %v (regression — netless fork must succeed)", err)
	}

	if spyCalled {
		t.Error("doSpawnForkChildSupervisor called for a netless child; want skipped")
	}
}

// TestForkWith_NetworkedCallSite_SpiesAreCalled verifies that runForkWith
// calls the real doSpawnForkSupervisors (spawnForkChildSupervisors) for a
// networked parent, so that deleting cmd_fork.go:116-125 (the supervisor-spawn
// block) turns this test RED (AC-3).
//
// This closes the call-site gap identified in the CORRECTION: the existing
// TestForkSupervisor_* tests call spawnForkChildSupervisors directly (one
// frame below the call site), so removing the call site in runForkWith leaves
// every gate green.  This test sits at the correct frame.
func TestForkWith_NetworkedCallSite_SpiesAreCalled(t *testing.T) {
	// Redirect store root so spawnForkChildSupervisors reads/writes within
	// a clean temp dir.
	storeRoot := t.TempDir()
	origRoot := doStoreDefaultRoot
	t.Cleanup(func() { doStoreDefaultRoot = origRoot })
	doStoreDefaultRoot = func() (string, error) { return storeRoot, nil }

	// Keep doSpawnForkSupervisors as the real spawnForkChildSupervisors —
	// do NOT replace it.  Only the per-child spawn function gets a spy.
	var mu sync.Mutex
	var spyCalls []domain.SandboxID
	origChild := doSpawnForkChildSupervisor
	t.Cleanup(func() { doSpawnForkChildSupervisor = origChild })
	doSpawnForkChildSupervisor = func(_ context.Context, _ *service.Service, id domain.SandboxID, _ string) error {
		mu.Lock()
		spyCalls = append(spyCalls, id)
		mu.Unlock()
		return nil
	}

	// Build a service backed by netFakeDriver so the service layer assigns a
	// non-zero NetnsChildPID to every fork child.
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	aStore, err := artifact.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("artifact.NewStore: %v", err)
	}
	drv := &netFakeDriver{FakeDriver: fake.New()}
	svc := service.New(st, drv, lifecycle.New()).WithArtifacts(aStore)

	ref := startedSandbox(t, svc, "proj/fork-callsite")
	parent, err := svc.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("svc.Get parent: %v", err)
	}

	// Write the parent's spawn.json so spawnForkChildSupervisors can read it.
	parentStateDir := supervisor.DefaultStateDir(storeRoot, parent.ID)
	parentCfg := supervisor.Config{
		SandboxRef: parent.ID.String(),
		StoreRoot:  storeRoot,
		StateDir:   parentStateDir,
		MemoryMiB:  512,
		BootVCPUs:  2,
	}
	if err := supervisor.WriteSpawnSpec(parentStateDir, parentCfg); err != nil {
		t.Fatalf("WriteSpawnSpec parent: %v", err)
	}

	const wantCount = 2
	out, _, _ := capture(true)
	if err := runForkWith(context.Background(), []string{ref, "--count", "2"}, out, svc); err != nil {
		t.Fatalf("runForkWith networked: %v", err)
	}

	mu.Lock()
	n := len(spyCalls)
	mu.Unlock()

	// Without cmd_fork.go:116-125, doSpawnForkSupervisors is never called →
	// spy records 0 calls → this assertion turns RED.
	if n != wantCount {
		t.Fatalf("doSpawnForkChildSupervisor called %d times, want %d", n, wantCount)
	}
}

