// Package gitssh defines the wire protocol between the in-guest nexus3-agent
// git-ssh shim and the host-side relay.
//
// # Protocol overview
//
// 1. Guest → Host: one request frame [4-byte len BE][JSON-encoded Request]
//    followed by raw stdin bytes until EOF.
// 2. Host → Guest: zero or more framed responses:
//      [4-byte len BE][1-byte FrameType][payload]
//    FrameTypeStdout (0x01): SSH process stdout bytes (payload = raw bytes).
//    FrameTypeExit   (0x02): SSH process exit code (payload = 4-byte int32 BE).
// 3. After FrameTypeExit the host closes the connection.
//
// The guest shim reads stdin concurrently while writing it to the connection
// and reads frames from the connection for stdout/exit.
package gitssh

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// FrameType identifies the kind of a host→guest frame.
type FrameType uint8

const (
	// FrameTypeStdout carries SSH process stdout bytes from host to guest.
	FrameTypeStdout FrameType = 0x01
	// FrameTypeExit carries the SSH process exit code (4-byte int32 BE) from
	// host to guest. After this frame the host closes the connection.
	FrameTypeExit FrameType = 0x02
)

// maxRequestSize is the maximum allowed byte size of a serialised Request.
// 4 KiB is generous — typical argv + cwd fits in under 512 bytes.
const maxRequestSize = 4 * 1024

// Request is the initial message sent by the guest shim to the host relay.
// It carries everything the host relay needs to invoke ssh on the host.
type Request struct {
	// Argv is the SSH argument list git passed to GIT_SSH_COMMAND:
	// typically [user@host, git-receive-pack '/owner/repo.git'].
	Argv []string `json:"argv"`
	// Cwd is the working directory of the calling git process. The host relay
	// uses it to resolve the originating repository for policy checks.
	Cwd string `json:"cwd"`
	// GitProtocol is the value of the GIT_PROTOCOL environment variable, if
	// set by git (e.g. "version=2"). Empty when git did not set it.
	GitProtocol string `json:"git_protocol,omitempty"`
}

// WriteRequest encodes req as a 4-byte-length-prefixed JSON frame and writes
// it to w. Used by the guest shim before streaming stdin.
func WriteRequest(w io.Writer, req Request) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("gitssh/wire: marshal request: %w", err)
	}
	if err := writeFrame(w, payload); err != nil {
		return fmt.Errorf("gitssh/wire: write request: %w", err)
	}
	return nil
}

// ReadRequest reads a 4-byte-length-prefixed JSON frame from r and decodes
// it into a Request. Used by the host relay (T3) as its first operation on a
// new connection.
func ReadRequest(r io.Reader) (Request, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Request{}, fmt.Errorf("gitssh/wire: read request header: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxRequestSize {
		return Request{}, fmt.Errorf("gitssh/wire: request payload too large: %d bytes (max %d)", n, maxRequestSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Request{}, fmt.Errorf("gitssh/wire: read request payload: %w", err)
	}
	var req Request
	if err := json.Unmarshal(buf, &req); err != nil {
		return Request{}, fmt.Errorf("gitssh/wire: unmarshal request: %w", err)
	}
	return req, nil
}

// WriteStdout writes a FrameTypeStdout frame carrying data to w.
// Used by the host relay to send SSH process output to the guest.
func WriteStdout(w io.Writer, data []byte) error {
	return WriteFrame(w, FrameTypeStdout, data)
}

// WriteExitFrame writes a FrameTypeExit frame carrying a 4-byte big-endian
// int32 exit code to w. Used by the host relay when the SSH process exits.
func WriteExitFrame(w io.Writer, code int32) error {
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], uint32(code))
	return WriteFrame(w, FrameTypeExit, payload[:])
}

// WriteFrame writes a typed frame [4-byte total length][1-byte type][payload]
// to w. total length = 1 + len(payload). Used by the host relay.
func WriteFrame(w io.Writer, ft FrameType, payload []byte) error {
	total := uint32(1 + len(payload))
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], total)
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("gitssh/wire: write frame header: %w", err)
	}
	if _, err := w.Write([]byte{byte(ft)}); err != nil {
		return fmt.Errorf("gitssh/wire: write frame type: %w", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("gitssh/wire: write frame payload: %w", err)
		}
	}
	return nil
}

// ReadFrame reads one typed frame from r. Returns (FrameType, payload, error).
// Used by the guest shim when reading responses from the host relay.
func ReadFrame(r io.Reader) (FrameType, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, fmt.Errorf("gitssh/wire: read frame header: %w", err)
	}
	total := binary.BigEndian.Uint32(hdr[:])
	if total == 0 {
		return 0, nil, fmt.Errorf("gitssh/wire: zero-length frame")
	}
	buf := make([]byte, total)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, fmt.Errorf("gitssh/wire: read frame body: %w", err)
	}
	return FrameType(buf[0]), buf[1:], nil
}

// writeFrame writes a 4-byte-length-prefixed raw payload (no type byte) to w.
// Used internally for the request frame only.
func writeFrame(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
