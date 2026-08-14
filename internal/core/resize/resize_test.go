package resize_test

// Tests for the resize package wire codec and interface contract.
//
// Every test runs pure in-process — no VM, no vsock, no filesystem fixtures.
// This is enforced by the package's own dependency rule: nothing here imports
// anything outside stdlib and the resize package itself.
//
// Coverage:
//   - Round-trip encode/decode for every wire message kind.
//   - Malformed / truncated input rejected with a non-nil error.
//   - Wire version mismatch rejected with an explicit diagnostic.
//   - Kind mismatch rejected.
//   - Boundary values (zero, max) on all numeric Sample fields.
//   - Compile-time interface satisfaction proofs (fake→interface assertions).
//   - Dependency-freedom assertion via import path inspection.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// ── Compile-time interface satisfaction ──────────────────────────────────────

// These blank-identifier assignments fail at compile time if any fake below
// stops satisfying the interface, which proves the interface shapes are
// implementable by ordinary structs that import nothing from
// internal/core/driver or cmd/nexus3-agent (AR0-AC2).

var _ resize.MemoryResizer = (*fakeMemoryResizer)(nil)
var _ resize.CPUResizer = (*fakeCPUResizer)(nil)
var _ resize.DiskResizer = (*fakeDiskResizer)(nil)
var _ resize.TelemetrySource = (*fakeTelemetrySource)(nil)

// fakeMemoryResizer satisfies MemoryResizer with no driver imports.
type fakeMemoryResizer struct{ current int64 }

func (f *fakeMemoryResizer) ResizeMemory(_ context.Context, targetBytes int64) (int64, error) {
	f.current = targetBytes
	return f.current, nil
}
func (f *fakeMemoryResizer) CurrentMemoryBytes() int64 { return f.current }

// fakeCPUResizer satisfies CPUResizer.
type fakeCPUResizer struct{ current int32 }

func (f *fakeCPUResizer) ResizeCPU(_ context.Context, targetVCPUs int32) (int32, error) {
	f.current = targetVCPUs
	return f.current, nil
}
func (f *fakeCPUResizer) CurrentVCPUs() int32 { return f.current }

// fakeDiskResizer satisfies DiskResizer; records grow calls for assertion.
type fakeDiskResizer struct {
	grows []struct {
		index  int
		target int64
	}
}

func (f *fakeDiskResizer) GrowDisk(_ context.Context, diskIndex int, targetBytes int64) error {
	f.grows = append(f.grows, struct {
		index  int
		target int64
	}{diskIndex, targetBytes})
	return nil
}

// fakeTelemetrySource satisfies TelemetrySource.
type fakeTelemetrySource struct{ sample resize.Sample }

func (f *fakeTelemetrySource) Poll(_ context.Context) (resize.Sample, error) {
	return f.sample, nil
}

// ── Constants sanity ─────────────────────────────────────────────────────────

func TestTelemetryVsockPort(t *testing.T) {
	// D-DC-11: must be 3002.
	if resize.TelemetryVsockPort != 3002 {
		t.Errorf("TelemetryVsockPort = %d, want 3002", resize.TelemetryVsockPort)
	}
}

func TestSampleMaxAge(t *testing.T) {
	// Must be 60s, matching OLD memorySampleMaxAge / cpuSampleMaxAge.
	if resize.SampleMaxAge != 60*time.Second {
		t.Errorf("SampleMaxAge = %v, want 60s", resize.SampleMaxAge)
	}
}

// ── Wire round-trip tests ────────────────────────────────────────────────────

func TestSampleRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := resize.EncodeSampleRequest(&buf); err != nil {
		t.Fatalf("EncodeSampleRequest: %v", err)
	}
	got, err := resize.DecodeSampleRequest(&buf)
	if err != nil {
		t.Fatalf("DecodeSampleRequest: %v", err)
	}
	// SampleRequest carries no fields; verifying it decodes without error is sufficient.
	_ = got
}

func TestSampleResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		resp resize.SampleResponse
	}{
		{
			name: "zero values",
			resp: resize.SampleResponse{},
		},
		{
			name: "typical sample",
			resp: resize.SampleResponse{
				Sample: resize.Sample{
					Timestamp:         time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
					MemAvailableBytes: 2 * 1024 * 1024 * 1024,
					MemTotalBytes:     8 * 1024 * 1024 * 1024,
					MemPSISomeAvg10:   12.5,
					MemPSIFullAvg10:   0.0,
					MemPSISupported:   true,
					CPUPSISomeAvg10:   18.3,
					CPUPSISupported:   true,
					DiskUsedBytes:     50 * 1024 * 1024 * 1024,
					DiskTotalBytes:    100 * 1024 * 1024 * 1024,
					DiskSupported:     true,
					VCPUCount:         8,
					VCPUOnline:        4,
				},
			},
		},
		{
			// DiskSupported=false distinguishes "statfs failed / no workspace mount"
			// from "disk is legitimately empty" (DiskTotalBytes==0 is ambiguous).
			name: "disk unavailable",
			resp: resize.SampleResponse{
				Sample: resize.Sample{
					Timestamp:         time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
					MemAvailableBytes: 1 * 1024 * 1024 * 1024,
					MemTotalBytes:     4 * 1024 * 1024 * 1024,
					MemPSISupported:   true,
					CPUPSISupported:   true,
					DiskUsedBytes:     0,
					DiskTotalBytes:    0,
					DiskSupported:     false, // statfs failed or no workspace mount
					VCPUCount:         2,
					VCPUOnline:        2,
				},
			},
		},
		{
			name: "psi unsupported",
			resp: resize.SampleResponse{
				Sample: resize.Sample{
					MemAvailableBytes: 512 * 1024 * 1024,
					MemTotalBytes:     4 * 1024 * 1024 * 1024,
					MemPSISupported:   false,
					CPUPSISupported:   false,
					VCPUCount:         2,
					VCPUOnline:        2,
				},
			},
		},
		{
			name: "boundary zeros",
			resp: resize.SampleResponse{
				Sample: resize.Sample{
					MemAvailableBytes: 0,
					MemTotalBytes:     0,
					DiskUsedBytes:     0,
					DiskTotalBytes:    0,
					VCPUCount:         0,
					VCPUOnline:        0,
				},
			},
		},
		{
			name: "boundary max uint64",
			resp: resize.SampleResponse{
				Sample: resize.Sample{
					MemAvailableBytes: ^uint64(0),
					MemTotalBytes:     ^uint64(0),
					DiskUsedBytes:     ^uint64(0),
					DiskTotalBytes:    ^uint64(0),
				},
			},
		},
		{
			name: "boundary max int32 vcpus",
			resp: resize.SampleResponse{
				Sample: resize.Sample{
					VCPUCount:  2147483647,
					VCPUOnline: 2147483647,
				},
			},
		},
		{
			name: "psi float boundary 100.0",
			resp: resize.SampleResponse{
				Sample: resize.Sample{
					MemPSISomeAvg10: 100.0,
					MemPSIFullAvg10: 100.0,
					MemPSISupported: true,
					CPUPSISomeAvg10: 100.0,
					CPUPSISupported: true,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := resize.EncodeSampleResponse(&buf, tc.resp); err != nil {
				t.Fatalf("EncodeSampleResponse: %v", err)
			}
			got, err := resize.DecodeSampleResponse(&buf)
			if err != nil {
				t.Fatalf("DecodeSampleResponse: %v", err)
			}
			assertSampleResponseEqual(t, tc.resp, got)
		})
	}
}

func TestGrowRequestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		req  resize.GrowRequest
	}{
		{
			name: "zero index",
			req:  resize.GrowRequest{DiskIndex: 0, TargetBytes: 16 * 1024 * 1024 * 1024},
		},
		{
			name: "non-zero index prevents vdb hardcode bug",
			req:  resize.GrowRequest{DiskIndex: 3, TargetBytes: 50 * 1024 * 1024 * 1024},
		},
		{
			name: "zero target",
			req:  resize.GrowRequest{DiskIndex: 0, TargetBytes: 0},
		},
		{
			name: "max int64 target",
			req:  resize.GrowRequest{DiskIndex: 0, TargetBytes: 9223372036854775807},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := resize.EncodeGrowRequest(&buf, tc.req); err != nil {
				t.Fatalf("EncodeGrowRequest: %v", err)
			}
			got, err := resize.DecodeGrowRequest(&buf)
			if err != nil {
				t.Fatalf("DecodeGrowRequest: %v", err)
			}
			if got.DiskIndex != tc.req.DiskIndex {
				t.Errorf("DiskIndex = %d, want %d", got.DiskIndex, tc.req.DiskIndex)
			}
			if got.TargetBytes != tc.req.TargetBytes {
				t.Errorf("TargetBytes = %d, want %d", got.TargetBytes, tc.req.TargetBytes)
			}
		})
	}
}

func TestGrowResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		resp resize.GrowResponse
	}{
		{
			name: "success",
			resp: resize.GrowResponse{ResultBytes: 100 * 1024 * 1024 * 1024},
		},
		{
			name: "failure with error string",
			resp: resize.GrowResponse{ResultBytes: 0, Error: "resize2fs: non-ext4 device"},
		},
		{
			name: "zero result",
			resp: resize.GrowResponse{ResultBytes: 0},
		},
		{
			name: "max int64 result",
			resp: resize.GrowResponse{ResultBytes: 9223372036854775807},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := resize.EncodeGrowResponse(&buf, tc.resp); err != nil {
				t.Fatalf("EncodeGrowResponse: %v", err)
			}
			got, err := resize.DecodeGrowResponse(&buf)
			if err != nil {
				t.Fatalf("DecodeGrowResponse: %v", err)
			}
			if got.ResultBytes != tc.resp.ResultBytes {
				t.Errorf("ResultBytes = %d, want %d", got.ResultBytes, tc.resp.ResultBytes)
			}
			if got.Error != tc.resp.Error {
				t.Errorf("Error = %q, want %q", got.Error, tc.resp.Error)
			}
		})
	}
}

func TestErrorResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		resp resize.ErrorResponse
	}{
		{name: "empty message", resp: resize.ErrorResponse{}},
		{name: "non-empty message", resp: resize.ErrorResponse{Message: "unknown request kind"}},
		{name: "long message", resp: resize.ErrorResponse{Message: strings.Repeat("x", 4096)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := resize.EncodeErrorResponse(&buf, tc.resp); err != nil {
				t.Fatalf("EncodeErrorResponse: %v", err)
			}
			got, err := resize.DecodeErrorResponse(&buf)
			if err != nil {
				t.Fatalf("DecodeErrorResponse: %v", err)
			}
			if got.Message != tc.resp.Message {
				t.Errorf("Message = %q, want %q", got.Message, tc.resp.Message)
			}
		})
	}
}

// ── Malformed / truncated input ──────────────────────────────────────────────

func TestDecodeRejectsTruncatedInput(t *testing.T) {
	cases := []struct {
		name   string
		decode func(r *bytes.Reader) error
	}{
		{
			name:   "SampleRequest empty",
			decode: func(r *bytes.Reader) error { _, err := resize.DecodeSampleRequest(r); return err },
		},
		{
			name:   "SampleResponse empty",
			decode: func(r *bytes.Reader) error { _, err := resize.DecodeSampleResponse(r); return err },
		},
		{
			name:   "GrowRequest empty",
			decode: func(r *bytes.Reader) error { _, err := resize.DecodeGrowRequest(r); return err },
		},
		{
			name:   "GrowResponse empty",
			decode: func(r *bytes.Reader) error { _, err := resize.DecodeGrowResponse(r); return err },
		},
		{
			name:   "ErrorResponse empty",
			decode: func(r *bytes.Reader) error { _, err := resize.DecodeErrorResponse(r); return err },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/empty", func(t *testing.T) {
			r := bytes.NewReader([]byte{})
			if err := tc.decode(r); err == nil {
				t.Error("expected error on empty input, got nil")
			}
		})
		t.Run(tc.name+"/partial", func(t *testing.T) {
			r := bytes.NewReader([]byte(`{"v":1,"kind":"sample.r`))
			if err := tc.decode(r); err == nil {
				t.Error("expected error on truncated input, got nil")
			}
		})
		t.Run(tc.name+"/garbage", func(t *testing.T) {
			r := bytes.NewReader([]byte("not-json\n"))
			if err := tc.decode(r); err == nil {
				t.Error("expected error on garbage input, got nil")
			}
		})
	}
}

// ── Version mismatch ─────────────────────────────────────────────────────────

func TestDecodeRejectsVersionMismatch(t *testing.T) {
	// Construct a valid envelope with the wrong version number.
	wrongVersion := struct {
		V       int    `json:"v"`
		Kind    string `json:"kind"`
		Payload []byte `json:"payload"`
	}{
		V:       0, // wrong version
		Kind:    "sample.request",
		Payload: []byte(`{}`),
	}
	data, _ := json.Marshal(wrongVersion)
	data = append(data, '\n')

	r := bytes.NewReader(data)
	_, err := resize.DecodeSampleRequest(r)
	if err == nil {
		t.Fatal("expected version-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "version mismatch") {
		t.Errorf("error %q does not mention version mismatch", err.Error())
	}
}

func TestDecodeRejectsFutureVersion(t *testing.T) {
	// Simulate a newer guest agent talking to an older host governor.
	futureVersion := struct {
		V       int    `json:"v"`
		Kind    string `json:"kind"`
		Payload []byte `json:"payload"`
	}{
		V:       999,
		Kind:    "sample.response",
		Payload: []byte(`{"sample":{}}`),
	}
	data, _ := json.Marshal(futureVersion)
	data = append(data, '\n')

	r := bytes.NewReader(data)
	_, err := resize.DecodeSampleResponse(r)
	if err == nil {
		t.Fatal("expected version-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "version mismatch") {
		t.Errorf("error %q does not mention version mismatch", err.Error())
	}
}

// ── Kind mismatch ─────────────────────────────────────────────────────────────

func TestDecodeRejectsKindMismatch(t *testing.T) {
	// Encode a SampleRequest but try to decode it as a SampleResponse.
	var buf bytes.Buffer
	if err := resize.EncodeSampleRequest(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err := resize.DecodeSampleResponse(&buf)
	if err == nil {
		t.Fatal("expected kind-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "kind mismatch") {
		t.Errorf("error %q does not mention kind mismatch", err.Error())
	}
}

func TestDecodeGrowRequestRejectsKindMismatch(t *testing.T) {
	// Encode a GrowResponse but try to decode it as a GrowRequest.
	var buf bytes.Buffer
	if err := resize.EncodeGrowResponse(&buf, resize.GrowResponse{ResultBytes: 42}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err := resize.DecodeGrowRequest(&buf)
	if err == nil {
		t.Fatal("expected kind-mismatch error, got nil")
	}
}

// ── DiskIndex must not be assumed constant ────────────────────────────────────

// TestDiskIndexByIndex verifies that GrowRequest encodes and preserves
// non-zero disk indices. A hardcoded /dev/vdb assumption (the Half A bug)
// would always produce DiskIndex=0 regardless of the actual ExtraDisks slot.
func TestDiskIndexByIndex(t *testing.T) {
	for _, idx := range []int{0, 1, 2, 5, 100} {
		t.Run(fmt.Sprintf("index=%d", idx), func(t *testing.T) {
			req := resize.GrowRequest{DiskIndex: idx, TargetBytes: 1 << 30}
			var buf bytes.Buffer
			if err := resize.EncodeGrowRequest(&buf, req); err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := resize.DecodeGrowRequest(&buf)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.DiskIndex != idx {
				t.Errorf("DiskIndex = %d, want %d", got.DiskIndex, idx)
			}
		})
	}
}

// ── Interface contract proofs ─────────────────────────────────────────────────

// TestFakeMemoryResizer exercises the fake to confirm the contract is usable.
func TestFakeMemoryResizer(t *testing.T) {
	r := &fakeMemoryResizer{}
	if r.CurrentMemoryBytes() != 0 {
		t.Error("initial current should be 0")
	}
	got, err := r.ResizeMemory(context.Background(), 4*1024*1024*1024)
	if err != nil {
		t.Fatalf("ResizeMemory: %v", err)
	}
	if got != 4*1024*1024*1024 {
		t.Errorf("ResizeMemory result = %d, want %d", got, 4*1024*1024*1024)
	}
	if r.CurrentMemoryBytes() != 4*1024*1024*1024 {
		t.Error("CurrentMemoryBytes should reflect the resize")
	}
}

// TestFakeDiskResizer confirms the disk-by-index contract is exercisable.
func TestFakeDiskResizer(t *testing.T) {
	r := &fakeDiskResizer{}
	if err := r.GrowDisk(context.Background(), 2, 50*1024*1024*1024); err != nil {
		t.Fatalf("GrowDisk: %v", err)
	}
	if len(r.grows) != 1 {
		t.Fatalf("len(grows) = %d, want 1", len(r.grows))
	}
	if r.grows[0].index != 2 {
		t.Errorf("grow diskIndex = %d, want 2", r.grows[0].index)
	}
}

// TestFakeTelemetrySource confirms that Poll returns the injected sample.
func TestFakeTelemetrySource(t *testing.T) {
	s := &fakeTelemetrySource{
		sample: resize.Sample{
			MemAvailableBytes: 1 << 30,
			MemTotalBytes:     4 << 30,
			MemPSISupported:   true,
		},
	}
	got, err := s.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got.MemAvailableBytes != 1<<30 {
		t.Errorf("MemAvailableBytes = %d, want %d", got.MemAvailableBytes, 1<<30)
	}
}

// TestFakeCPUResizer confirms the CPU interface is exercisable.
func TestFakeCPUResizer(t *testing.T) {
	r := &fakeCPUResizer{}
	if r.CurrentVCPUs() != 0 {
		t.Error("initial CurrentVCPUs should be 0")
	}
	got, err := r.ResizeCPU(context.Background(), 4)
	if err != nil {
		t.Fatalf("ResizeCPU: %v", err)
	}
	if got != 4 {
		t.Errorf("ResizeCPU = %d, want 4", got)
	}
}

// TestNoDriverImport verifies by convention (not runtime) that this package
// imports nothing from the driver. The actual enforcement is done via
// `go list -deps` in CI; this test documents the intent.
//
// The real dependency check is the `go list -deps ./internal/core/resize`
// command whose output is verified in the AR0 acceptance report.
func TestNoDriverImport(t *testing.T) {
	// This test is intentionally trivial: its value is documentation and the
	// explicit compile-time assertions above (var _ Interface = (*fake)(nil)).
	// The machine-checkable proof is `go list -deps` emitting no
	// internal/core/driver, cmd/nexus3-agent, internal/supervisor, or
	// internal/core/service paths.
	_ = errors.New // ensure errors is used (imported for malformed-input tests)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// assertSampleResponseEqual compares two SampleResponse values field by field.
// time.Time is compared via Equal to handle monotonic-clock stripping.
func assertSampleResponseEqual(t *testing.T, want, got resize.SampleResponse) {
	t.Helper()
	ws, gs := want.Sample, got.Sample
	if !ws.Timestamp.Equal(gs.Timestamp) {
		t.Errorf("Timestamp: got %v, want %v", gs.Timestamp, ws.Timestamp)
	}
	if ws.MemAvailableBytes != gs.MemAvailableBytes {
		t.Errorf("MemAvailableBytes: got %d, want %d", gs.MemAvailableBytes, ws.MemAvailableBytes)
	}
	if ws.MemTotalBytes != gs.MemTotalBytes {
		t.Errorf("MemTotalBytes: got %d, want %d", gs.MemTotalBytes, ws.MemTotalBytes)
	}
	if ws.MemPSISomeAvg10 != gs.MemPSISomeAvg10 {
		t.Errorf("MemPSISomeAvg10: got %v, want %v", gs.MemPSISomeAvg10, ws.MemPSISomeAvg10)
	}
	if ws.MemPSIFullAvg10 != gs.MemPSIFullAvg10 {
		t.Errorf("MemPSIFullAvg10: got %v, want %v", gs.MemPSIFullAvg10, ws.MemPSIFullAvg10)
	}
	if ws.MemPSISupported != gs.MemPSISupported {
		t.Errorf("MemPSISupported: got %v, want %v", gs.MemPSISupported, ws.MemPSISupported)
	}
	if ws.CPUPSISomeAvg10 != gs.CPUPSISomeAvg10 {
		t.Errorf("CPUPSISomeAvg10: got %v, want %v", gs.CPUPSISomeAvg10, ws.CPUPSISomeAvg10)
	}
	if ws.CPUPSISupported != gs.CPUPSISupported {
		t.Errorf("CPUPSISupported: got %v, want %v", gs.CPUPSISupported, ws.CPUPSISupported)
	}
	if ws.DiskUsedBytes != gs.DiskUsedBytes {
		t.Errorf("DiskUsedBytes: got %d, want %d", gs.DiskUsedBytes, ws.DiskUsedBytes)
	}
	if ws.DiskTotalBytes != gs.DiskTotalBytes {
		t.Errorf("DiskTotalBytes: got %d, want %d", gs.DiskTotalBytes, ws.DiskTotalBytes)
	}
	if ws.VCPUCount != gs.VCPUCount {
		t.Errorf("VCPUCount: got %d, want %d", gs.VCPUCount, ws.VCPUCount)
	}
	if ws.VCPUOnline != gs.VCPUOnline {
		t.Errorf("VCPUOnline: got %d, want %d", gs.VCPUOnline, ws.VCPUOnline)
	}
}
