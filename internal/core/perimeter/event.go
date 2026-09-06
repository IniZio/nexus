package perimeter

import (
	"encoding/json"
	"fmt"
	"time"
)

// MarshalJSON encodes EgressDecision as its string name ("allow" or "deny")
// so that EgressEvent has a stable, human-readable JSON serialisation.
func (d EgressDecision) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON decodes an EgressDecision from its string name.
func (d *EgressDecision) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("EgressDecision: %w", err)
	}
	switch s {
	case "allow":
		*d = Allow
	case "deny":
		*d = Deny
	default:
		return fmt.Errorf("EgressDecision: unknown value %q", s)
	}
	return nil
}

// EgressEvent is a structured event emitted by the MITM proxy and the
// netfilter layer each time a connection attempt is evaluated. It is consumed
// by the `nexus3 egress log` CLI and egress-monitor subscribers.
//
// Verdict reuses the existing [EgressDecision] enum (Allow/Deny). JSON
// serialises Verdict as its string form ("allow" or "deny") via the
// MarshalJSON/UnmarshalJSON pair above.
type EgressEvent struct {
	Host      string         `json:"host"`
	Verdict   EgressDecision `json:"verdict"` // Allow or Deny
	Reason    string         `json:"reason"`
	Timestamp time.Time      `json:"timestamp"`
}
