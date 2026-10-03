package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/hubclient"
)

type fakeHub struct {
	emitted    []hubclient.Event
	emitErr    error
	all        []hubclient.Event
	readFilter hubclient.Filter
	readCursor string
	items      []hubclient.Item
}

func (f *fakeHub) Emit(_ context.Context, ev hubclient.Event) error {
	f.emitted = append(f.emitted, ev)
	return f.emitErr
}
func (f *fakeHub) Last(context.Context, string) (*hubclient.Event, error) { return nil, nil }
func (f *fakeHub) LastAll(context.Context) ([]hubclient.Event, error)     { return f.all, nil }
func (f *fakeHub) Read(_ context.Context, flt hubclient.Filter, cursor string) (<-chan hubclient.Item, error) {
	f.readFilter, f.readCursor = flt, cursor
	ch := make(chan hubclient.Item, len(f.items))
	for _, it := range f.items {
		ch <- it
	}
	close(ch)
	return ch, nil
}

func useFakeHub(t *testing.T, f *fakeHub, root string) {
	t.Helper()
	oc, or := hubNewClient, hubStoreRoot
	t.Cleanup(func() { hubNewClient, hubStoreRoot = oc, or })
	hubNewClient = func() (hubAPI, error) { return f, nil }
	hubStoreRoot = func() (string, error) { return root, nil }
}

func TestHubWatchFlagsAndLines(t *testing.T) {
	f := &fakeHub{items: []hubclient.Item{{Gap: true}, {Event: hubclient.Event{ID: "e1", Cursor: "c1", Type: "message"}}}}
	useFakeHub(t, f, t.TempDir())
	var so bytes.Buffer
	out := NewOutput(&so, io.Discard, false)
	if err := runHub(context.Background(), []string{"watch", "--topic", "host", "--seat", "s1", "--cursor", "c0"}, out); err != nil {
		t.Fatal(err)
	}
	if got := f.readFilter.Topics; len(got) != 2 || got[0] != "host" || got[1] != "seat:s1" || f.readCursor != "c0" {
		t.Errorf("filter=%v cursor=%q", got, f.readCursor)
	}
	lines := strings.Split(strings.TrimSpace(so.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"kind":"gap"`) ||
		!strings.Contains(lines[1], `"kind":"event"`) || !strings.Contains(lines[1], `"cursor":"c1"`) {
		t.Errorf("stdout = %q", so.String())
	}
}

func TestHubEmitBinaryInstalled(t *testing.T) {
	f := &fakeHub{}
	useFakeHub(t, f, t.TempDir())
	out := NewOutput(io.Discard, io.Discard, false)
	err := runHub(context.Background(), []string{"emit", "binary-installed", "--path", "/p", "--version", "v1", "--agent-hash", "abc"}, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.emitted) != 1 {
		t.Fatalf("emitted %d", len(f.emitted))
	}
	ev := f.emitted[0]
	if ev.Topic != "host" || ev.Type != "binary.installed" {
		t.Errorf("ev = %+v", ev)
	}
	var p hubclient.BinaryInstalledPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p != (hubclient.BinaryInstalledPayload{Path: "/p", Version: "v1", AgentHash: "abc"}) {
		t.Errorf("payload = %s (%v)", ev.Payload, err)
	}
}

func TestHubEmitFailureWarnsExitsZero(t *testing.T) {
	f := &fakeHub{emitErr: errors.New("boom")}
	useFakeHub(t, f, t.TempDir())
	var se bytes.Buffer
	out := NewOutput(io.Discard, &se, false)
	if err := runHub(context.Background(), []string{"emit", "binary-installed", "--path", "/p"}, out); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if !strings.Contains(se.String(), "WARN") {
		t.Errorf("stderr = %q", se.String())
	}
}

func TestHubPSListsSandboxesWithLastEvent(t *testing.T) {
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	sb := domain.Sandbox{ID: id, Name: "n", Project: "p", State: domain.Stopped}
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatal(err)
	}
	f := &fakeHub{all: []hubclient.Event{{ID: "evt-9", Type: "sandbox.died", Subject: id.String(), Payload: json.RawMessage(`{"cause":"host_oom"}`)}}}
	useFakeHub(t, f, root)
	var so bytes.Buffer
	out := NewOutput(&so, io.Discard, false)
	if err := runHub(context.Background(), []string{"ps"}, out); err != nil {
		t.Fatal(err)
	}
	s := so.String()
	for _, w := range []string{id.String(), "sandbox.died", "host_oom", "evt-9"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in %q", w, s)
		}
	}
}

func TestHubUnknownSubcommand(t *testing.T) {
	out := NewOutput(io.Discard, io.Discard, false)
	var ue *UsageError
	if err := runHub(context.Background(), []string{"nope"}, out); !errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
}
