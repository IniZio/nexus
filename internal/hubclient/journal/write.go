//go:build linux

// Package journal writes hub events to the systemd journal as CloudEvents fields.
package journal

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/IniZio/nexus/internal/hubclient"
)

// SocketPath is the native journal socket; tests override it.
var SocketPath = "/run/systemd/journal/socket"

// Emit writes ev to the journal. It returns nil when the journal socket is absent.
func Emit(ctx context.Context, ev hubclient.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := ev.ID
	if id == "" {
		id = newID()
	}
	ts := time.Now()
	if ev.TS != 0 {
		ts = time.UnixMilli(ev.TS)
	}
	source := ev.Actor
	if source == "" {
		source = "nexus"
	}
	msg := string(ev.Payload)
	if msg == "" {
		msg = "{}"
	}
	fields := [][2]string{
		{hubclient.CE_SPECVERSION, hubclient.CESpecVersion},
		{hubclient.CE_ID, id},
		{hubclient.CE_SOURCE, source},
		{hubclient.CE_TYPE, ev.Type},
		{hubclient.CE_SUBJECT, ev.Topic},
		{hubclient.CE_TIME, ts.UTC().Format(time.RFC3339Nano)},
		{hubclient.NEXUS_SANDBOX, ev.Subject},
		{hubclient.SYSLOG_IDENTIFIER, hubclient.SyslogIdentifier},
		{"MESSAGE", msg},
	}
	if ev.DataContentType != "" {
		fields = append(fields, [2]string{hubclient.CE_DATACONTENTTYPE, ev.DataContentType})
	}
	var buf []byte
	for _, f := range fields {
		buf = appendField(buf, f[0], f[1])
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: SocketPath, Net: "unixgram"})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) ||
			errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EACCES) {
			return nil
		}
		return err
	}
	defer conn.Close()
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("journal: write: %w", err)
	}
	return nil
}

// appendField uses the native binary-safe framing: NAME\n<le64 len><value>\n.
func appendField(buf []byte, name, value string) []byte {
	buf = append(buf, name...)
	buf = append(buf, '\n')
	buf = binary.LittleEndian.AppendUint64(buf, uint64(len(value)))
	buf = append(buf, value...)
	return append(buf, '\n')
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
