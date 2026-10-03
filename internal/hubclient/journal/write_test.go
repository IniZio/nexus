//go:build linux

package journal

import (
	"context"
	"encoding/binary"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/hubclient"
)

func decode(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	for len(b) > 0 {
		i := 0
		for i < len(b) && b[i] != '\n' {
			i++
		}
		name := string(b[:i])
		b = b[i+1:]
		n := binary.LittleEndian.Uint64(b[:8])
		out[name] = string(b[8 : 8+n])
		b = b[8+n+1:]
	}
	return out
}

func TestEmitFields(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "j.sock")
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	old := SocketPath
	SocketPath = sock
	defer func() { SocketPath = old }()

	ev := hubclient.Event{TS: 1700000000123, Topic: "sandbox/x", Type: hubclient.TypeSandboxDied,
		Actor: "supervisor", Subject: "sb-1", Payload: []byte("{\n  \"a\": 1\n}")}
	if err := Emit(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65536)
	_ = l.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := l.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	f := decode(t, buf[:n])
	want := map[string]string{
		"CE_SPECVERSION": "1.0", "CE_SOURCE": "supervisor", "CE_TYPE": "sandbox.died",
		"CE_SUBJECT": "sandbox/x", "NEXUS_SANDBOX": "sb-1", "SYSLOG_IDENTIFIER": "nexus-hub",
		"MESSAGE": "{\n  \"a\": 1\n}", "CE_TIME": "2023-11-14T22:13:20.123Z",
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %q, want %q", k, f[k], v)
		}
	}
	if len(f["CE_ID"]) != 36 {
		t.Errorf("CE_ID = %q", f["CE_ID"])
	}
}

func TestEmitMissingSocketNoop(t *testing.T) {
	old := SocketPath
	SocketPath = filepath.Join(t.TempDir(), "absent")
	defer func() { SocketPath = old }()
	if err := Emit(context.Background(), hubclient.Event{Type: "x"}); err != nil {
		t.Fatalf("err = %v", err)
	}
}
