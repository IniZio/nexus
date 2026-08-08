package sni

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// Bridge dials proxyAddr via TCP, opens an HTTP CONNECT tunnel to host:443 on
// the caller's behalf, and then splices bytes bidirectionally between raw and
// the proxy connection until either side closes.
//
// proxyAddr is an explicit parameter supplied by the caller — Bridge never
// auto-discovers or hardcodes a proxy address.
//
// Bridge returns an error when:
//   - the dial to proxyAddr fails,
//   - writing the CONNECT request fails,
//   - reading the CONNECT response fails, or
//   - the proxy returns a non-200 status.
//
// On a successful 200 response, Bridge splices traffic until one side closes,
// then closes both connections and returns nil.
func Bridge(raw net.Conn, host string, proxyAddr string) error {
	proxy, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		return fmt.Errorf("sni: dial proxy %s: %w", proxyAddr, err)
	}

	target := net.JoinHostPort(host, "443")

	// Send HTTP CONNECT request.
	req := "CONNECT " + target + " HTTP/1.1\r\n" +
		"Host: " + target + "\r\n" +
		"\r\n"
	if _, err := io.WriteString(proxy, req); err != nil {
		proxy.Close()
		return fmt.Errorf("sni: write CONNECT: %w", err)
	}

	// Parse the CONNECT response. We read one byte at a time to guarantee
	// zero look-ahead into the subsequent TLS tunnel stream.
	code, statusText, err := readConnectResponse(proxy)
	if err != nil {
		proxy.Close()
		return fmt.Errorf("sni: read CONNECT response: %w", err)
	}
	if code != 200 {
		proxy.Close()
		return fmt.Errorf("sni: CONNECT %s: proxy returned %d %s", target, code, statusText)
	}

	// Splice bidirectionally until either side closes.
	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(proxy, raw)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(raw, proxy)
		errc <- err
	}()

	// One direction done; close both ends to unblock the other goroutine.
	<-errc
	proxy.Close()
	raw.Close()
	<-errc

	return nil
}

// readConnectResponse reads a minimal HTTP/1.x response from r, consuming
// exactly the status line and all header lines (up to and including the blank
// line that terminates the headers). It returns the numeric status code and the
// reason phrase, or an error.
//
// r is read one byte at a time so no bytes beyond the headers are consumed.
func readConnectResponse(r io.Reader) (code int, reason string, err error) {
	// Read status line: "HTTP/1.x NNN reason\r\n"
	line, err := readResponseLine(r)
	if err != nil {
		return 0, "", fmt.Errorf("status line: %w", err)
	}

	// Parse "HTTP/1.x" <space> code <space> reason
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return 0, "", fmt.Errorf("malformed status line: %q", line)
	}
	c, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", fmt.Errorf("bad status code %q: %w", parts[1], err)
	}
	rsn := ""
	if len(parts) == 3 {
		rsn = parts[2]
	}

	// Consume header lines until the blank line that ends the headers.
	for {
		l, err := readResponseLine(r)
		if err != nil {
			return 0, "", fmt.Errorf("reading headers: %w", err)
		}
		if l == "" {
			break
		}
	}

	return c, rsn, nil
}

// readResponseLine reads one CRLF-terminated line from r, one byte at a time,
// and returns the line without the trailing CRLF.
func readResponseLine(r io.Reader) (string, error) {
	var line []byte
	buf := [1]byte{}
	for {
		if _, err := r.Read(buf[:]); err != nil {
			return "", err
		}
		b := buf[0]
		if b == '\n' {
			// Strip trailing CR if present.
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return string(line), nil
		}
		line = append(line, b)
	}
}
