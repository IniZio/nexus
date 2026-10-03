package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/hubclient"
	"github.com/IniZio/nexus/internal/hubstate"
)

// hubStateOpen opens the hub coordination state; overridden in tests.
var hubStateOpen = func() (*hubstate.Store, error) { return hubstate.Open("") }

// hubStdin is the stdin `send` reads; overridden in tests.
var hubStdin io.Reader = os.Stdin

func hubSessionErr(verb string, err error) error {
	return &CodedError{Code: ErrCodeInternalError, Msg: "hub " + verb + ": " + err.Error(), Err: err}
}

func runHubSession(ctx context.Context, verb string, args []string, out *Output) error {
	switch verb {
	case "hello":
		return runHubHello(ctx, args, out)
	case "seat":
		return runHubSeatTake(ctx, args, out)
	case "seats":
		return runHubSeats(args, out)
	case "send":
		return runHubSend(ctx, args, out)
	case "inbox":
		return runHubInbox(ctx, args, out)
	case "ack":
		return runHubAck(args, out)
	case "digest":
		return runHubDigest(ctx, args, out)
	case "heartbeat":
		return runHubHeartbeat(args, out)
	}
	return &UsageError{Msg: hubUsage}
}

// hubAgentPID is the process a session is bound to: the nearest agent
// ancestor of this process, else its parent.
func hubAgentPID() int {
	if pid, _, err := hubclient.FindAgentAncestor(os.Getpid(), nil); err == nil && pid > 0 {
		return pid
	}
	return os.Getppid()
}

func newHubSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}

func runHubHello(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("hub hello", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	pid := fs.Int("pid", 0, "pid of the registering process (default: agent ancestor)")
	seat := fs.String("seat", "", "explicit seat")
	kind := fs.String("kind", "", "session kind")
	agent := fs.String("agent", "", "agent name")
	cwd := fs.String("cwd", "", "working directory (default: current)")
	shell := fs.Bool("shell", false, "print export lines instead of JSON")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub hello: " + err.Error()}
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "usage: nexus hub hello [--pid N] [--seat S] [--kind K] [--agent A] [--cwd C] [--shell]"}
	}
	if *pid <= 0 {
		*pid = hubAgentPID()
	}
	if *cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return hubSessionErr("hello", err)
		}
		*cwd = wd
	}
	st, err := hubStateOpen()
	if err != nil {
		return hubSessionErr("hello", err)
	}
	proc, err := hubstate.ProcOf(*pid)
	if err != nil {
		return hubSessionErr("hello", err)
	}
	root := hubRepoRoot(*cwd)
	want, explicit := *seat, *seat != ""
	if !explicit {
		want = filepath.Base(root)
	}
	sid := newHubSessionID()
	if prev := os.Getenv(hubclient.EnvSession); prev != "" {
		if s, lerr := st.LookupSession(prev); lerr == nil && s.Proc == proc {
			sid = prev
		}
	}
	res, err := st.Claim(hubstate.ClaimReq{Seat: want, Explicit: explicit, SessionID: sid, Proc: proc})
	if err != nil {
		return hubSessionErr("hello", err)
	}
	host, _ := os.Hostname()
	if err := st.RegisterSession(hubstate.Session{
		ID: sid, Kind: *kind, Agent: *agent, Cwd: *cwd, RepoRoot: root, Seat: res.Seat, Proc: proc, Host: host,
	}); err != nil {
		return hubSessionErr("hello", err)
	}
	if *shell {
		fmt.Fprintf(out.Stdout(), "export %s=%s %s=%s\n", hubclient.EnvSession, shellQuote(sid), hubclient.EnvSeat, shellQuote(res.Seat))
		return nil
	}
	digest := ""
	if mb, merr := hubMailbox(st); merr == nil {
		dseat, o := res.Seat, hubclient.DigestOpts{}
		if res.SubSeat && res.Predecessor != nil && !res.PredecessorLive {
			dseat, o.TakeSeat = want, want
		}
		if d, derr := mb.Digest(ctx, dseat, hubOwnedSandboxes(ctx, dseat), o); derr == nil {
			digest = d
		}
	}
	return json.NewEncoder(out.Stdout()).Encode(struct {
		Session string `json:"session"`
		Seat    string `json:"seat"`
		Sub     bool   `json:"sub"`
		Digest  string `json:"digest"`
	}{sid, res.Seat, res.SubSeat, digest})
}

// hubOwnedSandboxes lists ids of sandboxes labelled owner-seat=<seat>.
func hubOwnedSandboxes(ctx context.Context, seat string) []string {
	root, err := hubStoreRoot()
	if err != nil {
		return nil
	}
	st, err := store.NewFileStore(root)
	if err != nil {
		return nil
	}
	sbs, err := st.GetByLabels(ctx, map[string]string{hubclient.LabelOwnerSeat: seat})
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(sbs))
	for i := range sbs {
		ids = append(ids, sbs[i].ID.String())
	}
	return ids
}

func hubMailbox(st *hubstate.Store) (*hubclient.Mailbox, error) {
	c, err := hubNewClient()
	if err != nil {
		return nil, err
	}
	return &hubclient.Mailbox{
		Store: st, Emitter: c, Reader: c,
		Sandboxes: func(seat string) []string { return hubOwnedSandboxes(context.Background(), seat) },
		SeatStart: func(seat string) time.Time {
			s, err := st.GetSeat(seat)
			if err != nil {
				return time.Time{}
			}
			return s.Taken
		},
	}, nil
}

func runHubSeatTake(ctx context.Context, args []string, out *Output) error {
	if len(args) != 2 || args[0] != "take" {
		return &UsageError{Msg: "usage: nexus hub seat take <seat>"}
	}
	name := args[1]
	st, err := hubStateOpen()
	if err != nil {
		return hubSessionErr("seat", err)
	}
	proc, err := hubstate.ProcOf(hubAgentPID())
	if err != nil {
		return hubSessionErr("seat", err)
	}
	sid := os.Getenv(hubclient.EnvSession)
	if sid == "" {
		return &UsageError{Msg: "hub seat take: " + hubclient.EnvSession + " is not set (run `nexus hub hello --shell` first)"}
	}
	prev, err := st.Take(name, sid, proc)
	if err != nil {
		return hubSessionErr("seat take", err)
	}
	if c, cerr := hubNewClient(); cerr == nil {
		payload, _ := json.Marshal(hubclient.SeatTakenPayload{Seat: name, From: prev.SessionID, To: sid})
		_ = c.Emit(ctx, hubclient.Event{Topic: hubclient.SeatTopic(name), Type: hubclient.TypeSeatTaken, Actor: hubActor(), Payload: payload})
	}
	return json.NewEncoder(out.Stdout()).Encode(map[string]any{"seat": name, "from": prev.SessionID, "to": sid})
}

func runHubSeats(args []string, out *Output) error {
	fs := flag.NewFlagSet("hub seats", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	fs.Bool("json", true, "JSON output (always)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub seats: " + err.Error()}
	}
	st, err := hubStateOpen()
	if err != nil {
		return hubSessionErr("seats", err)
	}
	seats, err := st.ListSeats()
	if err != nil {
		return hubSessionErr("seats", err)
	}
	type row struct {
		Seat     string `json:"seat"`
		Occupant string `json:"occupant"`
		Live     bool   `json:"live"`
		Explicit bool   `json:"explicit"`
	}
	rows := make([]row, 0, len(seats))
	for _, s := range seats {
		rows = append(rows, row{s.Name, s.Occupant.SessionID, hubstate.Alive(s.Occupant.Proc), s.Explicit})
	}
	return json.NewEncoder(out.Stdout()).Encode(rows)
}

func runHubSend(ctx context.Context, args []string, out *Output) error {
	if len(args) < 1 || len(args) > 2 {
		return &UsageError{Msg: "usage: nexus hub send <seat> [text|-]"}
	}
	to, text := args[0], ""
	if len(args) == 2 {
		text = args[1]
	}
	if len(args) == 1 || text == "-" {
		b, err := io.ReadAll(hubStdin)
		if err != nil {
			return hubSessionErr("send", err)
		}
		text = strings.TrimRight(string(b), "\n")
	}
	if text == "" {
		return &UsageError{Msg: "hub send: empty message"}
	}
	st, err := hubStateOpen()
	if err != nil {
		return hubSessionErr("send", err)
	}
	mb, err := hubMailbox(st)
	if err != nil {
		return hubSessionErr("send", err)
	}
	from := os.Getenv(hubclient.EnvSeat)
	if from == "" {
		from = hubActor()
	}
	id, err := mb.Send(ctx, from, to, text)
	if err != nil {
		return hubSessionErr("send", err)
	}
	return json.NewEncoder(out.Stdout()).Encode(map[string]string{"id": id})
}

func runHubInbox(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("hub inbox", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	seat := fs.String("seat", os.Getenv(hubclient.EnvSeat), "seat to read")
	urgent := fs.Bool("urgent", false, "only urgent events")
	fs.Bool("since-cursor", true, "read after the stored cursor (always)")
	ack := fs.Bool("ack", false, "advance the cursor after printing")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub inbox: " + err.Error()}
	}
	if fs.NArg() != 0 || *seat == "" {
		return &UsageError{Msg: "usage: nexus hub inbox --seat S [--urgent] [--since-cursor] [--ack]"}
	}
	st, err := hubStateOpen()
	if err != nil {
		return hubSessionErr("inbox", err)
	}
	mb, err := hubMailbox(st)
	if err != nil {
		return hubSessionErr("inbox", err)
	}
	if *urgent {
		mb.Store = urgentStore{st}
	}
	topics := []string{hubclient.TopicHost}
	ownedSet := map[string]bool{}
	for _, id := range hubOwnedSandboxes(ctx, *seat) {
		topics = append(topics, hubclient.SandboxTopic(id))
		ownedSet[hubclient.SandboxTopic(id)] = true
	}
	in, err := mb.Inbox(ctx, *seat, topics...)
	if err != nil && !errors.Is(err, hubclient.ErrUnsupported) {
		return hubSessionErr("inbox", err)
	}
	mailAt := map[string]bool{}
	for _, id := range in.MailIDs {
		mailAt[id] = true
	}
	enc := json.NewEncoder(out.Stdout())
	var mailIDs []string
	for _, it := range in.Items {
		wl := hubclient.WatchLine{Kind: hubclient.KindEvent, Event: it.Event}
		if it.Gap {
			wl = hubclient.WatchLine{Kind: hubclient.KindGap}
		} else if *urgent {
			ev := it.Event
			if ownedSet[ev.Topic] {
				ev.Payload = withOwnerSeat(ev.Payload, *seat)
			}
			if !hubclient.IsUrgent(ev, *seat) {
				continue
			}
		}
		if err := enc.Encode(wl); err != nil {
			return hubSessionErr("inbox", err)
		}
		if mailAt[it.Event.ID] {
			mailIDs = append(mailIDs, it.Event.ID)
		}
	}
	if *ack {
		return mb.Ack(*seat, in.Cursor, mailIDs)
	}
	return nil
}

// urgentStore routes the mailbox cursor to the urgent cursor so urgent-only
// acks never consume non-urgent events.
type urgentStore struct{ *hubstate.Store }

func (u urgentStore) Cursor(seat string) (string, error) { return u.Store.UrgentCursor(seat) }
func (u urgentStore) AckMail(seat, cursor string, ids []string) error {
	return u.Store.AckMailUrgent(seat, cursor, ids)
}

// withOwnerSeat sets owner_seat on a payload from an owned sandbox topic,
// since lifecycle emitters do not stamp it.
func withOwnerSeat(raw json.RawMessage, seat string) json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	m["owner_seat"], _ = json.Marshal(seat)
	b, _ := json.Marshal(m)
	return b
}

func runHubAck(args []string, out *Output) error {
	fs := flag.NewFlagSet("hub ack", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	seat := fs.String("seat", os.Getenv(hubclient.EnvSeat), "seat")
	cursor := fs.String("cursor", "", "journal cursor to persist")
	mail := fs.String("mail", "", "comma-separated mail ids to delete")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub ack: " + err.Error()}
	}
	if fs.NArg() != 0 || *seat == "" || (*cursor == "" && *mail == "") {
		return &UsageError{Msg: "usage: nexus hub ack --seat S [--cursor C] [--mail id[,id...]]"}
	}
	var ids []string
	for _, id := range strings.Split(*mail, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	st, err := hubStateOpen()
	if err != nil {
		return hubSessionErr("ack", err)
	}
	if err := st.AckMail(*seat, *cursor, ids); err != nil {
		return hubSessionErr("ack", err)
	}
	return nil
}

func runHubDigest(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("hub digest", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	seat := fs.String("seat", os.Getenv(hubclient.EnvSeat), "seat")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub digest: " + err.Error()}
	}
	if fs.NArg() != 0 || *seat == "" {
		return &UsageError{Msg: "usage: nexus hub digest --seat S"}
	}
	st, err := hubStateOpen()
	if err != nil {
		return hubSessionErr("digest", err)
	}
	mb, err := hubMailbox(st)
	if err != nil {
		return hubSessionErr("digest", err)
	}
	d, err := mb.Digest(ctx, *seat, hubOwnedSandboxes(ctx, *seat), hubclient.DigestOpts{})
	if err != nil {
		return hubSessionErr("digest", err)
	}
	fmt.Fprint(out.Stdout(), d)
	return nil
}

func runHubHeartbeat(args []string, out *Output) error {
	fs := flag.NewFlagSet("hub heartbeat", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	session := fs.String("session", os.Getenv(hubclient.EnvSession), "session id")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub heartbeat: " + err.Error()}
	}
	if fs.NArg() != 0 || *session == "" {
		return &UsageError{Msg: "usage: nexus hub heartbeat [--session ID]"}
	}
	st, err := hubStateOpen()
	if err != nil {
		return hubSessionErr("heartbeat", err)
	}
	if err := st.Heartbeat(*session); err != nil {
		return hubSessionErr("heartbeat", err)
	}
	return nil
}
