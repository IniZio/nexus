package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// ipcEgressAllowPath is the HTTP path for the runtime egress-allow request.
// The handler is not wired in this wave; the path and codec are defined here
// so all consumers share one contract.
const ipcEgressAllowPath = "/supervisor/egress-allow"

// EgressAllowRequest is the JSON body of a POST to ipcEgressAllowPath.
// It asks the supervisor to dynamically add Host to the MITM allowset for
// this sandbox at runtime.
type EgressAllowRequest struct {
	Host string `json:"host"`
}

// EgressAllowResponse is the JSON body the supervisor returns for an
// egress-allow request.
type EgressAllowResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// EncodeEgressAllowRequest serialises req to JSON.
func EncodeEgressAllowRequest(req EgressAllowRequest) ([]byte, error) {
	return json.Marshal(req)
}

// DecodeEgressAllowRequest deserialises an EgressAllowRequest from r.
// It returns an error (never panics) when the input is malformed or Host is empty.
func DecodeEgressAllowRequest(r io.Reader) (EgressAllowRequest, error) {
	var req EgressAllowRequest
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return EgressAllowRequest{}, fmt.Errorf("egress-allow: decode request: %w", err)
	}
	if req.Host == "" {
		return EgressAllowRequest{}, fmt.Errorf("egress-allow: host is required")
	}
	return req, nil
}

// EncodeEgressAllowResponse serialises resp to JSON.
func EncodeEgressAllowResponse(resp EgressAllowResponse) ([]byte, error) {
	return json.Marshal(resp)
}

// DecodeEgressAllowResponse deserialises an EgressAllowResponse from r.
// It returns an error (never panics) when the input is malformed.
func DecodeEgressAllowResponse(r io.Reader) (EgressAllowResponse, error) {
	var resp EgressAllowResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return EgressAllowResponse{}, fmt.Errorf("egress-allow: decode response: %w", err)
	}
	return resp, nil
}

// RoundtripEgressAllow is a test-only helper that encodes req, decodes the
// result, and returns the round-tripped value. It is used to prove codec
// symmetry in tests without a live HTTP server.
func roundtripEgressAllow(req EgressAllowRequest) (EgressAllowRequest, error) {
	b, err := EncodeEgressAllowRequest(req)
	if err != nil {
		return EgressAllowRequest{}, err
	}
	return DecodeEgressAllowRequest(bytes.NewReader(b))
}

// RequestEgressAllow dials sockPath (the supervisor's Unix-domain socket) and
// sends a POST /supervisor/egress-allow request to add host to the running
// sandbox's perimeter at runtime. It returns a clear error when:
//   - sockPath does not exist or the supervisor has exited (dial failure),
//   - the supervisor responds with OK:false (e.g. malformed host, perimeter not
//     yet ready), or
//   - the response cannot be decoded.
//
// T6's "nexus3 egress allow" CLI calls this after resolving the sandbox's
// supervisor sock path via supervisor.SockPath(stateDir).
func RequestEgressAllow(ctx context.Context, sockPath string, host string) error {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	body, err := EncodeEgressAllowRequest(EgressAllowRequest{Host: host})
	if err != nil {
		return fmt.Errorf("egress-allow: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+ipcEgressAllowPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("egress-allow: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("egress-allow: dial %s: %w", sockPath, err)
	}
	defer resp.Body.Close()
	result, err := DecodeEgressAllowResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("egress-allow: decode response (status %d): %w", resp.StatusCode, err)
	}
	if !result.OK {
		return fmt.Errorf("egress-allow: server error: %s", result.Error)
	}
	return nil
}

func roundtripEgressAllowResponse(resp EgressAllowResponse) (EgressAllowResponse, error) {
	b, err := EncodeEgressAllowResponse(resp)
	if err != nil {
		return EgressAllowResponse{}, err
	}
	return DecodeEgressAllowResponse(bytes.NewReader(b))
}
