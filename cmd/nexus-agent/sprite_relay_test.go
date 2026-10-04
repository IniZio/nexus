package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/driver/sprites/tunnel"
)

type relayRig struct {
	host  *tunnel.HostSession
	addr  net.Addr
	done  chan error
	stdin io.Closer // host->relay pipe writer
	pid   string
}

func startRelay(t *testing.T, direct string) *relayRig {
	t.Helper()
	// relay stdin <- hostW ; relay stdout -> hostR
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	hostConn := tunnel.Join(inW, outR)
	relayConn := tunnel.Join(outW, inR)
	h, err := tunnel.Host(hostConn)
	if err != nil {
		t.Fatal(err)
	}
	pid := filepath.Join(t.TempDir(), "relay.pid")
	o := spriteRelayOpts{
		listen:      "127.0.0.1:0",
		secretHosts: parseHostSet("api.anthropic.com, GitHub.com"),
		pidfile:     pid,
		dial:        func(string) (net.Conn, error) { return net.Dial("tcp", direct) },
	}
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- spriteRelay(o, relayConn, ready) }()
	r := &relayRig{host: h, done: done, stdin: inW, pid: pid}
	select {
	case r.addr = <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("relay not ready")
	}
	t.Cleanup(func() { h.Close(); inW.Close() })
	return r
}

func TestSpriteRelaySecretHostSplicesConnectToHost(t *testing.T) {
	r := startRelay(t, "127.0.0.1:1")
	got := make(chan string, 1)
	go func() {
		s, err := r.host.Accept()
		if err != nil {
			return
		}
		defer s.Close()
		br := bufio.NewReader(s)
		var b strings.Builder
		for !strings.HasSuffix(b.String(), "\r\n\r\n") {
			l, err := br.ReadString('\n')
			b.WriteString(l)
			if err != nil {
				break
			}
		}
		got <- b.String()
		io.WriteString(s, "HTTP/1.1 200 Connection established\r\n\r\nPONG")
	}()
	c, err := net.Dial("tcp", r.addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Choice: the entire client conn incl. the CONNECT line is spliced onto the stream.
	io.WriteString(c, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
	select {
	case s := <-got:
		if !strings.HasPrefix(s, "CONNECT api.anthropic.com:443 HTTP/1.1\r\n") {
			t.Fatalf("host saw %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no stream on host")
	}
	buf := make([]byte, 64)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := io.ReadAtLeast(c, buf, len("HTTP/1.1 200 Connection established\r\n\r\nPONG"))
	if !strings.HasSuffix(string(buf[:n]), "PONG") {
		t.Fatalf("client got %q", buf[:n])
	}
}

func TestSpriteRelayNonSecretDialedDirectly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			io.Copy(c, c) // echo
		}
	}()
	r := startRelay(t, ln.Addr().String())
	c, err := net.Dial("tcp", r.addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	br := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	l, _ := br.ReadString('\n')
	if !strings.HasPrefix(l, "HTTP/1.1 200 Connection established") {
		t.Fatalf("status %q", l)
	}
	br.ReadString('\n')
	io.WriteString(c, "hello")
	b := make([]byte, 5)
	if _, err := io.ReadFull(br, b); err != nil || string(b) != "hello" {
		t.Fatalf("echo %q %v", b, err)
	}
}

func TestSpriteRelayRejectsPlainHTTP(t *testing.T) {
	r := startRelay(t, "127.0.0.1:1")
	c, _ := net.Dial("tcp", r.addr.String())
	defer c.Close()
	io.WriteString(c, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	l, _ := bufio.NewReader(c).ReadString('\n')
	if !strings.Contains(l, "405") {
		t.Fatalf("got %q", l)
	}
}

func TestSpriteRelayStdinEOFExitsFailClosed(t *testing.T) {
	r := startRelay(t, "127.0.0.1:1")
	if b, err := os.ReadFile(r.pid); err != nil || len(bytes.TrimSpace(b)) == 0 {
		t.Fatalf("pidfile: %q %v", b, err)
	}
	r.stdin.Close()
	select {
	case err := <-r.done:
		if err == nil {
			t.Fatal("want non-nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not exit on stdin EOF")
	}
	if c, err := net.DialTimeout("tcp", r.addr.String(), 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("listener still open")
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

// Everything the relay writes to its stdout must be valid yamux: the host
// session parses it without dying, and the first byte is a yamux version 0 header.
func TestSpriteRelayStdoutIsOnlyTunnelFrames(t *testing.T) {
	inR, inW := io.Pipe()
	pr, pw := io.Pipe()
	seen := &lockedBuf{}
	h, err := tunnel.Host(tunnel.Join(inW, io.NopCloser(io.TeeReader(pr, seen))))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ready := make(chan net.Addr, 1)
	go spriteRelay(spriteRelayOpts{listen: "127.0.0.1:0", secretHosts: parseHostSet("github.com")}, tunnel.Join(pw, inR), ready)
	addr := <-ready
	go func() {
		if s, _ := h.Accept(); s != nil {
			io.Copy(io.Discard, s)
		}
	}()
	c, _ := net.Dial("tcp", addr.String())
	io.WriteString(c, "CONNECT github.com:443 HTTP/1.1\r\n\r\n")
	time.Sleep(200 * time.Millisecond)
	c.Close()
	select {
	case <-h.Closed():
		t.Fatal("host session died: relay wrote non-yamux bytes")
	default:
	}
	if b := seen.Bytes(); len(b) == 0 || b[0] != 0 {
		t.Fatalf("stdout does not start with a yamux frame: %v", b)
	}
}
