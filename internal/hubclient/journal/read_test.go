//go:build linux

package journal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/hubclient"
)

func entry(cursor, id, subj, sbx, typ string) string {
	return `{"__CURSOR":"` + cursor + `","CE_SPECVERSION":"1.0","CE_ID":"` + id + `","CE_SOURCE":"nexus","CE_TYPE":"` + typ +
		`","CE_SUBJECT":"` + subj + `","CE_TIME":"2026-10-03T12:00:00Z","NEXUS_SANDBOX":"` + sbx + `","MESSAGE":"{\"a\":1}"}` + "\n"
}

func setup(t *testing.T, out, probe string) string {
	t.Helper()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("FAKE_JCTL_ARGV", argv)
	t.Setenv("FAKE_JCTL_OUT", write("out", out))
	t.Setenv("FAKE_JCTL_PROBE", write("probe", probe))
	old := JournalctlBin
	JournalctlBin, _ = filepath.Abs("testdata/fake-journalctl")
	t.Cleanup(func() { JournalctlBin = old })
	return argv
}

func drain(t *testing.T, ch <-chan hubclient.Item) []hubclient.Item {
	t.Helper()
	var got []hubclient.Item
	timeout := time.After(5 * time.Second)
	for {
		select {
		case it, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, it)
		case <-timeout:
			t.Fatal("timeout")
		}
	}
}

func argvLines(t *testing.T, p string) []string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestReadDedupeAndMapping(t *testing.T) {
	out := entry("c1", "id1", "sandbox:a", "a", "sandbox.started") + entry("c1", "id1", "sandbox:a", "a", "sandbox.started") + entry("c2", "id2", "host", "", "binary.installed") + "{\"CE_SPECVERSION\":[50]}\n"
	setup(t, out, "")
	ch, err := NewReader().Read(context.Background(), hubclient.Filter{}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(got), got)
	}
	e := got[0].Event
	if e.ID != "id1" || e.Cursor != "c1" || e.Topic != "sandbox:a" || e.Subject != "a" || e.Type != "sandbox.started" || string(e.Payload) != `{"a":1}` || e.TS == 0 {
		t.Fatalf("bad mapping: %+v", e)
	}
}

func TestReadGap(t *testing.T) {
	argv := setup(t, entry("c9", "id9", "host", "", "x"), "")
	ch, err := NewReader().Read(context.Background(), hubclient.Filter{}, "gone")
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 2 || !got[0].Gap || got[1].Gap || got[1].Event.ID != "id9" {
		t.Fatalf("want gap then event, got %+v", got)
	}
	if l := argvLines(t, argv); !strings.Contains(l[len(l)-1], "--after-cursor=gone") {
		t.Fatalf("missing after-cursor: %v", l)
	}
}

func TestReadCursorFoundNoGap(t *testing.T) {
	setup(t, entry("c2", "id2", "host", "", "x"), entry("c1", "id1", "host", "", "x"))
	ch, err := NewReader().Read(context.Background(), hubclient.Filter{}, "c1")
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 1 || got[0].Gap {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestReadFilterArgs(t *testing.T) {
	argv := setup(t, "", "")
	ch, err := NewReader().Read(context.Background(), hubclient.Filter{Topics: []string{"seat:x", "host"}, Types: []string{"message"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	l := argvLines(t, argv)
	want := "--user -o json --follow -n0 CE_SPECVERSION=1.0 CE_SUBJECT=seat:x CE_SUBJECT=host CE_TYPE=message"
	if l[0] != want {
		t.Fatalf("argv\n got %q\nwant %q", l[0], want)
	}
}

func TestReadFilterSandboxesArgs(t *testing.T) {
	argv := setup(t, "", "")
	f := hubclient.Filter{Topics: []string{"seat:x", "host"}, Sandboxes: []string{"id1", "id2"}}
	ch, err := NewReader().Read(context.Background(), f, "")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	want := "--user -o json --follow -n0 CE_SPECVERSION=1.0 CE_SUBJECT=seat:x CE_SUBJECT=host + CE_SPECVERSION=1.0 NEXUS_SANDBOX=id1 NEXUS_SANDBOX=id2"
	if l := argvLines(t, argv); l[0] != want {
		t.Fatalf("argv\n got %q\nwant %q", l[0], want)
	}
}

func TestParseEntryDataContentType(t *testing.T) {
	ev, ok := parseEntry([]byte(`{"CE_SPECVERSION":"1.0","CE_DATACONTENTTYPE":"text/plain"}`))
	if !ok || ev.DataContentType != "text/plain" {
		t.Fatalf("%+v %v", ev, ok)
	}
}

func TestLastAndLastAll(t *testing.T) {
	out := entry("c3", "i3", "sandbox:a", "a", "sandbox.stopped") + entry("c2", "i2", "sandbox:b", "b", "sandbox.started") + entry("c1", "i1", "sandbox:a", "a", "sandbox.started")
	argv := setup(t, out, "")
	r := NewReader()
	ev, err := r.Last(context.Background(), "a")
	if err != nil || ev == nil || ev.ID != "i3" {
		t.Fatalf("last: %+v %v", ev, err)
	}
	if l := argvLines(t, argv); !strings.Contains(l[0], "NEXUS_SANDBOX=a + CE_SPECVERSION=1.0 CE_SUBJECT=a") {
		t.Fatalf("last argv: %q", l[0])
	}
	all, err := r.LastAll(context.Background())
	if err != nil || len(all) != 2 || all[0].ID != "i3" || all[1].ID != "i2" {
		t.Fatalf("lastall: %+v %v", all, err)
	}
}

func TestFieldShapes(t *testing.T) {
	ev, ok := parseEntry([]byte(`{"CE_SPECVERSION":["1.0"],"CE_ID":[105,100],"MESSAGE":[110,117,108,108]}`))
	if !ok || ev.ID != "id" || string(ev.Payload) != "null" {
		t.Fatalf("%+v %v", ev, ok)
	}
}

func TestReadNoCursorWithSinceArgs(t *testing.T) {
	argv := setup(t, "", "")
	since := time.Date(2026, 10, 3, 12, 0, 5, 0, time.UTC)
	ch, err := NewReader().Read(context.Background(), hubclient.Filter{Topics: []string{"seat:x"}, Since: since}, "")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	want := "--user -o json --follow --since=2026-10-03 12:00:05 UTC CE_SPECVERSION=1.0 CE_SUBJECT=seat:x"
	if l := argvLines(t, argv); l[0] != want {
		t.Fatalf("argv\n got %q\nwant %q", l[0], want)
	}
}
