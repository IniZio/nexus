package perimeter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// T0-AC2: EgressEvent round-trips through JSON with all fields preserved.
func TestEgressEvent_RoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	orig := EgressEvent{
		Host:      "api.github.com",
		Verdict:   Deny,
		Reason:    "host not in allowset",
		Timestamp: ts,
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verdict must appear as the string "deny", not an integer.
	if !strings.Contains(string(b), `"verdict":"deny"`) {
		t.Errorf("expected verdict to be JSON string \"deny\", got: %s", b)
	}

	var got EgressEvent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Host != orig.Host {
		t.Errorf("Host: got %q, want %q", got.Host, orig.Host)
	}
	if got.Verdict != orig.Verdict {
		t.Errorf("Verdict: got %v, want %v", got.Verdict, orig.Verdict)
	}
	if got.Reason != orig.Reason {
		t.Errorf("Reason: got %q, want %q", got.Reason, orig.Reason)
	}
	if !got.Timestamp.Equal(orig.Timestamp) {
		t.Errorf("Timestamp: got %v, want %v", got.Timestamp, orig.Timestamp)
	}

	// Allow verdict also round-trips as string.
	orig.Verdict = Allow
	b, _ = json.Marshal(orig)
	if !strings.Contains(string(b), `"verdict":"allow"`) {
		t.Errorf("expected verdict to be JSON string \"allow\", got: %s", b)
	}
	_ = json.Unmarshal(b, &got)
	if got.Verdict != Allow {
		t.Errorf("Allow round-trip: got %v", got.Verdict)
	}
}
