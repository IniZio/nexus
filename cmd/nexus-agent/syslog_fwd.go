package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdlayher/vsock"
)

// syslogFwdVsockPort is the vsock port the host supervisor dials to receive
// forwarded CloudEvents. Must match supervisor.SyslogFwdVsockPort.
const syslogFwdVsockPort uint32 = 3003

const (
	syslogMaxDatagram = 16 << 10
	syslogMaxData     = 8 << 10
	syslogQueueCap    = 256
	syslogRatePerSec  = 100
	syslogBurst       = 200
	ceSDID            = "ce@32473"
)

// ceEvent is one forwarded CloudEvent, sent as a JSON line.
type ceEvent struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Subject string `json:"subject,omitempty"`
	Time    string `json:"time,omitempty"`
	// DataContentType is the CloudEvents datacontenttype, empty when absent.
	DataContentType string `json:"datacontenttype,omitempty"`
	Data            string `json:"data"`
}

// parseCEEntry parses an RFC 5424 line from /dev/log. Only entries carrying a
// ce@32473 structured-data element with specversion, id, source and type are forwarded;
// everything else (RFC 3164, plain logs, other SD) is dropped, as is any entry
// whose data exceeds syslogMaxData.
func parseCEEntry(line string) (ceEvent, bool) {
	var ev ceEvent
	line = strings.TrimRight(line, "\x00\r\n")
	if len(line) < 4 || line[0] != '<' {
		return ev, false
	}
	gt := strings.IndexByte(line, '>')
	if gt < 0 || gt+2 > len(line) || line[gt+1] != '1' || line[gt+2] != ' ' {
		return ev, false
	}
	rest := line[gt+3:]
	for i := 0; i < 5; i++ { // TIMESTAMP HOSTNAME APP PROCID MSGID
		sp := strings.IndexByte(rest, ' ')
		if sp < 0 {
			return ev, false
		}
		rest = rest[sp+1:]
	}
	params, msg, ok := parseSD(rest)
	if !ok || params == nil {
		return ev, false
	}
	ev = ceEvent{
		ID: params["id"], Source: params["source"], Type: params["type"],
		Subject: params["subject"], Time: params["time"],
		DataContentType: params["datacontenttype"],
		Data:            strings.TrimPrefix(msg, "\xef\xbb\xbf"),
	}
	if params["specversion"] == "" || ev.ID == "" || ev.Source == "" || ev.Type == "" || len(ev.Data) > syslogMaxData {
		return ev, false
	}
	if ev.Data == "" {
		ev.Data = "{}"
	}
	return ev, true
}

// parseJournalEntry parses one native journal datagram: KEY=VALUE\n lines and
// KEY\n<le64 len>VALUE\n binary fields. Only entries with CE_SPECVERSION,
// CE_ID, CE_SOURCE and CE_TYPE are forwarded; MESSAGE is the event data.
func parseJournalEntry(b []byte) (ceEvent, bool) {
	var ev ceEvent
	var spec string
	for len(b) > 0 {
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 {
			return ev, false
		}
		line := b[:nl]
		b = b[nl+1:]
		var key string
		var val []byte
		if eq := bytes.IndexByte(line, '='); eq >= 0 {
			key, val = string(line[:eq]), line[eq+1:]
		} else {
			if len(line) == 0 {
				continue
			}
			if len(b) < 8 {
				return ev, false
			}
			n := binary.LittleEndian.Uint64(b[:8])
			if n > uint64(len(b)-8) {
				return ev, false
			}
			key, val = string(line), b[8:8+n]
			b = b[8+n:]
			if len(b) == 0 || b[0] != '\n' {
				return ev, false
			}
			b = b[1:]
		}
		v := string(val)
		switch key {
		case "CE_SPECVERSION":
			spec = v
		case "CE_ID":
			ev.ID = v
		case "CE_SOURCE":
			ev.Source = v
		case "CE_TYPE":
			ev.Type = v
		case "CE_SUBJECT":
			ev.Subject = v
		case "CE_TIME":
			ev.Time = v
		case "CE_DATACONTENTTYPE":
			ev.DataContentType = v
		case "MESSAGE":
			ev.Data = v
		}
	}
	if spec == "" || ev.ID == "" || ev.Source == "" || ev.Type == "" || len(ev.Data) > syslogMaxData {
		return ev, false
	}
	if ev.Data == "" {
		ev.Data = "{}"
	}
	return ev, true
}

// parseSD consumes the STRUCTURED-DATA field and returns the ce@32473 params
// (nil when absent) and the remaining MSG.
func parseSD(s string) (map[string]string, string, bool) {
	if s == "" {
		return nil, "", false
	}
	if s[0] == '-' {
		return nil, "", true
	}
	var ce map[string]string
	i := 0
	for i < len(s) && s[i] == '[' {
		i++
		start := i
		for i < len(s) && s[i] != ' ' && s[i] != ']' {
			i++
		}
		id := s[start:i]
		params := map[string]string{}
		for i < len(s) && s[i] == ' ' {
			i++
			ns := i
			for i < len(s) && s[i] != '=' {
				i++
			}
			if i+1 >= len(s) || s[i+1] != '"' {
				return nil, "", false
			}
			name := s[ns:i]
			i += 2
			var val strings.Builder
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\' || s[i+1] == ']') {
					val.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == '"' {
					closed = true
					i++
					break
				}
				val.WriteByte(c)
				i++
			}
			if !closed {
				return nil, "", false
			}
			params[name] = val.String()
		}
		if i >= len(s) || s[i] != ']' {
			return nil, "", false
		}
		i++
		if id == ceSDID {
			ce = params
		}
	}
	return ce, strings.TrimPrefix(s[i:], " "), true
}

// ceForwarder queues parsed events for the host connection. Producers never
// block: a full queue or exhausted rate budget drops the newest entry.
type ceForwarder struct {
	mu      sync.Mutex
	q       chan ceEvent
	dropped atomic.Int64
	tokens  float64
	last    time.Time
	now     func() time.Time
}

func newCEForwarder() *ceForwarder {
	return &ceForwarder{
		q: make(chan ceEvent, syslogQueueCap), tokens: syslogBurst,
		now: time.Now,
	}
}

// offer queues an RFC 5424 datagram from /dev/log.
func (f *ceForwarder) offer(datagram string) {
	if ev, ok := parseCEEntry(datagram); ok {
		f.enqueue(ev)
	}
}

// offerJournal queues a native journal datagram.
func (f *ceForwarder) offerJournal(datagram string) {
	if ev, ok := parseJournalEntry([]byte(datagram)); ok {
		f.enqueue(ev)
	}
}

func (f *ceForwarder) enqueue(ev ceEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.now()
	if !f.last.IsZero() {
		f.tokens += t.Sub(f.last).Seconds() * syslogRatePerSec
		if f.tokens > syslogBurst {
			f.tokens = syslogBurst
		}
	}
	f.last = t
	if f.tokens < 1 {
		f.dropped.Add(1)
		return
	}
	select {
	case f.q <- ev:
		f.tokens--
	default:
		f.dropped.Add(1)
	}
}

// serve writes queued events to conn as JSON lines until it fails or ctx ends.
func (f *ceForwarder) serve(ctx context.Context, conn net.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()
	enc := json.NewEncoder(conn)
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-f.q:
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := enc.Encode(ev); err != nil {
				return
			}
		}
	}
}

// startSyslogForward binds /dev/log and the vsock channel. Failures are logged
// and non-fatal.
func startSyslogForward(ctx context.Context, con *os.File) {
	f := newCEForwarder()
	lis, err := vsock.Listen(syslogFwdVsockPort, nil)
	if err != nil {
		consoleLog(con, "nexus-agent: syslog-fwd: vsock.Listen %d: %v\n", syslogFwdVsockPort, err)
		return
	}
	go func() {
		<-ctx.Done()
		lis.Close()
	}()
	if err := listenDevLog(ctx, f.offer); err != nil {
		consoleLog(con, "nexus-agent: syslog-fwd: /dev/log: %v\n", err)
	}
	if err := listenJournal(ctx, f.offerJournal); err != nil {
		consoleLog(con, "nexus-agent: syslog-fwd: journal socket: %v\n", err)
	}
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			f.serve(ctx, c)
		}
	}()
}
