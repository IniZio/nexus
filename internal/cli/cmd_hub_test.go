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
	emitted   []hubclient.Event
	emitErr   error
	all       []hubclient.Event
	watchArgs []any
}

func (f *fakeHub) Emit(_ context.Context, ev hubclient.Event) error {
	f.emitted = append(f.emitted, ev)
	return f.emitErr
}
func (f *fakeHub) Last(context.Context, string) (*hubclient.Event, error) { return nil, nil }
func (f *fakeHub) LastAll(context.Context) ([]hubclient.Event, error)     { return f.all, nil }
func (f *fakeHub) Watch(_ context.Context, topic string, cursor int64, ack bool, seat string, w io.Writer) error {
	f.watchArgs = []any{topic, cursor, ack, seat}
	_, _ = w.Write([]byte(`{"kind":"event"}` + "\n"))
	return nil
}

func useFakeHub(t *testing.T, f *fakeHub, root string) {
	t.Helper()
	oc, or := hubNewClient, hubStoreRoot
	t.Cleanup(func() { hubNewClient, hubStoreRoot = oc, or })
	hubNewClient = func() (hubAPI, error) { return f, nil }
	hubStoreRoot = func() (string, error) { return root, nil }
}

func TestHubWatchPassesFlags(t *testing.T) {
	f := &fakeHub{}
	useFakeHub(t, f, t.TempDir())
	var so bytes.Buffer
	out := NewOutput(&so, io.Discard, false)
	if err := runHub(context.Background(), []string{"watch", "--topic", "host", "--cursor", "7", "--ack"}, out); err != nil {
		t.Fatal(err)
	}
	if f.watchArgs[0] != "host" || f.watchArgs[1] != int64(7) || f.watchArgs[2] != true {
		t.Errorf("watch args = %v", f.watchArgs)
	}
	if !strings.Contains(so.String(), `"kind":"event"`) {
		t.Errorf("stdout = %q", so.String())
	}
}

func TestHubWatchSeatPassthrough(t *testing.T) {
	t.Setenv(hubclient.EnvSession, "")
	run := func(args ...string) []any {
		f := &fakeHub{}
		useFakeHub(t, f, t.TempDir())
		out := NewOutput(io.Discard, io.Discard, false)
		if err := runHub(context.Background(), append([]string{"watch", "--topic", "h"}, args...), out); err != nil {
			t.Fatal(err)
		}
		return f.watchArgs
	}
	if a := run("--consumer", "s1"); a[1] != int64(-1) || a[3] != "s1" {
		t.Errorf("consumer: %v", a)
	}
	if a := run("--seat", "s1", "--cursor", "5"); a[1] != int64(5) || a[3] != "s1" {
		t.Errorf("explicit cursor: %v", a)
	}
	if a := run(); a[1] != int64(0) || a[3] != "" {
		t.Errorf("none: %v", a)
	}
	t.Setenv(hubclient.EnvSession, "envseat")
	if a := run(); a[1] != int64(-1) || a[3] != "envseat" {
		t.Errorf("env: %v", a)
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
	f := &fakeHub{all: []hubclient.Event{{Seq: 9, Type: "sandbox.died", Subject: id.String(), Payload: json.RawMessage(`{"cause":"host_oom"}`)}}}
	useFakeHub(t, f, root)
	var so bytes.Buffer
	out := NewOutput(&so, io.Discard, false)
	if err := runHub(context.Background(), []string{"ps"}, out); err != nil {
		t.Fatal(err)
	}
	s := so.String()
	for _, w := range []string{id.String(), "sandbox.died", "host_oom", "9"} {
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
