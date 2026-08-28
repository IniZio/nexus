package cli

// cmd_herdr_worktree_race_test.go — concurrency tests for herdrWorktreeSandbox.
//
// Asserts that two concurrent callers for the same workspace converge on
// exactly ONE sandbox create.  The guard under test is the per-handle
// create-intent flock added in step 6.1.

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestHerdrWorktreeSandboxConcurrentCreateConverges drives two concurrent
// herdrWorktreeSandbox calls for the same workspace and asserts that exactly
// one sandbox create is issued.  The loser must not leave an orphan.
func TestHerdrWorktreeSandboxConcurrentCreateConverges(t *testing.T) {
	// Not t.Parallel: this test uses t.Setenv (below), which is incompatible
	// with parallel tests.

	// herdrWorktreeSandbox resolves the herdr binary (Step 2) before it ever
	// reaches createFn. resolveHerdrBin honors HERDR_BIN_PATH first, without
	// checking the path exists, so set it here. Without this the test passes
	// only on machines that happen to have `herdr` on PATH (e.g. a dev box) and
	// hangs in CI — resolveHerdrBin errors, herdrWorktreeSandbox returns early,
	// and createFn is never called.
	t.Setenv("HERDR_BIN_PATH", "herdr-stub-not-executed")

	storeRoot := t.TempDir()
	ctx := context.Background()

	branch := "feature/raceproof"
	// Use a fixed RepoKey so the derived handle is deterministic.
	// Production code: strip "/.git" → "/repo", base → "repo".
	const repoKey = "/repo/.git"
	handle := herdrWorktreeSandboxHandle("repo", branch) // "repo/feature-raceproof"

	// stubInfo is the worktree info both callers will see.
	stubInfo := herdrWorktreeInfo{
		IsLinkedWorktree: true,
		Branch:           branch,
		Path:             t.TempDir(),
		RepoKey:          repoKey,
	}

	// Inject list function: both callers always see the worktree.
	orig := herdrListWorktreeForWorkspaceFn
	herdrListWorktreeForWorkspaceFn = func(_ context.Context, _, _ string) (herdrWorktreeInfo, error) {
		return stubInfo, nil
	}
	t.Cleanup(func() { herdrListWorktreeForWorkspaceFn = orig })

	// Inject rename function: no-op.
	origRename := herdrWorkspaceRenameFn
	herdrWorkspaceRenameFn = func(_ context.Context, _, _, _ string) error { return nil }
	t.Cleanup(func() { herdrWorkspaceRenameFn = origRename })

	// createFn: counts calls, blocks until released, then writes a stub binding
	// so getFn can find the sandbox.
	var createCount atomic.Int32
	// release is closed by the test to let the first creator finish.
	release := make(chan struct{})
	// started is sent to when any createFn begins executing.
	started := make(chan struct{}, 2)

	// Use a real ULID sandbox ID so HerdrSpaceGetByHandle succeeds after create.
	stubID := domain.NewSandboxID()

	createFn := func(_ context.Context, h, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies) error {
		createCount.Add(1)
		started <- struct{}{}
		<-release // block until test releases
		// Write the binding so getFn finds it.
		_ = HerdrSpacePut(ctx, storeRoot, HerdrSpaceBinding{
			SpaceLabel:       "nexus3:" + h,
			HerdrWorkspaceID: "wTEST-winner",
			SandboxHandle:    h,
			SandboxID:        stubID.String(),
		})
		return nil
	}

	// getFn returns a stub sandbox with the pre-known ID.
	getFn := func(_ context.Context, _ string) (domain.Sandbox, error) {
		return domain.Sandbox{ID: stubID}, nil
	}

	// Launch caller 1 (workspaceID "wA") in a goroutine.
	var wg sync.WaitGroup
	wg.Add(2)
	var errA, errB error

	go func() {
		defer wg.Done()
		var buf bytes.Buffer
		errA = herdrWorktreeSandbox(ctx, "wA", &buf, storeRoot,
			false, false, false, createFn, getFn)
	}()

	// Wait until caller 1 has started its create (holds the lock).
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("caller 1 did not reach createFn within 5s")
	}

	// Launch caller 2 (workspaceID "wB") while caller 1 holds the lock.
	go func() {
		defer wg.Done()
		var buf bytes.Buffer
		errB = herdrWorktreeSandbox(ctx, "wB", &buf, storeRoot,
			false, false, false, createFn, getFn)
	}()

	// Give caller 2 a moment to reach the lock acquire and block.
	time.Sleep(100 * time.Millisecond)

	// Release caller 1 to finish the create and write the binding.
	close(release)

	wg.Wait()

	if errA != nil {
		t.Errorf("caller 1 returned error: %v", errA)
	}
	if errB != nil {
		t.Errorf("caller 2 returned error: %v", errB)
	}

	got := createCount.Load()
	if got != 1 {
		t.Errorf("createFn called %d time(s); want exactly 1 (the guard must stop the second create)", got)
	}

	// The binding must exist exactly once for this handle.
	bindings, err := HerdrSpaceList(ctx, storeRoot)
	if err != nil {
		t.Fatalf("HerdrSpaceList: %v", err)
	}
	var count int
	for _, b := range bindings {
		if b.SandboxHandle == handle {
			count++
		}
	}
	if count != 1 {
		t.Errorf("binding count for handle %q = %d; want 1", handle, count)
	}
}
