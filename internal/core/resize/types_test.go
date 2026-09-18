package resize_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/resize"
)

// rawEnvelope lets external tests inspect wire envelope fields without
// importing unexported types.
type rawEnvelope struct {
	V    int             `json:"v"`
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"payload"`
}

func TestEncodeStreamRequest_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := resize.EncodeStreamRequest(&buf); err != nil {
		t.Fatalf("EncodeStreamRequest: %v", err)
	}
	var env rawEnvelope
	if err := json.NewDecoder(&buf).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.V != 1 {
		t.Errorf("version: got %d, want 1", env.V)
	}
	if env.Kind != "sample.stream" {
		t.Errorf("kind: got %q, want %q", env.Kind, "sample.stream")
	}
}

func TestStreamDecoder_ThreeFramesThenEOF(t *testing.T) {
	var buf bytes.Buffer
	now := time.Now().UTC().Truncate(time.Second)
	for i := range 3 {
		resp := resize.SampleResponse{
			Sample: resize.Sample{
				Timestamp:         now,
				MemAvailableBytes: uint64(i + 1),
			},
		}
		if err := resize.EncodeSampleResponse(&buf, resp); err != nil {
			t.Fatalf("EncodeSampleResponse %d: %v", i, err)
		}
	}
	dec := resize.NewStreamDecoder(&buf)
	for i := range 3 {
		s, err := dec.Next()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if s.MemAvailableBytes != uint64(i+1) {
			t.Errorf("frame %d: MemAvailableBytes = %d, want %d", i, s.MemAvailableBytes, i+1)
		}
	}
	_, err := dec.Next()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF after last frame, got %v", err)
	}
}

func TestStreamDecoder_ErrorResponseFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := resize.EncodeErrorResponse(&buf, resize.ErrorResponse{Message: "internal failure"}); err != nil {
		t.Fatalf("EncodeErrorResponse: %v", err)
	}
	dec := resize.NewStreamDecoder(&buf)
	_, err := dec.Next()
	if err == nil {
		t.Fatal("expected error from ErrorResponse frame, got nil")
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("expected guest error, got EOF")
	}
}

func TestIsStreamUnsupported_LegacyMessage(t *testing.T) {
	legacyErr := errors.New("resize/wire: kind mismatch: unknown kind \"sample.stream\"")
	if !resize.IsStreamUnsupported(legacyErr) {
		t.Error("IsStreamUnsupported: expected true for legacy 'unknown kind' message")
	}
	sentinel := resize.ErrStreamUnsupported
	if !resize.IsStreamUnsupported(sentinel) {
		t.Error("IsStreamUnsupported: expected true for ErrStreamUnsupported sentinel")
	}
	wrapped := errors.Join(sentinel, errors.New("extra"))
	if !resize.IsStreamUnsupported(wrapped) {
		t.Error("IsStreamUnsupported: expected true for wrapped sentinel")
	}
	if resize.IsStreamUnsupported(errors.New("unrelated error")) {
		t.Error("IsStreamUnsupported: expected false for unrelated error")
	}
	if resize.IsStreamUnsupported(nil) {
		t.Error("IsStreamUnsupported: expected false for nil")
	}
}

func TestSampleRequest_V1Unchanged(t *testing.T) {
	var buf bytes.Buffer
	if err := resize.EncodeSampleRequest(&buf); err != nil {
		t.Fatalf("EncodeSampleRequest: %v", err)
	}
	req, err := resize.DecodeSampleRequest(&buf)
	if err != nil {
		t.Fatalf("DecodeSampleRequest: %v", err)
	}
	_ = req

	var respBuf bytes.Buffer
	now := time.Now().UTC().Truncate(time.Second)
	resp := resize.SampleResponse{Sample: resize.Sample{Timestamp: now, MemTotalBytes: 1024}}
	if err := resize.EncodeSampleResponse(&respBuf, resp); err != nil {
		t.Fatalf("EncodeSampleResponse: %v", err)
	}
	got, err := resize.DecodeSampleResponse(&respBuf)
	if err != nil {
		t.Fatalf("DecodeSampleResponse: %v", err)
	}
	if got.Sample.MemTotalBytes != 1024 {
		t.Errorf("MemTotalBytes: got %d, want 1024", got.Sample.MemTotalBytes)
	}
	if got.Sample.Trigger != "" {
		t.Errorf("Trigger: expected empty on polled sample, got %q", got.Sample.Trigger)
	}

	// Verify Trigger is omitted from JSON when empty.
	var rawBuf bytes.Buffer
	resize.EncodeSampleResponse(&rawBuf, resize.SampleResponse{Sample: resize.Sample{Timestamp: now}}) //nolint:errcheck
	raw := rawBuf.String()
	if bytes.Contains([]byte(raw), []byte(`"trigger"`)) {
		t.Errorf("'trigger' key present in JSON for empty Trigger: %s", raw)
	}
}

func TestSample_IsTriggered(t *testing.T) {
	cases := []struct {
		trigger string
		want    bool
	}{
		{resize.TriggerNone, false},
		{resize.TriggerHeartbeat, false},
		{resize.TriggerPSIMemory, true},
		{resize.TriggerPSICPU, true},
	}
	for _, c := range cases {
		s := resize.Sample{Trigger: c.trigger}
		if got := s.IsTriggered(); got != c.want {
			t.Errorf("Trigger=%q: IsTriggered()=%v, want %v", c.trigger, got, c.want)
		}
	}
}

var _ resize.TelemetryStream = (*fakeTelemetryStream)(nil)

type fakeTelemetryStream struct{}

func (f *fakeTelemetryStream) Stream(_ context.Context) (<-chan resize.Sample, <-chan error, error) {
	return nil, nil, nil
}
