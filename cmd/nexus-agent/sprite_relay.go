package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IniZio/nexus/internal/core/driver/sprites/tunnel"
)

const defaultSpriteRelaySecretHosts = "api.anthropic.com,platform.claude.com,github.com,api.github.com"

type spriteRelayOpts struct {
	listen      string
	secretHosts map[string]bool
	pidfile     string
	dial        func(addr string) (net.Conn, error) // direct dial; replaced in tests
}

// runSpriteRelay implements `nexus-agent sprite-relay`: an HTTP CONNECT proxy
// on loopback whose secret-host connections ride a yamux tunnel over
// stdin/stdout to the host broker. Stdout is the tunnel: log to stderr only.
// Does not return.
func runSpriteRelay(args []string) {
	fs := flag.NewFlagSet("sprite-relay", flag.ExitOnError)
	listen := fs.String("listen", envOr("NEXUS_SPRITE_RELAY_LISTEN", "127.0.0.1:3128"), "listen address")
	secrets := fs.String("secret-hosts", envOr("NEXUS_SPRITE_RELAY_SECRET_HOSTS", defaultSpriteRelaySecretHosts), "comma-separated hosts brokered via host")
	pidfile := fs.String("pidfile", os.Getenv("NEXUS_SPRITE_RELAY_PIDFILE"), "pidfile path")
	_ = fs.Parse(args)
	opts := spriteRelayOpts{listen: *listen, secretHosts: parseHostSet(*secrets), pidfile: *pidfile}
	err := spriteRelay(opts, tunnel.Join(os.Stdout, os.Stdin), nil)
	fmt.Fprintf(os.Stderr, "nexus-agent sprite-relay: exit: %v\n", err)
	os.Exit(1)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseHostSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, h := range strings.Split(s, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			m[h] = true
		}
	}
	return m
}

// spriteRelay serves until the tunnel closes (fail closed: always returns non-nil).
// ready, if non-nil, receives the bound listener address.
func spriteRelay(o spriteRelayOpts, rwc io.ReadWriteCloser, ready chan<- net.Addr) error {
	if o.dial == nil {
		o.dial = func(a string) (net.Conn, error) { return net.DialTimeout("tcp", a, 15*time.Second) }
	}
	sess, err := tunnel.Guest(rwc)
	if err != nil {
		rwc.Close()
		return err
	}
	defer sess.Close()
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	if o.pidfile != "" {
		if err := os.WriteFile(o.pidfile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
			return err
		}
		defer os.Remove(o.pidfile)
	}
	if ready != nil {
		ready <- ln.Addr()
	}
	go func() { <-sess.Closed(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-sess.Closed():
				return errors.New("tunnel closed")
			default:
				return err
			}
		}
		go serveRelayConn(o, sess, c)
	}
}

func serveRelayConn(o spriteRelayOpts, sess *tunnel.GuestSession, c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	var head bytes.Buffer
	for !bytes.HasSuffix(head.Bytes(), []byte("\r\n\r\n")) {
		line, err := br.ReadBytes('\n')
		head.Write(line)
		if err != nil || head.Len() > 16<<10 {
			return
		}
	}
	f := strings.Fields(head.String())
	if len(f) < 3 || f[0] != "CONNECT" {
		io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\nAllow: CONNECT\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	host, _, err := net.SplitHostPort(f[1])
	if err != nil {
		io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	var up net.Conn
	if o.secretHosts[strings.ToLower(host)] {
		// Splice the whole client conn, CONNECT line included: the host
		// broker's goproxy answers CONNECT itself.
		if up, err = sess.Open(); err != nil {
			io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			return
		}
		if _, err = up.Write(head.Bytes()); err != nil {
			up.Close()
			return
		}
	} else {
		if up, err = o.dial(f[1]); err != nil {
			io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			return
		}
		io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	}
	defer up.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(up, br); closeWrite(up) }()
	go func() { defer wg.Done(); io.Copy(c, up); closeWrite(c) }()
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
