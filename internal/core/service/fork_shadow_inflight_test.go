package service_test

// TBD-PD-38: during the fork window, the shadow copies ForkFrom writes for
// each child were not lease-protected.
//
// D-PD-74's flock lease covers the child's ULID-keyed <childID>.raw and
// workspace disk. The shadow copies are named <childID>-<parentSafeHandle>
// .shadow.<name>.ext4 and the reaper correlates them by HANDLE, so the ULID
// intent cannot protect them even in principle — the same keying mismatch
// TBD-PD-25 turned on, one layer down.
//
// These tests fire a REAL service.Reap(apply=true) from inside ForkFrom, which
// is the actual concurrency rather than a parse of it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/service"
)

// seedParentShadowDisk writes a shadow disk owned by the parent handle and
// returns its basename.
func seedParentShadowDisk(t *testing.T, diskDir, parentHandle, dirName string) string {
	t.Helper()
	base := service.SafeHandle(parentHandle) + ".shadow." + dirName + ".ext4"
	if err := os.WriteFile(filepath.Join(diskDir, base), []byte("parent shadow"), 0o600); err != nil {
		t.Fatalf("seed parent shadow disk: %v", err)
	}
	return base
}

// materialiseChildShadowCopies stands in for the driver's per-child copy,
// using the driver's OWN naming function so the test cannot drift from
// production by agreeing with the code under test instead of with reality.
func materialiseChildShadowCopies(t *testing.T, diskDir string, childIDs []domain.SandboxID, parentBases []string) []string {
	t.Helper()
	var paths []string
	for _, id := range childIDs {
		for _, base := range parentBases {
			p := cloudhypervisor.ChildExtraDiskPath(id, filepath.Join(diskDir, base))
			if err := os.WriteFile(p, []byte("child shadow copy"), 0o600); err != nil {
				t.Fatalf("materialise child shadow copy %s: %v", p, err)
			}
			paths = append(paths, p)
		}
	}
	return paths
}

// The crux. A reap landing inside the fork window must not take the shadow
// copies of children Fork is about to return as live sandboxes.
func TestFork_InFlightChildShadowCopiesSurviveConcurrentReap(t *testing.T) {
	h := newForkInflightHarness(t)

	parentBase := seedParentShadowDisk(t, h.diskDir, h.parent.Handle(), "node_modules")

	var copies []string
	var report *service.ReapReport
	h.drv.onFork = func(childIDs []domain.SandboxID) {
		h.materialiseChildDisks(t, childIDs)
		copies = materialiseChildShadowCopies(t, h.diskDir, childIDs, []string{parentBase})
		report = h.reapApply(t)
	}

	children, err := h.svc.Fork(context.Background(), h.parent.ID.String(), 2)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("Fork returned %d children, want 2", len(children))
	}
	if len(copies) != 2 {
		t.Fatalf("materialised %d shadow copies, want 2", len(copies))
	}

	for _, p := range copies {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("child shadow copy %s was DELETED mid-fork: %v", filepath.Base(p), statErr)
		}
	}
	if report != nil {
		for _, d := range report.Deleted {
			if strings.Contains(d, ".shadow.") {
				t.Errorf("reap deleted a shadow resource mid-fork: %s", d)
			}
		}
	}

	// The parent's own shadow disk must be untouched too — it is owned by a
	// live sandbox and was never part of this window.
	if _, statErr := os.Stat(filepath.Join(h.diskDir, parentBase)); statErr != nil {
		t.Errorf("parent shadow disk was deleted: %v", statErr)
	}
}

// The lease must not be a permanent shield. Once Fork returns, the children
// have records and are protected by ownership; the intent files must be gone,
// or a crashed fork would leak shadow copies forever.
func TestFork_ShadowIntentsAreReleasedAfterFork(t *testing.T) {
	h := newForkInflightHarness(t)
	parentBase := seedParentShadowDisk(t, h.diskDir, h.parent.Handle(), "node_modules")

	h.drv.onFork = func(childIDs []domain.SandboxID) {
		h.materialiseChildDisks(t, childIDs)
		materialiseChildShadowCopies(t, h.diskDir, childIDs, []string{parentBase})
	}

	children, err := h.svc.Fork(context.Background(), h.parent.ID.String(), 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	entries, err := os.ReadDir(h.diskDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".shadow-intent.json") {
			t.Errorf("shadow intent %s survived a completed fork", e.Name())
		}
	}

	// And the copies are still there, now protected by the child's record.
	child := children[0]
	want := cloudhypervisor.ChildExtraDiskPath(child.ID, filepath.Join(h.diskDir, parentBase))
	if _, statErr := os.Stat(want); statErr != nil {
		t.Errorf("child shadow copy missing after fork: %v", statErr)
	}
}

// A fork of a parent with NO shadow disks must not publish an intent. An
// intent naming paths nothing writes is not harmless: it is a file the reaper
// enumerates and an operator has to explain.
func TestFork_NoShadowDisksPublishesNoIntent(t *testing.T) {
	h := newForkInflightHarness(t)

	var midForkEntries []string
	h.drv.onFork = func(childIDs []domain.SandboxID) {
		h.materialiseChildDisks(t, childIDs)
		entries, err := os.ReadDir(h.diskDir)
		if err != nil {
			t.Errorf("ReadDir: %v", err)
			return
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".shadow-intent.json") {
				midForkEntries = append(midForkEntries, e.Name())
			}
		}
	}

	if _, err := h.svc.Fork(context.Background(), h.parent.ID.String(), 2); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if len(midForkEntries) != 0 {
		t.Errorf("fork of a shadow-less parent published intents: %v", midForkEntries)
	}
}

// A fork that fails after the intents are published must leave none behind.
// The intent's flock is held by THIS process, so a leaked intent does not just
// litter the directory — it keeps protecting copies that no child will ever
// own, for the lifetime of the process. That is a leak the reaper cannot
// clean up, which is worse than the exposure it was built to close.
func TestFork_FailedForkLeavesNoShadowIntents(t *testing.T) {
	h := newForkInflightHarness(t)
	parentBase := seedParentShadowDisk(t, h.diskDir, h.parent.Handle(), "node_modules")

	h.drv.onFork = func(childIDs []domain.SandboxID) {
		materialiseChildShadowCopies(t, h.diskDir, childIDs, []string{parentBase})
	}
	h.drv.SetForkError(errForkBoom)

	if _, err := h.svc.Fork(context.Background(), h.parent.ID.String(), 2); err == nil {
		t.Fatal("Fork succeeded despite an injected driver error")
	}

	entries, err := os.ReadDir(h.diskDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".shadow-intent.json") {
			t.Errorf("failed fork leaked shadow intent %s; its flock is still held by this process, "+
				"so it protects orphans permanently", e.Name())
		}
	}

	// And the now-ownerless copies must be reclaimable, not shielded.
	report := h.reapApply(t)
	for _, e := range report.Entries {
		base := filepath.Base(e.Resource.Path)
		// Only the per-child copies are ownerless. They are the ones whose
		// name begins with a child ULID; the parent's own disk starts with its
		// safeHandle and is legitimately owned.
		if !strings.Contains(base, ".shadow.") || !strings.HasPrefix(base, "sb-") {
			continue
		}
		if e.Status != service.ReapStatusOrphan {
			t.Errorf("orphaned copy %s classified %s after a failed fork; it is unreclaimable",
				filepath.Base(e.Resource.Path), e.Status)
		}
	}
}

var errForkBoom = errForkBoomType{}

type errForkBoomType struct{}

func (errForkBoomType) Error() string { return "injected fork failure" }
