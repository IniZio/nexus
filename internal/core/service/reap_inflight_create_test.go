package service

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"

	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/store"
)

// TestReap_InFlightCreate_DiskSurvivesConcurrentReap drives a real
// CreateAndBoot and runs a real Reap --apply from INSIDE the create window —
// after cowExt4 has materialised <id>.raw and before store.Create commits the
// record.
//
// The WorkspaceCapturer hook (stage 4.5) is the synchronisation point: it runs
// after the disk copy and before the record write, so calling Reap there places
// the reaper in the vulnerable window deterministically, with no sleeps and no
// timing dependence.
//
// Invariant: a create that is in flight must keep its disks. The reaper's job
// is reclaiming LEAKED disks, and a disk whose creator is still running is not
// leaked.
func TestReap_InFlightCreate_DiskSurvivesConcurrentReap(t *testing.T) {
	ctx := context.Background()

	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	fd := fake.New()
	svc := New(st, fd, lifecycle.New())

	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})
	// Empty ProcDir: no process anywhere carries the ULID during the window —
	// which is exactly the production situation, since the creator is the
	// nexus3 CLI (its cmdline has no ULID) and cloud-hypervisor is not launched
	// until after store.Create.
	emptyProcDir := t.TempDir()

	var reapReport *ReapReport
	var rawAtCaptureTime string

	capturer := func(_ context.Context, _, outExt4 string, _ int64) error {
		// Derive the in-flight .raw path from the workspace disk path.
		base := filepath.Base(outExt4)
		const suffix = "-workspace.ext4"
		idStr := base[:len(base)-len(suffix)]
		rawAtCaptureTime = filepath.Join(diskDir, idStr+".raw")
		if _, statErr := os.Stat(rawAtCaptureTime); statErr != nil {
			t.Errorf("setup: in-flight .raw missing at capture time: %v", statErr)
		}

		// A concurrent `nexus3 reap --apply` fires mid-create.
		rep, reapErr := Reap(ctx, st, idx, true /*apply*/, ReapOptions{ProcDir: emptyProcDir})
		if reapErr != nil {
			t.Errorf("Reap: %v", reapErr)
		}
		reapReport = rep

		// Materialise the workspace disk so the create can proceed.
		f, cErr := os.Create(outExt4)
		if cErr != nil {
			return cErr
		}
		return f.Close()
	}

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "inflight",
		CreateAndBootOptions{
			Image:             ImageSpec{Digest: string(img.Digest)},
			CacheRoot:         cacheRoot,
			DiskDir:           diskDir,
			Workspace:         &WorkspaceSpec{SourcePath: "/repo", GuestPath: "/workspace/repo"},
			WorkspaceCapturer: capturer,
			// Skip rather than fatal when the host genuinely lacks the disk space
			// this test needs. The skip fires only on measured insufficient space;
			// hosts with enough space run and pass normally.
			DiskPreflight: func(dir string, projected int64, _ string) (*DiskPreflightResult, error) {
				free, fErr := DiskStatfs(dir)
				if fErr != nil {
					return nil, fmt.Errorf("statfs %s: %w", dir, fErr)
				}
				if projected > free {
					t.Skipf("insufficient disk space at %s: need %.2f GiB (%d bytes), only %.2f GiB (%d bytes) free",
						dir, float64(projected)/(1<<30), projected, float64(free)/(1<<30), free)
				}
				return &DiskPreflightResult{ProjectedBytes: projected, FreeBytes: free}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// The record is committed and the sandbox is live...
	if _, getErr := st.Get(ctx, sb.ID); getErr != nil {
		t.Fatalf("record missing after create: %v", getErr)
	}
	// ...so its disk must still exist. If the mid-create reap unlinked it, the
	// sandbox now owns a disk that is gone: silent data loss.
	if _, statErr := os.Stat(rawAtCaptureTime); statErr != nil {
		t.Errorf("DATA LOSS: disk of committed sandbox %s was deleted by a concurrent reap: %v",
			sb.ID, statErr)
		t.Errorf("reaper deleted: %v", reapReport.Deleted)
	}
}

// writeUnleasedIntent writes an intent file the way a crashed creator (or an
// older binary that predates leases) leaves one behind: on disk, with nobody
// holding its lease. It returns the intent and disk paths.
func writeUnleasedIntent(t *testing.T, diskDir string, id domain.SandboxID) (intentPath, rawPath string) {
	t.Helper()
	rawPath = filepath.Join(diskDir, id.String()+".raw")
	intentPath = IntentPath(diskDir, id)
	body := `{"id":"` + id.String() + `","disk_copy_path":"` + rawPath + `"}`
	if err := os.WriteFile(intentPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}
	if err := os.WriteFile(rawPath, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	return intentPath, rawPath
}

// TestReap_UnleasedIntentIsReclaimed pins the reaper's core purpose: an intent
// with no live lease — a crashed create, or one written by a binary that
// predates leases — is still reclaimed, disks and all.
//
// Adding a keep-condition to a reaper is only safe if the keep-condition can
// expire. This is the test that says it does.
func TestReap_UnleasedIntentIsReclaimed(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	id := domain.NewSandboxID()
	intentPath, rawPath := writeUnleasedIntent(t, diskDir, id)

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	report, err := Reap(ctx, st, idx, true /*apply*/, ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(report.Deleted) != 2 {
		t.Errorf("expected intent + raw to be reclaimed, deleted = %v", report.Deleted)
	}
	for _, p := range []string{intentPath, rawPath} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("leaked resource not reclaimed: %s", p)
		}
	}
}

// TestReap_LeaseIsALeaseNotABlock verifies both halves of the lease contract
// with the real writeCreateIntent lease:
//
//	held    → every resource of that sandbox is kept (intent, .raw, workspace)
//	released → the very same resources are reclaimed
//
// The second half is what stops the fix from degrading into a permanent
// keep-condition.
func TestReap_LeaseIsALeaseNotABlock(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	id := domain.NewSandboxID()
	rawPath := filepath.Join(diskDir, id.String()+".raw")
	wsPath := filepath.Join(diskDir, id.String()+"-workspace.ext4")

	lease, err := writeCreateIntent(diskDir, id, rawPath, wsPath)
	if err != nil {
		t.Fatalf("writeCreateIntent: %v", err)
	}
	for _, p := range []string{rawPath, wsPath} {
		if err := os.WriteFile(p, make([]byte, 4096), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	// ── Lease held: everything is kept, and for the right reason ────────────
	report, err := Reap(ctx, st, idx, true /*apply*/, ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap (leased): %v", err)
	}
	if len(report.Deleted) != 0 {
		t.Errorf("leased create was reclaimed: %v", report.Deleted)
	}
	if len(report.Entries) != 3 {
		t.Fatalf("expected 3 resources (intent, raw, workspace), got %d: %v",
			len(report.Entries), report.Entries)
	}
	for _, e := range report.Entries {
		if e.Status != ReapStatusLive {
			t.Errorf("%s: status = %q, want %q", filepath.Base(e.Resource.Path), e.Status, ReapStatusLive)
		}
		if !strings.Contains(e.Reason, "create in flight") {
			t.Errorf("%s: reason = %q, want the in-flight lease reason",
				filepath.Base(e.Resource.Path), e.Reason)
		}
	}

	// ── Lease released: the same resources become reclaimable ───────────────
	// release removes the intent file, so recreate it unleased — that is
	// exactly the on-disk state a creator killed mid-window leaves behind.
	lease.release()
	if err := os.WriteFile(IntentPath(diskDir, id), []byte(`{"id":"`+id.String()+`"}`), 0o600); err != nil {
		t.Fatalf("rewrite intent: %v", err)
	}

	report, err = Reap(ctx, st, idx, true /*apply*/, ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap (released): %v", err)
	}
	if len(report.Deleted) != 3 {
		t.Errorf("after release, expected all 3 resources reclaimed, deleted = %v", report.Deleted)
	}
	for _, p := range []string{rawPath, wsPath} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("resource not reclaimed after lease release: %s", p)
		}
	}
}

// TestHelperHoldIntentLease is not a test: it is the helper process body for
// TestReap_KilledCreatorDoesNotBlockReclamation. It takes the lease on the
// intent file named by the environment and then blocks, standing in for a
// creator that is inside the create window.
//
// It exits on its own after a bounded wait so a failed parent cannot leave a
// process behind.
func TestHelperHoldIntentLease(t *testing.T) {
	path := os.Getenv("NEXUS3_TEST_HOLD_INTENT_LEASE")
	if path == "" {
		t.Skip("helper process body; not a standalone test")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("helper: open %s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("helper: flock: %v", err)
	}
	fmt.Println("LEASE-HELD")
	os.Stdout.Sync()
	time.Sleep(60 * time.Second)
}

// TestReap_KilledCreatorDoesNotBlockReclamation is the load-bearing test for
// the whole design: the lease must be released by the KERNEL when the creator
// dies, with no cooperation from the dying process.
//
// A separate process takes the lease and is then SIGKILLed — no defers, no
// cleanup, no chance to release anything. The reaper must keep the resources
// while that process lives and reclaim them once it is dead.
//
// This is what rules out the failure mode that would make the fix worse than
// the bug: a crashed creator leaving a keep-condition that never expires. It is
// also the only test here that exercises the lease across process boundaries,
// which is the configuration production actually runs in (`nexus3 reap` is a
// different process from `nexus3 sandbox create`).
func TestReap_KilledCreatorDoesNotBlockReclamation(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	id := domain.NewSandboxID()
	intentPath, rawPath := writeUnleasedIntent(t, diskDir, id)

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	// ── Start the stand-in creator and wait until it holds the lease ────────
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldIntentLease", "-test.v")
	cmd.Env = append(os.Environ(), "NEXUS3_TEST_HOLD_INTENT_LEASE="+intentPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	held := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "LEASE-HELD") {
				close(held)
				return
			}
		}
		if err := sc.Err(); err != nil {
			t.Errorf("lease-held scanner: %v", err)
		}
	}()
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("helper never reported holding the lease")
	}

	// ── Creator alive: resources are kept ──────────────────────────────────
	report, err := Reap(ctx, st, idx, true /*apply*/, ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap (creator alive): %v", err)
	}
	if len(report.Deleted) != 0 {
		t.Errorf("reaped an in-flight create held by a live process: %v", report.Deleted)
	}
	if _, statErr := os.Stat(rawPath); statErr != nil {
		t.Errorf("in-flight disk deleted while creator alive: %v", statErr)
	}

	// ── SIGKILL the creator: the kernel drops the lease ────────────────────
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait()
	killed = true

	report, err = Reap(ctx, st, idx, true /*apply*/, ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap (creator dead): %v", err)
	}
	if len(report.Deleted) != 2 {
		t.Errorf("dead creator blocked reclamation — deleted = %v, want intent + raw", report.Deleted)
	}
	for _, p := range []string{intentPath, rawPath} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("resource still present after the creator died: %s", p)
		}
	}
}

// TestWriteCreateIntent_LeasedBeforeVisible pins the ordering the whole design
// rests on: the intent file must never be discoverable by the reaper in an
// unleased state.
//
// If the intent were created directly at its canonical path and flocked
// afterwards, a reaper scanning in between would see an unleased intent for a
// live create and reclaim its disks — the original bug, just with a smaller
// window. writeCreateIntent therefore stages the file under a name the
// resource index does not match, takes the lease, and renames it into place.
//
// The intentFileSyncer seam runs after the lease is taken and before the
// rename, which is exactly the instant to inspect: the canonical path must not
// exist yet, and the staging file must already be leased.
func TestWriteCreateIntent_LeasedBeforeVisible(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()
	canonical := IntentPath(dir, id)
	staging := canonical + ".tmp"

	orig := intentFileSyncer
	t.Cleanup(func() { intentFileSyncer = orig })

	var checked bool
	intentFileSyncer = func(f *os.File) error {
		checked = true
		if _, err := os.Stat(canonical); !os.IsNotExist(err) {
			t.Errorf("intent is already visible at %s before the lease is published", canonical)
		}
		// intentLeaseHeld opens its own descriptor, and flock(2) conflicts
		// between distinct open file descriptions even within one process, so
		// this genuinely probes the lease.
		if probeIntentLease(staging) != leaseHeld {
			t.Errorf("staging intent %s is not leased at sync time", staging)
		}
		return orig(f)
	}

	lease, err := writeCreateIntent(dir, id, filepath.Join(dir, id.String()+".raw"), "")
	if err != nil {
		t.Fatalf("writeCreateIntent: %v", err)
	}
	t.Cleanup(lease.release)

	if !checked {
		t.Fatal("intentFileSyncer seam was not invoked — the ordering was not observed")
	}
	if probeIntentLease(canonical) != leaseHeld {
		t.Error("published intent is not leased")
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging file %s was left behind", staging)
	}
}

// commitOnListStore is a store.Store decorator that snapshots records FIRST and
// only then lets the creator finish: on the first List it delegates (capturing
// the pre-commit view), then commits the record and releases the intent lease.
//
// That is precisely the interleaving the reaper's observation order must
// survive, forced deterministically with no sleeps.
type commitOnListStore struct {
	store.Store
	once       sync.Once
	afterFirst func()
}

func (s *commitOnListStore) List(ctx context.Context) ([]domain.Sandbox, error) {
	out, err := s.Store.List(ctx)
	// The creator races in here: it commits and releases AFTER this snapshot
	// was taken but BEFORE the caller inspects anything else.
	s.once.Do(s.afterFirst)
	return out, err
}

// TestReap_ObservationOrder_CommitBetweenRecordSnapshotAndLeaseProbe pins the
// ORDER in which Reap observes the world.
//
// The reaper cannot read the filesystem and the store atomically. If it
// snapshots records BEFORE probing the lease, a create that commits in between
// falls through both checks: absent from the stale record snapshot, and no
// longer leased by the time the probe runs, because release strictly follows
// store.Create. Its disk is then unlinked while the sandbox is live — the
// original data-loss signature with a smaller window.
//
// Probing the lease BEFORE listing records makes the outcome provable:
//
//   - leased at probe time      → kept as in flight;
//   - not leased at probe time  → the creator had already released, which
//     happens only after store.Create committed, so the later st.List is
//     guaranteed to see the record → kept as owned.
//
// This test forces the losing interleaving. It fails when the in-flight probe
// is moved back below st.List.
func TestReap_ObservationOrder_CommitBetweenRecordSnapshotAndLeaseProbe(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	id := domain.NewSandboxID()
	rawPath := filepath.Join(diskDir, id.String()+".raw")

	// A create is in flight: intent leased, disk materialised, no record yet.
	lease, err := writeCreateIntent(diskDir, id, rawPath, "")
	if err != nil {
		t.Fatalf("writeCreateIntent: %v", err)
	}
	defer lease.release()
	if err := os.WriteFile(rawPath, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("write raw: %v", err)
	}

	base, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	decorated := &commitOnListStore{Store: base}
	decorated.afterFirst = func() {
		// The creator finishes: commit, then release. This mirrors production
		// ordering exactly — CreateAndBoot's deferred release runs after
		// store.Create has returned.
		if createErr := base.Create(ctx, domain.Sandbox{
			ID: id, Name: "racing", Project: "proj", State: domain.Created,
		}); createErr != nil {
			t.Errorf("commit record: %v", createErr)
		}
		lease.release()
	}

	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})
	report, err := Reap(ctx, decorated, idx, true /*apply*/, ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// The sandbox is live: its record is committed.
	if _, getErr := base.Get(ctx, id); getErr != nil {
		t.Fatalf("setup: record was not committed: %v", getErr)
	}
	// So its disk must still be there.
	if _, statErr := os.Stat(rawPath); statErr != nil {
		t.Errorf("DATA LOSS: disk of live sandbox %s unlinked because the record snapshot "+
			"was taken before the lease probe: %v", id, statErr)
		t.Errorf("reaper deleted: %v", report.Deleted)
	}
	// Either keep-reason is safe here (leased at probe time, or owned by the
	// time records are read); reclaiming is not.
	for _, e := range report.Entries {
		if e.Resource.Path != rawPath {
			continue
		}
		if e.Status == ReapStatusOrphan {
			t.Errorf("disk of live sandbox classified %q (reason: %s)", e.Status, e.Reason)
		}
	}
}

// TestReap_UnreadableIntentIsReportedDistinctly covers the one keep-condition
// that does NOT expire on its own: an intent file the reaper cannot read (for
// example one left mode-0600 by another uid). Ambiguity must keep — the
// reaper's N-AC2 rule — but it must not be reported as "create in flight", or
// an operator staring at a permanent keep would read it as a create that is
// merely still running.
func TestReap_UnreadableIntentIsReportedDistinctly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file modes do not deny access, cannot make the intent unreadable")
	}
	ctx := context.Background()
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	id := domain.NewSandboxID()
	intentPath, rawPath := writeUnleasedIntent(t, diskDir, id)
	if err := os.Chmod(intentPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(intentPath, 0o600) })

	if got := probeIntentLease(intentPath); got != leaseUnknown {
		t.Fatalf("probeIntentLease on an unreadable intent = %v, want leaseUnknown", got)
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})
	report, err := Reap(ctx, st, idx, true /*apply*/, ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if len(report.Deleted) != 0 {
		t.Errorf("ambiguous intent must keep, but reaper deleted: %v", report.Deleted)
	}
	var found bool
	for _, e := range report.Entries {
		if e.Resource.Path != rawPath {
			continue
		}
		found = true
		if strings.Contains(e.Reason, "create in flight") {
			t.Errorf("unreadable intent reported as a live create: %q", e.Reason)
		}
		if !strings.Contains(e.Reason, "unreadable") || !strings.Contains(e.Reason, "does not expire") {
			t.Errorf("reason does not identify a non-expiring keep: %q", e.Reason)
		}
	}
	if !found {
		t.Fatalf("disk resource missing from report: %v", report.Entries)
	}
}
