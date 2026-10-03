package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/hubclient"
	"github.com/IniZio/nexus/internal/hubstate"
)

func hubSessionEnv(t *testing.T, f *fakeHub) *hubstate.Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(hubclient.EnvSeat, "")
	t.Setenv(hubclient.EnvSession, "")
	useFakeHub(t, f, t.TempDir())
	st, err := hubstate.Open("")
	if err != nil {
		t.Skipf("hubstate unavailable: %v", err)
	}
	return st
}

func runHubT(t *testing.T, args ...string) string {
	t.Helper()
	var so bytes.Buffer
	if err := runHub(context.Background(), args, NewOutput(&so, io.Discard, false)); err != nil {
		t.Fatalf("hub %v: %v", args, err)
	}
	return so.String()
}

func gitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	return dir
}

func TestHubHelloJSONAndRepoKeyedSeat(t *testing.T) {
	hubSessionEnv(t, &fakeHub{})
	repo := gitRepo(t, "myrepo")
	pid := os.Getpid()
	var res struct {
		Session string `json:"session"`
		Seat    string `json:"seat"`
		Sub     bool   `json:"sub"`
		Digest  string `json:"digest"`
	}
	raw := runHubT(t, "hello", "--pid", itoa(pid), "--cwd", filepath.Join(repo, "sub"))
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("%q: %v", raw, err)
	}
	if res.Seat != "myrepo" || res.Sub || res.Session == "" {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Digest, "nexus hub digest for myrepo") {
		t.Errorf("digest = %q", res.Digest)
	}
}

func TestHubHelloShellAndExplicitSeat(t *testing.T) {
	hubSessionEnv(t, &fakeHub{})
	got := runHubT(t, "hello", "--pid", itoa(os.Getpid()), "--seat", "it's", "--shell", "--cwd", t.TempDir())
	if !strings.HasPrefix(got, "export NEXUS_HUB_SESSION='") || !strings.Contains(got, ` NEXUS_HUB_SEAT='it'\''s'`) {
		t.Errorf("shell = %q", got)
	}
	var rows []struct {
		Seat     string `json:"seat"`
		Live     bool   `json:"live"`
		Explicit bool   `json:"explicit"`
	}
	if err := json.Unmarshal([]byte(runHubT(t, "seats", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Seat != "it's" || !rows[0].Live || !rows[0].Explicit {
		t.Errorf("rows = %+v", rows)
	}
}

func inboxFake() *fakeHub {
	return &fakeHub{items: []hubclient.Item{
		{Event: hubclient.Event{ID: "e1", Cursor: "c1", Topic: "host", Type: hubclient.TypeBinaryInstalled}},
		{Event: hubclient.Event{ID: "e2", Cursor: "c2", Topic: "seat:alpha", Type: hubclient.TypeMessage, Payload: json.RawMessage(`{"to":"alpha","from":"b","text":"hi"}`)}},
		{Event: hubclient.Event{ID: "e3", Cursor: "c3", Topic: "host", Type: hubclient.TypeBinaryInstalled}},
	}}
}

func TestHubInboxUrgentOnlyAndCursorOnlyWithAck(t *testing.T) {
	st := hubSessionEnv(t, inboxFake())
	lines := func(s string) []hubclient.WatchLine {
		var out []hubclient.WatchLine
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			if l == "" {
				continue
			}
			var wl hubclient.WatchLine
			if err := json.Unmarshal([]byte(l), &wl); err != nil {
				t.Fatal(err)
			}
			out = append(out, wl)
		}
		return out
	}
	all := lines(runHubT(t, "inbox", "--seat", "alpha"))
	if len(all) != 3 {
		t.Fatalf("all = %d lines", len(all))
	}
	urg := lines(runHubT(t, "inbox", "--seat", "alpha", "--urgent"))
	if len(urg) != 1 || urg[0].ID != "e2" || urg[0].Cursor != "c2" || urg[0].Kind != hubclient.KindEvent {
		t.Fatalf("urgent = %+v", urg)
	}
	if c, _ := st.Cursor("alpha"); c != "" {
		t.Errorf("cursor moved without --ack: %q", c)
	}
	runHubT(t, "inbox", "--seat", "alpha", "--urgent", "--ack")
	if c, _ := st.Cursor("alpha"); c != "" {
		t.Errorf("urgent ack moved plain cursor: %q", c)
	}
	if c, _ := st.UrgentCursor("alpha"); c != "c3" {
		t.Errorf("urgent cursor = %q, want c3", c)
	}
	if all := lines(runHubT(t, "inbox", "--seat", "alpha")); len(all) != 3 {
		t.Errorf("plain inbox after urgent ack = %d lines, want 3", len(all))
	}
	runHubT(t, "inbox", "--seat", "alpha", "--ack")
	if c, _ := st.Cursor("alpha"); c != "c3" {
		t.Errorf("ack cursor = %q, want c3", c)
	}
}

func TestHubSendWritesMailAndEmits(t *testing.T) {
	f := &fakeHub{}
	st := hubSessionEnv(t, f)
	old := hubStdin
	t.Cleanup(func() { hubStdin = old })
	hubStdin = strings.NewReader("hello there\n")
	var res struct{ ID string }
	if err := json.Unmarshal([]byte(runHubT(t, "send", "beta", "-")), &res); err != nil || res.ID == "" {
		t.Fatalf("id: %v %q", err, res.ID)
	}
	mails, _ := st.ListMail("beta")
	if len(mails) != 1 || mails[0].Text != "hello there" || mails[0].ID != res.ID {
		t.Errorf("mails = %+v", mails)
	}
	if len(f.emitted) != 1 || f.emitted[0].Type != hubclient.TypeMessage || f.emitted[0].Topic != "seat:beta" {
		t.Errorf("emitted = %+v", f.emitted)
	}
}

func TestHubAckPersistsCursor(t *testing.T) {
	st := hubSessionEnv(t, &fakeHub{})
	runHubT(t, "ack", "--seat", "alpha", "--cursor", "s=abc;i=1")
	if c, _ := st.Cursor("alpha"); c != "s=abc;i=1" {
		t.Errorf("cursor = %q", c)
	}
}

func TestHubAckMailDeletesMailFiles(t *testing.T) {
	st := hubSessionEnv(t, &fakeHub{})
	old := hubStdin
	t.Cleanup(func() { hubStdin = old })
	hubStdin = strings.NewReader("ping\n")
	var res struct{ ID string }
	if err := json.Unmarshal([]byte(runHubT(t, "send", "alpha", "-")), &res); err != nil || res.ID == "" {
		t.Fatalf("send: %v", err)
	}
	var line hubclient.WatchLine
	first := runHubT(t, "inbox", "--seat", "alpha", "--since-cursor")
	if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &line); err != nil || line.ID != res.ID {
		t.Fatalf("inbox line = %q (%v), want id %s", first, err, res.ID)
	}
	if again := runHubT(t, "inbox", "--seat", "alpha", "--since-cursor"); !strings.Contains(again, res.ID) {
		t.Fatalf("mail gone without ack: %q", again)
	}
	runHubT(t, "ack", "--seat", "alpha", "--cursor", "c1", "--mail", res.ID)
	if after := runHubT(t, "inbox", "--seat", "alpha", "--since-cursor"); strings.Contains(after, res.ID) {
		t.Errorf("mail redelivered after ack --mail: %q", after)
	}
	if mails, _ := st.ListMail("alpha"); len(mails) != 0 {
		t.Errorf("mail files left: %+v", mails)
	}
}

func TestHubOwnerSeatLabelRoutesSandboxEvents(t *testing.T) {
	f := &fakeHub{}
	hubSessionEnv(t, f)
	root := t.TempDir()
	useFakeHub(t, f, root)
	fst, err := store.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	owned := domain.NewSandboxID()
	other := domain.NewSandboxID()
	for id, labels := range map[domain.SandboxID]map[string]string{
		owned: {hubclient.LabelOwnerSeat: "alpha"},
		other: {hubclient.LabelOwnerSeat: "zed"},
	} {
		sb := domain.Sandbox{ID: id, Name: id.String()[:6], Project: "p", State: domain.Stopped, Labels: labels}
		if err := fst.Create(context.Background(), sb); err != nil {
			t.Fatal(err)
		}
	}
	f.items = []hubclient.Item{{Event: hubclient.Event{
		ID: "d1", Cursor: "c9", Topic: hubclient.SandboxTopic(owned.String()), Type: hubclient.TypeSandboxDied,
		Payload: json.RawMessage(`{"id":"x"}`),
	}}}
	got := runHubT(t, "inbox", "--seat", "alpha", "--urgent")
	if !strings.Contains(got, `"d1"`) {
		t.Errorf("owned sandbox death not routed: %q", got)
	}
	if len(f.readFilter.Sandboxes) != 1 || f.readFilter.Sandboxes[0] != owned.String() {
		t.Errorf("filter sandboxes = %v, want [%s]", f.readFilter.Sandboxes, owned)
	}
}

func TestHubSendPositionalText(t *testing.T) {
	f := &fakeHub{}
	st := hubSessionEnv(t, f)
	runHubT(t, "send", "beta", "ping")
	mails, _ := st.ListMail("beta")
	if len(mails) != 1 || mails[0].Text != "ping" {
		t.Errorf("mails = %+v", mails)
	}
}

func TestHubSeatTake(t *testing.T) {
	f := &fakeHub{}
	st := hubSessionEnv(t, f)
	var so bytes.Buffer
	err := runHub(context.Background(), []string{"seat", "take", "s1"}, NewOutput(&so, io.Discard, false))
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("no session: err = %v, want UsageError", err)
	}
	me, err := hubstate.ProcOf(os.Getpid())
	if err != nil {
		t.Skip(err)
	}
	if _, err := st.Claim(hubstate.ClaimReq{Seat: "s1", SessionID: "old", Proc: me}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterSession(hubstate.Session{ID: "old", Seat: "s1", Proc: me}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(hubclient.EnvSession, "new")
	var res struct{ Seat, From, To string }
	if err := json.Unmarshal([]byte(runHubT(t, "seat", "take", "s1")), &res); err != nil {
		t.Fatal(err)
	}
	if res.From != "old" || res.To != "new" {
		t.Errorf("res = %+v", res)
	}
	if s, _ := st.GetSeat("s1"); s.Occupant.SessionID != "new" || !s.Explicit {
		t.Errorf("seat = %+v", s)
	}
	if len(f.emitted) != 1 || f.emitted[0].Type != hubclient.TypeSeatTaken {
		t.Errorf("emitted = %+v", f.emitted)
	}
}

func TestHubDigestVerb(t *testing.T) {
	hubSessionEnv(t, &fakeHub{})
	got := runHubT(t, "digest", "--seat", "alpha")
	if !strings.Contains(got, "nexus hub digest for alpha") || !strings.Contains(got, "unread messages: none") {
		t.Errorf("digest = %q", got)
	}
}

func TestHubHeartbeat(t *testing.T) {
	st := hubSessionEnv(t, &fakeHub{})
	if err := st.RegisterSession(hubstate.Session{ID: "sess1", Seat: "alpha"}); err != nil {
		t.Fatal(err)
	}
	runHubT(t, "heartbeat", "--session", "sess1")
	var ue *UsageError
	if err := runHub(context.Background(), []string{"heartbeat"}, NewOutput(io.Discard, io.Discard, false)); !errors.As(err, &ue) {
		t.Errorf("no session: err = %v, want UsageError", err)
	}
}

func TestHubInboxNoCursorStartsAtSeatTaken(t *testing.T) {
	f := &fakeHub{}
	st := hubSessionEnv(t, f)
	runHubT(t, "hello", "--pid", itoa(os.Getpid()), "--seat", "alpha", "--cwd", t.TempDir())
	runHubT(t, "inbox", "--seat", "alpha")
	seat, err := st.GetSeat("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if f.readCursor != "" || f.readFilter.Since.IsZero() || !f.readFilter.Since.Equal(seat.Taken) {
		t.Errorf("cursor=%q since=%v, want since=%v", f.readCursor, f.readFilter.Since, seat.Taken)
	}
	if err := st.AckMail("alpha", "c9", nil); err != nil {
		t.Fatal(err)
	}
	runHubT(t, "inbox", "--seat", "alpha")
	if f.readCursor != "c9" || !f.readFilter.Since.IsZero() {
		t.Errorf("with cursor: cursor=%q since=%v", f.readCursor, f.readFilter.Since)
	}
}

func TestHubAckMailOnlyKeepsCursor(t *testing.T) {
	st := hubSessionEnv(t, &fakeHub{})
	if err := st.AckMail("alpha", "keep", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMail(hubstate.Mail{ID: "m1", To: "alpha", From: "b", Text: "x", TS: 1}); err != nil {
		t.Fatal(err)
	}
	runHubT(t, "ack", "--seat", "alpha", "--mail", "m1")
	if c, _ := st.Cursor("alpha"); c != "keep" {
		t.Errorf("cursor = %q, want keep", c)
	}
	if mails, _ := st.ListMail("alpha"); len(mails) != 0 {
		t.Errorf("mail left: %+v", mails)
	}
	err := runHub(context.Background(), []string{"ack", "--seat", "alpha"}, NewOutput(io.Discard, io.Discard, false))
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("ack with neither flag: %v, want UsageError", err)
	}
}
