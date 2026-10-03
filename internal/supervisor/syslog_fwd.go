package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/hubclient"
	"github.com/IniZio/nexus/internal/hubclient/journal"
)

// SyslogFwdVsockPort is the guest vsock port serving forwarded CloudEvents.
// Must match syslogFwdVsockPort in cmd/nexus-agent.
const SyslogFwdVsockPort uint32 = 3003

const (
	syslogFwdMaxLine = 32 << 10
	syslogFwdMaxData = 8 << 10
	syslogFwdDialTO  = 5 * time.Second
	syslogFwdMinBO   = time.Second
	syslogFwdMaxBO   = 30 * time.Second
)

// guestCE is one JSON line from the guest agent's syslog forwarder.
type guestCE struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Subject string `json:"subject"`
	Time    string `json:"time"`

	DataContentType string `json:"datacontenttype"`
	Data            string `json:"data"`
}

type syslogForwarder struct {
	sandbox string
	dial    func(ctx context.Context) (net.Conn, error)
	emit    func(ctx context.Context, ev hubclient.Event) error
	minBO   time.Duration
	maxBO   time.Duration
}

// startSyslogForward receives CloudEvents the guest agent forwards from
// /dev/log and writes them to the host journal tagged NEXUS_SANDBOX=<id>.
func startSyslogForward(ctx context.Context, d driver.GuestDialer, id domain.SandboxID) {
	f := &syslogForwarder{
		sandbox: id.String(),
		dial: func(ctx context.Context) (net.Conn, error) {
			dctx, cancel := context.WithTimeout(ctx, syslogFwdDialTO)
			defer cancel()
			return d.DialGuest(dctx, id, SyslogFwdVsockPort)
		},
		emit:  journal.Emit,
		minBO: syslogFwdMinBO, maxBO: syslogFwdMaxBO,
	}
	go f.run(ctx)
}

func (f *syslogForwarder) run(ctx context.Context) {
	bo := f.minBO
	for ctx.Err() == nil {
		conn, err := f.dial(ctx)
		if err == nil {
			bo = f.minBO
			f.consume(ctx, conn)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(bo):
		}
		if bo *= 2; bo > f.maxBO {
			bo = f.maxBO
		}
	}
}

func (f *syslogForwarder) consume(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	r := bufio.NewReaderSize(conn, 4096)
	for {
		line, err := readBoundedLine(r, syslogFwdMaxLine)
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				slog.Debug("supervisor.syslogfwd.read", "err", err)
			}
			return
		}
		if line == nil {
			continue
		}
		f.handle(ctx, line)
	}
}

// readBoundedLine returns the next line, or nil when the line exceeded max
// (the excess is discarded).
func readBoundedLine(r *bufio.Reader, max int) ([]byte, error) {
	var out []byte
	over := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !over {
			if len(out)+len(chunk) > max {
				over = true
				out = nil
			} else {
				out = append(out, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		if over {
			return nil, nil
		}
		return out, nil
	}
}

func (f *syslogForwarder) handle(ctx context.Context, line []byte) {
	var ce guestCE
	if err := json.Unmarshal(line, &ce); err != nil {
		return
	}
	if ce.ID == "" || ce.Source == "" || ce.Type == "" || len(ce.Data) > syslogFwdMaxData {
		return
	}
	data := ce.Data
	if data == "" {
		data = "{}"
	}
	ev := hubclient.Event{
		ID: ce.ID, Topic: ce.Subject, Type: ce.Type, Actor: ce.Source,
		Subject: f.sandbox, Payload: json.RawMessage(data), DataContentType: ce.DataContentType,
	}
	if t, err := time.Parse(time.RFC3339Nano, ce.Time); err == nil {
		ev.TS = t.UnixMilli()
	}
	ectx, cancel := context.WithTimeout(ctx, hubEmitTimeout)
	defer cancel()
	if err := f.emit(ectx, ev); err != nil {
		slog.Debug("supervisor.syslogfwd.emit", "err", err)
	}
}
