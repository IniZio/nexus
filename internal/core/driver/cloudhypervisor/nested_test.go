//go:build linux

package cloudhypervisor

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestConfig_defaultNestedVirt verifies that a zero-value / default Config has
// NestedVirt=false, matching D-N3N-02 (opt-in, default-off).
func TestConfig_defaultNestedVirt(t *testing.T) {
	var cfg Config
	if cfg.NestedVirt {
		t.Error("Config.NestedVirt must default to false")
	}
}

// TestVMCPUsConfig_defaultPathSendsNestedFalse verifies that on the default
// (non-nested) path the vmCPUsConfig JSON payload explicitly contains
// "nested": false — NOT omitted. Cloud Hypervisor v53's CpusConfig.nested
// defaults to true when the field is absent (OpenAPI schema), so omitting it
// would silently enable guest KVM and violate D-N3N-02's default-off guarantee.
func TestVMCPUsConfig_defaultPathSendsNestedFalse(t *testing.T) {
	cfg := &vmCPUsConfig{
		BootVCPUs: 1,
		MaxVCPUs:  1,
		// Nested is zero value (false) — field must be present in JSON.
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	v, ok := m["nested"]
	if !ok {
		t.Fatalf("default vmCPUsConfig JSON must contain explicit 'nested' field (CH default is true); got %s", b)
	}
	if v != false {
		t.Errorf("default vmCPUsConfig JSON nested = %v, want false; got %s", v, b)
	}
}

// TestVMCPUsConfig_nestedTrueIncludesField verifies that when Nested is
// explicitly set to true (the NestedVirt=true path) the JSON payload includes
// "nested": true. Verified against CH v53 CpusConfig.nested field
// (cloud-hypervisor.yaml @ v53.0).
func TestVMCPUsConfig_nestedTrueIncludesField(t *testing.T) {
	cfg := &vmCPUsConfig{
		BootVCPUs: 2,
		MaxVCPUs:  2,
		Nested:    true,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	v, ok := m["nested"]
	if !ok {
		t.Fatalf("expected 'nested' field in JSON; got %s", b)
	}
	if v != true {
		t.Errorf("nested field = %v, want true", v)
	}
}

// TestNestedDetection_absent verifies that hostNestedKVMSupported returns false
// and nestedVirtPreflight returns a non-nil, descriptive error when both sysfs
// files are absent (or return "0").
func TestNestedDetection_absent(t *testing.T) {
	// Override the sysfs reader to simulate no nested-KVM support.
	orig := sysfsNestedParamReader
	sysfsNestedParamReader = func(path string) (string, error) {
		return "0", nil
	}
	t.Cleanup(func() { sysfsNestedParamReader = orig })

	if hostNestedKVMSupported() {
		t.Error("hostNestedKVMSupported() must return false when sysfs reports '0'")
	}

	err := nestedVirtPreflight()
	if err == nil {
		t.Fatal("nestedVirtPreflight() must return an error when nested KVM is unsupported")
	}
	// Error must be actionable: mention modprobe or the sysfs paths.
	msg := err.Error()
	if len(msg) < 50 {
		t.Errorf("error message too short to be actionable: %q", msg)
	}
}

// TestNestedDetection_intelEnabled verifies that hostNestedKVMSupported returns
// true and nestedVirtPreflight passes the KVM-support check when the Intel sysfs
// file reports "Y".
func TestNestedDetection_intelEnabled(t *testing.T) {
	// Override reader: Intel nested enabled, AMD absent.
	origReader := sysfsNestedParamReader
	sysfsNestedParamReader = func(path string) (string, error) {
		switch path {
		case "/sys/module/kvm_intel/parameters/nested":
			return "Y", nil
		default:
			return "", errors.New("not found")
		}
	}
	t.Cleanup(func() { sysfsNestedParamReader = origReader })

	// Override /dev/kvm opener to succeed.
	origOpener := kvmDeviceOpener
	kvmDeviceOpener = func() error { return nil }
	t.Cleanup(func() { kvmDeviceOpener = origOpener })

	if !hostNestedKVMSupported() {
		t.Error("hostNestedKVMSupported() must return true when kvm_intel nested='Y'")
	}
	if err := nestedVirtPreflight(); err != nil {
		t.Errorf("nestedVirtPreflight() returned unexpected error: %v", err)
	}
}

// TestNestedDetection_kvmDeviceInaccessible verifies that nestedVirtPreflight
// returns an actionable error when the host supports nested KVM but /dev/kvm
// is not accessible (e.g. user is not in the kvm group).
func TestNestedDetection_kvmDeviceInaccessible(t *testing.T) {
	// Override reader: nested enabled.
	origReader := sysfsNestedParamReader
	sysfsNestedParamReader = func(path string) (string, error) {
		return "1", nil
	}
	t.Cleanup(func() { sysfsNestedParamReader = origReader })

	// Override /dev/kvm opener to fail.
	origOpener := kvmDeviceOpener
	kvmDeviceOpener = func() error { return errors.New("permission denied") }
	t.Cleanup(func() { kvmDeviceOpener = origOpener })

	err := nestedVirtPreflight()
	if err == nil {
		t.Fatal("nestedVirtPreflight() must return an error when /dev/kvm is inaccessible")
	}
	msg := err.Error()
	if len(msg) < 50 {
		t.Errorf("error message too short to be actionable: %q", msg)
	}
}
