package wire_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent/wire"
)

// roundTrip encodes one frame with write and immediately decodes it from the
// same in-memory buffer.  It fails the test on any encode or decode error and
// returns the decoded Frame.
func roundTrip(t *testing.T, write func(*wire.Writer) error) wire.Frame {
	t.Helper()
	var buf bytes.Buffer
	if err := write(wire.NewWriter(&buf)); err != nil {
		t.Fatalf("encode: %v", err)
	}
	f, err := wire.NewReader(&buf).ReadFrame()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return f
}

// --- round-trip tests for every frame type ---

func TestRoundTrip_Handshake(t *testing.T) {
	const sid = "sess-abc"
	const offset = uint64(1234567890)
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteHandshake(wire.Handshake{SessionID: sid, ResumeFromOffset: offset})
	})
	if f.Type != wire.FrameHandshake {
		t.Fatalf("type: got %d, want FrameHandshake", f.Type)
	}
	if f.Handshake.SessionID != sid {
		t.Errorf("session_id: got %q, want %q", f.Handshake.SessionID, sid)
	}
	if f.Handshake.ResumeFromOffset != offset {
		t.Errorf("resume_from_offset: got %d, want %d", f.Handshake.ResumeFromOffset, offset)
	}
}

func TestRoundTrip_HandshakeAck_Alive(t *testing.T) {
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive})
	})
	if f.Type != wire.FrameHandshakeAck {
		t.Fatalf("type: got %d, want FrameHandshakeAck", f.Type)
	}
	if f.HandshakeAck.Status != wire.AckAlive {
		t.Errorf("status: got %d, want AckAlive", f.HandshakeAck.Status)
	}
	if f.HandshakeAck.ExitCode != 0 {
		t.Errorf("exit_code: got %d, want 0 (alive)", f.HandshakeAck.ExitCode)
	}
}

func TestRoundTrip_HandshakeAck_Exited(t *testing.T) {
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckExited, ExitCode: 42})
	})
	if f.HandshakeAck.Status != wire.AckExited {
		t.Errorf("status: got %d, want AckExited", f.HandshakeAck.Status)
	}
	if f.HandshakeAck.ExitCode != 42 {
		t.Errorf("exit_code: got %d, want 42", f.HandshakeAck.ExitCode)
	}
}

func TestRoundTrip_Data_Stdout(t *testing.T) {
	payload := []byte("hello, stdout")
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteData(wire.StreamStdout, payload)
	})
	if f.Type != wire.FrameData {
		t.Fatalf("type: got %d, want FrameData", f.Type)
	}
	if f.Data.Tag != wire.StreamStdout {
		t.Errorf("tag: got %d, want StreamStdout", f.Data.Tag)
	}
	if !bytes.Equal(f.Data.Payload, payload) {
		t.Errorf("payload mismatch: got %q, want %q", f.Data.Payload, payload)
	}
}

func TestRoundTrip_Data_Stderr(t *testing.T) {
	payload := []byte("error output")
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteData(wire.StreamStderr, payload)
	})
	if f.Data.Tag != wire.StreamStderr {
		t.Errorf("tag: got %d, want StreamStderr", f.Data.Tag)
	}
	if !bytes.Equal(f.Data.Payload, payload) {
		t.Errorf("payload mismatch")
	}
}

func TestRoundTrip_Data_Stdin(t *testing.T) {
	payload := []byte("user input\r")
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteData(wire.StreamStdin, payload)
	})
	if f.Data.Tag != wire.StreamStdin {
		t.Errorf("tag: got %d, want StreamStdin", f.Data.Tag)
	}
	if !bytes.Equal(f.Data.Payload, payload) {
		t.Errorf("payload mismatch")
	}
}

func TestRoundTrip_Winsize(t *testing.T) {
	ws := wire.Winsize{Rows: 24, Cols: 80, XPixels: 1920, YPixels: 1080}
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteWinsize(ws)
	})
	if f.Type != wire.FrameWinsize {
		t.Fatalf("type: got %d, want FrameWinsize", f.Type)
	}
	if *f.Winsize != ws {
		t.Errorf("winsize: got %+v, want %+v", *f.Winsize, ws)
	}
}

func TestRoundTrip_Exit_Zero(t *testing.T) {
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteExit(wire.Exit{Code: 0})
	})
	if f.Type != wire.FrameExit {
		t.Fatalf("type: got %d, want FrameExit", f.Type)
	}
	if f.Exit.Code != 0 {
		t.Errorf("code: got %d, want 0", f.Exit.Code)
	}
}

func TestRoundTrip_Exit_Nonzero(t *testing.T) {
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteExit(wire.Exit{Code: 127})
	})
	if f.Exit.Code != 127 {
		t.Errorf("code: got %d, want 127", f.Exit.Code)
	}
}

// --- 64 KiB cap enforcement ---

func TestData_OversizedPayloadRejected(t *testing.T) {
	oversized := make([]byte, wire.MaxDataPayload+1)
	var buf bytes.Buffer
	err := wire.NewWriter(&buf).WriteData(wire.StreamStdout, oversized)
	if !errors.Is(err, wire.ErrDataPayloadTooLarge) {
		t.Fatalf("expected ErrDataPayloadTooLarge, got %v", err)
	}
	if buf.Len() != 0 {
		t.Error("no bytes should have been written on error")
	}
}

func TestData_ExactMaxPayloadAccepted(t *testing.T) {
	exact := make([]byte, wire.MaxDataPayload)
	for i := range exact {
		exact[i] = byte(i % 251) // prime-mod pattern, not all zeros
	}
	f := roundTrip(t, func(w *wire.Writer) error {
		return w.WriteData(wire.StreamStdout, exact)
	})
	if !bytes.Equal(f.Data.Payload, exact) {
		t.Error("boundary-size (64 KiB) payload round-trip failed")
	}
}

// --- net.Pipe reattach test ---

// TestNetPipe_HandshakeAndReattach exercises the full reattach protocol over a
// real net.Pipe() connection pair.
//
// It simulates a guest that has accumulated guestOutput bytes and is serving
// the data-plane port.  The test makes two connections:
//
//  1. First pass (offset=0): host receives the complete output.
//  2. Second pass (offset=reattachOffset): host receives only the tail starting
//     at that byte offset, matching the replay semantics from doc 04.
//
// The guest-side simulation keeps the output ring inline; in production the
// ring lives in cmd/nexus3-agent.
func TestNetPipe_HandshakeAndReattach(t *testing.T) {
	const sessionID = "test-session-pipe-1"
	guestOutput := []byte("line one\nline two\nline three\n")

	// serve reads one Handshake, sends HandshakeAck(alive), then streams
	// guestOutput[resumeFrom:] as two Data frames, then closes.
	serve := func(conn net.Conn) {
		defer conn.Close()
		r := wire.NewReader(conn)
		w := wire.NewWriter(conn)

		f, err := r.ReadFrame()
		if err != nil || f.Type != wire.FrameHandshake {
			t.Errorf("serve: expected Handshake, got type=%v err=%v", f.Type, err)
			return
		}
		if f.Handshake.SessionID != sessionID {
			t.Errorf("serve: session_id: got %q, want %q", f.Handshake.SessionID, sessionID)
		}
		resumeFrom := f.Handshake.ResumeFromOffset

		if err := w.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive}); err != nil {
			t.Errorf("serve: WriteHandshakeAck: %v", err)
			return
		}

		tail := guestOutput
		if resumeFrom < uint64(len(guestOutput)) {
			tail = guestOutput[resumeFrom:]
		} else {
			tail = nil
		}

		// Split into two frames to exercise multi-frame reassembly.
		mid := len(tail) / 2
		if mid > 0 {
			if err := w.WriteData(wire.StreamStdout, tail[:mid]); err != nil {
				t.Errorf("serve: WriteData first half: %v", err)
				return
			}
		}
		if err := w.WriteData(wire.StreamStdout, tail[mid:]); err != nil {
			t.Errorf("serve: WriteData second half: %v", err)
		}
		// Closing conn signals EOF to the reader side.
	}

	// connect performs one full connect → handshake → collect-data cycle over
	// a net.Pipe pair.  It returns the concatenation of all Data payloads.
	connect := func(resumeFrom uint64) []byte {
		t.Helper()
		guestConn, hostConn := net.Pipe()
		go serve(guestConn)

		w := wire.NewWriter(hostConn)
		r := wire.NewReader(hostConn)

		if err := w.WriteHandshake(wire.Handshake{
			SessionID:        sessionID,
			ResumeFromOffset: resumeFrom,
		}); err != nil {
			t.Fatalf("connect(offset=%d): WriteHandshake: %v", resumeFrom, err)
		}

		ackF, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("connect(offset=%d): ReadFrame (ack): %v", resumeFrom, err)
		}
		if ackF.Type != wire.FrameHandshakeAck {
			t.Fatalf("connect(offset=%d): expected FrameHandshakeAck, got %d", resumeFrom, ackF.Type)
		}
		if ackF.HandshakeAck.Status != wire.AckAlive {
			t.Fatalf("connect(offset=%d): expected AckAlive, got %d", resumeFrom, ackF.HandshakeAck.Status)
		}

		var received []byte
		for {
			df, err := r.ReadFrame()
			if err == io.EOF || errors.Is(err, io.ErrClosedPipe) {
				break
			}
			if err != nil {
				// net.Pipe close from the serve side races; accept common closed errors.
				t.Logf("connect(offset=%d): ReadFrame: %v (treating as EOF)", resumeFrom, err)
				break
			}
			if df.Type != wire.FrameData {
				t.Fatalf("connect(offset=%d): expected FrameData, got %d", resumeFrom, df.Type)
			}
			received = append(received, df.Data.Payload...)
		}
		hostConn.Close()
		return received
	}

	// Pass 1: full output (offset=0).
	got1 := connect(0)
	if !bytes.Equal(got1, guestOutput) {
		t.Errorf("pass1: got %q, want %q", got1, guestOutput)
	}

	// Pass 2 (reattach): tail from byte offset 10.
	const reattachOffset = 10
	got2 := connect(reattachOffset)
	want2 := guestOutput[reattachOffset:]
	if !bytes.Equal(got2, want2) {
		t.Errorf("reattach(offset=%d): got %q, want %q", reattachOffset, got2, want2)
	}
}
