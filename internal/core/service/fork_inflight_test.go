package service_test

// fork_inflight_test.go pins the fork half of the in-flight-create invariant:
//
//	A sandbox disk must not be deleted while the sandbox that owns it is still
//	being created.
//
// CreateAndBoot closes its window with a flocked create-intent file. Fork
// reaches the same window by a different path: the driver writes
// <diskDir>/<childID>.raw for every child inside ForkFrom, and Service.Fork
// does not commit any child record until ForkFrom has returned for all of
// them. Without a lease, a `nexus3 reap --apply` firing in that window sees a
// ULID-keyed disk with no record and no live process — an orphan by every
// rule the reaper has — and unlinks it, while Fork goes on to return the child
// as a live sandbox. Silent loss, not a failed fork.
//
// These tests drive the real Service.Fork and the real Reap. The driver
// wrapper below is the synchronisation point: it materialises the child disks
// and fires the reap from inside the window, so there are no sleeps and no
// timing dependence.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// reapDuringForkDriver wraps FakeDriver so a test can run arbitrary code at the
// exact point the real driver materialises child disks: inside ForkFrom,
// before any child record exists.
//
// onFork runs BEFORE the embedded ForkFrom so that the observation happens
// while the fork is genuinely in flight, matching cloudhypervisor/fork.go
// where reflinkCopy writes <childID>.raw before the call returns.
type reapDuringForkDriver struct {
	*fake.FakeDriver
	onFork func(childIDs []domain.SandboxID)
}

func (d *reapDuringForkDriver) ForkFrom(
	ctx context.Context, snap artifact.Snapshot, childIDs []domain.SandboxID,
) ([]string, error) {
	if d.onFork != nil {
		d.onFork(childIDs)
	}
	return d.FakeDriver.ForkFrom(ctx, snap, childIDs)
}

// forkInflightHarness wires a Service whose disk directory is the one the
// ResourceIndex scans, seeds a Running parent, and returns everything the
// tests need to fire a real reap from inside the fork window.
type forkInflightHarness struct {
	svc     *service.Service
	st      store.Store
	drv     *reapDuringForkDriver
	parent  domain.Sandbox
	diskDir string
	idx     *service.ResourceIndex
	// procDir is deliberately empty: during the fork window no process
	// anywhere carries a child ULID in its cmdline (the forking process is the
	// nexus3 CLI, and the child VMM is not addressable by ULID), which is
	// exactly the production situation the /proc gate cannot see.
	procDir string
}

func newForkInflightHarness(t *testing.T) *forkInflightHarness {
	t.Helper()
	ctx := context.Background()

	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir disks: %v", err)
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := &reapDuringForkDriver{FakeDriver: fake.New()}
	svc := service.New(st, drv, lifecycle.New()).WithDiskDir(diskDir)

	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "fork-inflight-parent",
		Project: "proj",
		State:   domain.Created,
	}
	if err := st.Create(ctx, parent); err != nil {
		t.Fatalf("seed parent record: %v", err)
	}
	started, err := svc.Start(ctx, parent.ID.String())
	if err != nil {
		t.Fatalf("Start(parent): %v", err)
	}

	return &forkInflightHarness{
		svc:     svc,
		st:      st,
		drv:     drv,
		parent:  started,
		diskDir: diskDir,
		idx:     service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()}),
		procDir: t.TempDir(),
	}
}

// materialiseChildDisks writes a <childID>.raw for each child, standing in for
// the driver's reflink copy. Content is non-empty so a deletion is detectable
// as a missing file rather than an ambiguous zero-length one.
func (h *forkInflightHarness) materialiseChildDisks(t *testing.T, childIDs []domain.SandboxID) []string {
	t.Helper()
	paths := make([]string, 0, len(childIDs))
	for _, id := range childIDs {
		p := filepath.Join(h.diskDir, id.String()+".raw")
		if err := os.WriteFile(p, []byte("child disk contents"), 0o600); err != nil {
			t.Errorf("materialise child disk %s: %v", p, err)
		}
		paths = append(paths, p)
	}
	return paths
}

func (h *forkInflightHarness) reapApply(t *testing.T) *service.ReapReport {
	t.Helper()
	rep, err := service.Reap(context.Background(), h.st, h.idx, true, /*apply*/
		service.ReapOptions{ProcDir: h.procDir})
	if err != nil {
		t.Errorf("Reap: %v", err)
	}
	return rep
}

// TestFork_InFlightChildDiskSurvivesConcurrentReap is the crux: a reap running
// inside the fork window must not take the disks of children that Fork is
// about to return as live sandboxes.
func TestFork_InFlightChildDiskSurvivesConcurrentReap(t *testing.T) {
	ctx := context.Background()
	h := newForkInflightHarness(t)

	var report *service.ReapReport
	h.drv.onFork = func(childIDs []domain.SandboxID) {
		h.materialiseChildDisks(t, childIDs)
		// A concurrent `nexus3 reap --apply` fires mid-fork.
		report = h.reapApply(t)
	}

	children, err := h.svc.Fork(ctx, h.parent.ID.String(), 2)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("Fork returned %d children, want 2", len(children))
	}

	for _, child := range children {
		// The record is committed and Fork reported the child as live...
		if _, getErr := h.st.Get(ctx, child.ID); getErr != nil {
			t.Fatalf("record missing after fork: %v", getErr)
		}
		// ...so its disk must still be there.
		raw := filepath.Join(h.diskDir, child.ID.String()+".raw")
		if _, statErr := os.Stat(raw); statErr != nil {
			t.Errorf("DATA LOSS: disk of committed fork child %s was deleted by a "+
				"concurrent reap: %v", child.ID, statErr)
			if report != nil {
				t.Errorf("reaper deleted: %v", report.Deleted)
			}
		}
	}
}

// TestFork_ChildLeaseIsALeaseNotABlock is the complement: the protection must
// not turn a failed fork into a permanent leak. When Fork fails after the
// driver has already written child disks, no child record is ever committed —
// so the leases must be released on the way out and the disks must become
// reclaimable orphans on the very next reap.
//
// Without this, the fix would trade silent data loss for unbounded disk growth
// that no reap can ever clear, which on multi-GiB images is its own outage.
func TestFork_ChildLeaseIsALeaseNotABlock(t *testing.T) {
	ctx := context.Background()
	h := newForkInflightHarness(t)

	var childRaws []string
	h.drv.onFork = func(childIDs []domain.SandboxID) {
		childRaws = h.materialiseChildDisks(t, childIDs)
	}
	h.drv.SetForkError(errForkFailed)

	if _, err := h.svc.Fork(ctx, h.parent.ID.String(), 2); err == nil {
		t.Fatal("harness: Fork succeeded; this test needs the driver failure to propagate")
	}
	if len(childRaws) != 2 {
		t.Fatalf("harness: %d child disks materialised, want 2", len(childRaws))
	}

	// Fork has returned, so every lease it held is gone. The disks belong to no
	// sandbox and no live creator: they are orphans, and a reap must reclaim them.
	report := h.reapApply(t)
	for _, raw := range childRaws {
		if _, err := os.Stat(raw); err == nil {
			t.Errorf("LEAK: disk %s of a failed fork survived a reap — the child lease "+
				"is behaving as a permanent block rather than a lease (reaper deleted: %v)",
				raw, report.Deleted)
		}
	}
	// The intent files must be gone too, so a later reap has nothing to probe.
	matches, err := filepath.Glob(filepath.Join(h.diskDir, "*.create-intent.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("LEAK: create-intent files survived a failed fork plus a reap: %v", matches)
	}
}

// errForkFailed is a driver-layer failure injected to exercise Fork's error path.
var errForkFailed = forkTestError("driver: fork failed")

type forkTestError string

func (e forkTestError) Error() string { return string(e) }

// reapOnCreateStore fires a reap at the instant a child record is committed —
// after Fork decided the child is real, before the record is durable. It is
// the ordering probe: if a lease were dropped even slightly before its
// store.Create, this is where the reaper would find the disk unowned and
// unleased.
type reapOnCreateStore struct {
	store.Store
	onCreate func()
}

func (s *reapOnCreateStore) Create(ctx context.Context, sb domain.Sandbox) error {
	if s.onCreate != nil {
		s.onCreate()
	}
	return s.Store.Create(ctx, sb)
}

// TestFork_LeaseHeldAcrossTheCommitItself pins the ordering claim behind the
// fix: a child's lease must survive until AFTER its record is committed, not
// merely until the driver returns.
//
// The reap fires from inside store.Create, which is the narrowest possible
// window — the record does not exist yet, and it is the last instant at which
// the lease is the only thing standing between the child and the reaper.
func TestFork_LeaseHeldAcrossTheCommitItself(t *testing.T) {
	ctx := context.Background()
	h := newForkInflightHarness(t)

	// Wrap the store the service writes through, keeping the same underlying
	// FileStore the reaper reads, so both observe one set of records.
	wrapped := &reapOnCreateStore{Store: h.st}
	svc := service.New(wrapped, h.drv, lifecycle.New()).WithDiskDir(h.diskDir)

	h.drv.onFork = func(childIDs []domain.SandboxID) {
		h.materialiseChildDisks(t, childIDs)
	}
	var report *service.ReapReport
	wrapped.onCreate = func() { report = h.reapApply(t) }

	children, err := svc.Fork(ctx, h.parent.ID.String(), 2)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	for _, child := range children {
		raw := filepath.Join(h.diskDir, child.ID.String()+".raw")
		if _, statErr := os.Stat(raw); statErr != nil {
			t.Errorf("DATA LOSS: disk of fork child %s was deleted by a reap firing "+
				"during its own commit: %v (reaper deleted: %v)", child.ID, statErr, report.Deleted)
		}
	}
}
