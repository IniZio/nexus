package hub

import (
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/hubclient"
)

func add(t *testing.T, h *Hub, typ, subj string, ts int64) int64 {
	t.Helper()
	seq, err := h.Append(context.Background(), hubclient.Event{Topic: "sandbox:" + subj, Type: typ, Subject: subj, TS: ts})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestDeliverNoGapFresh(t *testing.T) {
	h := open(t)
	add(t, h, "sandbox.created", "a", 1000)
	add(t, h, "sandbox.started", "a", 2000)
	lines, err := h.Deliver(context.Background(), "", 0, 0)
	if err != nil || len(lines) != 2 || lines[0].Kind != hubclient.KindEvent || lines[0].Count != 1 {
		t.Fatalf("lines=%+v err=%v", lines, err)
	}
}

func TestDeliverGapOnce(t *testing.T) {
	h := open(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		add(t, h, "t", "s", int64(i+1)*100000)
	}
	if _, err := h.db.Exec(`DELETE FROM events WHERE seq <= 3`); err != nil {
		t.Fatal(err)
	}
	lines, err := h.Deliver(ctx, "", 1, 0)
	if err != nil || len(lines) != 3 {
		t.Fatalf("lines=%+v err=%v", lines, err)
	}
	if g := lines[0]; g.Kind != hubclient.KindGap || g.From != 2 || g.To != 3 {
		t.Fatalf("gap=%+v", g)
	}
	if lines[1].Kind != hubclient.KindEvent || lines[1].Seq != 4 || lines[2].Seq != 5 {
		t.Fatalf("events=%+v", lines[1:])
	}
	lines, _ = h.Deliver(ctx, "", 3, 0)
	if len(lines) != 2 || lines[0].Kind != hubclient.KindEvent {
		t.Fatalf("cursor at boundary must not gap: %+v", lines)
	}
}

func TestDeliverCoalesce(t *testing.T) {
	h := open(t)
	add(t, h, "sandbox.died", "a", 100)
	add(t, h, "sandbox.died", "b", 1000)
	add(t, h, "sandbox.died", "a", 10000)
	last := add(t, h, "sandbox.died", "a", 29000)
	add(t, h, "sandbox.died", "a", 31100)
	lines, err := h.Deliver(context.Background(), "", 0, 0)
	if err != nil || len(lines) != 3 {
		t.Fatalf("lines=%+v err=%v", lines, err)
	}
	if lines[0].Subject != "b" || lines[0].Count != 1 {
		t.Fatalf("b=%+v", lines[0])
	}
	if lines[1].Seq != last || lines[1].Count != 3 {
		t.Fatalf("a group=%+v", lines[1])
	}
	if lines[2].Count != 1 {
		t.Fatalf("new window=%+v", lines[2])
	}
	for i := 1; i < len(lines); i++ {
		if lines[i].Seq <= lines[i-1].Seq {
			t.Fatalf("order broken: %+v", lines)
		}
	}
}

func TestDeliverCoalesceDifferentType(t *testing.T) {
	h := open(t)
	add(t, h, "sandbox.started", "a", 100)
	add(t, h, "sandbox.died", "a", 1000)
	lines, _ := h.Deliver(context.Background(), "", 0, 0)
	if len(lines) != 2 {
		t.Fatalf("lines=%+v", lines)
	}
}

func TestPrune(t *testing.T) {
	h := open(t)
	ctx := context.Background()
	now := time.Now()
	h.now = func() time.Time { return now }
	old := now.Add(-8 * 24 * time.Hour).UnixMilli()
	add(t, h, "t", "a", old)
	add(t, h, "t", "a", old)
	for i := 0; i < 5; i++ {
		add(t, h, "t", "a", now.UnixMilli())
	}
	n, err := h.Prune(ctx, RetainAge, 3)
	if err != nil || n != 4 {
		t.Fatalf("pruned=%d err=%v", n, err)
	}
	ev, _ := h.ReadAfter(ctx, "", 0, 0)
	if len(ev) != 3 || ev[0].Seq != 5 {
		t.Fatalf("remaining=%+v", ev)
	}
	lines, _ := h.Deliver(ctx, "", 0, 0)
	if lines[0].Kind != hubclient.KindGap || lines[0].From != 1 || lines[0].To != 4 {
		t.Fatalf("expected gap after prune: %+v", lines[0])
	}
}
