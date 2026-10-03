package hubclient

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/hubstate"
)

type fakeBus struct {
	mu     sync.Mutex
	events []Event
	items  []Item
	gotCur string
	gotF   Filter
}

func (f *fakeBus) Emit(_ context.Context, ev Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeBus) Read(_ context.Context, flt Filter, cursor string) (<-chan Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotF = flt
	f.gotCur = cursor
	ch := make(chan Item, len(f.items))
	for _, it := range f.items {
		ch <- it
	}
	close(ch)
	return ch, nil
}

func newBox(t *testing.T, dir string, bus *fakeBus) *Mailbox {
	t.Helper()
	st, err := hubstate.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Mailbox{Store: st, Emitter: bus, Reader: bus, Idle: 20 * time.Millisecond}
}

func TestSendInbox(t *testing.T) {
	bus := &fakeBus{}
	mb := newBox(t, t.TempDir(), bus)
	id, err := mb.Send(context.Background(), "a", "repo#x", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if len(bus.events) != 1 || bus.events[0].Topic != "seat:repo#x" || bus.events[0].ID != id || bus.events[0].Type != TypeMessage {
		t.Fatalf("emitted %+v", bus.events)
	}
	// journal echoes the same event: must dedupe
	bus.items = []Item{{Event: Event{ID: id, Cursor: "c1", Type: TypeMessage, Topic: "seat:repo#x"}}}
	in, err := mb.Inbox(context.Background(), "repo#x")
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Items) != 1 || in.Items[0].Event.ID != id || in.Cursor != "c1" {
		t.Fatalf("inbox = %+v", in)
	}
	if !strings.Contains(string(in.Items[0].Event.Payload), `"text":"hi"`) {
		t.Fatalf("payload %s", in.Items[0].Event.Payload)
	}
}

func TestAckPersistsAndUnackedSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	bus := &fakeBus{}
	mb := newBox(t, dir, bus)
	id1, _ := mb.Send(context.Background(), "a", "s", "one")
	id2, _ := mb.Send(context.Background(), "a", "s", "two")
	if err := mb.Ack("s", "cur9", []string{id1}); err != nil {
		t.Fatal(err)
	}
	bus2 := &fakeBus{}
	mb2 := newBox(t, dir, bus2)
	in, err := mb2.Inbox(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Items) != 1 || in.Items[0].Event.ID != id2 {
		t.Fatalf("after restart = %+v", in.Items)
	}
	if bus2.gotCur != "cur9" || in.Cursor != "cur9" {
		t.Fatalf("cursor read=%q result=%q", bus2.gotCur, in.Cursor)
	}
}

func TestDigestDefaultSeat(t *testing.T) {
	d := FormatDigest("repo#1", nil, DigestOpts{TakeSeat: "repo"})
	if !strings.Contains(d, "you own nothing; nexus hub seat take repo") {
		t.Fatalf("digest = %q", d)
	}
}

func TestDigestSections(t *testing.T) {
	bus := &fakeBus{items: []Item{
		{Gap: true},
		{Event: Event{ID: "e1", Cursor: "c", Type: TypeSandboxDied, Topic: "sandbox:x"}},
	}}
	mb := newBox(t, t.TempDir(), bus)
	if _, err := mb.Send(context.Background(), "peer", "s", "ping"); err != nil {
		t.Fatal(err)
	}
	d, err := mb.Digest(context.Background(), "s", []string{"x"}, DigestOpts{Leases: []string{"sandbox/x"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"peer: ping", "sandbox.died sandbox:x", "gap:", "sandbox/x", "open budget reservations: none"} {
		if !strings.Contains(d, w) {
			t.Errorf("digest missing %q:\n%s", w, d)
		}
	}
}

func TestInboxPassesSeatSandboxes(t *testing.T) {
	bus := &fakeBus{}
	mb := newBox(t, t.TempDir(), bus)
	mb.Sandboxes = func(seat string) []string { return []string{"id-" + seat} }
	if _, err := mb.Inbox(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if len(bus.gotF.Sandboxes) != 1 || bus.gotF.Sandboxes[0] != "id-s" {
		t.Fatalf("filter %+v", bus.gotF)
	}
}

func TestInboxNoCursorUsesSeatStart(t *testing.T) {
	bus := &fakeBus{}
	mb := newBox(t, t.TempDir(), bus)
	start := time.Unix(1700000000, 0)
	mb.SeatStart = func(string) time.Time { return start }
	if _, err := mb.Inbox(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if !bus.gotF.Since.Equal(start) {
		t.Fatalf("since %v, want %v", bus.gotF.Since, start)
	}
	if err := mb.Ack("s", "c1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := mb.Inbox(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if !bus.gotF.Since.IsZero() || bus.gotCur != "c1" {
		t.Fatalf("with cursor: since %v cursor %q", bus.gotF.Since, bus.gotCur)
	}
}
