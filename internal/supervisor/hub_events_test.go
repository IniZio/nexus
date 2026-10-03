package supervisor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/oomattr"
	"github.com/IniZio/nexus/internal/hubclient"
)

type fakeHub struct {
	events []hubclient.Event
}

func (f *fakeHub) emit(_ context.Context, ev hubclient.Event) error {
	f.events = append(f.events, ev)
	return nil
}

func newTestLifecycle(f *fakeHub, snaps ...oomattr.Snapshot) *lifecycleEvents {
	l := newLifecycleEvents(f, "sb1", "h1")
	i := 0
	l.take = func() oomattr.Snapshot {
		s := snaps[min(i, len(snaps)-1)]
		i++
		return s
	}
	return l
}

func TestLifecycleDiedDeltas(t *testing.T) {
	f := &fakeHub{}
	l := newTestLifecycle(f,
		oomattr.Snapshot{VmstatOOMKill: 1, ScopeOOMKill: 2},
		oomattr.Snapshot{VmstatOOMKill: 3, ScopeOOMKill: 2, AncestorOOMKill: 4},
	)
	l.adopted()
	l.died(oomattr.Signal{Signal: 9, Code: 0})
	ev := f.events[len(f.events)-1]
	var p hubclient.SandboxDiedPayload
	_ = json.Unmarshal(ev.Payload, &p)
	if ev.Type != hubclient.TypeSandboxDied || p.Signal != 9 || p.OOM.VmstatDelta != 2 || p.OOM.AncestorOOMDelta != 4 || p.Backfilled {
		t.Fatalf("%+v", p)
	}
	if p.Cause == "" {
		t.Fatal("empty cause")
	}
}

func TestLifecycleAdoptedEmitsNothing(t *testing.T) {
	f := &fakeHub{}
	newTestLifecycle(f, oomattr.Snapshot{}).adopted()
	if len(f.events) != 0 {
		t.Fatal("adopted emitted")
	}
}

func TestLifecycleNilSafe(t *testing.T) {
	var l *lifecycleEvents
	l.adopted()
	l.died(oomattr.Signal{})
}

type fakeExiter struct {
	info cloudhypervisor.ExitInfo
	ok   bool
}

func (f fakeExiter) RuntimeExit(string) (cloudhypervisor.ExitInfo, bool) { return f.info, f.ok }

func TestExitSignal(t *testing.T) {
	if got := exitSignal(fakeExiter{cloudhypervisor.ExitInfo{Signal: 9, Code: 1}, true}, "x"); got.Signal != 9 || got.Code != 1 {
		t.Fatalf("%+v", got)
	}
	if got := exitSignal(fakeExiter{}, "x"); got != (oomattr.Signal{}) {
		t.Fatalf("%+v", got)
	}
	if got := exitSignal(struct{}{}, "x"); got != (oomattr.Signal{}) {
		t.Fatalf("%+v", got)
	}
}

func TestLifecycleDiedAdoptedUnknownSignal(t *testing.T) {
	f := &fakeHub{}
	l := newTestLifecycle(f, oomattr.Snapshot{}, oomattr.Snapshot{VmstatOOMKill: 1})
	l.adopted()
	l.died(oomattr.UnknownExit)
	var p hubclient.SandboxDiedPayload
	_ = json.Unmarshal(f.events[len(f.events)-1].Payload, &p)
	if p.Cause != hubclient.CauseHostOOM || p.Signal != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestLifecycleDiedCleanExitIgnoresOOM(t *testing.T) {
	f := &fakeHub{}
	l := newTestLifecycle(f, oomattr.Snapshot{}, oomattr.Snapshot{VmstatOOMKill: 1})
	l.adopted()
	l.died(oomattr.Signal{})
	var p hubclient.SandboxDiedPayload
	_ = json.Unmarshal(f.events[len(f.events)-1].Payload, &p)
	if p.Cause != hubclient.CauseUnknown {
		t.Fatalf("%+v", p)
	}
}
