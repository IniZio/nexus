package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/hubclient"
	"github.com/IniZio/nexus/internal/hubclient/journal"
)

func init() {
	Register(Command{
		Name:    "hub",
		Summary: "Session hub event log (watch, ps, emit)",
		Run:     runHub,
	})
}

const hubUsage = "usage: nexus hub watch [--topic T] [--seat S] [--cursor C] | ps | emit binary-installed --path P --version V --agent-hash H | hello [--pid N] [--seat S] [--kind K] [--agent A] [--cwd C] [--shell] | seat take <seat> | seats [--json] | send <seat> [text|-] | inbox --seat S [--urgent] [--since-cursor] [--ack] | ack --seat S [--cursor C] [--mail id[,id...]] | digest --seat S | heartbeat [--session ID]"

// hubAPI is the subset of the hub client the command needs.
type hubAPI interface {
	Emit(ctx context.Context, ev hubclient.Event) error
	Last(ctx context.Context, subject string) (*hubclient.Event, error)
	LastAll(ctx context.Context) ([]hubclient.Event, error)
	Read(ctx context.Context, f hubclient.Filter, cursor string) (<-chan hubclient.Item, error)
}

// hubNewClient builds the production client; overridden in tests.
var hubNewClient = func() (hubAPI, error) {
	return hubclient.New(), nil
}

func init() {
	hubclient.RegisterTransport(func() hubclient.Transport { return journalTransport{r: journal.NewReader()} })
}

// journalTransport composes journal.Emit with a journal.Reader.
type journalTransport struct{ r *journal.Reader }

func (journalTransport) Emit(ctx context.Context, ev hubclient.Event) error {
	return journal.Emit(ctx, ev)
}

func (t journalTransport) Read(ctx context.Context, f hubclient.Filter, cursor string) (<-chan hubclient.Item, error) {
	return t.r.Read(ctx, f, cursor)
}

func (t journalTransport) Last(ctx context.Context, subject string) (*hubclient.Event, error) {
	return t.r.Last(ctx, subject)
}

func (t journalTransport) LastAll(ctx context.Context) ([]hubclient.Event, error) {
	return t.r.LastAll(ctx)
}

// hubStoreRoot resolves the sandbox store root; overridden in tests.
var hubStoreRoot = store.DefaultRoot

func runHub(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: hubUsage}
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "watch":
		return runHubWatch(ctx, rest, out)
	case "ps":
		return runHubPS(ctx, rest, out)
	case "emit":
		return runHubEmit(ctx, rest, out)
	case "hello", "seat", "seats", "send", "inbox", "digest", "heartbeat", "ack":
		return runHubSession(ctx, verb, rest, out)
	default:
		return &UsageError{Msg: fmt.Sprintf("hub: unknown subcommand %q; valid: watch, ps, emit, hello, seat, seats, send, inbox, digest, heartbeat, ack", verb)}
	}
}

func runHubWatch(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("hub watch", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	topic := fs.String("topic", "", "topic to watch (empty: all)")
	cursor := fs.String("cursor", "", "resume after this journal cursor (empty: from now)")
	seat := fs.String("seat", "", "also watch seat:<S> direct events")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub watch: " + err.Error()}
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: hubUsage}
	}
	var f hubclient.Filter
	if *topic != "" {
		f.Topics = append(f.Topics, *topic)
	}
	if *seat != "" {
		f.Topics = append(f.Topics, hubclient.SeatTopic(*seat))
	}
	c, err := hubNewClient()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "hub watch: " + err.Error(), Err: err}
	}
	items, err := c.Read(ctx, f, *cursor)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "hub watch: " + err.Error(), Err: err}
	}
	enc := json.NewEncoder(out.Stdout())
	for it := range items {
		wl := hubclient.WatchLine{Kind: hubclient.KindEvent, Event: it.Event}
		if it.Gap {
			wl = hubclient.WatchLine{Kind: hubclient.KindGap}
		}
		if err := enc.Encode(wl); err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "hub watch: " + err.Error(), Err: err}
		}
	}
	return nil
}

type hubPSRow struct {
	ID        string `json:"id"`
	Handle    string `json:"handle"`
	State     string `json:"state"`
	LastID    string `json:"last_id,omitempty"`
	LastType  string `json:"last_type,omitempty"`
	LastTS    int64  `json:"last_ts,omitempty"`
	LastCause string `json:"last_cause,omitempty"`
}

func runHubPS(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("hub ps", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub ps: " + err.Error()}
	}
	root, err := hubStoreRoot()
	if err != nil {
		out.EmitError(ErrCodeInternalError, "hub ps: resolve state directory: "+err.Error())
		return nil
	}
	st, err := store.NewFileStore(root)
	if err != nil {
		out.EmitError(ErrCodeInternalError, "hub ps: open sandbox store: "+err.Error())
		return nil
	}
	sbs, err := st.List(ctx)
	if err != nil {
		out.EmitError(ErrCodeInternalError, "hub ps: list sandboxes: "+err.Error())
		return nil
	}
	c, err := hubNewClient()
	if err != nil {
		out.EmitError(ErrCodeInternalError, "hub ps: "+err.Error())
		return nil
	}
	last := map[string]hubclient.Event{}
	all, err := c.LastAll(ctx)
	if err != nil {
		out.EmitError(ErrCodeInternalError, "hub ps: "+err.Error())
		return nil
	}
	for _, ev := range all {
		last[ev.Subject] = ev
	}
	rows := make([]hubPSRow, 0, len(sbs))
	for i := range sbs {
		sb := &sbs[i]
		row := hubPSRow{ID: sb.ID.String(), Handle: sb.Handle(), State: sb.State.String()}
		ev, ok := last[row.ID]
		if !ok {
			if p, lerr := c.Last(ctx, row.ID); lerr == nil && p != nil {
				ev, ok = *p, true
			}
		}
		if ok {
			row.LastID, row.LastType, row.LastTS = ev.ID, ev.Type, ev.TS
			var p struct {
				Cause string `json:"cause"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil {
				row.LastCause = p.Cause
			}
		}
		rows = append(rows, row)
	}
	if !out.IsJSON() {
		trs := make([][]string, 0, len(rows))
		for _, r := range rows {
			trs = append(trs, []string{r.ID, r.Handle, r.State, r.LastType, r.LastCause, r.LastID})
		}
		fmt.Fprint(out.Stdout(), hubTable([]string{"ID", "HANDLE", "STATE", "LAST EVENT", "CAUSE", "EVENT ID"}, trs))
	}
	out.EmitSuccess("hub.ps", rows, fmt.Sprintf("%d sandboxes", len(rows)))
	return nil
}

func hubTable(headers []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString(strings.Join(headers, "\t") + "\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t") + "\n")
	}
	return b.String()
}

func runHubEmit(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 || args[0] != "binary-installed" {
		return &UsageError{Msg: "hub emit: valid kinds: binary-installed"}
	}
	fs := flag.NewFlagSet("hub emit binary-installed", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	path := fs.String("path", "", "installed binary path")
	version := fs.String("version", "", "binary version")
	agentHash := fs.String("agent-hash", "", "embedded agent hash")
	if err := fs.Parse(args[1:]); err != nil {
		return &UsageError{Msg: "hub emit: " + err.Error()}
	}
	payload, _ := json.Marshal(hubclient.BinaryInstalledPayload{Path: *path, Version: *version, AgentHash: *agentHash})
	ev := hubclient.Event{
		Topic:   hubclient.TopicHost,
		Type:    hubclient.TypeBinaryInstalled,
		Actor:   hubActor(),
		Subject: hubclient.TopicHost,
		Payload: payload,
	}
	c, err := hubNewClient()
	if err == nil {
		err = c.Emit(ctx, ev)
	}
	if err != nil {
		fmt.Fprintf(out.Stderr(), "WARN: hub emit binary.installed failed: %v\n", err)
		return nil
	}
	out.EmitSuccess("hub.emit", ev, "emitted binary.installed")
	return nil
}

func hubActor() string {
	if v := os.Getenv(hubclient.EnvSession); v != "" {
		return v
	}
	return hubclient.ActorAnonymous
}

func hubRepoRoot(cwd string) string {
	b, err := exec.Command("git", "-C", cwd, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return cwd
	}
	dir := strings.TrimSpace(string(b))
	if dir == "" {
		return cwd
	}
	if filepath.Base(dir) == ".git" {
		return filepath.Dir(dir)
	}
	return dir
}
