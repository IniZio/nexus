//go:build linux

package journal

import (
	"bufio"
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/hubclient"
)

// JournalctlBin is the journalctl binary; tests override it.
var JournalctlBin = "journalctl"

const (
	dedupeCap   = 4096
	lastAllScan = 20000
	maxLine     = 16 << 20
)

// Reader reads hub events from the per-user journal.
type Reader struct{}

// NewReader returns a Reader.
func NewReader() *Reader { return &Reader{} }

func matchArgs(f hubclient.Filter) []string {
	group := func(field string, vals []string) []string {
		args := []string{hubclient.CE_SPECVERSION + "=" + hubclient.CESpecVersion}
		for _, v := range vals {
			args = append(args, field+"="+v)
		}
		for _, t := range f.Types {
			args = append(args, hubclient.CE_TYPE+"="+t)
		}
		return args
	}
	args := group(hubclient.CE_SUBJECT, f.Topics)
	if len(f.Sandboxes) > 0 {
		args = append(args, "+")
		args = append(args, group(hubclient.NEXUS_SANDBOX, f.Sandboxes)...)
	}
	return args
}

// Read streams events after cursor ("" = from f.Since, else now) until ctx is cancelled.
// A cursor the journal no longer holds yields one Gap item first.
func (r *Reader) Read(ctx context.Context, f hubclient.Filter, cursor string) (<-chan hubclient.Item, error) {
	gap := false
	args := []string{"--user", "-o", "json", "--follow"}
	if cursor == "" {
		if f.Since.IsZero() {
			args = append(args, "-n0")
		} else {
			args = append(args, "--since="+f.Since.UTC().Format("2006-01-02 15:04:05 UTC"))
		}
	} else {
		found, err := cursorExists(ctx, cursor)
		if err != nil {
			return nil, err
		}
		gap = !found
		args = append(args, "--after-cursor="+cursor)
	}
	args = append(args, matchArgs(f)...)
	cmd := exec.CommandContext(ctx, JournalctlBin, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan hubclient.Item)
	go func() {
		defer close(ch)
		defer func() { _ = cmd.Wait() }()
		send := func(it hubclient.Item) bool {
			select {
			case ch <- it:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if gap && !send(hubclient.Item{Gap: true}) {
			return
		}
		seen := newLRU(dedupeCap)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), maxLine)
		for sc.Scan() {
			ev, ok := parseEntry(sc.Bytes())
			if !ok || seen.add(ev.ID) {
				continue
			}
			if !send(hubclient.Item{Event: ev}) {
				return
			}
		}
	}()
	return ch, nil
}

func cursorExists(ctx context.Context, cursor string) (bool, error) {
	out, err := exec.CommandContext(ctx, JournalctlBin, "--user", "--cursor="+cursor, "-n1", "-o", "json", "-q").Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return false, nil
		}
		return false, err
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if ln == "" {
			continue
		}
		var e struct {
			Cursor string `json:"__CURSOR"`
		}
		if json.Unmarshal([]byte(ln), &e) == nil && e.Cursor == cursor {
			return true, nil
		}
	}
	return false, nil
}

// Last returns the newest event whose NEXUS_SANDBOX or CE_SUBJECT is subject.
func (r *Reader) Last(ctx context.Context, subject string) (*hubclient.Event, error) {
	spec := hubclient.CE_SPECVERSION + "=" + hubclient.CESpecVersion
	evs, err := query(ctx, "-n1", spec, hubclient.NEXUS_SANDBOX+"="+subject, "+", spec, hubclient.CE_SUBJECT+"="+subject)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	return &evs[0], nil
}

// LastAll returns the newest event per sandbox subject.
func (r *Reader) LastAll(ctx context.Context) ([]hubclient.Event, error) {
	evs, err := query(ctx, "-n", strconv.Itoa(lastAllScan), hubclient.CE_SPECVERSION+"="+hubclient.CESpecVersion)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var res []hubclient.Event
	for _, ev := range evs {
		if ev.Subject == "" || seen[ev.Subject] {
			continue
		}
		seen[ev.Subject] = true
		res = append(res, ev)
	}
	return res, nil
}

// query runs journalctl newest-first and returns parsed events.
func query(ctx context.Context, extra ...string) ([]hubclient.Event, error) {
	args := append([]string{"--user", "-o", "json", "-r", "-q"}, extra...)
	out, err := exec.CommandContext(ctx, JournalctlBin, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	var evs []hubclient.Event
	for _, ln := range strings.Split(string(out), "\n") {
		if ev, ok := parseEntry([]byte(ln)); ok {
			evs = append(evs, ev)
		}
	}
	return evs, nil
}

// field decodes a journalctl JSON value: string, byte array, or array of
// repeated values (first wins).
func field(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []byte
	if raw[0] == '[' && json.Unmarshal(raw, &bs) == nil {
		return string(bs)
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return field(arr[0])
	}
	return ""
}

func parseEntry(line []byte) (hubclient.Event, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return hubclient.Event{}, false
	}
	if field(m[hubclient.CE_SPECVERSION]) != hubclient.CESpecVersion {
		return hubclient.Event{}, false
	}
	ev := hubclient.Event{
		ID:      field(m[hubclient.CE_ID]),
		Cursor:  field(m["__CURSOR"]),
		Topic:   field(m[hubclient.CE_SUBJECT]),
		Type:    field(m[hubclient.CE_TYPE]),
		Actor:   field(m[hubclient.CE_SOURCE]),
		Subject: field(m[hubclient.NEXUS_SANDBOX]),

		DataContentType: field(m[hubclient.CE_DATACONTENTTYPE]),
	}
	if t, err := time.Parse(time.RFC3339Nano, field(m[hubclient.CE_TIME])); err == nil {
		ev.TS = t.UnixMilli()
	} else if us, err := strconv.ParseInt(field(m["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
		ev.TS = us / 1000
	}
	if msg := field(m["MESSAGE"]); json.Valid([]byte(msg)) {
		ev.Payload = json.RawMessage(msg)
	}
	return ev, true
}

type lru struct {
	cap int
	ll  *list.List
	m   map[string]*list.Element
}

func newLRU(n int) *lru { return &lru{cap: n, ll: list.New(), m: map[string]*list.Element{}} }

// add records id and reports whether it was already present. Empty ids never dedupe.
func (l *lru) add(id string) bool {
	if id == "" {
		return false
	}
	if e, ok := l.m[id]; ok {
		l.ll.MoveToFront(e)
		return true
	}
	l.m[id] = l.ll.PushFront(id)
	if l.ll.Len() > l.cap {
		last := l.ll.Back()
		l.ll.Remove(last)
		delete(l.m, last.Value.(string))
	}
	return false
}
