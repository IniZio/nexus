package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/hubclient"
)

func init() {
	Register(Command{
		Name:    "hub",
		Summary: "Session hub event log (watch, ps, emit)",
		Run:     runHub,
	})
}

const hubUsage = "usage: nexus hub watch [--topic T] [--cursor N] [--seat S] [--ack] | ps | emit binary-installed --path P --version V --agent-hash H"

// hubAPI is the subset of the hub client the command needs.
type hubAPI interface {
	Emit(ctx context.Context, ev hubclient.Event) error
	Last(ctx context.Context, subject string) (*hubclient.Event, error)
	LastAll(ctx context.Context) ([]hubclient.Event, error)
	Watch(ctx context.Context, topic string, cursor int64, ack bool, seat string, stdout io.Writer) error
}

// hubNewClient builds the production client; overridden in tests.
var hubNewClient = func() (hubAPI, error) {
	return clientHub{c: hubclient.New()}, nil
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
	default:
		return &UsageError{Msg: fmt.Sprintf("hub: unknown subcommand %q; valid: watch, ps, emit", verb)}
	}
}

func runHubWatch(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("hub watch", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	topic := fs.String("topic", "", "topic to watch (empty: all)")
	cursor := fs.Int64("cursor", 0, "resume after this seq")
	ack := fs.Bool("ack", false, "acknowledge delivered events")
	seat := fs.String("seat", "", "consumer name; resumes from its stored cursor (default $NEXUS_HUB_SESSION)")
	fs.StringVar(seat, "consumer", "", "alias for --seat")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "hub watch: " + err.Error()}
	}
	cursorSet := false
	fs.Visit(func(f *flag.Flag) { cursorSet = cursorSet || f.Name == "cursor" })
	if *seat == "" {
		*seat = os.Getenv(hubclient.EnvSession)
	}
	if *seat != "" && !cursorSet {
		*cursor = -1
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: hubUsage}
	}
	c, err := hubNewClient()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "hub watch: " + err.Error(), Err: err}
	}
	if err := c.Watch(ctx, *topic, *cursor, *ack, *seat, out.Stdout()); err != nil && ctx.Err() == nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "hub watch: " + err.Error(), Err: err}
	}
	return nil
}

type hubPSRow struct {
	ID        string `json:"id"`
	Handle    string `json:"handle"`
	State     string `json:"state"`
	LastSeq   int64  `json:"last_seq,omitempty"`
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
			// LastAll may be limited; fall back to a point lookup.
			if p, lerr := c.Last(ctx, row.ID); lerr == nil && p != nil {
				ev, ok = *p, true
			}
		}
		if ok {
			row.LastSeq, row.LastType, row.LastTS = ev.Seq, ev.Type, ev.TS
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
			trs = append(trs, []string{r.ID, r.Handle, r.State, r.LastType, r.LastCause, strconv.FormatInt(r.LastSeq, 10)})
		}
		fmt.Fprint(out.Stdout(), hubTable([]string{"ID", "HANDLE", "STATE", "LAST EVENT", "CAUSE", "SEQ"}, trs))
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

// clientHub adapts hubclient.Client to hubAPI.
type clientHub struct{ c *hubclient.Client }

func (h clientHub) Emit(ctx context.Context, ev hubclient.Event) error {
	_, err := h.c.Append(ctx, ev)
	return err
}

func (h clientHub) Last(ctx context.Context, subject string) (*hubclient.Event, error) {
	return h.c.Last(ctx, subject)
}

func (h clientHub) LastAll(ctx context.Context) ([]hubclient.Event, error) {
	return h.c.LastAll(ctx)
}

func (h clientHub) Watch(ctx context.Context, topic string, cursor int64, ack bool, seat string, stdout io.Writer) error {
	w, err := h.c.Watch(ctx, topic, cursor, ack, seat)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	for wl := range w.Lines {
		if err := enc.Encode(wl); err != nil {
			return err
		}
		if ack && wl.Kind == hubclient.KindEvent {
			if err := w.Ack(wl.Seq); err != nil {
				return err
			}
		}
	}
	return w.Wait()
}
