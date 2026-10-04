package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/supervisor"
)

// hibernatedCLIFixture returns a Hibernated sandbox with a spawn spec on disk
// and a spawn seam that records its restore request and flips the record to
// Running, writing the outcome file the way a restore-mode supervisor does.
func hibernatedCLIFixture(t *testing.T) (*service.Service, domain.Sandbox, *[]*restoreSpawn) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(st, fake.New(), lifecycle.New())
	sb, err := svc.Create(ctx, "proj", "hib", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(ctx, sb.ID, func(r *domain.Sandbox) error {
		r.State = domain.Hibernated
		r.HibernateDir = "/snap/dir"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	root, _ := store.DefaultRoot()
	stateDir := supervisor.DefaultStateDir(root, sb.ID)
	if err := supervisor.WriteSpawnSpec(stateDir, supervisor.Config{SandboxRef: sb.ID.String(), StoreRoot: root}); err != nil {
		t.Fatal(err)
	}
	var calls []*restoreSpawn
	orig := spawnSupervisorFn
	t.Cleanup(func() { spawnSupervisorFn = orig })
	spawnSupervisorFn = func(ctx context.Context, _ *service.Service, id domain.SandboxID, dir string, rs *restoreSpawn) error {
		calls = append(calls, rs)
		_ = st.Update(ctx, id, func(r *domain.Sandbox) error { r.State = domain.Running; return nil })
		return os.WriteFile(supervisor.ResumeOutcomePath(dir),
			[]byte(`{"id":"`+id.String()+`","state":"running","resumed_from":"snapshot","restore_mode":"copy","restore_ms":7,"agent_ready_ms":3,"total_ms":12}`), 0o600)
	}
	return svc, sb, &calls
}

func TestSandboxStart_HibernatedSpawnsRestoreSupervisor(t *testing.T) {
	svc, sb, calls := hibernatedCLIFixture(t)
	out, stdout, _ := capture(true)
	if err := runSandboxStart(context.Background(), []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] == nil || (*calls)[0].Dir != "/snap/dir" || (*calls)[0].NoColdFallback {
		t.Fatalf("calls=%+v", *calls)
	}
	if !strings.Contains(stdout.String(), "sandbox.started") {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestResumeHibernated_ForwardsOptionsAndReturnsReport(t *testing.T) {
	svc, sb, calls := hibernatedCLIFixture(t)
	res, err := resumeHibernated(context.Background(), svc, sb.ID.String(), resumeOptions{Mode: "ondemand", NoColdFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	rs := (*calls)[0]
	if rs.Mode != "ondemand" || !rs.NoColdFallback || rs.Dir != "/snap/dir" {
		t.Fatalf("restore=%+v", rs)
	}
	if res.Already || res.Sandbox.State != domain.Running || res.Report == nil || res.Report.ResumedFrom != "snapshot" || res.Report.RestoreMs != 7 {
		t.Fatalf("res=%+v", res)
	}
}

func TestResumeHibernated_RunningIsNoop(t *testing.T) {
	svc, sb, calls := hibernatedCLIFixture(t)
	if _, err := resumeHibernated(context.Background(), svc, sb.ID.String(), resumeOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := resumeHibernated(context.Background(), svc, sb.ID.String(), resumeOptions{})
	if err != nil || !res.Already || len(*calls) != 1 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, len(*calls))
	}
}

func TestEnsureDetachedSupervisor_PlainStartHasNoRestoreRequest(t *testing.T) {
	svc, sb, calls := hibernatedCLIFixture(t)
	stopped := sb
	stopped.State = domain.Stopped
	if err := ensureDetachedSupervisor(context.Background(), svc, stopped); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != nil {
		t.Fatalf("calls=%+v", *calls)
	}
}

func TestResumeHibernated_StrictSpawnFailureMapsToSnapshotFailed(t *testing.T) {
	svc, sb, _ := hibernatedCLIFixture(t)
	spawnSupervisorFn = func(context.Context, *service.Service, domain.SandboxID, string, *restoreSpawn) error {
		return errors.New("spawn supervisor: service: snapshot failed: restore failed: boom")
	}
	_, err := resumeHibernated(context.Background(), svc, sb.ID.String(), resumeOptions{NoColdFallback: true})
	if !errors.Is(err, service.ErrSnapshotFailed) || hibernateCodeFor(err) != "snapshot_failed" {
		t.Fatalf("err=%v code=%s", err, hibernateCodeFor(err))
	}
}

func TestResumeHibernated_RunningWithDeadSupervisorColdStarts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	svc := newTestService(t)
	created, err := svc.Create(ctx, "proj", "dead", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sb, err := svc.Start(ctx, created.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSupervisor(ctx, sb.ID, 424242, "/nonexistent/supervisor.sock"); err != nil {
		t.Fatal(err)
	}
	root, _ := store.DefaultRoot()
	if err := supervisor.WriteSpawnSpec(supervisor.DefaultStateDir(root, sb.ID), supervisor.Config{SandboxRef: sb.ID.String(), StoreRoot: root}); err != nil {
		t.Fatal(err)
	}
	origLive, origRec, origSpawn := supervisorLiveFn, reconcileDeadSupervisorFn, spawnSupervisorFn
	t.Cleanup(func() { supervisorLiveFn, reconcileDeadSupervisorFn, spawnSupervisorFn = origLive, origRec, origSpawn })
	supervisorLiveFn = func(domain.Sandbox) bool { return false }
	reconcileDeadSupervisorFn = func(ctx context.Context, id domain.SandboxID) error {
		_, err := svc.Stop(ctx, id.String())
		return err
	}
	spawned := 0
	spawnSupervisorFn = func(ctx context.Context, s *service.Service, id domain.SandboxID, _ string, _ *restoreSpawn) error {
		spawned++
		_, err := s.Start(ctx, id.String())
		return err
	}
	res, err := resumeHibernated(ctx, svc, sb.ID.String(), resumeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Already || res.Report == nil || res.Report.ResumedFrom != "cold" || res.Report.FallbackReason == "" || spawned != 1 || res.Sandbox.State != domain.Running {
		t.Fatalf("res=%+v report=%+v spawned=%d", res, res.Report, spawned)
	}
}
