package hubclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeHub(t *testing.T, script string) *Client {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nexus-hub")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Client{Resolve: func() (string, error) { return p, nil }}
}

func TestAppendAndLast(t *testing.T) {
	c := fakeHub(t, `
[ "$1" = "--protocol" ] && [ "$2" = "1" ] || exit 9
case "$3" in
append) cat >/dev/null; echo '{"seq":7}';;
last) echo '{"seq":7,"topic":"sandbox:a","type":"sandbox.started","subject":"a"}';;
last-all) echo '{"seq":1,"subject":"a"}'; echo '{"seq":2,"subject":"b"}';;
esac`)
	ctx := context.Background()
	seq, err := c.Append(ctx, Event{Topic: "t", Type: TypeSandboxCreated})
	if err != nil || seq != 7 {
		t.Fatalf("Append = %d, %v", seq, err)
	}
	ev, err := c.Last(ctx, "a")
	if err != nil || ev == nil || ev.Seq != 7 || ev.Subject != "a" {
		t.Fatalf("Last = %+v, %v", ev, err)
	}
	all, err := c.LastAll(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("LastAll = %+v, %v", all, err)
	}
}

func TestProtocolMismatch(t *testing.T) {
	c := fakeHub(t, `echo "want protocol 2" >&2; exit 3`)
	_, err := c.Append(context.Background(), Event{})
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("err = %v, want ErrProtocolMismatch", err)
	}
}

func TestEmitBestEffortSwallowsFailure(t *testing.T) {
	for name, c := range map[string]*Client{
		"exit1":    fakeHub(t, `echo boom >&2; exit 1`),
		"mismatch": fakeHub(t, `exit 3`),
		"resolve":  {Resolve: func() (string, error) { return "", errors.New("no binary") }},
	} {
		t.Run(name, func(t *testing.T) { c.EmitBestEffort(context.Background(), Event{Type: TypeSandboxDied}) })
	}
}

func TestWatchStreams(t *testing.T) {
	c := fakeHub(t, `
echo '{"kind":"event","seq":1,"topic":"t","type":"x"}'
echo '{"kind":"gap","from":2,"to":9,"count":8}'`)
	w, err := c.Watch(context.Background(), "t", 0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []WatchLine
	for l := range w.Lines {
		got = append(got, l)
	}
	if err := w.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != KindEvent || got[0].Seq != 1 || got[1].Kind != KindGap || got[1].From != 2 || got[1].To != 9 {
		t.Fatalf("lines = %+v", got)
	}
}

func TestWatchAckAndCancel(t *testing.T) {
	c := fakeHub(t, `
echo '{"kind":"event","seq":1}'
read l; echo "{\"kind\":\"event\",\"seq\":2,\"payload\":$l}"
exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, "t", 0, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if l := <-w.Lines; l.Seq != 1 {
		t.Fatalf("first = %+v", l)
	}
	if err := w.Ack(1); err != nil {
		t.Fatal(err)
	}
	l := <-w.Lines
	if l.Seq != 2 || string(l.Payload) != `{"ack":1}` {
		t.Fatalf("second = %+v payload %s", l, l.Payload)
	}
	cancel()
	for range w.Lines {
	}
	if err := w.Wait(); err != nil {
		t.Fatalf("Wait after cancel = %v", err)
	}
}

func TestAppendFillsActor(t *testing.T) {
	var got string
	p := filepath.Join(t.TempDir(), "nexus-hub")
	script := "#!/bin/sh\ncat > \"" + p + ".in\"\necho '{\"seq\":1}'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{Resolve: func() (string, error) { return p, nil }}
	read := func() string {
		b, _ := os.ReadFile(p + ".in")
		return string(b)
	}
	t.Setenv(EnvSession, "")
	_, _ = c.Append(context.Background(), Event{Topic: "t"})
	if got = read(); !strings.Contains(got, `"actor":"anonymous"`) {
		t.Fatalf("anon: %s", got)
	}
	t.Setenv(EnvSession, "seatA")
	_, _ = c.Append(context.Background(), Event{Topic: "t"})
	if got = read(); !strings.Contains(got, `"actor":"seatA"`) {
		t.Fatalf("env: %s", got)
	}
	_, _ = c.Append(context.Background(), Event{Topic: "t", Actor: "system:supervisor"})
	if got = read(); !strings.Contains(got, `"actor":"system:supervisor"`) {
		t.Fatalf("explicit: %s", got)
	}
}

func TestWatchSeatArgs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nexus-hub")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho \"$@\" > "+p+".args\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{Resolve: func() (string, error) { return p, nil }}
	w, err := c.Watch(context.Background(), "t", -1, false, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Wait()
	b, _ := os.ReadFile(p + ".args")
	if got := strings.TrimSpace(string(b)); got != "--protocol 1 watch --topic t --seat s1" {
		t.Fatalf("args = %q", got)
	}
}
