package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/hubclient"
)

func open(t *testing.T) *Hub {
	t.Helper()
	h, err := Open(filepath.Join(t.TempDir(), "hub", "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func TestOpenPragmasAndTables(t *testing.T) {
	h := open(t)
	var mode string
	if err := h.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	var bt int
	if err := h.db.QueryRow("PRAGMA busy_timeout").Scan(&bt); err != nil || bt != 5000 {
		t.Fatalf("busy_timeout = %d, %v", bt, err)
	}
	for _, tbl := range []string{"events", "cursors"} {
		var n int
		if err := h.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
}

func TestRefusedFS(t *testing.T) {
	cases := []struct {
		magic int64
		want  bool
	}{
		{0x6969, true},     // NFS
		{0x01021997, true}, // 9p
		{0x65735546, true}, // FUSE (virtiofs)
		{0x517B, true},     // SMB
		{0xEF53, false},    // ext4
		{0x58465342, false},
		{0x01021994, false},
	}
	for _, c := range cases {
		if _, got := refusedFSName(c.magic); got != c.want {
			t.Errorf("magic %#x refused=%v want %v", c.magic, got, c.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := open(t)
	mk := func(topic, typ, subj string) hubclient.Event {
		return hubclient.Event{Topic: topic, Type: typ, Subject: subj, Payload: json.RawMessage(`{"id":"` + subj + `"}`)}
	}
	s1, err := h.Append(ctx, mk("sandbox:a", hubclient.TypeSandboxCreated, "a"))
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := h.Append(ctx, mk("sandbox:a", hubclient.TypeSandboxStarted, "a"))
	s3, _ := h.Append(ctx, mk("sandbox:b", hubclient.TypeSandboxCreated, "b"))
	if !(s1 < s2 && s2 < s3) {
		t.Fatalf("seqs not increasing: %d %d %d", s1, s2, s3)
	}

	got, err := h.ReadAfter(ctx, "sandbox:a", s1, 0)
	if err != nil || len(got) != 1 || got[0].Seq != s2 || got[0].Actor != hubclient.ActorAnonymous || got[0].TS == 0 {
		t.Fatalf("ReadAfter = %+v, %v", got, err)
	}
	all, _ := h.ReadAfter(ctx, "", 0, 2)
	if len(all) != 2 {
		t.Fatalf("limit: got %d", len(all))
	}

	last, ok, err := h.LastBySubject(ctx, "a")
	if err != nil || !ok || last.Seq != s2 || last.Type != hubclient.TypeSandboxStarted {
		t.Fatalf("LastBySubject = %+v %v %v", last, ok, err)
	}
	if _, ok, _ := h.LastBySubject(ctx, "nope"); ok {
		t.Fatal("LastBySubject found missing subject")
	}
	la, err := h.LastAll(ctx)
	if err != nil || len(la) != 2 || la[0].Seq != s2 || la[1].Seq != s3 {
		t.Fatalf("LastAll = %+v, %v", la, err)
	}

	if err := h.Ack(ctx, "seat1", "sandbox:a", s2); err != nil {
		t.Fatal(err)
	}
	_ = h.Ack(ctx, "seat1", "sandbox:a", s1)
	if c, _ := h.Cursor(ctx, "seat1", "sandbox:a"); c != s2 {
		t.Fatalf("cursor = %d want %d", c, s2)
	}
	if c, _ := h.Cursor(ctx, "seat2", "sandbox:a"); c != 0 {
		t.Fatalf("unset cursor = %d", c)
	}
}
