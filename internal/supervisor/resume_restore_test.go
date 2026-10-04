package supervisor

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

type resumeDrv struct {
	*fake.FakeDriver
	restores   int
	restoreErr error
	gotDir     string
	gotMode    driver.RestoreMode
}

func (d *resumeDrv) HibernateTo(context.Context, domain.SandboxID, string) (driver.HibernateResult, error) {
	return driver.HibernateResult{}, nil
}
func (d *resumeDrv) StopAfterHibernate(context.Context, domain.SandboxID) error { return nil }
func (d *resumeDrv) RestoreInPlace(_ context.Context, _ domain.SandboxID, dir string, o driver.RestoreOptions) (driver.RestoreResult, error) {
	d.restores++
	d.gotDir, d.gotMode = dir, o.Mode
	if d.restoreErr != nil {
		return driver.RestoreResult{}, d.restoreErr
	}
	return driver.RestoreResult{RestoreMs: 7, AgentReadyMs: 3, TotalMs: 12, Mode: o.Mode}, nil
}
func (d *resumeDrv) NetnsState(domain.SandboxID) (driver.NetnsIdentity, bool) {
	return driver.NetnsIdentity{ChildPID: 4242, ChildPGID: 4242, ChildStartTime: 9, VhostSocket: "/run/v.sock", APISocket: "/run/a.sock", ControlSocket: "/run/c.sock", ControlToken: "tok"}, true
}

func hibernatedFixture(t *testing.T) (*service.Service, *resumeDrv, store.Store, domain.Sandbox, Config) {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &resumeDrv{FakeDriver: fake.New()}
	svc := service.New(st, d, lifecycle.New())
	ctx := context.Background()
	sb, err := svc.Create(ctx, "p", "r", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(ctx, sb.ID, func(r *domain.Sandbox) error {
		r.State = domain.Hibernated
		r.HibernateDir = "/snap/dir"
		r.NetnsChildPID = 0
		r.VhostSocket = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return svc, d, st, sb, Config{SandboxRef: sb.ID.String(), StateDir: t.TempDir()}
}

func TestStartOrResume_RestoresHibernated(t *testing.T) {
	svc, d, st, sb, cfg := hibernatedFixture(t)
	cfg.RestoreFrom, cfg.RestoreMode = "/snap/dir", "ondemand"
	got, err := startOrResume(context.Background(), svc, st, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.restores != 1 || d.gotDir != "/snap/dir" || d.gotMode != driver.RestoreModeOnDemand {
		t.Fatalf("restores=%d dir=%q mode=%q", d.restores, d.gotDir, d.gotMode)
	}
	rec, _ := st.Get(context.Background(), sb.ID)
	if got.State != domain.Running || rec.State != domain.Running {
		t.Fatalf("state=%v rec=%v", got.State, rec.State)
	}
	if rec.NetnsChildPID != 4242 || rec.VhostSocket != "/run/v.sock" || rec.NetnsControlToken != "tok" {
		t.Fatalf("netns not persisted: %+v", rec)
	}
	rep, err := ReadResumeOutcome(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ResumedFrom != "snapshot" || rep.RestoreMs != 7 || rep.AgentReadyMs != 3 || rep.State != "running" || rep.RestoreMode != "ondemand" || rep.ID != sb.ID.String() {
		t.Fatalf("report=%+v", rep)
	}
}

func TestStartOrResume_HibernatedRecordWithoutFlag(t *testing.T) {
	svc, d, st, _, cfg := hibernatedFixture(t)
	if _, err := startOrResume(context.Background(), svc, st, cfg, nil); err != nil {
		t.Fatal(err)
	}
	if d.restores != 1 {
		t.Fatalf("restores=%d", d.restores)
	}
}

func TestStartOrResume_OverridesHibernateDir(t *testing.T) {
	svc, d, st, _, cfg := hibernatedFixture(t)
	cfg.RestoreFrom = "/other/dir"
	if _, err := startOrResume(context.Background(), svc, st, cfg, nil); err != nil {
		t.Fatal(err)
	}
	if d.gotDir != "/other/dir" {
		t.Fatalf("dir=%q", d.gotDir)
	}
}

func TestStartOrResume_ColdFallbackPersistsState(t *testing.T) {
	svc, d, st, sb, cfg := hibernatedFixture(t)
	d.restoreErr = errors.New("vm.restore exploded")
	cfg.RestoreFrom = "/snap/dir"
	if _, err := startOrResume(context.Background(), svc, st, cfg, nil); err != nil {
		t.Fatal(err)
	}
	rec, _ := st.Get(context.Background(), sb.ID)
	if rec.State != domain.Running || rec.NetnsChildPID != 4242 || rec.VhostSocket != "/run/v.sock" {
		t.Fatalf("rec=%+v", rec)
	}
	rep, err := ReadResumeOutcome(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ResumedFrom != "cold" || rep.FallbackReason != "vm.restore exploded" {
		t.Fatalf("report=%+v", rep)
	}
}

func TestStartOrResume_NoColdFallbackFails(t *testing.T) {
	svc, d, st, sb, cfg := hibernatedFixture(t)
	d.restoreErr = errors.New("boom")
	cfg.RestoreFrom, cfg.NoColdFallback = "/snap/dir", true
	if _, err := startOrResume(context.Background(), svc, st, cfg, nil); err == nil {
		t.Fatal("want error")
	}
	rec, _ := st.Get(context.Background(), sb.ID)
	if rec.State != domain.Hibernated {
		t.Fatalf("state=%v", rec.State)
	}
	if _, err := ReadResumeOutcome(cfg.StateDir); err == nil {
		t.Fatal("no outcome file expected on failure")
	}
}

func TestStartOrResume_RestoreRequestedButNotHibernated(t *testing.T) {
	svc, d, st, sb, cfg := hibernatedFixture(t)
	_ = st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error { r.State = domain.Stopped; return nil })
	cfg.RestoreFrom = "/snap/dir"
	if _, err := startOrResume(context.Background(), svc, st, cfg, nil); err == nil || d.restores != 0 {
		t.Fatalf("err=%v restores=%d", err, d.restores)
	}
}

func TestStartOrResume_PlainStartForStopped(t *testing.T) {
	svc, d, st, sb, cfg := hibernatedFixture(t)
	_ = st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error { r.State = domain.Stopped; return nil })
	got, err := startOrResume(context.Background(), svc, st, cfg, nil)
	if err != nil || got.State != domain.Running || d.restores != 0 {
		t.Fatalf("got=%v err=%v restores=%d", got.State, err, d.restores)
	}
}

func TestResumeFlagsArgvAndSpecNotPersisted(t *testing.T) {
	cfg := Config{SandboxRef: "x", StoreRoot: "/s", StateDir: t.TempDir(), RestoreFrom: "/snap", RestoreMode: "ondemand", NoColdFallback: true}
	argv := BuildSupervisorArgv(SpawnConfig{Config: cfg})
	for _, want := range []string{"--restore-from", "/snap", "--restore-mode", "ondemand", "--no-cold-fallback"} {
		if !slices.Contains(argv, want) {
			t.Errorf("argv missing %q: %v", want, argv)
		}
	}
	plain := BuildSupervisorArgv(SpawnConfig{Config: Config{SandboxRef: "x"}})
	if slices.Contains(plain, "--restore-from") || slices.Contains(plain, "--no-cold-fallback") {
		t.Errorf("plain argv carries restore flags: %v", plain)
	}
	if err := WriteSpawnSpec(cfg.StateDir, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSpawnSpec(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.RestoreFrom != "" || got.RestoreMode != "" || got.NoColdFallback {
		t.Fatalf("restore request leaked into spawn.json: %+v", got)
	}
}

func clockCase(t *testing.T, mutate func(*resumeDrv, *Config), fn func(context.Context, domain.SandboxID) (int64, error)) (ResumeReport, int) {
	t.Helper()
	svc, d, st, _, cfg := hibernatedFixture(t)
	cfg.RestoreFrom = "/snap/dir"
	mutate(d, &cfg)
	calls := 0
	if _, err := startOrResume(context.Background(), svc, st, cfg, func(ctx context.Context, id domain.SandboxID) (int64, error) {
		calls++
		return fn(ctx, id)
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := ReadResumeOutcome(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	return rep, calls
}

func TestStartOrResume_SnapshotResumeSetsClockAndRecordsSkew(t *testing.T) {
	rep, calls := clockCase(t, func(*resumeDrv, *Config) {}, func(context.Context, domain.SandboxID) (int64, error) { return -4200, nil })
	if calls != 1 || rep.ClockSkewMs != -4200 {
		t.Fatalf("calls=%d report=%+v", calls, rep)
	}
}

func TestStartOrResume_ColdFallbackSkipsClock(t *testing.T) {
	rep, calls := clockCase(t, func(d *resumeDrv, _ *Config) { d.restoreErr = errors.New("boom") }, func(context.Context, domain.SandboxID) (int64, error) { return 5, nil })
	if calls != 0 || rep.ResumedFrom != "cold" || rep.ClockSkewMs != 0 {
		t.Fatalf("calls=%d report=%+v", calls, rep)
	}
}

func TestStartOrResume_SetClockFailureTolerated(t *testing.T) {
	rep, calls := clockCase(t, func(*resumeDrv, *Config) {}, func(context.Context, domain.SandboxID) (int64, error) { return 0, errors.New("rpc down") })
	if calls != 1 || rep.ResumedFrom != "snapshot" || rep.State != "running" || rep.ClockSkewMs != 0 {
		t.Fatalf("calls=%d report=%+v", calls, rep)
	}
}
