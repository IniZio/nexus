package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/IniZio/nexus/internal/herdragent"
	"github.com/IniZio/nexus/internal/hubclient"
)

type spritesMarkerSvc struct {
	*markerExecService
	mu    sync.Mutex
	argvs [][]string
	cwds  []string
}

func (s *spritesMarkerSvc) DriverName() string { return "sprites" }

func (s *spritesMarkerSvc) Exec(ctx context.Context, ref string, argv []string, env map[string]string, cwd, stdin string) (int32, string, string, error) {
	s.mu.Lock()
	s.argvs = append(s.argvs, argv)
	s.cwds = append(s.cwds, cwd)
	s.mu.Unlock()
	return s.markerExecService.Exec(ctx, ref, argv, env, cwd, stdin)
}

func captureDelegateEmits(t *testing.T) *[]hubclient.Event {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var evs []hubclient.Event
	orig := emitDelegateDoneEvent
	emitDelegateDoneEvent = func(_ context.Context, ev hubclient.Event) { evs = append(evs, ev) }
	t.Cleanup(func() { emitDelegateDoneEvent = orig })
	return &evs
}

func TestDelegateTargetFor_ByBackend(t *testing.T) {
	ch := delegateTargetFor(&stubService{})
	if ch.sprites || ch.marker != delegateDoneMarker || ch.workDir != "/workspace" || !strings.Contains(ch.orders, "logger") {
		t.Fatalf("CH target changed: %+v", ch)
	}
	sp := delegateTargetFor(&spritesMarkerSvc{markerExecService: &markerExecService{stubService: &stubService{}}})
	if !sp.sprites || sp.marker != spritesDoneMarker || sp.workDir != spritesWorkDir {
		t.Fatalf("sprites target: %+v", sp)
	}
	if strings.Contains(sp.orders, "/run/nexus") || strings.Contains(sp.orders, "logger") || !strings.Contains(sp.orders, spritesDoneMarker) {
		t.Fatalf("sprites orders wrong:\n%s", sp.orders)
	}
}

func TestPoll_SpritesMarker_EmitsDelegateDoneOnce(t *testing.T) {
	evs := captureDelegateEmits(t)
	svc := &spritesMarkerSvc{markerExecService: &markerExecService{stubService: &stubService{}, marker: "all green\n"}}
	for i := 0; i < 3; i++ {
		res, err := buildPollResult(context.Background(), svc, "proj/b", herdragent.State{})
		if err != nil || res.DoneVia != "marker" || res.MarkerContent != "all green" {
			t.Fatalf("poll %d: %+v err=%v", i, res, err)
		}
	}
	if len(*evs) != 1 {
		t.Fatalf("emits = %d, want 1", len(*evs))
	}
	ev := (*evs)[0]
	if ev.Type != hubclient.TypeDelegateDone || ev.Topic != hubclient.SandboxTopic("proj/b") {
		t.Fatalf("event: %+v", ev)
	}
	if got := svc.argvs[0]; len(got) != 2 || got[1] != spritesDoneMarker {
		t.Fatalf("marker argv = %v", got)
	}
}

func TestPoll_SpritesNoMarker_NoEmitAndUsesCloneDir(t *testing.T) {
	evs := captureDelegateEmits(t)
	svc := &spritesMarkerSvc{markerExecService: &markerExecService{stubService: &stubService{}}}
	res, err := buildPollResult(context.Background(), svc, "proj/b", herdragent.State{})
	if err != nil || res.DoneVia != "git" || len(*evs) != 0 {
		t.Fatalf("res=%+v err=%v emits=%d", res, err, len(*evs))
	}
	if svc.cwds[len(svc.cwds)-1] != spritesWorkDir {
		t.Fatalf("git cwd = %q", svc.cwds[len(svc.cwds)-1])
	}
}

func TestPoll_CHMarker_NoHostEmit(t *testing.T) {
	evs := captureDelegateEmits(t)
	svc := &markerExecService{stubService: &stubService{}, marker: "done"}
	res, err := buildPollResult(context.Background(), svc, "proj/b", herdragent.State{})
	if err != nil || res.DoneVia != "marker" || len(*evs) != 0 {
		t.Fatalf("res=%+v err=%v emits=%d", res, err, len(*evs))
	}
}

func TestDispatchClearsDoneFlag_AllowsSecondEmit(t *testing.T) {
	evs := captureDelegateEmits(t)
	emitDelegateDoneOnce(context.Background(), "sb1", "a")
	emitDelegateDoneOnce(context.Background(), "sb1", "a")
	clearDelegateDoneFlag("sb1")
	emitDelegateDoneOnce(context.Background(), "sb1", "b")
	if len(*evs) != 2 {
		t.Fatalf("emits = %d, want 2", len(*evs))
	}
}

func TestDispatch_SpritesBrief(t *testing.T) {
	captureDelegateEmits(t)
	var brief string
	orig := runHostCLI
	t.Cleanup(func() { runHostCLI = orig })
	runHostCLI = func(_ context.Context, argv ...string) (string, error) {
		brief = argv[len(argv)-1]
		return "ok", nil
	}
	svc := &spritesMarkerSvc{markerExecService: &markerExecService{stubService: &stubService{}}}
	cs, closeFn := connectPairSvc(t, svc)
	defer closeFn()
	res := callTool(t, cs, "delegate_agent_dispatch", map[string]any{"ref": "proj/b", "brief": "do work"})
	if res.IsError {
		t.Fatal(resultText(t, res))
	}
	if !strings.Contains(brief, spritesDoneMarker) || strings.Contains(brief, "logger") || strings.Contains(brief, "/run/nexus") {
		t.Fatalf("brief:\n%s", brief)
	}
	if got := svc.argvs[0]; len(got) != 3 || got[2] != spritesDoneMarker {
		t.Fatalf("rm argv = %v", got)
	}
}
