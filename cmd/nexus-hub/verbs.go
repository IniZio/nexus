package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/hub"
	"github.com/IniZio/nexus/internal/hubclient"
)

type usageError string

func (u usageError) Error() string { return string(u) }

// version is the nexus-hub build version, set via -ldflags.
var version = "dev"

const watchPoll = 100 * time.Millisecond

func dispatch(ctx context.Context, dbFile, verb string, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var subject, topic, seat string
	var cursor int64
	var ack bool
	switch verb {
	case hubclient.VerbLast:
		fs.StringVar(&subject, "subject", "", "")
	case hubclient.VerbWatch:
		fs.StringVar(&topic, "topic", "", "")
		fs.Int64Var(&cursor, "cursor", 0, "")
		fs.BoolVar(&ack, "ack", false, "")
		fs.StringVar(&seat, "seat", "", "")
		fs.StringVar(&seat, "consumer", "", "")
	case hubclient.VerbAppend, hubclient.VerbLastAll, hubclient.VerbVersion:
	default:
		return usageError("unknown verb " + strconv.Quote(verb))
	}
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	enc := json.NewEncoder(stdout)

	if verb == hubclient.VerbVersion {
		return enc.Encode(map[string]any{"version": version, "protocol": hubclient.ProtocolVersion})
	}
	if verb == hubclient.VerbLast && subject == "" {
		return usageError("last: --subject required")
	}
	if verb == hubclient.VerbWatch && topic == "" {
		return usageError("watch: --topic required")
	}

	h, err := hub.Open(dbFile)
	if err != nil {
		return err
	}
	defer h.Close()

	switch verb {
	case hubclient.VerbAppend:
		var e hubclient.Event
		if err := json.NewDecoder(stdin).Decode(&e); err != nil {
			return fmt.Errorf("append: decode event: %w", err)
		}
		seq, err := h.Append(ctx, e)
		if err != nil {
			return err
		}
		return enc.Encode(hubclient.AppendResult{Seq: seq})
	case hubclient.VerbLast:
		e, ok, err := h.LastBySubject(ctx, subject)
		if err != nil {
			return err
		}
		if !ok {
			return enc.Encode(nil)
		}
		return enc.Encode(e)
	case hubclient.VerbLastAll:
		evs, err := h.LastAll(ctx)
		if err != nil {
			return err
		}
		for _, e := range evs {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	default:
		cursorSet := false
		fs.Visit(func(f *flag.Flag) { cursorSet = cursorSet || f.Name == "cursor" })
		if seat != "" && !cursorSet {
			if cursor, err = h.Cursor(ctx, seat, topic); err != nil {
				return err
			}
		}
		return watch(ctx, h, enc, topic, seat, cursor, ack, stdin)
	}
}

// watch streams events with seq > cursor as JSONL. With ack, each stdin line is
// an integer seq (or {"seq":n}) recorded as the seat's cursor; EOF on stdin
// does not end the watch. seat defaults to $NEXUS_HUB_SESSION, then anonymous.
func watch(ctx context.Context, h *hub.Hub, enc *json.Encoder, topic, seat string, cursor int64, ack bool, stdin io.Reader) error {
	if seat == "" {
		seat = os.Getenv(hubclient.EnvSession)
	}
	if seat == "" {
		seat = hubclient.ActorAnonymous
	}
	if ack {
		go func() {
			sc := bufio.NewScanner(stdin)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				n, err := strconv.ParseInt(line, 10, 64)
				if err != nil {
					var a struct {
						Seq int64 `json:"seq"`
						Ack int64 `json:"ack"`
					}
					if json.Unmarshal([]byte(line), &a) != nil {
						continue
					}
					n = a.Seq
					if a.Ack != 0 {
						n = a.Ack
					}
				}
				_ = h.Ack(ctx, seat, topic, n)
			}
		}()
	}
	_, _ = h.Prune(ctx, hub.RetainAge, hub.RetainRows)
	tick := time.NewTicker(watchPoll)
	defer tick.Stop()
	for {
		evs, err := h.Deliver(ctx, topic, cursor, 500)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, l := range evs {
			if err := enc.Encode(l); err != nil {
				return err
			}
			if l.Kind == hubclient.KindGap {
				cursor = l.To
			} else {
				cursor = l.Seq
			}
		}
		if len(evs) >= 500 {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
