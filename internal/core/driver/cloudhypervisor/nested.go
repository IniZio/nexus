//go:build linux

// nested.go — opt-in nested virtualisation support for the cloud-hypervisor
// driver. All entry points are Linux-only; the feature itself requires Linux
// KVM.
//
// Design decision D-N3N-02: nested virt MUST be opt-in and default-off because
// it widens the sandbox isolation perimeter (see Config.NestedVirt docs).
package cloudhypervisor

import (
	"fmt"
	"os"
	"strings"
)

// sysfsNestedParamReader is injectable for unit testing so tests can mock the
// sysfs files without modifying the host kernel module state.
// Production code uses the real os.ReadFile.
var sysfsNestedParamReader = func(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// kvmDeviceOpener is injectable for unit testing /dev/kvm access checks.
var kvmDeviceOpener = func() error {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	f.Close()
	return nil
}

// hostNestedKVMSupported returns true if the host kernel has nested KVM
// enabled for either Intel (kvm_intel) or AMD (kvm_amd).
//
// Ported from old nexus:
//
//	packages/nexus/internal/core/runtime/nested_vm_detector.go
//
// The sysfs paths are:
//
//	/sys/module/kvm_intel/parameters/nested — "1" or "Y" when enabled
//	/sys/module/kvm_amd/parameters/nested   — "1" or "Y" when enabled
//
// Returns false (not an error) when neither module is loaded or nested is
// disabled; the caller is responsible for converting this to an actionable error.
func hostNestedKVMSupported() bool {
	return checkNestedParam("/sys/module/kvm_intel/parameters/nested") ||
		checkNestedParam("/sys/module/kvm_amd/parameters/nested")
}

func checkNestedParam(path string) bool {
	val, err := sysfsNestedParamReader(path)
	if err != nil {
		return false
	}
	return strings.ToUpper(val) == "1" || strings.ToUpper(val) == "Y"
}

// nestedVirtPreflight runs the two checks that must pass before a nested VM
// can be started:
//
//  1. Host nested-KVM support: /sys/module/kvm_intel(amd)/parameters/nested
//     must report "1" or "Y". If neither module is loaded or nested is
//     disabled, the inner VM would run without hardware acceleration (unusably
//     slow or broken) — we fail loudly instead of silently booting.
//
//  2. /dev/kvm accessibility: the process (inside its user+network namespace —
//     the kvm supplementary group is preserved across the userns boundary via
//     GidMappingsEnableSetgroups=false in ch_netns.go:netnsChildAttr) must be
//     able to open /dev/kvm with read+write permissions.
//
// Returns a non-nil, user-actionable error if either check fails. On success
// returns nil and the caller may proceed to set CpusConfig.nested=true.
func nestedVirtPreflight() error {
	if !hostNestedKVMSupported() {
		return fmt.Errorf(
			"cloudhypervisor: nested virt requested (Config.NestedVirt=true / NEXUS_NESTED_VIRT=1) " +
				"but the host does not support nested KVM: " +
				"neither /sys/module/kvm_intel/parameters/nested nor " +
				"/sys/module/kvm_amd/parameters/nested reports '1' or 'Y'. " +
				"Enable nested virt in the host kernel or hypervisor " +
				"(e.g. modprobe kvm_intel nested=1 or kvm_amd nested=1) " +
				"before starting a nested sandbox",
		)
	}
	if err := kvmDeviceOpener(); err != nil {
		return fmt.Errorf(
			"cloudhypervisor: nested virt requested but cannot open /dev/kvm: %w. "+
				"Ensure the host user is in the 'kvm' group "+
				"(usermod -aG kvm $USER, then re-login) and that /dev/kvm exists",
			err,
		)
	}
	return nil
}
