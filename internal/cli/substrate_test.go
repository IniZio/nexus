package cli

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// ── probe helpers ─────────────────────────────────────────────────────────────

// workingLinuxProbes returns probes that simulate a Linux host with a working
// cloud-hypervisor binary at binPath and a functioning /dev/kvm.
func workingLinuxProbes(binPath string) probes {
	return probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return binPath, nil },
		openKVM:  func() error { return nil },
	}
}

// ── NEXUS3_SUBSTRATE override tests ──────────────────────────────────────────

func TestSelectWith_NoneEnv_NoDriver(t *testing.T) {
	p := workingLinuxProbes("/usr/bin/cloud-hypervisor")
	drv, serr := selectWith(p, "none")
	if drv != nil {
		t.Error("NEXUS3_SUBSTRATE=none: expected nil driver")
	}
	if serr == nil {
		t.Fatal("NEXUS3_SUBSTRATE=none: expected non-nil SubstrateError")
	}
	if !errors.Is(serr, service.ErrNoSubstrate) {
		t.Errorf("SubstrateError must wrap service.ErrNoSubstrate; errors.Is returned false")
	}
}

func TestSelectWith_FakeEnv_Rejected(t *testing.T) {
	// "fake" must not be accepted via the environment — it would expose the fake
	// driver to production use and could cause data loss in recover.
	p := workingLinuxProbes("/usr/bin/cloud-hypervisor")
	drv, serr := selectWith(p, "fake")
	if drv != nil {
		t.Error("NEXUS3_SUBSTRATE=fake: expected nil driver")
	}
	if serr == nil {
		t.Fatal("NEXUS3_SUBSTRATE=fake: expected SubstrateError")
	}
	if !strings.Contains(serr.Msg, "fake") {
		t.Errorf("error message should name the rejected value; got: %s", serr.Msg)
	}
	if !errors.Is(serr, service.ErrNoSubstrate) {
		t.Errorf("SubstrateError must wrap service.ErrNoSubstrate")
	}
}

func TestSelectWith_UnknownEnv_Rejected(t *testing.T) {
	p := workingLinuxProbes("/usr/bin/cloud-hypervisor")
	drv, serr := selectWith(p, "totallyunknown")
	if drv != nil {
		t.Error("unknown override: expected nil driver")
	}
	if serr == nil {
		t.Fatal("unknown override: expected SubstrateError")
	}
	if !strings.Contains(serr.Msg, "totallyunknown") {
		t.Errorf("error message should name the rejected value; got: %s", serr.Msg)
	}
}

// ── Capability failure modes ──────────────────────────────────────────────────

func TestSelectWith_NonLinuxPlatform(t *testing.T) {
	p := probes{
		goos:     "darwin",
		lookPath: func(string) (string, error) { return "/usr/bin/cloud-hypervisor", nil },
		openKVM:  func() error { return nil },
	}
	drv, serr := selectWith(p, "")
	if drv != nil {
		t.Error("non-Linux platform: expected nil driver")
	}
	if serr == nil {
		t.Fatal("non-Linux platform: expected SubstrateError")
	}
	if !strings.Contains(serr.Msg, "darwin") {
		t.Errorf("error message should name the platform; got: %s", serr.Msg)
	}
	if !errors.Is(serr, service.ErrNoSubstrate) {
		t.Errorf("SubstrateError must wrap service.ErrNoSubstrate")
	}
}

func TestSelectWith_BinaryNotFound(t *testing.T) {
	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		openKVM:  func() error { return nil },
	}
	drv, serr := selectWith(p, "")
	if drv != nil {
		t.Error("binary not found: expected nil driver")
	}
	if serr == nil {
		t.Fatal("binary not found: expected SubstrateError")
	}
	if !strings.Contains(serr.Msg, "cloud-hypervisor not found in PATH") {
		t.Errorf("error message should state binary not found; got: %s", serr.Msg)
	}
	if !errors.Is(serr, service.ErrNoSubstrate) {
		t.Errorf("SubstrateError must wrap service.ErrNoSubstrate")
	}
}

func TestSelectWith_KVMAbsent(t *testing.T) {
	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "/usr/bin/cloud-hypervisor", nil },
		// Return a PathError that wraps fs.ErrNotExist, matching what os.OpenFile
		// returns when the device file does not exist.
		openKVM: func() error {
			return &os.PathError{Op: "open", Path: "/dev/kvm", Err: os.ErrNotExist}
		},
	}
	drv, serr := selectWith(p, "")
	if drv != nil {
		t.Error("kvm absent: expected nil driver")
	}
	if serr == nil {
		t.Fatal("kvm absent: expected SubstrateError")
	}
	if !strings.Contains(serr.Msg, "/dev/kvm not found") {
		t.Errorf("error message should mention /dev/kvm not found; got: %s", serr.Msg)
	}
	if !errors.Is(serr, service.ErrNoSubstrate) {
		t.Errorf("SubstrateError must wrap service.ErrNoSubstrate")
	}
}

func TestSelectWith_KVMPermissionDenied(t *testing.T) {
	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "/usr/bin/cloud-hypervisor", nil },
		openKVM: func() error {
			return &os.PathError{Op: "open", Path: "/dev/kvm", Err: os.ErrPermission}
		},
	}
	drv, serr := selectWith(p, "")
	if drv != nil {
		t.Error("kvm permission denied: expected nil driver")
	}
	if serr == nil {
		t.Fatal("kvm permission denied: expected SubstrateError")
	}
	if !strings.Contains(serr.Msg, "permission denied") {
		t.Errorf("error message should mention permission denied; got: %s", serr.Msg)
	}
	if !strings.Contains(serr.Remediation, "kvm group") {
		t.Errorf("remediation should mention kvm group; got: %s", serr.Remediation)
	}
	if !errors.Is(serr, service.ErrNoSubstrate) {
		t.Errorf("SubstrateError must wrap service.ErrNoSubstrate")
	}
}

// ── Positive path (real substrate, skip when unavailable) ────────────────────

// TestSelectSubstrate_PositivePath verifies that SelectSubstrate returns the
// CH driver when the binary and /dev/kvm are both available. This test skips
// cleanly on machines without KVM or cloud-hypervisor so CI on non-KVM hosts
// is not broken.
func TestSelectSubstrate_PositivePath(t *testing.T) {
	if v := os.Getenv("NEXUS3_SUBSTRATE"); v == "none" {
		t.Skip("NEXUS3_SUBSTRATE=none: substrate disabled, skipping positive-path test")
	}
	drv, serr := SelectSubstrate()
	if serr != nil {
		t.Skipf("substrate not available on this host (%v) — positive path test skipped", serr)
	}
	if drv == nil {
		t.Fatal("SelectSubstrate returned nil driver with nil error")
	}
	if drv.Name() != "cloud-hypervisor" {
		t.Errorf("driver name = %q, want \"cloud-hypervisor\"", drv.Name())
	}
}

// ── SubstrateError wraps ErrNoSubstrate ──────────────────────────────────────

func TestSubstrateError_WrapsErrNoSubstrate(t *testing.T) {
	e := &SubstrateError{Msg: "test failure"}
	if !errors.Is(e, service.ErrNoSubstrate) {
		t.Error("SubstrateError.Unwrap must include service.ErrNoSubstrate so errors.Is returns true")
	}
}

// ── Kernel check (check 4) ────────────────────────────────────────────────────

// TestSelectWith_MissingKernel_SubstrateError verifies that when the first
// three checks (platform, binary, kvm) all pass but the kernel image is
// missing, selectWith returns a SubstrateError that mentions NEXUS3_KERNEL_PATH
// rather than producing a driver with an empty KernelPath that would fail
// later at VM boot with an opaque "Cannot open kernel file" error.
func TestSelectWith_MissingKernel_SubstrateError(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "/nonexistent-kernel-for-test/vmlinux")

	p := workingLinuxProbes("/usr/bin/cloud-hypervisor")
	drv, serr := selectWith(p, "")
	if drv != nil {
		t.Error("missing kernel: expected nil driver")
	}
	if serr == nil {
		t.Fatal("missing kernel: expected SubstrateError, got nil")
	}
	if !strings.Contains(serr.Msg, "NEXUS3_KERNEL_PATH") {
		t.Errorf("SubstrateError.Msg should mention NEXUS3_KERNEL_PATH; got: %s", serr.Msg)
	}
	if !errors.Is(serr, service.ErrNoSubstrate) {
		t.Errorf("SubstrateError must wrap service.ErrNoSubstrate")
	}
}

// TestRunAllChecks_MissingKernel_KernelCheckPresent verifies that when all
// three platform/binary/kvm checks pass but the kernel is missing, runAllChecks
// appends a "kernel" CheckResult with OK=false (so cmd_doctor can report it)
// and returns a nil driver.
func TestRunAllChecks_MissingKernel_KernelCheckPresent(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "/nonexistent-kernel-for-test/vmlinux")

	p := workingLinuxProbes("/usr/bin/cloud-hypervisor")
	checks, drv := runAllChecks(p)
	if drv != nil {
		t.Error("missing kernel: expected nil driver")
	}

	// Find the kernel check in the results.
	var kernelCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "kernel" {
			kernelCheck = &checks[i]
			break
		}
	}
	if kernelCheck == nil {
		t.Fatalf("expected a \"kernel\" CheckResult in %v", checks)
	}
	if kernelCheck.OK {
		t.Error("kernel check should be OK=false when the kernel is missing")
	}
	if !strings.Contains(kernelCheck.Detail, "NEXUS3_KERNEL_PATH") {
		t.Errorf("kernel check detail should mention NEXUS3_KERNEL_PATH; got: %s", kernelCheck.Detail)
	}
	if kernelCheck.Remediation == "" {
		t.Error("kernel check should have non-empty Remediation")
	}
}

// ── runAllChecks always reports all checks ────────────────────────────────────

func TestRunAllChecks_NonLinux_ReportsAllThree(t *testing.T) {
	p := probes{
		goos:     "darwin",
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		openKVM:  func() error { return &os.PathError{Op: "open", Path: "/dev/kvm", Err: os.ErrNotExist} },
	}
	checks, drv := runAllChecks(p)
	if drv != nil {
		t.Error("expected nil driver when platform is not Linux")
	}
	// Must return at least the three core checks.
	if len(checks) < 3 {
		t.Errorf("expected at least 3 checks, got %d", len(checks))
	}
	// Platform check must have failed.
	if checks[0].Name != "platform" || checks[0].OK {
		t.Errorf("first check should be a failed platform check; got name=%q ok=%v", checks[0].Name, checks[0].OK)
	}
	// Binary and KVM checks must be present and marked not-OK (skipped due to platform).
	if len(checks) >= 2 && checks[1].OK {
		t.Errorf("binary check should not be OK on non-Linux platform")
	}
	if len(checks) >= 3 && checks[2].OK {
		t.Errorf("kvm check should not be OK on non-Linux platform")
	}
}

// TestRunAllChecks_AllFail_DriversNil verifies that a nil driver is returned
// when any capability check fails.
func TestRunAllChecks_AllFail_DriverNil(t *testing.T) {
	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		openKVM:  func() error { return &os.PathError{Op: "open", Path: "/dev/kvm", Err: os.ErrPermission} },
	}
	checks, drv := runAllChecks(p)
	if drv != nil {
		t.Error("expected nil driver when checks fail")
	}
	if len(checks) < 3 {
		t.Errorf("expected at least 3 checks, got %d", len(checks))
	}
}
