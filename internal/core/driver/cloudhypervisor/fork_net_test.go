package cloudhypervisor

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// findNetTap

// TestFindNetTap_Present verifies that findNetTap returns the tap name of the
// first net device when config.json contains a "net" array.
func TestFindNetTap_Present(t *testing.T) {
	cfg := buildNetConfig([]netSpec{{tap: "nx3g-aabbccddee", mac: "52:54:00:01:02:03", numQueues: 2}})
	got, err := findNetTap(cfg)
	if err != nil {
		t.Fatalf("findNetTap: %v", err)
	}
	if got != "nx3g-aabbccddee" {
		t.Errorf("findNetTap: got %q, want %q", got, "nx3g-aabbccddee")
	}
}

// TestFindNetTap_AbsentField verifies that findNetTap returns errNoNet when
// config.json has no "net" field.
func TestFindNetTap_AbsentField(t *testing.T) {
	cfg := []byte(`{"cpus":{"boot_vcpus":1,"max_vcpus":1}}`)
	_, err := findNetTap(cfg)
	if !errors.Is(err, errNoNet) {
		t.Errorf("findNetTap (no net field): got %v, want errNoNet", err)
	}
}

// TestFindNetTap_EmptyArray verifies that findNetTap returns errNoNet when the
// "net" field is an empty array.
func TestFindNetTap_EmptyArray(t *testing.T) {
	cfg := []byte(`{"net":[]}`)
	_, err := findNetTap(cfg)
	if !errors.Is(err, errNoNet) {
		t.Errorf("findNetTap (empty net array): got %v, want errNoNet", err)
	}
}

// TestFindNetTap_MultipleEntries verifies that findNetTap returns the tap from
// the first net entry when multiple entries are present.
func TestFindNetTap_MultipleEntries(t *testing.T) {
	cfg := buildNetConfig([]netSpec{
		{tap: "nx3g-first00000", mac: "52:54:00:01:02:03", numQueues: 2},
		{tap: "nx3g-second0000", mac: "52:54:00:04:05:06", numQueues: 2},
	})
	got, err := findNetTap(cfg)
	if err != nil {
		t.Fatalf("findNetTap: %v", err)
	}
	if got != "nx3g-first00000" {
		t.Errorf("findNetTap (multi-net): got %q, want first entry", got)
	}
}

// rewriteConfigNetTap

// TestRewriteConfigNetTap_SingleEntry verifies that rewriteConfigNetTap
// rewrites the tap field of the matching net entry, preserving all other fields.
func TestRewriteConfigNetTap_SingleEntry(t *testing.T) {
	cfg := buildNetConfig([]netSpec{{tap: "nx3g-parent0000", mac: "52:54:00:aa:bb:cc", numQueues: 2}})
	got, err := rewriteConfigNetTap(cfg, "nx3g-parent0000", "nx3g-child00000")
	if err != nil {
		t.Fatalf("rewriteConfigNetTap: %v", err)
	}

	// Verify tap was rewritten.
	tap, err := findNetTap(got)
	if err != nil {
		t.Fatalf("findNetTap on rewritten config: %v", err)
	}
	if tap != "nx3g-child00000" {
		t.Errorf("tap after rewrite: got %q, want %q", tap, "nx3g-child00000")
	}

	// Verify other fields are preserved.
	nets := parseNets(t, got)
	if len(nets) != 1 {
		t.Fatalf("net count: got %d, want 1", len(nets))
	}
	if mac := mustNetField(t, nets[0], "mac"); mac != "52:54:00:aa:bb:cc" {
		t.Errorf("mac after rewrite: got %q, want unchanged", mac)
	}
	if nq := mustNetFieldInt(t, nets[0], "num_queues"); nq != 2 {
		t.Errorf("num_queues after rewrite: got %d, want 2", nq)
	}
}

// TestRewriteConfigNetTap_MultipleEntries verifies that only the matching entry
// is rewritten; other entries are left unchanged.
func TestRewriteConfigNetTap_MultipleEntries(t *testing.T) {
	cfg := buildNetConfig([]netSpec{
		{tap: "nx3g-parent0000", mac: "52:54:00:01:02:03", numQueues: 2},
		{tap: "nx3g-other00000", mac: "52:54:00:04:05:06", numQueues: 4},
	})
	got, err := rewriteConfigNetTap(cfg, "nx3g-parent0000", "nx3g-child00000")
	if err != nil {
		t.Fatalf("rewriteConfigNetTap: %v", err)
	}

	nets := parseNets(t, got)
	if len(nets) != 2 {
		t.Fatalf("net count: got %d, want 2", len(nets))
	}
	// First entry: tap rewritten.
	if tap := mustNetField(t, nets[0], "tap"); tap != "nx3g-child00000" {
		t.Errorf("nets[0].tap: got %q, want %q", tap, "nx3g-child00000")
	}
	// Second entry: unchanged.
	if tap := mustNetField(t, nets[1], "tap"); tap != "nx3g-other00000" {
		t.Errorf("nets[1].tap: got %q (unchanged expected)", tap)
	}
}

// TestRewriteConfigNetTap_NoMatch verifies that rewriteConfigNetTap returns an
// error when no net entry has the specified oldTap.
func TestRewriteConfigNetTap_NoMatch(t *testing.T) {
	cfg := buildNetConfig([]netSpec{{tap: "nx3g-parent0000", mac: "52:54:00:aa:bb:cc", numQueues: 2}})
	_, err := rewriteConfigNetTap(cfg, "nx3g-doesnotexist", "nx3g-child00000")
	if err == nil {
		t.Fatal("rewriteConfigNetTap with no match: expected error, got nil")
	}
}

// TestRewriteConfigNetTap_PreservesTopLevelFields verifies that the top-level
// config fields (cpus, memory, etc.) are preserved after a net tap rewrite.
func TestRewriteConfigNetTap_PreservesTopLevelFields(t *testing.T) {
	// Build a config with both disk and net fields.
	cfg := buildFullConfig(
		[]diskSpec{{path: "/vm/parent.raw", imageType: "Raw"}},
		[]netSpec{{tap: "nx3g-parent0000", mac: "52:54:00:aa:bb:cc", numQueues: 2}},
	)
	got, err := rewriteConfigNetTap(cfg, "nx3g-parent0000", "nx3g-child00000")
	if err != nil {
		t.Fatalf("rewriteConfigNetTap: %v", err)
	}

	// Disk fields must survive the net tap rewrite.
	disks := parseDisks(t, got)
	if len(disks) != 1 {
		t.Fatalf("disk count after net rewrite: got %d, want 1", len(disks))
	}
	if p := mustDiskPath(t, disks[0]); p != "/vm/parent.raw" {
		t.Errorf("disk path after net rewrite: got %q, want unchanged", p)
	}
}

// TestRewriteConfigNetTap_RoundTrip verifies that chaining rewriteConfigDiskPath
// and rewriteConfigNetTap on the same config produces a valid config with both
// rewrites applied — as prepareChildRestoreDir does.
func TestRewriteConfigNetTap_RoundTrip(t *testing.T) {
	parentID := domain.NewSandboxID()
	childID := domain.NewSandboxID()
	parentGuestTap, _, _ := tapIfNames(parentID)
	childGuestTap, _, _ := tapIfNames(childID)
	parentDisk := "/vm/" + parentID.String() + ".raw"
	childDisk := "/vm/" + childID.String() + ".raw"

	cfg := buildFullConfig(
		[]diskSpec{{path: parentDisk, imageType: "Raw"}},
		[]netSpec{{tap: parentGuestTap, mac: "52:54:00:aa:bb:cc", numQueues: 2}},
	)

	// Apply disk rewrite first, then net tap rewrite (same order as prepareChildRestoreDir).
	after, err := rewriteConfigDiskPath(cfg, parentDisk, childDisk)
	if err != nil {
		t.Fatalf("rewriteConfigDiskPath: %v", err)
	}
	after, err = rewriteConfigNetTap(after, parentGuestTap, childGuestTap)
	if err != nil {
		t.Fatalf("rewriteConfigNetTap: %v", err)
	}

	// Verify disk was rewritten.
	disks := parseDisks(t, after)
	if p := mustDiskPath(t, disks[0]); p != childDisk {
		t.Errorf("disk path: got %q, want %q", p, childDisk)
	}

	// Verify tap was rewritten to childGuestTap.
	tap, err := findNetTap(after)
	if err != nil {
		t.Fatalf("findNetTap: %v", err)
	}
	if tap != childGuestTap {
		t.Errorf("tap: got %q, want %q", tap, childGuestTap)
	}

	// Verify the child's tap == tapIfNames(childID).guest.
	want, _, _ := tapIfNames(childID)
	if tap != want {
		t.Errorf("child tap %q != tapIfNames(childID).guest %q", tap, want)
	}
}

// test helpers

// netSpec describes one net entry in a synthetic config.json.
type netSpec struct {
	tap       string
	mac       string
	numQueues int
}

// buildNetConfig marshals a synthetic CH config.json with only the given net devices.
func buildNetConfig(nets []netSpec) []byte {
	type netEntry map[string]any
	entries := make([]netEntry, len(nets))
	for i, ns := range nets {
		entries[i] = netEntry{
			"tap":        ns.tap,
			"mac":        ns.mac,
			"num_queues": ns.numQueues,
		}
	}
	b, err := json.Marshal(map[string]any{"net": entries})
	if err != nil {
		panic("buildNetConfig: " + err.Error())
	}
	return b
}

// buildFullConfig builds a synthetic config.json with both disk and net fields.
func buildFullConfig(disks []diskSpec, nets []netSpec) []byte {
	type diskEntry map[string]any
	type netEntry map[string]any
	diskEntries := make([]diskEntry, len(disks))
	for i, ds := range disks {
		e := diskEntry{"path": ds.path}
		if ds.imageType != "" {
			e["image_type"] = ds.imageType
		}
		diskEntries[i] = e
	}
	netEntries := make([]netEntry, len(nets))
	for i, ns := range nets {
		netEntries[i] = netEntry{
			"tap":        ns.tap,
			"mac":        ns.mac,
			"num_queues": ns.numQueues,
		}
	}
	b, err := json.Marshal(map[string]any{
		"disks": diskEntries,
		"net":   netEntries,
	})
	if err != nil {
		panic("buildFullConfig: " + err.Error())
	}
	return b
}

// parseNets decodes the "net" array from a config.json blob.
func parseNets(t *testing.T, cfg []byte) []map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &top); err != nil {
		t.Fatalf("parseNets: unmarshal top: %v", err)
	}
	var nets []map[string]json.RawMessage
	if err := json.Unmarshal(top["net"], &nets); err != nil {
		t.Fatalf("parseNets: unmarshal net: %v", err)
	}
	return nets
}

// mustNetField decodes a string field from a net entry map.
func mustNetField(t *testing.T, net map[string]json.RawMessage, field string) string {
	t.Helper()
	raw, ok := net[field]
	if !ok {
		t.Fatalf("net entry has no %q field", field)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode net.%s: %v", field, err)
	}
	return s
}

// mustNetFieldInt decodes an integer field from a net entry map.
func mustNetFieldInt(t *testing.T, net map[string]json.RawMessage, field string) int {
	t.Helper()
	raw, ok := net[field]
	if !ok {
		t.Fatalf("net entry has no %q field", field)
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("decode net.%s: %v", field, err)
	}
	return n
}
