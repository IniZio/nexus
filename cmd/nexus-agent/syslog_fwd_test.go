package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

const sampleCE = `<14>1 2026-10-03T12:00:00Z host gw - - [ce@32473 specversion="1.0" id="E1" source="groundwork:///workspace" type="groundwork.child_gate" subject="link:L1" datacontenttype="application/json"] {"link_id":"L1","slice":"S1","verdict":"APPROVE"}`

func TestParseCEEntry(t *testing.T) {
	ev, ok := parseCEEntry(sampleCE + "\n")
	if !ok {
		t.Fatal("not parsed")
	}
	want := ceEvent{ID: "E1", Source: "groundwork:///workspace", Type: "groundwork.child_gate",
		Subject: "link:L1", DataContentType: "application/json", Data: `{"link_id":"L1","slice":"S1","verdict":"APPROVE"}`}
	if ev != want {
		t.Fatalf("got %+v want %+v", ev, want)
	}
}

func TestParseCEEntryEscapedSD(t *testing.T) {
	line := `<14>1 2026-10-03T12:00:00Z h a - - [other x="1"][ce@32473 specversion="1.0" id="a\"b\]c\\d" source="s" type="t"] {}`
	ev, ok := parseCEEntry(line)
	if !ok {
		t.Fatal("not parsed")
	}
	if ev.ID != `a"b]c\d` {
		t.Fatalf("id=%q", ev.ID)
	}
}

func TestParseCEEntryDrops(t *testing.T) {
	cases := map[string]string{
		"no ce sd":       `<14>1 2026-10-03T12:00:00Z h a - - [other id="1"] {}`,
		"nil sd":         `<14>1 2026-10-03T12:00:00Z h a - - - hello`,
		"rfc3164":        `<13>Oct  3 12:00:00 host tag: hello`,
		"missing type":   `<14>1 2026-10-03T12:00:00Z h a - - [ce@32473 specversion="1.0" id="1" source="s"] {}`,
		"no specversion": `<14>1 2026-10-03T12:00:00Z h a - - [ce@32473 id="1" source="s" type="t"] {}`,
		"unterminated":   `<14>1 2026-10-03T12:00:00Z h a - - [ce@32473 id="1`,
		"empty":          ``,
		"oversize": `<14>1 2026-10-03T12:00:00Z h a - - [ce@32473 specversion="1.0" id="1" source="s" type="t"] ` +
			strings.Repeat("x", syslogMaxData+1),
	}
	for name, line := range cases {
		if _, ok := parseCEEntry(line); ok {
			t.Errorf("%s: expected drop", name)
		}
	}
}

func TestForwarderQueueFullDropsNewest(t *testing.T) {
	f := newCEForwarder()
	clock := time.Unix(1000, 0)
	f.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	for i := 0; i < syslogQueueCap+10; i++ {
		f.offer(sampleCE)
	}
	if len(f.q) != syslogQueueCap {
		t.Fatalf("queue=%d", len(f.q))
	}
	if f.dropped.Load() != 10 {
		t.Fatalf("dropped=%d", f.dropped.Load())
	}
}

func TestForwarderRateLimit(t *testing.T) {
	f := newCEForwarder()
	f.now = func() time.Time { return time.Unix(1000, 0) }
	for i := 0; i < syslogBurst+50; i++ {
		f.offer(sampleCE)
	}
	if got := len(f.q); got != syslogBurst || f.dropped.Load() != 50 {
		t.Fatalf("queued=%d dropped=%d", got, f.dropped.Load())
	}
}

func TestForwarderServeWritesJSONLines(t *testing.T) {
	f := newCEForwarder()
	f.offer(sampleCE)
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.serve(ctx, a)
	var ev ceEvent
	if err := json.NewDecoder(b).Decode(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != "E1" || !bytes.Contains([]byte(ev.Data), []byte("APPROVE")) {
		t.Fatalf("%+v", ev)
	}
}

func jText(k, v string) string { return k + "=" + v + "\n" }

func jBin(k, v string) string {
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], uint64(len(v)))
	return k + "\n" + string(l[:]) + v + "\n"
}

const journalHead = "SYSLOG_IDENTIFIER=logger\nCE_SPECVERSION=1.0\nCE_ID=E1\nCE_SOURCE=groundwork:///workspace\n" +
	"CE_TYPE=groundwork.child_gate\nCE_SUBJECT=link:L1\nCE_TIME=2026-10-03T12:00:00Z\nCE_DATACONTENTTYPE=application/json\n"

func TestParseJournalEntry(t *testing.T) {
	ev, ok := parseJournalEntry([]byte(journalHead + `MESSAGE={"verdict":"APPROVE"}` + "\n"))
	if !ok {
		t.Fatal("not parsed")
	}
	want := ceEvent{ID: "E1", Source: "groundwork:///workspace", Type: "groundwork.child_gate",
		Subject: "link:L1", Time: "2026-10-03T12:00:00Z", DataContentType: "application/json",
		Data: `{"verdict":"APPROVE"}`}
	if ev != want {
		t.Fatalf("got %+v want %+v", ev, want)
	}
}

func TestParseJournalEntryBinaryMultiline(t *testing.T) {
	msg := "{\"a\":\n\"b\"}\n"
	ev, ok := parseJournalEntry([]byte(journalHead + jBin("MESSAGE", msg)))
	if !ok || ev.Data != msg || ev.DataContentType != "application/json" {
		t.Fatalf("ok=%v %+v", ok, ev)
	}
	mid := jText("CE_SPECVERSION", "1.0") + jBin("CE_ID", "E\n1") + jText("CE_SOURCE", "s") + jText("CE_TYPE", "t")
	ev, ok = parseJournalEntry([]byte(mid))
	if !ok || ev.ID != "E\n1" || ev.Data != "{}" {
		t.Fatalf("ok=%v %+v", ok, ev)
	}
}

func TestParseJournalEntryDrops(t *testing.T) {
	long := strings.Repeat("x", syslogMaxData+1)
	trunc := jBin("MESSAGE", "hello")
	cases := map[string][]byte{
		"no specversion": []byte("CE_ID=1\nCE_SOURCE=s\nCE_TYPE=t\nMESSAGE={}\n"),
		"no type":        []byte("CE_SPECVERSION=1.0\nCE_ID=1\nCE_SOURCE=s\n"),
		"plain log":      []byte("MESSAGE=hi\nPRIORITY=6\n"),
		"oversize data":  []byte(journalHead + jBin("MESSAGE", long)),
		"len past end":   []byte(journalHead + trunc[:len(trunc)-3]),
		"bad terminator": []byte(journalHead + trunc[:len(trunc)-1] + "x"),
		"no newline":     []byte("CE_SPECVERSION=1.0"),
		"empty":          nil,
	}
	for name, b := range cases {
		if _, ok := parseJournalEntry(b); ok {
			t.Errorf("%s: expected drop", name)
		}
	}
}

func TestForwarderJournalSharedQueueAndLimits(t *testing.T) {
	f := newCEForwarder()
	f.now = func() time.Time { return time.Unix(1000, 0) }
	dg := journalHead + "MESSAGE={}\n"
	for i := 0; i < syslogBurst+20; i++ {
		if i%2 == 0 {
			f.offerJournal(dg)
		} else {
			f.offer(sampleCE)
		}
	}
	if got := len(f.q); got != syslogBurst || f.dropped.Load() != 20 {
		t.Fatalf("queued=%d dropped=%d", got, f.dropped.Load())
	}
	if syslogMaxDatagram != 16<<10 || syslogQueueCap != 256 {
		t.Fatal("limits changed")
	}
}
