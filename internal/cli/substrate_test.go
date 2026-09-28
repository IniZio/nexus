package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/hostbin"
	"github.com/IniZio/nexus/internal/core/service"
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

// ── NEXUS_SUBSTRATE override tests ──────────────────────────────────────────

func TestSelectWith_NoneEnv_NoDriver(t *testing.T) {
	p := workingLinuxProbes("/usr/bin/cloud-hypervisor")
	drv, serr := selectWith(p, "none")
	if drv != nil {
		t.Error("NEXUS_SUBSTRATE=none: expected nil driver")
	}
	if serr == nil {
		t.Fatal("NEXUS_SUBSTRATE=none: expected non-nil SubstrateError")
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
		t.Error("NEXUS_SUBSTRATE=fake: expected nil driver")
	}
	if serr == nil {
		t.Fatal("NEXUS_SUBSTRATE=fake: expected SubstrateError")
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
	if v := os.Getenv("NEXUS_SUBSTRATE"); v == "none" {
		t.Skip("NEXUS_SUBSTRATE=none: substrate disabled, skipping positive-path test")
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
// missing, selectWith returns a SubstrateError that mentions NEXUS_KERNEL_PATH
// rather than producing a driver with an empty KernelPath that would fail
// later at VM boot with an opaque "Cannot open kernel file" error.
func TestSelectWith_MissingKernel_SubstrateError(t *testing.T) {
	t.Setenv("NEXUS_KERNEL_PATH", "/nonexistent-kernel-for-test/vmlinux")

	p := workingLinuxProbes("/usr/bin/cloud-hypervisor")
	drv, serr := selectWith(p, "")
	if drv != nil {
		t.Error("missing kernel: expected nil driver")
	}
	if serr == nil {
		t.Fatal("missing kernel: expected SubstrateError, got nil")
	}
	if !strings.Contains(serr.Msg, "NEXUS_KERNEL_PATH") {
		t.Errorf("SubstrateError.Msg should mention NEXUS_KERNEL_PATH; got: %s", serr.Msg)
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
	t.Setenv("NEXUS_KERNEL_PATH", "/nonexistent-kernel-for-test/vmlinux")

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
	if !strings.Contains(kernelCheck.Detail, "NEXUS_KERNEL_PATH") {
		t.Errorf("kernel check detail should mention NEXUS_KERNEL_PATH; got: %s", kernelCheck.Detail)
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

func TestRunAllChecks_BaseImage_Cached(t *testing.T) {
	kernelFile := t.TempDir() + "/vmlinux"
	if err := os.WriteFile(kernelFile, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_KERNEL_PATH", kernelFile)

	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "/usr/bin/cloud-hypervisor", nil },
		openKVM:  func() error { return nil },
		listImages: func(context.Context) ([]domain.Image, error) {
			return []domain.Image{{Ref: herdrDefaultImage, Size: 1024}}, nil
		},
		registryReachable: func(string) error { return nil },
	}
	checks, _ := runAllChecks(p)

	var baseCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "base_image" {
			baseCheck = &checks[i]
			break
		}
	}
	if baseCheck == nil {
		t.Fatalf("expected base_image check in %v", checks)
	}
	if !baseCheck.OK {
		t.Errorf("base_image check should be OK=true when image is cached; got Detail=%q", baseCheck.Detail)
	}
}

func TestRunAllChecks_BaseImage_MissingRegistryReachable(t *testing.T) {
	kernelFile := t.TempDir() + "/vmlinux"
	if err := os.WriteFile(kernelFile, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_KERNEL_PATH", kernelFile)

	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "/usr/bin/cloud-hypervisor", nil },
		openKVM:  func() error { return nil },
		listImages: func(context.Context) ([]domain.Image, error) {
			return []domain.Image{}, nil
		},
		registryReachable: func(string) error { return nil },
	}
	checks, drv := runAllChecks(p)

	var baseCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "base_image" {
			baseCheck = &checks[i]
			break
		}
	}
	if baseCheck == nil {
		t.Fatalf("expected base_image check in %v", checks)
	}
	if baseCheck.OK {
		t.Error("base_image check should be OK=false when image is not cached")
	}
	if !strings.Contains(baseCheck.Detail, "registry reachable") {
		t.Errorf("detail should mention registry reachable; got: %s", baseCheck.Detail)
	}
	if drv == nil {
		t.Error("drv should be non-nil: base_image check is informational and must not block driver construction")
	}
}

func TestRunAllChecks_BaseImage_MissingRegistryUnreachable(t *testing.T) {
	kernelFile := t.TempDir() + "/vmlinux"
	if err := os.WriteFile(kernelFile, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_KERNEL_PATH", kernelFile)

	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "/usr/bin/cloud-hypervisor", nil },
		openKVM:  func() error { return nil },
		listImages: func(context.Context) ([]domain.Image, error) {
			return []domain.Image{}, nil
		},
		registryReachable: func(string) error { return errors.New("unreachable") },
	}
	checks, drv := runAllChecks(p)

	var baseCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "base_image" {
			baseCheck = &checks[i]
			break
		}
	}
	if baseCheck == nil {
		t.Fatalf("expected base_image check in %v", checks)
	}
	if baseCheck.OK {
		t.Error("base_image check should be OK=false when registry is unreachable")
	}
	if !strings.Contains(baseCheck.Detail, "unreachable") {
		t.Errorf("detail should mention unreachable; got: %s", baseCheck.Detail)
	}
	if drv == nil {
		t.Error("drv should be non-nil: base_image check is informational and must not block driver construction")
	}
}

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

func TestSubstrateFindsCloudHypervisorOutsidePATH(t *testing.T) {
	dir := t.TempDir()
	chBin := dir + "/cloud-hypervisor"
	if err := os.WriteFile(chBin, []byte(""), 0755); err != nil {
		t.Fatal(err)
	}
	kernelFile := dir + "/vmlinux"
	if err := os.WriteFile(kernelFile, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_KERNEL_PATH", kernelFile)

	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		openKVM:  func() error { return nil },
		resolveHostBin: func(_ context.Context, name string) (hostbin.Resolved, error) {
			return hostbin.Resolved{Name: name, Path: chBin, Source: hostbin.SourceCache}, nil
		},
	}
	checks, _ := runAllChecks(p)

	var binCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "binary" {
			binCheck = &checks[i]
			break
		}
	}
	if binCheck == nil {
		t.Fatal("expected binary check in results")
	}
	if !binCheck.OK {
		t.Errorf("binary check should be OK when resolveHostBin succeeds; detail: %s", binCheck.Detail)
	}
	if !strings.Contains(binCheck.Detail, chBin) {
		t.Errorf("binary detail should contain path %q; got %q", chBin, binCheck.Detail)
	}
}

// TestDoctorToolChecks_HostbinOK verifies that when resolveHostBin succeeds,
// tool checks report OK=true with source and path in Detail.
//
// MUTATION PROOF: remove the mke2fs/e2fsck/resize2fs checks from runAllChecks →
// those names are never found in the check slice → this test goes RED.
func TestDoctorToolChecks_HostbinOK(t *testing.T) {
	p := probes{
		goos:    "linux",
		openKVM: func() error { return nil },
		resolveHostBin: func(_ context.Context, name string) (hostbin.Resolved, error) {
			return hostbin.Resolved{Name: name, Path: "/cache/" + name, Source: hostbin.SourceEmbedded}, nil
		},
	}
	checks, _ := runAllChecks(p)

	for _, name := range []string{"mke2fs", "e2fsck", "resize2fs"} {
		var chk *CheckResult
		for i := range checks {
			if checks[i].Name == "tool_"+name {
				c := checks[i]
				chk = &c
				break
			}
		}
		if chk == nil {
			t.Errorf("check %q not found in runAllChecks output", "tool_"+name)
			continue
		}
		if !chk.OK {
			t.Errorf("check %q: OK=false; want true; detail=%q", "tool_"+name, chk.Detail)
		}
		if !strings.Contains(chk.Detail, "embedded") {
			t.Errorf("check %q: Detail should contain source; got %q", "tool_"+name, chk.Detail)
		}
		if !strings.Contains(chk.Detail, "/cache/"+name) {
			t.Errorf("check %q: Detail should contain path; got %q", "tool_"+name, chk.Detail)
		}
	}
}

// TestDoctorToolChecks_HostbinFail verifies that when resolveHostBin fails,
// tool checks report OK=false with a remediation that does not mention apt or
// any distro package manager.
func TestDoctorToolChecks_HostbinFail(t *testing.T) {
	p := probes{
		goos:    "linux",
		openKVM: func() error { return nil },
		resolveHostBin: func(_ context.Context, name string) (hostbin.Resolved, error) {
			return hostbin.Resolved{}, errors.New("hostbin: host binary not found: " + name)
		},
	}
	checks, _ := runAllChecks(p)

	for _, name := range []string{"mke2fs", "e2fsck", "resize2fs"} {
		var chk *CheckResult
		for i := range checks {
			if checks[i].Name == "tool_"+name {
				c := checks[i]
				chk = &c
				break
			}
		}
		if chk == nil {
			t.Errorf("check %q not found in runAllChecks output", "tool_"+name)
			continue
		}
		if chk.OK {
			t.Errorf("check %q: OK=true; want false on resolver error", "tool_"+name)
		}
		if chk.Remediation == "" {
			t.Errorf("check %q: Remediation is empty", "tool_"+name)
		}
		if strings.Contains(chk.Remediation, "apt") {
			t.Errorf("check %q: Remediation must not mention apt; got %q", "tool_"+name, chk.Remediation)
		}
	}
}

// ── resolveHostBin injection tests ────────────────────────────────────────────

func TestRunAllChecks_BinaryEmbedded(t *testing.T) {
	kernelFile := t.TempDir() + "/vmlinux"
	if err := os.WriteFile(kernelFile, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_KERNEL_PATH", kernelFile)

	const chPath = "/xdg/nexus/artifacts/abc123/cloud-hypervisor"
	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		openKVM:  func() error { return nil },
		resolveHostBin: func(_ context.Context, name string) (hostbin.Resolved, error) {
			return hostbin.Resolved{Name: name, Path: chPath, Source: hostbin.SourceEmbedded}, nil
		},
	}
	checks, drv := runAllChecks(p)

	var binCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "binary" {
			binCheck = &checks[i]
			break
		}
	}
	if binCheck == nil {
		t.Fatal("expected binary check")
	}
	if !binCheck.OK {
		t.Errorf("binary check: OK=false; detail=%q", binCheck.Detail)
	}
	if !strings.Contains(binCheck.Detail, chPath) {
		t.Errorf("detail should contain path %q; got %q", chPath, binCheck.Detail)
	}
	if !strings.Contains(binCheck.Detail, "embedded") {
		t.Errorf("detail should contain source; got %q", binCheck.Detail)
	}
	if drv == nil {
		t.Error("expected non-nil driver when binary resolves via embedded artifact")
	}
}

func TestRunAllChecks_BinaryEnvSource(t *testing.T) {
	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		openKVM:  func() error { return nil },
		resolveHostBin: func(_ context.Context, name string) (hostbin.Resolved, error) {
			return hostbin.Resolved{Name: name, Path: "/custom/cloud-hypervisor", Source: hostbin.SourceEnv}, nil
		},
	}
	checks, _ := runAllChecks(p)

	var binCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "binary" {
			binCheck = &checks[i]
			break
		}
	}
	if binCheck == nil {
		t.Fatal("expected binary check")
	}
	if !binCheck.OK {
		t.Errorf("binary check: OK=false; detail=%q", binCheck.Detail)
	}
	if !strings.Contains(binCheck.Detail, "env") {
		t.Errorf("detail should contain source; got %q", binCheck.Detail)
	}
}

func TestRunAllChecks_BinaryResolverError(t *testing.T) {
	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		openKVM:  func() error { return nil },
		resolveHostBin: func(_ context.Context, name string) (hostbin.Resolved, error) {
			return hostbin.Resolved{}, errors.New("hostbin: host binary not found: cloud-hypervisor")
		},
	}
	checks, drv := runAllChecks(p)

	if drv != nil {
		t.Error("expected nil driver when binary resolution fails")
	}
	var binCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "binary" {
			binCheck = &checks[i]
			break
		}
	}
	if binCheck == nil {
		t.Fatal("expected binary check")
	}
	if binCheck.OK {
		t.Error("binary check: expected OK=false on resolver error")
	}
	if strings.Contains(binCheck.Remediation, "Install cloud-hypervisor") {
		t.Errorf("remediation must not tell user to install manually; got %q", binCheck.Remediation)
	}
	if binCheck.Remediation == "" {
		t.Error("remediation should be non-empty")
	}
}

func TestRunAllChecks_BinaryNilResolver(t *testing.T) {
	const chPath = "/usr/bin/cloud-hypervisor"
	p := probes{
		goos:     "linux",
		lookPath: func(string) (string, error) { return chPath, nil },
		openKVM:  func() error { return nil },
		// resolveHostBin intentionally nil — exercises the legacy fallback path.
	}
	checks, _ := runAllChecks(p)

	var binCheck *CheckResult
	for i := range checks {
		if checks[i].Name == "binary" {
			binCheck = &checks[i]
			break
		}
	}
	if binCheck == nil {
		t.Fatal("expected binary check")
	}
	if !binCheck.OK {
		t.Errorf("nil resolver fallback: expected OK=true via lookPath; detail=%q", binCheck.Detail)
	}
	if !strings.Contains(binCheck.Detail, chPath) {
		t.Errorf("detail should contain path %q; got %q", chPath, binCheck.Detail)
	}
}
