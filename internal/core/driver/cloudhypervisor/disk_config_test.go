// disk_config_test.go — unit tests for vmDiskConfig marshaling.
//
// Acceptance criteria:
//  1. DiskDirect_ExtraDisk: vmDiskConfig{Direct:true} marshals "direct":true.
//  2. DiskDirect_Rootfs:    vmDiskConfig for rootfs (Direct unset) does NOT
//     marshal "direct" key (omitempty suppresses false).
//  3. DiskDirect_FullConfig: vmConfig with rootfs vda + one ExtraDisk renders
//     vda without "direct" and vdb with "direct":true, proving the driver's
//     ExtraDisks loop sets Direct=true only for extra disks.
package cloudhypervisor

import (
	"encoding/json"
	"testing"
)

// TestDiskConfig_ExtraDisk_Direct verifies that a vmDiskConfig constructed
// with Direct:true marshals the "direct":true JSON field sent to CH.
// This is the O_DIRECT knob that bypasses host page cache for scratch disks.
func TestDiskConfig_ExtraDisk_Direct(t *testing.T) {
	d := vmDiskConfig{
		Path:      "/tmp/scratch.raw",
		ImageType: "Raw",
		Direct:    true,
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v; raw=%s", err, b)
	}
	if v, ok := m["direct"].(bool); !ok || !v {
		t.Errorf("direct = %v (ok=%v), want true; raw=%s", m["direct"], ok, b)
	}
	if v, _ := m["image_type"].(string); v != "Raw" {
		t.Errorf("image_type = %q, want Raw", v)
	}
}

// TestDiskConfig_Rootfs_NoDirect verifies that the rootfs vmDiskConfig (which
// never sets Direct) does NOT emit a "direct" JSON key. The omitempty tag on
// Direct suppresses the false zero-value so CH uses its default (buffered I/O).
func TestDiskConfig_Rootfs_NoDirect(t *testing.T) {
	d := vmDiskConfig{
		Path:      "/tmp/rootfs.raw",
		ImageType: "Raw",
		// Direct intentionally zero/false — rootfs always buffered I/O.
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v; raw=%s", err, b)
	}
	if _, present := m["direct"]; present {
		t.Errorf("direct key present in rootfs disk JSON (want omitted); raw=%s", b)
	}
}

// TestDiskConfig_FullConfig_DirectScoping verifies that a vmConfig with a
// rootfs disk (vda, Direct=false) and one extra disk (vdb, Direct=true)
// marshals correctly: vda has no "direct" key, vdb has "direct":true.
// This mirrors the driver's ExtraDisks loop which hardcodes Direct=true.
func TestDiskConfig_FullConfig_DirectScoping(t *testing.T) {
	cfg := vmConfig{
		Payload: vmPayloadConfig{Kernel: "/boot/kernel"},
		Memory:  &vmMemoryConfig{SizeBytes: 512 * 1024 * 1024},
		Disks: []vmDiskConfig{
			{Path: "/vm/rootfs.raw", ImageType: "Raw"},          // vda — no Direct
			{Path: "/vm/scratch.raw", ImageType: "Raw", Direct: true}, // vdb — O_DIRECT
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal cfg: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v; raw=%s", err, b)
	}

	disks, ok := m["disks"].([]any)
	if !ok || len(disks) != 2 {
		t.Fatalf("disks: expected 2, got %v; raw=%s", m["disks"], b)
	}

	// vda — rootfs: must NOT have "direct"
	vda, _ := disks[0].(map[string]any)
	if _, present := vda["direct"]; present {
		t.Errorf("vda has unexpected direct key; raw=%s", b)
	}
	if p, _ := vda["path"].(string); p != "/vm/rootfs.raw" {
		t.Errorf("vda path = %q, want /vm/rootfs.raw", p)
	}

	// vdb — scratch: must have "direct":true
	vdb, _ := disks[1].(map[string]any)
	if v, ok := vdb["direct"].(bool); !ok || !v {
		t.Errorf("vdb direct = %v (ok=%v), want true; raw=%s", vdb["direct"], ok, b)
	}
	if p, _ := vdb["path"].(string); p != "/vm/scratch.raw" {
		t.Errorf("vdb path = %q, want /vm/scratch.raw", p)
	}
}
