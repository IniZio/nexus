package tunnel

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestConfigExplicit(t *testing.T) {
	c := Config()
	d := yamux.DefaultConfig()
	if c == nil {
		t.Fatal("nil config")
	}
	if c.KeepAliveInterval == d.KeepAliveInterval && c.ConnectionWriteTimeout == d.ConnectionWriteTimeout &&
		c.StreamOpenTimeout == d.StreamOpenTimeout {
		t.Fatal("config equals yamux defaults")
	}
	if !c.EnableKeepAlive || c.KeepAliveInterval <= 0 || c.ConnectionWriteTimeout <= 0 || c.StreamOpenTimeout <= 0 {
		t.Fatalf("timeouts not set: %+v", c)
	}
	if c.LogOutput == nil || c.Logger != nil {
		t.Fatal("want LogOutput set and Logger nil")
	}
	if err := yamux.VerifyConfig(c); err != nil {
		t.Fatal(err)
	}
}

func pipePair(t *testing.T) (*HostSession, *GuestSession, net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	h, err := Host(a)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Guest(b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(); g.Close() })
	return h, g, a, b
}

func TestRoundTrip(t *testing.T) {
	h, g, _, _ := pipePair(t)
	go func() {
		for {
			c, err := h.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()

	big := make([]byte, 4<<20+123)
	rand.Read(big)
	payloads := [][]byte{[]byte("hello"), big, []byte("third")}
	var wg sync.WaitGroup
	for i, p := range payloads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := g.Open()
			if err != nil {
				t.Errorf("open %d: %v", i, err)
				return
			}
			defer s.Close()
			go func() { s.Write(p); s.Close() }() // yamux Close half-closes: FIN, reads continue
			got, err := io.ReadAll(s)
			if err != nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
			if !bytes.Equal(got, p) {
				t.Errorf("stream %d mismatch: got %d bytes want %d", i, len(got), len(p))
			}
		}()
	}
	wg.Wait()
}

func TestUnderlyingCloseFailsClosed(t *testing.T) {
	h, g, a, _ := pipePair(t)
	a.Close()
	if _, err := h.Accept(); err == nil {
		t.Fatal("Accept succeeded after close")
	}
	select {
	case <-h.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("host Closed not signalled")
	}
	select {
	case <-g.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("guest Closed not signalled")
	}
	if _, err := g.Open(); err == nil {
		t.Fatal("Open succeeded after close")
	}
}

func TestJoin(t *testing.T) {
	pr, pw := io.Pipe()
	var buf bytes.Buffer
	rwc := Join(&nopWC{&buf}, pr)
	go pw.Write([]byte("abc"))
	b := make([]byte, 3)
	if _, err := io.ReadFull(rwc, b); err != nil || string(b) != "abc" {
		t.Fatalf("read %q %v", b, err)
	}
	rwc.Write([]byte("xyz"))
	if buf.String() != "xyz" {
		t.Fatalf("write %q", buf.String())
	}
	rwc.Close()
	if _, err := pr.Read(b); err == nil {
		t.Fatal("reader not closed")
	}
}

type nopWC struct{ io.Writer }

func (nopWC) Close() error { return nil }
