// Package gitssh defines the guest↔host git-ssh relay wire protocol.
package gitssh

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

type FrameType uint8

const (
	FrameTypeStdout FrameType = 0x01 // SSH stdout bytes
	FrameTypeExit   FrameType = 0x02 // 4-byte int32 BE exit code; host closes connection after
)

const maxRequestSize = 4 * 1024 // generous; typical argv+cwd < 512 bytes

// Request is the initial message sent by the guest shim to the host relay.
type Request struct {
	Argv        []string `json:"argv"`                   // SSH argv from GIT_SSH_COMMAND
	Cwd         string   `json:"cwd"`                    // caller's working directory
	GitProtocol string   `json:"git_protocol,omitempty"` // GIT_PROTOCOL env var, if set
}

// WriteRequest encodes req as a 4-byte-length-prefixed JSON frame.
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

// ReadRequest reads a 4-byte-length-prefixed JSON frame from r and decodes it.
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

func WriteStdout(w io.Writer, data []byte) error {
	return WriteFrame(w, FrameTypeStdout, data)
}

func WriteExitFrame(w io.Writer, code int32) error {
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], uint32(code))
	return WriteFrame(w, FrameTypeExit, payload[:])
}

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

// ReadFrame reads one typed frame from r.
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

func writeFrame(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
