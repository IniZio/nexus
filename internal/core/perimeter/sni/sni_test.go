package sni

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
)

// captureClientHello dials a real tls.Client against a net.Pipe() peer and
// captures the raw TLS ClientHello record. If serverName is empty the TLS
// stack omits the SNI extension; otherwise it is included.
//
// The function reads exactly the first TLS record (header + payload) so the
// returned slice is a complete, self-contained ClientHello that the parser can
// consume.
func captureClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	serverConn, clientConn := net.Pipe()

	// Drive the TLS client in a goroutine; Handshake will fail because there
	// is no real server, but the ClientHello is sent before any server reply.
	go func() {
		cfg := &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: true, //nolint:gosec // test-only fixture generation
		}
		tlsConn := tls.Client(clientConn, cfg)
		_ = tlsConn.Handshake() // error expected; we only need the ClientHello
		clientConn.Close()
	}()

	// Read exactly the first TLS record from the server side.
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(serverConn, hdr); err != nil {
		serverConn.Close()
		t.Fatalf("captureClientHello: read header: %v", err)
	}
	recLen := int(hdr[3])<<8 | int(hdr[4])
	body := make([]byte, recLen)
	if _, err := io.ReadFull(serverConn, body); err != nil {
		serverConn.Close()
		t.Fatalf("captureClientHello: read body: %v", err)
	}
	serverConn.Close()

	raw := make([]byte, 5+recLen)
	copy(raw, hdr)
	copy(raw[5:], body)
	return raw
}

// pipeWithBytes returns a net.Conn whose first read returns data followed by
// EOF. It is backed by a net.Pipe() pair so ParseSNI gets a real net.Conn.
func pipeWithBytes(t *testing.T, data []byte) net.Conn {
	t.Helper()
	server, client := net.Pipe()
	go func() {
		client.Write(data)
		client.Close()
	}()
	return server
}

// ── Acceptance 1 ────────────────────────────────────────────────────────────

// TestParseSNI_Present verifies that a ClientHello with SNI returns the
// server name and a nil error.
func TestParseSNI_Present(t *testing.T) {
	const want = "example.com"
	fixture := captureClientHello(t, want)
	conn := pipeWithBytes(t, fixture)

	host, replay, err := ParseSNI(conn)
	if err != nil {
		t.Fatalf("ParseSNI: unexpected error: %v", err)
	}
	if host != want {
		t.Errorf("host = %q; want %q", host, want)
	}

	// Replay must return the original bytes verbatim.
	got := make([]byte, len(fixture))
	if _, err := io.ReadFull(replay, got); err != nil {
		t.Fatalf("replay ReadFull: %v", err)
	}
	if !bytes.Equal(got, fixture) {
		t.Errorf("replay bytes mismatch:\ngot  %x\nwant %x", got, fixture)
	}
}

// TestParseSNI_Absent verifies that a ClientHello without SNI returns ErrNoSNI
// and an empty host, but the replay conn is still valid.
func TestParseSNI_Absent(t *testing.T) {
	// Empty ServerName causes Go's TLS stack to omit the SNI extension.
	fixture := captureClientHello(t, "")
	conn := pipeWithBytes(t, fixture)

	host, replay, err := ParseSNI(conn)
	if !errors.Is(err, ErrNoSNI) {
		t.Fatalf("ParseSNI: got err = %v; want ErrNoSNI", err)
	}
	if host != "" {
		t.Errorf("host = %q; want empty string", host)
	}

	// Replay must still return the original bytes even on ErrNoSNI.
	got := make([]byte, len(fixture))
	if _, err := io.ReadFull(replay, got); err != nil {
		t.Fatalf("replay ReadFull: %v", err)
	}
	if !bytes.Equal(got, fixture) {
		t.Errorf("replay bytes mismatch (ErrNoSNI path):\ngot  %x\nwant %x", got, fixture)
	}
}

// TestParseSNI_NonTLS verifies that data whose first byte is not 0x16 (the TLS
// handshake content type) is rejected with a non-sentinel error.
func TestParseSNI_NonTLS(t *testing.T) {
	// HTTP request — definitely not TLS.
	data := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	conn := pipeWithBytes(t, data)

	_, _, err := ParseSNI(conn)
	if err == nil {
		t.Fatal("ParseSNI: expected error for non-TLS data, got nil")
	}
	if errors.Is(err, ErrNoSNI) {
		t.Errorf("ParseSNI: got ErrNoSNI for non-TLS data; want a different error")
	}
}

// TestParseSNI_Truncated verifies that a TLS record header whose declared
// length exceeds the available bytes produces an error.
func TestParseSNI_Truncated(t *testing.T) {
	// Craft a TLS handshake record header (type=0x16, ver=3.3) that declares
	// 100 bytes of payload but only provides 10.
	hdr := []byte{0x16, 0x03, 0x03, 0x00, 0x64} // record len = 100
	truncated := append(hdr, make([]byte, 10)...)
	conn := pipeWithBytes(t, truncated)

	_, _, err := ParseSNI(conn)
	if err == nil {
		t.Fatal("ParseSNI: expected error for truncated record, got nil")
	}
	if errors.Is(err, ErrNoSNI) {
		t.Errorf("ParseSNI: got ErrNoSNI for truncated data; want a structural error")
	}
}

// TestParseSNI_ReplayAfterPresent confirms that after a successful SNI parse
// the replay conn can still read arbitrary data that follows the ClientHello
// on the same underlying connection.
func TestParseSNI_ReplayAfterPresent(t *testing.T) {
	const serverName = "test.example.org"
	fixture := captureClientHello(t, serverName)
	extra := []byte("subsequent TLS record data")

	server, client := net.Pipe()
	go func() {
		client.Write(fixture)
		client.Write(extra)
		client.Close()
	}()

	host, replay, err := ParseSNI(server)
	if err != nil {
		t.Fatalf("ParseSNI: %v", err)
	}
	if host != serverName {
		t.Errorf("host = %q; want %q", host, serverName)
	}

	// Read fixture bytes back via replay.
	gotFixture := make([]byte, len(fixture))
	if _, err := io.ReadFull(replay, gotFixture); err != nil {
		t.Fatalf("replay ReadFull (fixture): %v", err)
	}
	if !bytes.Equal(gotFixture, fixture) {
		t.Error("replayed fixture bytes mismatch")
	}

	// Read the extra bytes that follow — they must come from the underlying conn.
	gotExtra := make([]byte, len(extra))
	if _, err := io.ReadFull(replay, gotExtra); err != nil {
		t.Fatalf("replay ReadFull (extra): %v", err)
	}
	if !bytes.Equal(gotExtra, extra) {
		t.Errorf("extra bytes: got %q; want %q", gotExtra, extra)
	}
}
