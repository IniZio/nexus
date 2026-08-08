// Package sni implements the transparent-to-explicit TLS shim for nexus3's
// perimeter subsystem.
//
// The shim sits between the guest's raw :443 TCP connections and the
// per-sandbox L7 MITM proxy (see package mitm). It has two responsibilities:
//
//  1. [ParseSNI] extracts the SNI hostname from a peeked TLS ClientHello
//     without consuming the connection bytes, returning a replay-capable
//     [net.Conn] that the caller can hand off transparently.
//
//  2. [Bridge] takes that raw connection plus the parsed hostname and dials
//     the MITM proxy via an explicit HTTP CONNECT tunnel, then splices bytes
//     bidirectionally until either side closes.
//
// Neither function performs TLS termination — that is left to the MITM proxy.
package sni

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

// ErrNoSNI is returned by [ParseSNI] when the TLS ClientHello contains no SNI
// extension. The peeked bytes are not consumed; the returned [net.Conn] is
// still valid and replays all bytes that were read from the underlying
// connection.
var ErrNoSNI = errors.New("sni: ClientHello contains no SNI extension")

// maxClientHelloRecord is the largest TLS record payload we will read during
// SNI extraction. RFC 5246 caps TLS records at 16 384 bytes; a ClientHello is
// always far smaller in practice, but we honour the standard limit.
const maxClientHelloRecord = 16384

// PeekConn wraps a [net.Conn] and replays a slice of bytes that were peeked
// (consumed from the network but not yet processed by the caller) before
// delegating further reads to the underlying connection. All other [net.Conn]
// methods are forwarded unchanged.
type PeekConn struct {
	net.Conn
	buf []byte
	pos int
}

// Read implements [io.Reader]. Bytes from the peek buffer are returned first;
// once exhausted, subsequent reads go directly to the underlying [net.Conn].
func (c *PeekConn) Read(b []byte) (int, error) {
	if c.pos < len(c.buf) {
		n := copy(b, c.buf[c.pos:])
		c.pos += n
		return n, nil
	}
	return c.Conn.Read(b)
}

// ParseSNI reads exactly the first TLS record from raw, extracts the SNI
// hostname from the ClientHello it contains, and returns:
//
//   - host: the SNI server name, or "" when absent.
//   - replay: a [net.Conn] that replays all consumed bytes before reading
//     from raw; the caller MUST use replay instead of raw henceforth.
//   - err: nil on success; [ErrNoSNI] when the ClientHello has no SNI
//     extension (replay is still valid); a non-sentinel error when the
//     record is non-TLS or truncated.
//
// ParseSNI blocks until the full first TLS record has been read, which is
// always the case for an active TLS handshake.
func ParseSNI(raw net.Conn) (host string, replay net.Conn, err error) {
	// Read the 5-byte TLS record header.
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(raw, hdr); err != nil {
		return "", &PeekConn{Conn: raw, buf: hdr}, fmt.Errorf("sni: read record header: %w", err)
	}

	if hdr[0] != 0x16 { // ContentType: handshake
		return "", &PeekConn{Conn: raw, buf: hdr},
			fmt.Errorf("sni: not a TLS handshake record (content type 0x%02x)", hdr[0])
	}

	recordLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	if recordLen > maxClientHelloRecord {
		return "", &PeekConn{Conn: raw, buf: hdr},
			fmt.Errorf("sni: record length %d exceeds maximum %d", recordLen, maxClientHelloRecord)
	}

	// Read the full record payload.
	body := make([]byte, recordLen)
	if _, err := io.ReadFull(raw, body); err != nil {
		peeked := append(hdr, body...)
		return "", &PeekConn{Conn: raw, buf: peeked},
			fmt.Errorf("sni: read record body: %w", err)
	}

	peeked := make([]byte, 5+recordLen)
	copy(peeked, hdr)
	copy(peeked[5:], body)

	pc := &PeekConn{Conn: raw, buf: peeked}
	h, parseErr := extractSNI(peeked)
	return h, pc, parseErr
}

// extractSNI parses data as a complete TLS record containing a ClientHello and
// returns the SNI server name. It returns [ErrNoSNI] when no SNI extension is
// present and the record is otherwise well-formed.
func extractSNI(data []byte) (string, error) {
	// ── TLS record header (already validated by ParseSNI) ──────────────────
	// Bytes: [type(1)] [version(2)] [length(2)] [payload(length)...]
	if len(data) < 5 {
		return "", fmt.Errorf("sni: record too short (%d bytes)", len(data))
	}
	if data[0] != 0x16 {
		return "", fmt.Errorf("sni: not a TLS handshake record (0x%02x)", data[0])
	}
	recordLen := int(binary.BigEndian.Uint16(data[3:5]))
	if len(data) < 5+recordLen {
		return "", fmt.Errorf("sni: record truncated (have %d, need %d)", len(data), 5+recordLen)
	}
	payload := data[5 : 5+recordLen]

	// ── Handshake header ───────────────────────────────────────────────────
	// Bytes: [type(1)] [length(3)] [body(length)...]
	if len(payload) < 4 {
		return "", fmt.Errorf("sni: handshake header truncated")
	}
	if payload[0] != 0x01 { // HandshakeType: client_hello
		return "", fmt.Errorf("sni: not a ClientHello (handshake type 0x%02x)", payload[0])
	}
	helloLen := int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if len(payload) < 4+helloLen {
		return "", fmt.Errorf("sni: ClientHello truncated")
	}
	hello := payload[4 : 4+helloLen]

	// ── ClientHello body ───────────────────────────────────────────────────
	// client_version(2) + random(32) = 34 fixed bytes.
	if len(hello) < 35 {
		return "", fmt.Errorf("sni: ClientHello body too short (%d bytes)", len(hello))
	}
	pos := 34 // skip client_version + random

	// session_id: length(1) + data(length)
	sidLen := int(hello[pos])
	pos++
	if pos+sidLen > len(hello) {
		return "", fmt.Errorf("sni: ClientHello truncated at session ID")
	}
	pos += sidLen

	// cipher_suites: length(2) + data(length)
	if pos+2 > len(hello) {
		return "", fmt.Errorf("sni: ClientHello truncated at cipher suites length")
	}
	csLen := int(binary.BigEndian.Uint16(hello[pos : pos+2]))
	pos += 2
	if pos+csLen > len(hello) {
		return "", fmt.Errorf("sni: ClientHello truncated at cipher suites data")
	}
	pos += csLen

	// compression_methods: length(1) + data(length)
	if pos+1 > len(hello) {
		return "", fmt.Errorf("sni: ClientHello truncated at compression methods length")
	}
	cmLen := int(hello[pos])
	pos++
	if pos+cmLen > len(hello) {
		return "", fmt.Errorf("sni: ClientHello truncated at compression methods data")
	}
	pos += cmLen

	// No extensions field → no SNI (valid for very old TLS, treat as ErrNoSNI).
	if pos+2 > len(hello) {
		return "", ErrNoSNI
	}

	// extensions: total_length(2) + extension records
	extLen := int(binary.BigEndian.Uint16(hello[pos : pos+2]))
	pos += 2
	extEnd := pos + extLen
	if extEnd > len(hello) {
		return "", fmt.Errorf("sni: extensions block truncated")
	}

	// Walk extensions looking for type 0x0000 (server_name).
	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(hello[pos : pos+2])
		extDataLen := int(binary.BigEndian.Uint16(hello[pos+2 : pos+4]))
		pos += 4
		if pos+extDataLen > extEnd {
			return "", fmt.Errorf("sni: extension data truncated")
		}
		extData := hello[pos : pos+extDataLen]

		if extType == 0x0000 { // server_name extension
			return parseSNIExtension(extData)
		}
		pos += extDataLen
	}

	return "", ErrNoSNI
}

// parseSNIExtension parses the data field of a server_name (type 0x0000)
// extension and returns the first host_name entry, or [ErrNoSNI] if there is
// none.
//
// Wire format:
//
//	server_name_list_length(2) [name_type(1) name_length(2) name(name_length)...]
func parseSNIExtension(data []byte) (string, error) {
	if len(data) < 2 {
		return "", fmt.Errorf("sni: SNI extension too short")
	}
	listLen := int(binary.BigEndian.Uint16(data[0:2]))
	if 2+listLen > len(data) {
		return "", fmt.Errorf("sni: SNI name list truncated")
	}
	list := data[2 : 2+listLen]

	pos := 0
	for pos+3 <= len(list) {
		nameType := list[pos]
		nameLen := int(binary.BigEndian.Uint16(list[pos+1 : pos+3]))
		pos += 3
		if pos+nameLen > len(list) {
			return "", fmt.Errorf("sni: SNI name entry truncated")
		}
		if nameType == 0x00 { // host_name
			return string(list[pos : pos+nameLen]), nil
		}
		pos += nameLen
	}
	return "", ErrNoSNI
}
