package service_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

// listOnceAgentCfgStore decorates a store.Store so that the first List call
// returns the pre-create snapshot, then releases the dir flock and commits the
// record before returning. This forces the losing interleaving for the
// probe-before-list ordering test: if the reaper lists records first it sees no
// entry, and by the time the probe runs the flock is already free, so the dir
// is classified as orphan. Probe-first (the correct order) marks it in-flight
// before List runs.
type listOnceAgentCfgStore struct {
	store.Store
	once      sync.Once
	afterList func()
}

func (s *listOnceAgentCfgStore) List(ctx context.Context) ([]domain.Sandbox, error) {
	out, err := s.Store.List(ctx)
	s.once.Do(s.afterList)
	return out, err
}

func agentCfgDir(t *testing.T, disksDir string, id domain.SandboxID) string {
	t.Helper()
	dir := filepath.Join(disksDir, id.String()+"-agentcfg-lower")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir agentcfg-lower: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	return dir
}

func reapOpts(t *testing.T) (emptyProcDir string, opts service.ReapOptions) {
	t.Helper()
	emptyProcDir = t.TempDir()
	return emptyProcDir, service.ReapOptions{
		ProcDir:     emptyProcDir,
		NetnsKillFn: func(int) error { return nil },
	}
}

func TestReap_AgentCfgStage_Orphan(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	id := domain.NewSandboxID()
	dir := agentCfgDir(t, disksDir, id)

	_, opts := reapOpts(t)
	report, err := service.Reap(ctx, st, idx, true, opts)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	var found *service.ReapEntry
	for i := range report.Entries {
		e := &report.Entries[i]
		if e.Resource.Kind == service.KindAgentCfgStage && e.Resource.OwnerID == id {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("no KindAgentCfgStage entry for %s", id)
	}
	if found.Status != service.ReapStatusOrphan {
		t.Errorf("status = %s, want Orphan", found.Status)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir %s still exists after apply=true", dir)
	}
}

func TestReap_AgentCfgStage_Owned(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	id := domain.NewSandboxID()
	dir := agentCfgDir(t, disksDir, id)

	if err := st.Create(ctx, domain.Sandbox{ID: id, Name: "owned", Project: "proj", State: domain.Stopped}); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	_, opts := reapOpts(t)
	report, err := service.Reap(ctx, st, idx, true, opts)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	var found *service.ReapEntry
	for i := range report.Entries {
		e := &report.Entries[i]
		if e.Resource.Kind == service.KindAgentCfgStage && e.Resource.OwnerID == id {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("no KindAgentCfgStage entry for %s", id)
	}
	if found.Status != service.ReapStatusOwned {
		t.Errorf("status = %s, want Owned", found.Status)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir %s removed despite Owned status: %v", dir, err)
	}
}

func TestReap_AgentCfgStage_DirLeaseHeld(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	id := domain.NewSandboxID()
	dir := agentCfgDir(t, disksDir, id)

	f, err := os.Open(dir)
	if err != nil {
		t.Fatalf("open dir: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("flock dir: %v", err)
	}

	_, opts := reapOpts(t)
	report, err := service.Reap(ctx, st, idx, true, opts)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	var found *service.ReapEntry
	for i := range report.Entries {
		e := &report.Entries[i]
		if e.Resource.Kind == service.KindAgentCfgStage && e.Resource.OwnerID == id {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("no KindAgentCfgStage entry for %s", id)
	}
	if found.Status != service.ReapStatusLive {
		t.Errorf("status = %s, want Live (dir lease held)", found.Status)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir %s removed despite Live status: %v", dir, err)
	}
}

func TestReap_AgentCfgStage_CreateIntentLease(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	id := domain.NewSandboxID()
	agentCfgDir(t, disksDir, id)

	release, err := service.HoldCreateIntentForTest(disksDir, id)
	if err != nil {
		t.Fatalf("HoldCreateIntentForTest: %v", err)
	}
	defer release()

	_, opts := reapOpts(t)
	report, err := service.Reap(ctx, st, idx, true, opts)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	var found *service.ReapEntry
	for i := range report.Entries {
		e := &report.Entries[i]
		if e.Resource.Kind == service.KindAgentCfgStage && e.Resource.OwnerID == id {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("no KindAgentCfgStage entry for %s", id)
	}
	if found.Status != service.ReapStatusLive {
		t.Errorf("status = %s, want Live (create-intent lease)", found.Status)
	}
}

func TestReap_AgentCfgStage_NonULIDNamesSkipped(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	mustMkdir(t, filepath.Join(disksDir, "foo-agentcfg-lower"))
	id := domain.NewSandboxID()
	mustMkdir(t, filepath.Join(disksDir, id.String()+"-agentcfg-lower.staging"))

	_, opts := reapOpts(t)
	report, err := service.Reap(ctx, st, idx, false, opts)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	for _, e := range report.Entries {
		if e.Resource.Kind == service.KindAgentCfgStage {
			t.Errorf("unexpected KindAgentCfgStage entry for %s", e.Resource.Path)
		}
	}

	for _, name := range []string{"foo-agentcfg-lower", id.String() + "-agentcfg-lower.staging"} {
		p := filepath.Join(disksDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("dir %s unexpectedly gone: %v", p, err)
		}
	}
}

// TestReap_AgentCfgStage_ProbeBeforeList pins the probe-before-list ordering for
// agentcfg stage dirs.
//
// The race being modelled: a creator holds the dir flock while its record is
// not yet in the store. It commits (store.Create) and then releases the flock.
// If the reaper lists records BEFORE probing, it gets an empty snapshot; by the
// time the probe runs the flock is free, so the dir looks like an orphan and
// gets deleted. Probing FIRST (the correct order) marks the dir in-flight
// before st.List runs, so it is kept regardless of what the snapshot shows.
//
// The decorator intercepts List: it takes the pre-create snapshot, then lets
// the creator finish (commit + flock release), and returns the snapshot. This
// forces the losing interleaving deterministically, without goroutines or sleeps.
// The test fails when List is moved above the probe loop in reap.go.
func TestReap_AgentCfgStage_ProbeBeforeList(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	base, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	id := domain.NewSandboxID()
	dir := agentCfgDir(t, disksDir, id)

	f, err := os.Open(dir)
	if err != nil {
		t.Fatalf("open dir: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("flock dir: %v", err)
	}

	decorated := &listOnceAgentCfgStore{Store: base}
	decorated.afterList = func() {
		// Creator finishes: commit record, then release flock. This mirrors the
		// production ordering (store.Create before flock release).
		if createErr := base.Create(ctx, domain.Sandbox{
			ID: id, Name: "racing", Project: "proj", State: domain.Stopped,
		}); createErr != nil {
			t.Errorf("store.Create in List hook: %v", createErr)
		}
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}

	_, opts := reapOpts(t)
	report, err := service.Reap(ctx, decorated, idx, true, opts)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	var found *service.ReapEntry
	for i := range report.Entries {
		e := &report.Entries[i]
		if e.Resource.Kind == service.KindAgentCfgStage && e.Resource.OwnerID == id {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("no KindAgentCfgStage entry for %s", id)
	}
	if found.Status != service.ReapStatusLive {
		t.Errorf("DATA LOSS: status = %s, want Live; probe-before-list ordering broken: "+
			"agentcfg dir of live sandbox %s was classified orphan because List ran before probe", found.Status, id)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("DATA LOSS: agentcfg dir %s deleted while sandbox %s is live", dir, id)
	}
}
