package resize

// Wire codec for the guest↔host telemetry and resize-command channel.
//
// Protocol shape (D-DC-10): the host opens a vsock connection to guest port
// [TelemetryVsockPort] (3002), writes one newline-terminated JSON message,
// and reads one newline-terminated JSON reply. Every message is wrapped in an
// [envelope] that carries the codec version; a version mismatch causes an
// explicit error rather than silent misparse.
//
// Message kinds and direction:
//
//	sample.request  host→guest  ask the guest for a telemetry sample
//	sample.response guest→host  carry a [Sample]
//	disk.grow       host→guest  ask the guest to run resize2fs
//	disk.grew       guest→host  carry the resulting size (or error string)
//	error           guest→host  unrecoverable error the guest wants the host to log
//
// Versioning: increment [wireVersion] on any breaking change to the JSON
// shape (renamed field, removed field, changed type). The guest agent and host
// governor are built at different commits in long-running deployments; loud
// mismatch detection is preferable to silent misparse.

import (
	"encoding/json"
	"fmt"
	"io"
)

// wireVersion is the codec version embedded in every envelope.
// Increment on any breaking wire-shape change.
const wireVersion = 1

// msgKind is the discriminator for envelope.Kind.
type msgKind string

const (
	kindSampleRequest  msgKind = "sample.request"
	kindSampleResponse msgKind = "sample.response"
	kindGrowRequest    msgKind = "disk.grow"
	kindGrowResponse   msgKind = "disk.grew"
	kindError          msgKind = "error"
)

// envelope is the outer wrapper written to the wire for every message.
// It is unexported; callers use the typed Encode*/Decode* functions.
type envelope struct {
	Version int             `json:"v"`
	Kind    msgKind         `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// SampleRequest is sent host→guest to request a telemetry sample.
// It carries no fields; the kind field in the envelope is sufficient.
type SampleRequest struct{}

// SampleResponse is sent guest→host in reply to a [SampleRequest].
type SampleResponse struct {
	Sample Sample `json:"sample"`
}

// GrowRequest is sent host→guest to ask the guest to run resize2fs against
// the workspace disk. The host grows the backing file and updates the CH
// block device before sending this command; resize2fs must see the expanded
// device or it will refuse with "device size has not changed".
//
// DiskIndex is 0-based into ExtraDisks. The guest derives the block device
// path from it: ExtraDisks[0] → /dev/vdb, [1] → /dev/vdc, and so on.
// Sending an index rather than a path is deliberate: a hardcoded path such
// as /dev/vdb is wrong when ExtraDisks has more than one entry, and Half A
// already produced a bug of exactly that shape (motive.md §HB — Gap 2).
type GrowRequest struct {
	// DiskIndex is the ExtraDisks slot (0-based) to resize.
	DiskIndex int `json:"disk_index"`
	// TargetBytes is the new total device size after the host has already
	// expanded the backing file. resize2fs sizes the filesystem to fill it.
	TargetBytes int64 `json:"target_bytes"`
}

// GrowResponse is sent guest→host after resize2fs completes. On success
// ResultBytes holds the actual filesystem size reported by resize2fs.
// On failure Error is non-empty and ResultBytes is 0.
type GrowResponse struct {
	// ResultBytes is the new filesystem capacity in bytes, as reported by
	// resize2fs. Zero means the grow failed; check Error.
	ResultBytes int64 `json:"result_bytes"`
	// Error is non-empty when resize2fs (or the ext4 check before it) failed.
	// The governor logs this and does not retry until the next eval tick.
	Error string `json:"error,omitempty"`
}

// ErrorResponse is sent guest→host when the guest cannot handle a request
// (e.g. unrecognised kind, internal panic). The host logs the message.
type ErrorResponse struct {
	Message string `json:"message"`
}

// EncodeSampleRequest writes a [SampleRequest] to w as a single JSON line.
func EncodeSampleRequest(w io.Writer) error {
	return encode(w, kindSampleRequest, SampleRequest{})
}

// DecodeSampleRequest reads and decodes a [SampleRequest] from r.
// Returns a version-mismatch or kind-mismatch error if the envelope does not
// describe a sample.request at the expected wire version.
func DecodeSampleRequest(r io.Reader) (SampleRequest, error) {
	var req SampleRequest
	if err := decode(r, kindSampleRequest, &req); err != nil {
		return SampleRequest{}, err
	}
	return req, nil
}

// EncodeSampleResponse writes a [SampleResponse] to w as a single JSON line.
func EncodeSampleResponse(w io.Writer, resp SampleResponse) error {
	return encode(w, kindSampleResponse, resp)
}

// DecodeSampleResponse reads and decodes a [SampleResponse] from r.
func DecodeSampleResponse(r io.Reader) (SampleResponse, error) {
	var resp SampleResponse
	if err := decode(r, kindSampleResponse, &resp); err != nil {
		return SampleResponse{}, err
	}
	return resp, nil
}

// EncodeGrowRequest writes a [GrowRequest] to w as a single JSON line.
func EncodeGrowRequest(w io.Writer, req GrowRequest) error {
	return encode(w, kindGrowRequest, req)
}

// DecodeGrowRequest reads and decodes a [GrowRequest] from r.
func DecodeGrowRequest(r io.Reader) (GrowRequest, error) {
	var req GrowRequest
	if err := decode(r, kindGrowRequest, &req); err != nil {
		return GrowRequest{}, err
	}
	return req, nil
}

// EncodeGrowResponse writes a [GrowResponse] to w as a single JSON line.
func EncodeGrowResponse(w io.Writer, resp GrowResponse) error {
	return encode(w, kindGrowResponse, resp)
}

// DecodeGrowResponse reads and decodes a [GrowResponse] from r.
func DecodeGrowResponse(r io.Reader) (GrowResponse, error) {
	var resp GrowResponse
	if err := decode(r, kindGrowResponse, &resp); err != nil {
		return GrowResponse{}, err
	}
	return resp, nil
}

// EncodeErrorResponse writes an [ErrorResponse] to w as a single JSON line.
func EncodeErrorResponse(w io.Writer, resp ErrorResponse) error {
	return encode(w, kindError, resp)
}

// DecodeErrorResponse reads and decodes an [ErrorResponse] from r.
func DecodeErrorResponse(r io.Reader) (ErrorResponse, error) {
	var resp ErrorResponse
	if err := decode(r, kindError, &resp); err != nil {
		return ErrorResponse{}, err
	}
	return resp, nil
}

// encode marshals payload, wraps it in an envelope, and writes one JSON line
// to w. json.Encoder appends a newline, which is the message delimiter.
func encode(w io.Writer, kind msgKind, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("resize/wire: marshal %s payload: %w", kind, err)
	}
	env := envelope{
		Version: wireVersion,
		Kind:    kind,
		Payload: raw,
	}
	if err := json.NewEncoder(w).Encode(env); err != nil {
		return fmt.Errorf("resize/wire: encode %s envelope: %w", kind, err)
	}
	return nil
}

// decode reads one JSON line from r, checks the version and kind, then
// unmarshals the inner payload into dst.
func decode(r io.Reader, want msgKind, dst any) error {
	var env envelope
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		return fmt.Errorf("resize/wire: decode envelope: %w", err)
	}
	if env.Version != wireVersion {
		return fmt.Errorf("resize/wire: version mismatch: got %d, want %d (rebuild guest agent or host binary)", env.Version, wireVersion)
	}
	if env.Kind != want {
		return fmt.Errorf("resize/wire: kind mismatch: got %q, want %q", env.Kind, want)
	}
	if err := json.Unmarshal(env.Payload, dst); err != nil {
		return fmt.Errorf("resize/wire: unmarshal %s payload: %w", want, err)
	}
	return nil
}
