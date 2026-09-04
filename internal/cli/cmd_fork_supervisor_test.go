package cli

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/supervisor"
)

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
	children := []domain.Sandbox{
		{ID: child1ID, Provenance: &domain.Provenance{ParentID: parentID}},
		{ID: child2ID, Provenance: &domain.Provenance{ParentID: parentID}},
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

	children := []domain.Sandbox{
		{ID: child1ID, Provenance: &domain.Provenance{ParentID: parentID}},
		{ID: child2ID, Provenance: &domain.Provenance{ParentID: parentID}},
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
