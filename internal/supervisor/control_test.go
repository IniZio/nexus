package supervisor

import (
	"strings"
	"testing"
)

// T0-AC3: egress-allow request/response encodes+decodes symmetrically;
// a malformed message is rejected with an error, not a panic.
func TestEgressAllow_RoundTrip(t *testing.T) {
	req := EgressAllowRequest{Host: "registry.npmjs.org"}
	got, err := roundtripEgressAllow(req)
	if err != nil {
		t.Fatalf("roundtrip request: %v", err)
	}
	if got.Host != req.Host {
		t.Errorf("Host: got %q, want %q", got.Host, req.Host)
	}

	resp := EgressAllowResponse{OK: true}
	gotResp, err := roundtripEgressAllowResponse(resp)
	if err != nil {
		t.Fatalf("roundtrip response: %v", err)
	}
	if !gotResp.OK {
		t.Error("OK: got false, want true")
	}

	// Deny response with error string.
	respDeny := EgressAllowResponse{OK: false, Error: "host not allowed"}
	gotDeny, err := roundtripEgressAllowResponse(respDeny)
	if err != nil {
		t.Fatalf("roundtrip deny response: %v", err)
	}
	if gotDeny.OK || gotDeny.Error != "host not allowed" {
		t.Errorf("deny response: got %+v", gotDeny)
	}
}

func TestEgressAllow_MalformedRequest(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"not json", `not-json`},
		{"empty host", `{"host":""}`},
		{"empty body", ``},
		{"unknown field", `{"host":"x","extra":1}`}, // DisallowUnknownFields
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeEgressAllowRequest(strings.NewReader(tc.input))
			if err == nil {
				t.Fatal("expected error for malformed input, got nil")
			}
		})
	}
}
