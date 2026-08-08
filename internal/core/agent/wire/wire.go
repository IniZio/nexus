package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// DataPort is the fixed vsock port number the guest data-plane listener binds
// to.  It is intentionally distinct from driver.AgentControlPort (1024), which
// carries gRPC control traffic.  1025 is chosen to keep the two planes adjacent
// and human-readable in logs without colliding with registered service ports.
const DataPort uint32 = 1025

// MaxDataPayload is the hard cap on the byte length of the payload carried in
// a single [Data] frame.  The wire layer rejects oversized payloads rather than
// silently splitting them, so the chunking strategy — and therefore the
// resulting offset arithmetic — remains explicit at the call site.  Callers
// that need to send more than MaxDataPayload bytes must split the buffer into
// chunks of at most this size and call [Writer.WriteData] once per chunk.
const MaxDataPayload = 64 * 1024 // 64 KiB

// headerLen is the fixed byte count consumed by every frame's header:
// 4 bytes payload-length (uint32, big-endian) + 1 byte FrameType.
const headerLen = 5

// FrameType identifies the semantic kind of a frame.
type FrameType uint8

const (
	FrameHandshake    FrameType = 1
	FrameHandshakeAck FrameType = 2
	FrameData         FrameType = 3
	FrameWinsize      FrameType = 4
	FrameExit         FrameType = 5
	// FrameStdinClose is an in-band half-close signal sent by the host when its
	// stdin source reaches EOF.  On receipt the guest closes the process stdin
	// write-end so that pipe-reading programs (cat, tar, sort, …) see EOF and
	// can exit.  The frame carries no payload.  It does NOT close the whole
	// connection; stdout/stderr continue flowing until the Exit frame.
	FrameStdinClose FrameType = 6
)

// StreamTag classifies the direction and stdio stream of a [Data] frame.
type StreamTag uint8

const (
	StreamStdin  StreamTag = 0 // host → guest (keyboard input)
	StreamStdout StreamTag = 1 // guest → host
	StreamStderr StreamTag = 2 // guest → host
)

// AckStatus is the status field carried in a [HandshakeAck] frame.
type AckStatus uint8

const (
	AckAlive  AckStatus = 0 // session process is running
	AckExited AckStatus = 1 // session process has terminated
)

// Handshake is the first frame sent by the connecting side (host).  The host
// is oblivious to instance_id; it mints session_id and the guest uses it
// purely as a demux key.
type Handshake struct {
	// SessionID is a host-minted identifier for the session.  The guest is
	// oblivious to instance identity; session_id demuxes concurrent sessions
	// over the single fixed data-plane connection.
	SessionID string

	// ResumeFromOffset is the byte offset into the guest-authoritative output
	// ring from which the guest should begin replaying Data frames.  Zero
	// means "from the beginning" (or "this is a fresh attach").
	ResumeFromOffset uint64
}

// HandshakeAck is the reply sent by the serving side (guest) in response to a
// [Handshake].  After sending the ack the guest streams [Data] frames beginning
// at the requested offset.
type HandshakeAck struct {
	Status   AckStatus
	ExitCode int32 // meaningful only when Status == AckExited
}

// Data carries an incremental chunk of stdio bytes in one direction.
type Data struct {
	Tag     StreamTag
	Payload []byte // len(Payload) <= MaxDataPayload
}

// Winsize is an in-band terminal resize notification.  It travels as a regular
// frame on the data-plane connection rather than as a separate RPC on the
// control plane.
type Winsize struct {
	Rows    uint16
	Cols    uint16
	XPixels uint16
	YPixels uint16
}

// Exit signals that the session process has terminated.  It is the final frame
// the guest sends for a session; the output ring is complete after this point.
type Exit struct {
	Code int32
}

// Frame is a tagged union of all frame types produced by [Reader.ReadFrame].
// Exactly one of the pointer fields is non-nil; the non-nil field corresponds
// to Type.
type Frame struct {
	Type FrameType

	Handshake    *Handshake
	HandshakeAck *HandshakeAck
	Data         *Data
	Winsize      *Winsize
	Exit         *Exit
}

// Sentinel errors returned by this package.
var (
	// ErrDataPayloadTooLarge is returned by [Writer.WriteData] when the
	// payload exceeds [MaxDataPayload].  Callers must split large buffers.
	ErrDataPayloadTooLarge = errors.New("wire: data payload exceeds 64 KiB MaxDataPayload limit; caller must chunk")

	// ErrUnknownFrameType is returned by [Reader.ReadFrame] when the frame
	// header contains an unrecognised FrameType byte.
	ErrUnknownFrameType = errors.New("wire: unknown frame type")

	// ErrSessionIDTooLong is returned by [Writer.WriteHandshake] when the
	// session_id string is longer than 65535 bytes.
	ErrSessionIDTooLong = errors.New("wire: session_id exceeds 65535 bytes")
)

// Writer encodes frames and writes them to an underlying [io.Writer].  A single
// Writer is not safe for concurrent use; wrap it in a mutex if multiple
// goroutines must write to the same connection.
type Writer struct {
	w io.Writer
}

// NewWriter returns a Writer that encodes frames to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// WriteHandshake encodes and sends a [Handshake] frame.
func (wr *Writer) WriteHandshake(h Handshake) error {
	sid := []byte(h.SessionID)
	if len(sid) > 65535 {
		return ErrSessionIDTooLong
	}
	// payload: 2-byte sid-len + sid-bytes + 8-byte offset
	payloadLen := 2 + len(sid) + 8
	buf := make([]byte, headerLen+payloadLen)
	binary.BigEndian.PutUint32(buf[0:], uint32(payloadLen))
	buf[4] = byte(FrameHandshake)
	binary.BigEndian.PutUint16(buf[5:], uint16(len(sid)))
	copy(buf[7:], sid)
	binary.BigEndian.PutUint64(buf[7+len(sid):], h.ResumeFromOffset)
	_, err := wr.w.Write(buf)
	return err
}

// WriteHandshakeAck encodes and sends a [HandshakeAck] frame.
func (wr *Writer) WriteHandshakeAck(a HandshakeAck) error {
	// payload: 1-byte status + 4-byte exit_code (always present; 0 when alive)
	const payloadLen = 5
	buf := make([]byte, headerLen+payloadLen)
	binary.BigEndian.PutUint32(buf[0:], payloadLen)
	buf[4] = byte(FrameHandshakeAck)
	buf[5] = byte(a.Status)
	binary.BigEndian.PutUint32(buf[6:], uint32(a.ExitCode))
	_, err := wr.w.Write(buf)
	return err
}

// WriteData encodes and sends a [Data] frame.  Returns [ErrDataPayloadTooLarge]
// if len(payload) > [MaxDataPayload]; callers must chunk larger buffers.
func (wr *Writer) WriteData(tag StreamTag, payload []byte) error {
	if len(payload) > MaxDataPayload {
		return ErrDataPayloadTooLarge
	}
	// payload section: 1-byte stream_tag + payload bytes
	payloadLen := 1 + len(payload)
	buf := make([]byte, headerLen+payloadLen)
	binary.BigEndian.PutUint32(buf[0:], uint32(payloadLen))
	buf[4] = byte(FrameData)
	buf[5] = byte(tag)
	copy(buf[6:], payload)
	_, err := wr.w.Write(buf)
	return err
}

// WriteWinsize encodes and sends a [Winsize] frame.
func (wr *Writer) WriteWinsize(ws Winsize) error {
	// payload: 4 × uint16 = 8 bytes
	const payloadLen = 8
	buf := make([]byte, headerLen+payloadLen)
	binary.BigEndian.PutUint32(buf[0:], payloadLen)
	buf[4] = byte(FrameWinsize)
	binary.BigEndian.PutUint16(buf[5:], ws.Rows)
	binary.BigEndian.PutUint16(buf[7:], ws.Cols)
	binary.BigEndian.PutUint16(buf[9:], ws.XPixels)
	binary.BigEndian.PutUint16(buf[11:], ws.YPixels)
	_, err := wr.w.Write(buf)
	return err
}

// WriteStdinClose encodes and sends a [FrameStdinClose] frame.  It signals to
// the guest that the host stdin source has reached EOF.  The frame has no
// payload; its 5-byte header is the complete wire representation.
func (wr *Writer) WriteStdinClose() error {
	buf := make([]byte, headerLen)
	binary.BigEndian.PutUint32(buf[0:], 0) // payloadLen = 0
	buf[4] = byte(FrameStdinClose)
	_, err := wr.w.Write(buf)
	return err
}

// WriteExit encodes and sends an [Exit] frame.
func (wr *Writer) WriteExit(e Exit) error {
	// payload: int32 exit code = 4 bytes
	const payloadLen = 4
	buf := make([]byte, headerLen+payloadLen)
	binary.BigEndian.PutUint32(buf[0:], payloadLen)
	buf[4] = byte(FrameExit)
	binary.BigEndian.PutUint32(buf[5:], uint32(e.Code))
	_, err := wr.w.Write(buf)
	return err
}

// Reader decodes frames from an underlying [io.Reader].  A single Reader is
// not safe for concurrent use.
type Reader struct {
	r io.Reader
}

// NewReader returns a Reader that decodes frames from r.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// ReadFrame reads the next complete frame from the stream.  It blocks until the
// full frame (header + payload) has arrived.  Returns [io.EOF] when the
// underlying reader is cleanly closed before any bytes of the next frame are
// read; returns a wrapped error if the connection closes mid-frame.
func (rd *Reader) ReadFrame() (Frame, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(rd.r, hdr[:]); err != nil {
		return Frame{}, err // includes io.EOF / io.ErrUnexpectedEOF
	}
	payloadLen := binary.BigEndian.Uint32(hdr[0:])
	ft := FrameType(hdr[4])

	var payload []byte
	if payloadLen > 0 {
		payload = make([]byte, payloadLen)
		if _, err := io.ReadFull(rd.r, payload); err != nil {
			return Frame{}, fmt.Errorf("wire: reading payload for frame type %d: %w", ft, err)
		}
	}

	switch ft {
	case FrameHandshake:
		return decodeHandshake(payload)
	case FrameHandshakeAck:
		return decodeHandshakeAck(payload)
	case FrameData:
		return decodeData(payload)
	case FrameWinsize:
		return decodeWinsize(payload)
	case FrameExit:
		return decodeExit(payload)
	case FrameStdinClose:
		// Zero-payload sentinel; no further fields to decode.
		return Frame{Type: FrameStdinClose}, nil
	default:
		return Frame{}, fmt.Errorf("%w: %d", ErrUnknownFrameType, ft)
	}
}

// decodeHandshake parses a Handshake payload.
func decodeHandshake(p []byte) (Frame, error) {
	if len(p) < 2 {
		return Frame{}, errors.New("wire: handshake payload too short for sid length field")
	}
	sidLen := int(binary.BigEndian.Uint16(p[0:]))
	if len(p) < 2+sidLen+8 {
		return Frame{}, errors.New("wire: handshake payload truncated")
	}
	sid := string(p[2 : 2+sidLen])
	offset := binary.BigEndian.Uint64(p[2+sidLen:])
	return Frame{
		Type:      FrameHandshake,
		Handshake: &Handshake{SessionID: sid, ResumeFromOffset: offset},
	}, nil
}

// decodeHandshakeAck parses a HandshakeAck payload.
func decodeHandshakeAck(p []byte) (Frame, error) {
	if len(p) < 5 {
		return Frame{}, errors.New("wire: handshake_ack payload too short")
	}
	return Frame{
		Type: FrameHandshakeAck,
		HandshakeAck: &HandshakeAck{
			Status:   AckStatus(p[0]),
			ExitCode: int32(binary.BigEndian.Uint32(p[1:])),
		},
	}, nil
}

// decodeData parses a Data payload.
func decodeData(p []byte) (Frame, error) {
	if len(p) < 1 {
		return Frame{}, errors.New("wire: data payload too short for stream tag")
	}
	data := make([]byte, len(p)-1)
	copy(data, p[1:])
	return Frame{
		Type: FrameData,
		Data: &Data{Tag: StreamTag(p[0]), Payload: data},
	}, nil
}

// decodeWinsize parses a Winsize payload.
func decodeWinsize(p []byte) (Frame, error) {
	if len(p) < 8 {
		return Frame{}, errors.New("wire: winsize payload too short")
	}
	return Frame{
		Type: FrameWinsize,
		Winsize: &Winsize{
			Rows:    binary.BigEndian.Uint16(p[0:]),
			Cols:    binary.BigEndian.Uint16(p[2:]),
			XPixels: binary.BigEndian.Uint16(p[4:]),
			YPixels: binary.BigEndian.Uint16(p[6:]),
		},
	}, nil
}

// decodeExit parses an Exit payload.
func decodeExit(p []byte) (Frame, error) {
	if len(p) < 4 {
		return Frame{}, errors.New("wire: exit payload too short")
	}
	return Frame{
		Type: FrameExit,
		Exit: &Exit{Code: int32(binary.BigEndian.Uint32(p[0:]))},
	}, nil
}
