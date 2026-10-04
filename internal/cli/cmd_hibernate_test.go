package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/supervisor"
)

type envData struct {
	Kind string         `json:"kind"`
	Data map[string]any `json:"data"`
}

func decodeEnv(t *testing.T, s string) envData {
	t.Helper()
	var e envData
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return e
}

var hbStores = map[*service.Service]store.Store{}

func newRunningSandbox(t *testing.T) (*service.Service, domain.Sandbox) {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(st, fake.New(), lifecycle.New())
	sb, err := svc.Create(context.Background(), "proj", "hb", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hbStores[svc] = st
	t.Cleanup(func() { delete(hbStores, svc) })
	return svc, sb
}

func setHBState(t *testing.T, svc *service.Service, id domain.SandboxID, s domain.State) {
	t.Helper()
	if err := hbStores[svc].Update(context.Background(), id, func(r *domain.Sandbox) error { r.State = s; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRunHibernate_ViaSupervisorJSON(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	origLive, origReq := supervisorLiveFn, requestHibernateFn
	t.Cleanup(func() { supervisorLiveFn, requestHibernateFn = origLive, origReq })
	supervisorLiveFn = func(domain.Sandbox) bool { return true }
	requestHibernateFn = func(context.Context, string) (service.HibernateOutcome, error) {
		return service.HibernateOutcome{Result: driver.HibernateResult{
			PauseMs: 1, SnapshotMs: 2, TotalMs: 3, SnapshotBytes: 4096, SnapshotBytesOnDisk: 2048}}, nil
	}
	setHBState(t, svc, sb.ID, domain.Running)
	out, stdout, _ := capture(true)
	if err := runHibernateWithSvc(context.Background(), sb.ID.String(), out, svc); err != nil {
		t.Fatal(err)
	}
	e := decodeEnv(t, stdout.String())
	for k, want := range map[string]any{
		"id": sb.ID.String(), "state": "hibernated", "already": false,
		"pause_ms": 1.0, "snapshot_ms": 2.0, "total_ms": 3.0,
		"snapshot_bytes": 4096.0, "snapshot_bytes_on_disk": 2048.0,
	} {
		if e.Data[k] != want {
			t.Errorf("%s = %v, want %v", k, e.Data[k], want)
		}
	}
	if _, ok := e.Data["snapshot_dir"]; !ok {
		t.Error("snapshot_dir missing")
	}
}

func TestRunHibernate_AlreadyHibernated(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	setHBState(t, svc, sb.ID, domain.Hibernated)
	out, stdout, _ := capture(true)
	if err := runHibernateWithSvc(context.Background(), sb.ID.String(), out, svc); err != nil {
		t.Fatalf("already-hibernated must exit 0: %v", err)
	}
	if e := decodeEnv(t, stdout.String()); e.Data["already"] != true || e.Data["state"] != "hibernated" {
		t.Errorf("data = %v", e.Data)
	}
}

func TestRunHibernate_StoppedIsIllegalTransition(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	setHBState(t, svc, sb.ID, domain.Stopped)
	out, _, _ := capture(true)
	err := runHibernateWithSvc(context.Background(), sb.ID.String(), out, svc)
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Code != "illegal_transition" {
		t.Fatalf("err = %v, want illegal_transition", err)
	}
}

func TestHibernateCodeFor(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{service.ErrHibernateUnsupported, "hibernate_unsupported"},
		{fmt.Errorf("%w: live host-directory mount(s) [a]", service.ErrHibernateRefused), "hibernate_refused"},
		{fmt.Errorf("%w: %w", service.ErrSnapshotFailed, errors.New("disk full")), "snapshot_failed"},
		{errors.New("boom"), ErrCodeInternalError},
	} {
		if got := hibernateCodeFor(tc.err); got != tc.want {
			t.Errorf("hibernateCodeFor(%v) = %s, want %s", tc.err, got, tc.want)
		}
	}
	ce := errHibernate("hibernate", fmt.Errorf("%w: builder sandbox", service.ErrHibernateRefused))
	if !strings.Contains(ce.Msg, "builder sandbox") {
		t.Errorf("refused reason lost: %q", ce.Msg)
	}
}

func TestRunHibernate_SupervisorErrorCodeMapped(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	origLive, origReq := supervisorLiveFn, requestHibernateFn
	t.Cleanup(func() { supervisorLiveFn, requestHibernateFn = origLive, origReq })
	supervisorLiveFn = func(domain.Sandbox) bool { return true }
	requestHibernateFn = func(context.Context, string) (service.HibernateOutcome, error) {
		return service.HibernateOutcome{}, fmt.Errorf("hibernate supervisor: %w", service.ErrHibernateRefused)
	}
	setHBState(t, svc, sb.ID, domain.Running)
	out, _, _ := capture(true)
	err := runHibernateWithSvc(context.Background(), sb.ID.String(), out, svc)
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Code != "hibernate_refused" {
		t.Fatalf("err = %v", err)
	}
}

func TestRunResume_JSONFromSnapshot(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	orig := resumeHibernatedFn
	t.Cleanup(func() { resumeHibernatedFn = orig })
	var got resumeOptions
	resumeHibernatedFn = func(_ context.Context, _ *service.Service, _ string, o resumeOptions) (resumeResult, error) {
		got = o
		return resumeResult{Sandbox: sb, Report: &supervisor.ResumeReport{
			ResumedFrom: "snapshot", RestoreMode: "ondemand", RestoreMs: 5, AgentReadyMs: 6, TotalMs: 11, ClockSkewMs: 40}}, nil
	}
	out, stdout, stderr := capture(true)
	if err := runResumeWithSvc(context.Background(), sb.ID.String(), resumeOptions{Mode: "ondemand"}, out, svc); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "ondemand" {
		t.Errorf("opts not passed: %+v", got)
	}
	e := decodeEnv(t, stdout.String())
	for k, want := range map[string]any{
		"id": sb.ID.String(), "state": "running", "already": false, "resumed_from": "snapshot",
		"restore_mode": "ondemand", "restore_ms": 5.0, "agent_ready_ms": 6.0, "total_ms": 11.0, "clock_skew_ms": 40.0,
	} {
		if e.Data[k] != want {
			t.Errorf("%s = %v, want %v", k, e.Data[k], want)
		}
	}
	if _, ok := e.Data["fallback_reason"]; ok {
		t.Error("fallback_reason must be omitted")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunResume_ColdFallbackWarns(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	orig := resumeHibernatedFn
	t.Cleanup(func() { resumeHibernatedFn = orig })
	resumeHibernatedFn = func(context.Context, *service.Service, string, resumeOptions) (resumeResult, error) {
		return resumeResult{Sandbox: sb, Report: &supervisor.ResumeReport{ResumedFrom: "cold", FallbackReason: "ch version mismatch"}}, nil
	}
	out, stdout, stderr := capture(true)
	if err := runResumeWithSvc(context.Background(), sb.ID.String(), resumeOptions{}, out, svc); err != nil {
		t.Fatalf("cold fallback must exit 0: %v", err)
	}
	if want := "warning: snapshot restore failed (ch version mismatch); cold-started"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	e := decodeEnv(t, stdout.String())
	if e.Data["resumed_from"] != "cold" || e.Data["fallback_reason"] != "ch version mismatch" {
		t.Errorf("data = %v", e.Data)
	}
}

func TestRunResume_AlreadyAndMemory(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	orig := resumeHibernatedFn
	t.Cleanup(func() { resumeHibernatedFn = orig })
	resumeHibernatedFn = func(context.Context, *service.Service, string, resumeOptions) (resumeResult, error) {
		return resumeResult{Already: true, Sandbox: sb}, nil
	}
	out, stdout, _ := capture(true)
	if err := runResumeWithSvc(context.Background(), "x", resumeOptions{}, out, svc); err != nil {
		t.Fatal(err)
	}
	if e := decodeEnv(t, stdout.String()); e.Data["already"] != true || e.Data["state"] != "running" {
		t.Errorf("already data = %v", e.Data)
	}
	resumeHibernatedFn = func(context.Context, *service.Service, string, resumeOptions) (resumeResult, error) {
		return resumeResult{Sandbox: sb}, nil
	}
	out, stdout, _ = capture(true)
	if err := runResumeWithSvc(context.Background(), "x", resumeOptions{}, out, svc); err != nil {
		t.Fatal(err)
	}
	if e := decodeEnv(t, stdout.String()); e.Data["resumed_from"] != "memory" || e.Data["already"] != false {
		t.Errorf("memory data = %v", e.Data)
	}
}

func TestRunResume_NoColdFallbackErrors(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	orig := resumeHibernatedFn
	t.Cleanup(func() { resumeHibernatedFn = orig })
	var got resumeOptions
	resumeHibernatedFn = func(_ context.Context, _ *service.Service, _ string, o resumeOptions) (resumeResult, error) {
		got = o
		return resumeResult{}, fmt.Errorf("%w: restore failed", service.ErrSnapshotFailed)
	}
	out, _, _ := capture(true)
	err := runResumeWithSvc(context.Background(), sb.ID.String(), resumeOptions{NoColdFallback: true}, out, svc)
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Code != "snapshot_failed" || !got.NoColdFallback {
		t.Fatalf("err = %v opts = %+v", err, got)
	}
}

func TestRunResume_BadRestoreMode(t *testing.T) {
	out, _, _ := capture(true)
	err := runResume(context.Background(), []string{"--restore-mode", "bogus", "x"}, out)
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want UsageError", err)
	}
}

func TestRunPause_JSON(t *testing.T) {
	svc, sb := newRunningSandbox(t)
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	out, stdout, _ := capture(true)
	if err := runPauseWithSvc(context.Background(), sb.ID.String(), out, svc); err != nil {
		t.Fatal(err)
	}
	e := decodeEnv(t, stdout.String())
	if e.Data["state"] != "paused" {
		t.Errorf("data = %v", e.Data)
	}
}

func TestHibernateVerbsRegistered(t *testing.T) {
	for _, n := range []string{"hibernate", "resume", "pause"} {
		if _, ok := Lookup(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
}

func TestParseResumeArgs_FlagsAfterRef(t *testing.T) {
	ref, o, err := parseResumeArgs([]string{"hb/x", "--restore-mode", "ondemand", "--no-cold-fallback"})
	if err != nil || ref != "hb/x" || o.Mode != "ondemand" || !o.NoColdFallback {
		t.Fatalf("ref=%q o=%+v err=%v", ref, o, err)
	}
	ref, o, err = parseResumeArgs([]string{"--no-cold-fallback", "hb/x"})
	if err != nil || ref != "hb/x" || !o.NoColdFallback {
		t.Fatalf("ref=%q o=%+v err=%v", ref, o, err)
	}
	if _, _, err = parseResumeArgs([]string{"a", "b"}); err == nil {
		t.Fatal("two refs accepted")
	}
	if _, _, err = parseResumeArgs([]string{"a", "--restore-mode", "bogus"}); err == nil {
		t.Fatal("bad mode accepted")
	}
}
