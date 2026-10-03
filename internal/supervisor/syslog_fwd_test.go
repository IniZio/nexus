package supervisor

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/hubclient"
)

type captureEmit struct {
	mu  sync.Mutex
	evs []hubclient.Event
	ch  chan struct{}
}

func (c *captureEmit) emit(_ context.Context, ev hubclient.Event) error {
	c.mu.Lock()
	c.evs = append(c.evs, ev)
	c.mu.Unlock()
	c.ch <- struct{}{}
	return nil
}

func TestSyslogForwarderPreservesCEFields(t *testing.T) {
	cap := &captureEmit{ch: make(chan struct{}, 8)}
	a, b := net.Pipe()
	var once sync.Once
	f := &syslogForwarder{
		sandbox: "sb-1", emit: cap.emit, minBO: time.Millisecond, maxBO: time.Millisecond,
		dial: func(ctx context.Context) (net.Conn, error) {
			var c net.Conn
			once.Do(func() { c = a })
			if c == nil {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return c, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.run(ctx)

	lines := []string{
		`not json`,
		`{"id":"","source":"s","type":"t","data":"{}"}`,
		`{"id":"E2","source":"s","type":"t","data":"` + strings.Repeat("x", syslogFwdMaxData+1) + `"}`,
		`{"id":"E1","source":"groundwork:///workspace","type":"groundwork.child_gate","subject":"link:L1","datacontenttype":"application/json","time":"2026-10-03T12:00:00Z","data":"{\"verdict\":\"APPROVE\"}"}`,
	}
	go func() { _, _ = b.Write([]byte(strings.Join(lines, "\n") + "\n")) }()
	select {
	case <-cap.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	ev := cap.evs[0]
	if ev.ID != "E1" || ev.Type != "groundwork.child_gate" || ev.Topic != "link:L1" ||
		ev.Actor != "groundwork:///workspace" || ev.Subject != "sb-1" ||
		string(ev.Payload) != `{"verdict":"APPROVE"}` || ev.TS != 1791028800000 || ev.DataContentType != "application/json" {
		t.Fatalf("%+v", ev)
	}
	time.Sleep(50 * time.Millisecond)
	if len(cap.evs) != 1 {
		t.Fatalf("invalid lines leaked: %d events", len(cap.evs))
	}
}

func TestReadBoundedLineOversize(t *testing.T) {
	in := strings.Repeat("y", 100) + "\nok\n"
	r := bufio.NewReaderSize(strings.NewReader(in), 16)
	if l, err := readBoundedLine(r, 50); err != nil || l != nil {
		t.Fatalf("oversize: %q %v", l, err)
	}
	if l, err := readBoundedLine(r, 50); err != nil || string(l) != "ok\n" {
		t.Fatalf("next: %q %v", l, err)
	}
}
