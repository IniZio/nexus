package service_test

// Tests for the supervisor state dir's LIFETIME and PERMISSIONS (D-HSH-18,
// ticket 13 / slice s14-statedir-lifetime).
//
// Every test here drives a REAL production call site — Service.Remove,
// service.Reap, supervisor.WriteSpawnSpec, statedir.Ensure — rather than a
// local re-implementation of the same rule. The defect class this repo has
// shipped twice is a test that builds a stand-in and asserts on that, so the
// question asked of each case below is "does this invoke the function
// production invokes".

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/statedir"
	"github.com/IniZio/nexus3/internal/core/store"
)

// shortTempDir returns a temp dir under /tmp rather than t.TempDir().
//
// t.TempDir() embeds the test name, and these tests build
// <root>/supervisors/<26-char ULID>/supervisor.sock underneath it, which blows
// the 107-byte sun_path limit on AF_UNIX and fails the bind with a misleading
// "invalid argument".
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "n3sd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// mustStateDir creates <stateRoot>/supervisors/<id> with a couple of files in
// it, the way a real supervisor leaves it, and returns the path.
func mustStateDir(t *testing.T, stateRoot string, id domain.SandboxID) string {
	t.Helper()
	dir := statedir.SupervisorDir(stateRoot, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	for _, name := range []string{"spawn.json", "supervisor.log", "egress-decisions.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// ── 1. teardown ──────────────────────────────────────────────────────────────

// TestRemove_DeletesSupervisorStateDir proves the real Service.Remove path
// takes the state dir with it.
//
// XDG_STATE_HOME redirects store.DefaultRoot, which is the same resolver
// startSupervisor uses to CREATE this directory — so the test redirects both
// ends of the lifetime together and exercises the production path unmodified.
func TestRemove_DeletesSupervisorStateDir(t *testing.T) {
	ctx := context.Background()
	root := shortTempDir(t)
	t.Setenv("XDG_STATE_HOME", root)
	storeRoot := filepath.Join(root, "nexus3")

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, fake.New(), lifecycle.New())

	sbID := domain.NewSandboxID()
	if err := st.Create(ctx, domain.Sandbox{
		ID: sbID, Name: "sd-teardown", Project: "test", State: domain.Created,
	}); err != nil {
		t.Fatalf("st.Create: %v", err)
	}

	dir := mustStateDir(t, storeRoot, sbID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("precondition: state dir must exist: %v", err)
	}

	if err := svc.Remove(ctx, sbID.String()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("state dir %s survived Remove (stat err = %v); it must be deleted with the sandbox", dir, err)
	}
}

// TestRemove_SupervisorStateDirAbsentIsNotAnError proves the teardown is
// idempotent: a Remove of a sandbox that never had a state dir (never started,
// or a retried Remove) must not fail.
func TestRemove_SupervisorStateDirAbsentIsNotAnError(t *testing.T) {
	ctx := context.Background()
	t.Setenv("XDG_STATE_HOME", shortTempDir(t))

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, fake.New(), lifecycle.New())

	sbID := domain.NewSandboxID()
	if err := st.Create(ctx, domain.Sandbox{
		ID: sbID, Name: "sd-noop", Project: "test", State: domain.Created,
	}); err != nil {
		t.Fatalf("st.Create: %v", err)
	}
	if err := svc.Remove(ctx, sbID.String()); err != nil {
		t.Fatalf("Remove with no state dir: %v", err)
	}
}

// ── 2. permissions ───────────────────────────────────────────────────────────

// TestStatedirEnsure_Creates0700 drives the function every supervisor entry
// point (Serve, Adopt, RunReacquire, WriteSpawnSpec) calls.
func TestStatedirEnsure_Creates0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "supervisors", domain.NewSandboxID().String())
	if err := statedir.Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("state dir mode = %04o, want 0700", got)
	}
}

// TestStatedirEnsure_TightensExisting0755 is the case that decides whether the
// 641 directories already on the reference host ever stop being world-readable.
// MkdirAll alone is a no-op on an existing dir, so without the explicit Chmod
// the next supervisor to take ownership would write a private key into a 0755
// directory.
func TestStatedirEnsure_TightensExisting0755(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "supervisors", domain.NewSandboxID().String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir 0755: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // defeat umask
		t.Fatalf("chmod 0755: %v", err)
	}

	if err := statedir.Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("pre-existing 0755 state dir was left at %04o, want 0700", got)
	}
}

// TestStatedirEnsure_DoesNotTightenParents guards the blast radius: only the
// per-sandbox dir is narrowed, never the shared supervisors/ root or the store
// root, which other things read.
func TestStatedirEnsure_DoesNotTightenParents(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "supervisors")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	if err := statedir.Ensure(filepath.Join(parent, domain.NewSandboxID().String())); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("supervisors/ parent was narrowed to %04o; Ensure must only touch the leaf dir", got)
	}
}

// TestStatedirFileMode_IsOwnerOnly pins the constant every file inside the
// state dir is opened with. It is a constant rather than a literal precisely so
// the supervisor package and this one cannot drift.
func TestStatedirFileMode_IsOwnerOnly(t *testing.T) {
	if statedir.FileMode != 0o600 {
		t.Fatalf("statedir.FileMode = %04o, want 0600 — the dir holds the MITM CA private key", statedir.FileMode)
	}
	if statedir.DirMode != 0o700 {
		t.Fatalf("statedir.DirMode = %04o, want 0700", statedir.DirMode)
	}
}

// ── 3. prune, and the safety rail ────────────────────────────────────────────

// reapFixture builds the filesystem layout Reap enumerates plus an empty
// synthetic /proc, and returns the pieces each case customises.
type reapFixture struct {
	stateRoot string
	sockDir   string
	procDir   string
	st        store.Store
	idx       *service.ResourceIndex
}

func newReapFixture(t *testing.T) *reapFixture {
	t.Helper()
	root := shortTempDir(t)
	stateRoot := filepath.Join(root, "state")
	sockDir := filepath.Join(root, "sock")
	procDir := filepath.Join(root, "proc")
	for _, d := range []string{stateRoot, sockDir, procDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// The store is rooted at stateRoot, not a separate temp dir, because that is
	// the production relationship: IndexConfig.StateRoot defaults to
	// store.DefaultRoot(), the same root the CLI opens the FileStore at. The
	// record-dir gate in classifySupervisorState resolves
	// <storeRoot>/sandboxes/<ULID> off that root, so a fixture that split them
	// would not exercise the production geometry.
	st, err := store.NewFileStore(stateRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return &reapFixture{
		stateRoot: stateRoot,
		sockDir:   sockDir,
		procDir:   procDir,
		st:        st,
		idx: service.NewResourceIndex(service.IndexConfig{
			StateRoot: stateRoot,
			SocketDir: sockDir,
		}),
	}
}

// run invokes the real service.Reap. apply=true is wired with the seams Reap
// demands for a synthetic /proc so no real process can ever be signalled.
func (f *reapFixture) run(t *testing.T, apply bool) *service.ReapReport {
	t.Helper()
	rep, err := service.Reap(context.Background(), f.st, f.idx, apply, service.ReapOptions{
		ProcDir:     f.procDir,
		NetnsKillFn: func(int) error { return fmt.Errorf("test: kill must not be reached") },
	})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	return rep
}

// entryFor returns the report entry for a state dir path.
func entryFor(t *testing.T, rep *service.ReapReport, path string) service.ReapEntry {
	t.Helper()
	for _, e := range rep.Entries {
		if e.Resource.Kind == service.KindSupervisorState && e.Resource.Path == path {
			return e
		}
	}
	t.Fatalf("no KindSupervisorState entry for %s in report (%d entries)", path, len(rep.Entries))
	return service.ReapEntry{}
}

// plantProcCmdline writes a synthetic /proc/<pid>/cmdline carrying the ULID,
// which is how a live cloud-hypervisor or supervisor process is detected.
func plantProcCmdline(t *testing.T, procDir string, pid int, id domain.SandboxID) {
	t.Helper()
	d := filepath.Join(procDir, fmt.Sprint(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir proc pid: %v", err)
	}
	line := "cloud-hypervisor\x00--api-socket\x00" + id.String() + ".sock\x00"
	if err := os.WriteFile(filepath.Join(d, "cmdline"), []byte(line), 0o600); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
}

// TestReap_KeepsStateDirOfLiveSandbox — a sandbox with a store record is live
// as far as reap is concerned and its state dir must survive --apply.
func TestReap_KeepsStateDirOfLiveSandbox(t *testing.T) {
	f := newReapFixture(t)
	id := domain.NewSandboxID()
	if err := f.st.Create(context.Background(), domain.Sandbox{
		ID: id, Name: "live", Project: "test", State: domain.Running,
	}); err != nil {
		t.Fatalf("st.Create: %v", err)
	}
	dir := mustStateDir(t, f.stateRoot, id)

	rep := f.run(t, true)
	e := entryFor(t, rep, dir)
	if e.Status == service.ReapStatusOrphan {
		t.Fatalf("live sandbox's state dir classified orphan (reason=%q)", e.Reason)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("reap --apply deleted a LIVE sandbox's state dir %s: %v", dir, err)
	}
}

// TestReap_KeepsStateDirOfAdoptableSandbox — the live-VM / dead-supervisor
// class (recovery.OutcomeAdoptable, AC-8). Its state dir is exactly what a
// re-acquisition consumes, so deleting it makes a recoverable sandbox
// unrecoverable.
//
// The fixture reproduces that class faithfully: a store record (recovery only
// ever reaches OutcomeAdoptable from a record), a live VM process carrying the
// ULID in /proc, and NO supervisor — no supervisor.sock in the dir, no
// responsive control socket.
func TestReap_KeepsStateDirOfAdoptableSandbox(t *testing.T) {
	f := newReapFixture(t)
	id := domain.NewSandboxID()
	if err := f.st.Create(context.Background(), domain.Sandbox{
		ID: id, Name: "adoptable", Project: "test", State: domain.Running,
	}); err != nil {
		t.Fatalf("st.Create: %v", err)
	}
	dir := mustStateDir(t, f.stateRoot, id)
	plantProcCmdline(t, f.procDir, 4242, id) // live VM
	// deliberately no supervisor.sock: the supervisor is dead.

	rep := f.run(t, true)
	e := entryFor(t, rep, dir)
	if e.Status == service.ReapStatusOrphan {
		t.Fatalf("adoptable sandbox's state dir classified orphan (reason=%q)", e.Reason)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("reap --apply deleted an ADOPTABLE sandbox's state dir %s: %v", dir, err)
	}
}

// TestReap_KeepsStateDirWithResponsiveSupervisorSocket covers the gate the
// record-based judgement has no analogue for: the record is gone, the VM is
// gone, but a supervisor process is still serving its control socket INSIDE
// this directory. Deleting it strands a running process.
func TestReap_KeepsStateDirWithResponsiveSupervisorSocket(t *testing.T) {
	f := newReapFixture(t)
	id := domain.NewSandboxID()
	dir := mustStateDir(t, f.stateRoot, id)

	sockPath := filepath.Join(dir, "supervisor.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	rep := f.run(t, true)
	e := entryFor(t, rep, dir)
	if e.Status != service.ReapStatusLive {
		t.Fatalf("state dir with a RESPONSIVE supervisor.sock classified %s (reason=%q), want live",
			e.Status, e.Reason)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("reap --apply deleted a state dir whose supervisor is still serving: %v", err)
	}
}

// TestReap_RemovesStateDirOfGoneSandbox is the counterpart that makes the three
// keep-cases meaningful: with no record, no process and no responsive socket,
// --apply must actually collect the directory. This is the 640-orphan backlog.
func TestReap_RemovesStateDirOfGoneSandbox(t *testing.T) {
	f := newReapFixture(t)
	id := domain.NewSandboxID()
	dir := mustStateDir(t, f.stateRoot, id)

	rep := f.run(t, true)
	e := entryFor(t, rep, dir)
	if e.Status != service.ReapStatusOrphan {
		t.Fatalf("genuinely-gone sandbox's state dir classified %s (reason=%q), want orphan",
			e.Status, e.Reason)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("reap --apply left orphan state dir %s behind (stat err = %v)", dir, err)
	}
	var found bool
	for _, p := range rep.Deleted {
		if p == dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan state dir %s not listed in report.Deleted %v", dir, rep.Deleted)
	}
	if len(rep.Failed) != 0 {
		t.Fatalf("unexpected failures: %+v", rep.Failed)
	}
}

// TestReap_ReportsStateDirWithoutApply pins the report-then-apply shape: with
// apply=false the orphan is reported and nothing is touched.
func TestReap_ReportsStateDirWithoutApply(t *testing.T) {
	f := newReapFixture(t)
	id := domain.NewSandboxID()
	dir := mustStateDir(t, f.stateRoot, id)

	rep := f.run(t, false)
	if e := entryFor(t, rep, dir); e.Status != service.ReapStatusOrphan {
		t.Fatalf("state dir classified %s, want orphan", e.Status)
	}
	if len(rep.Deleted) != 0 {
		t.Fatalf("apply=false deleted %v", rep.Deleted)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("apply=false removed the dir: %v", err)
	}
}

// TestReap_KeepsStateDirWhenRecordIsUnreadable is the fail-OPEN case in the
// record gate.
//
// store.List silently skips every record it cannot decode, so a sandbox whose
// record.json is corrupt, half-written, or written by a NEWER schema than the
// running binary is absent from recordMap and therefore reads as "no owner".
// A stopped sandbox has no VM in /proc and no responsive socket either, so
// nothing else keeps it — and its state dir (which from the CA-persistence
// slice holds an MITM CA private key) would be deleted by --apply even though
// a correct-version binary reads that record fine.
//
// {"schema_version": 9999} reproduces the widest form of this: readRecord
// returns ErrSchemaTooNew for EVERY record when an older binary runs, so one
// --apply under a stale binary would collect every stopped sandbox at once.
func TestReap_KeepsStateDirWhenRecordIsUnreadable(t *testing.T) {
	f := newReapFixture(t)
	id := domain.NewSandboxID()

	// Plant the record dir the way FileStore lays it out, with a record this
	// binary cannot decode. Deliberately NOT via st.Create: the point is a
	// record on disk that store.List refuses to return.
	recDir := filepath.Join(f.stateRoot, "sandboxes", id.String())
	if err := os.MkdirAll(recDir, 0o700); err != nil {
		t.Fatalf("mkdir record dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recDir, "record.json"), []byte(`{"schema_version": 9999}`), 0o600); err != nil {
		t.Fatalf("write record.json: %v", err)
	}

	// Precondition: the store really cannot see it, so the keep below can only
	// come from the record-DIR gate, not from R1.
	got, err := f.st.List(context.Background())
	if err != nil {
		t.Fatalf("st.List: %v", err)
	}
	for _, sb := range got {
		if sb.ID == id {
			t.Fatalf("precondition failed: store.List decoded the too-new record; the gate under test is unreachable")
		}
	}

	dir := mustStateDir(t, f.stateRoot, id)

	rep := f.run(t, true)
	e := entryFor(t, rep, dir)
	if e.Status != service.ReapStatusLive {
		t.Fatalf("state dir of a sandbox with an UNREADABLE record classified %s (reason=%q), want live — "+
			"the sandbox is still adoptable and the dir holds the MITM CA private key", e.Status, e.Reason)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("reap --apply deleted the state dir of a sandbox whose record is merely unreadable: %v", err)
	}
	for _, p := range rep.Deleted {
		if p == dir {
			t.Fatalf("state dir %s reported as deleted", dir)
		}
	}
}

// TestResourceIndex_EnumeratesSupervisorStateDirs proves the dirs reach the
// report at all — the prune path is unreachable if List does not see them.
func TestResourceIndex_EnumeratesSupervisorStateDirs(t *testing.T) {
	f := newReapFixture(t)
	id := domain.NewSandboxID()
	dir := mustStateDir(t, f.stateRoot, id)
	// A non-ULID directory must be ignored, not misclassified.
	if err := os.MkdirAll(filepath.Join(f.stateRoot, "supervisors", "not-a-ulid"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	resources, err := f.idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var n int
	for _, r := range resources {
		if r.Kind != service.KindSupervisorState {
			continue
		}
		n++
		if r.Path != dir || r.OwnerID != id {
			t.Fatalf("unexpected supervisor-state resource %+v", r)
		}
	}
	if n != 1 {
		t.Fatalf("List returned %d supervisor-state resources, want 1", n)
	}
}
