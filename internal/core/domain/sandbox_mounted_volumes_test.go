package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestMountedVolumes_PreChangeJSON proves that a sandbox record serialised
// before MountedVolumes was introduced (i.e. JSON that has no
// "mounted_volumes" key) still decodes without error and yields the zero
// value (nil slice).  This is NOT a round-trip of the new struct — it is a
// hardcoded literal that could never contain the new field.
//
// The JSON omits non-required fields (ID, Name, Project, Labels) and uses a
// valid State wire string. The zero-value SandboxID ([16]byte{}) marshals
// to the all-zero Crockford string; we simply don't include ID in the
// literal so it takes the zero value silently.
func TestMountedVolumes_PreChangeJSON(t *testing.T) {
	// Literal that a pre-change encoder could have written: no mounted_volumes key.
	const preChangeJSON = `{"state":"stopped","remove_on_exit":false}`

	var sb domain.Sandbox
	if err := json.Unmarshal([]byte(preChangeJSON), &sb); err != nil {
		t.Fatalf("unmarshal pre-change JSON: %v", err)
	}
	if sb.MountedVolumes != nil {
		t.Errorf("expected nil MountedVolumes from pre-change JSON, got %v", sb.MountedVolumes)
	}
}

// TestMountedVolumes_RoundTrip_Populated checks that a sandbox with a
// non-empty MountedVolumes slice survives a JSON round-trip with all four
// fields intact.
func TestMountedVolumes_RoundTrip_Populated(t *testing.T) {
	original := domain.Sandbox{
		Name:    "test-sandbox",
		Project: "proj",
		State:   domain.Running,
		MountedVolumes: []domain.VolumeAttachment{
			{Name: "my-vol", GuestPath: "/mnt/data", Kind: "dir", ReadOnly: false},
			{Name: "ro-vol", GuestPath: "/mnt/ro", Kind: "disk", ReadOnly: true},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded domain.Sandbox
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.MountedVolumes) != 2 {
		t.Fatalf("expected 2 MountedVolumes, got %d", len(decoded.MountedVolumes))
	}
	first := decoded.MountedVolumes[0]
	if first.Name != "my-vol" || first.GuestPath != "/mnt/data" || first.Kind != "dir" || first.ReadOnly != false {
		t.Errorf("first attachment mismatch: %+v", first)
	}
	second := decoded.MountedVolumes[1]
	if second.Name != "ro-vol" || second.GuestPath != "/mnt/ro" || second.Kind != "disk" || second.ReadOnly != true {
		t.Errorf("second attachment mismatch: %+v", second)
	}
}

// TestMountedVolumes_RoundTrip_Nil checks that a sandbox with a nil
// MountedVolumes survives a JSON round-trip (the field must be omitted from
// the marshalled form and decoded back to nil).
func TestMountedVolumes_RoundTrip_Nil(t *testing.T) {
	original := domain.Sandbox{
		Name:           "no-vol-sandbox",
		Project:        "proj",
		State:          domain.Stopped,
		MountedVolumes: nil,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// "mounted_volumes" key must not appear when the slice is nil.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to raw map: %v", err)
	}
	if _, ok := raw["mounted_volumes"]; ok {
		t.Error("mounted_volumes key present in JSON for nil slice; expected omitempty to suppress it")
	}

	var decoded domain.Sandbox
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.MountedVolumes != nil {
		t.Errorf("expected nil MountedVolumes after round-trip, got %v", decoded.MountedVolumes)
	}
}
