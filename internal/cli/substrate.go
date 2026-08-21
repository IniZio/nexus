package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"

	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// SubstrateError is returned by SelectSubstrate when no usable substrate
// driver can be configured. Msg names the specific check that failed so the
// operator has actionable context. Unwrap returns service.ErrNoSubstrate so
// errors.Is and sandboxCodeFor both map this to no_substrate without any
// special-casing in the callers.
type SubstrateError struct {
	// Msg is the human-readable failure reason, naming the specific check.
	Msg string
	// Remediation is actionable guidance for the operator; may be empty.
	Remediation string
}

func (e *SubstrateError) Error() string { return e.Msg }

// Unwrap implements the multi-error interface (Go 1.20+). Returning
// service.ErrNoSubstrate makes errors.Is(serr, service.ErrNoSubstrate) true,
// which in turn makes sandboxCodeFor emit no_substrate without any extra
// special-casing.
func (e *SubstrateError) Unwrap() []error { return []error{service.ErrNoSubstrate} }

// CheckResult is the outcome of a single capability probe. cmd_doctor.go uses
// a []CheckResult to produce its full report; substrate selection consumes the
// same slice to return the first failed check as a SubstrateError.
type CheckResult struct {
	// Name is a stable identifier for the check (e.g. "platform", "binary", "kvm").
	Name string
	// Description is a human-readable label for what was probed.
	Description string
	// OK is true if the check passed.
	OK bool
	// Detail is the observed value (OK=true) or the failure reason (OK=false).
	Detail string
	// Remediation is actionable text when OK is false; empty when OK is true.
	Remediation string
}

// probes holds the environment-query functions used by selectWith and
// runAllChecks. Tests replace individual fields to simulate non-Linux
// platforms, missing binaries, or inaccessible devices without requiring root
// access or a real hypervisor binary. The production path uses defaultProbes.
type probes struct {
	// goos is the operating-system name as Go reports it (e.g. "linux", "darwin").
	goos string
	// lookPath resolves a command name to an absolute path; mirrors exec.LookPath.
	lookPath func(string) (string, error)
	// openKVM opens /dev/kvm for read-write access and closes it immediately.
	// Returns nil on success; the caller distinguishes fs.ErrNotExist (device
	// absent) from fs.ErrPermission (group membership required) from other errors.
	openKVM func() error
}

func defaultProbes() probes {
	return probes{
		goos:     runtime.GOOS,
		lookPath: exec.LookPath,
		openKVM: func() error {
			f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
			if err != nil {
				return err
			}
			return f.Close()
		},
	}
}

// SelectSubstrate selects and returns a usable driver.Driver based on
// capability probes and the NEXUS3_SUBSTRATE environment variable.
//
// Invariant: a non-nil driver is always a real substrate driver. The noop
// driver is never returned — callers that need a no-op fallback (cmd_sandbox.go)
// substitute it themselves on a non-nil error.
//
// NEXUS3_SUBSTRATE controls selection:
//   - unset or "cloudhypervisor": run every capability check; return the CH driver if they all pass.
//   - "none": skip checks and return a SubstrateError (useful for testing store-only verbs).
//   - any other value: return a SubstrateError naming the invalid value.
//
// "fake" is intentionally not an accepted value. Inject the fake driver
// directly in Go test code; do not expose it via the environment where it
// could be used accidentally against real data.
func SelectSubstrate() (driver.Driver, *SubstrateError) {
	return selectWith(defaultProbes(), os.Getenv("NEXUS3_SUBSTRATE"))
}

// runAllChecks runs every capability probe and returns all results.
// It always runs all three checks (platform, binary, kvm) regardless of
// intermediate failures so that cmd_doctor.go can report every issue at once.
// drv is non-nil only when all checks pass.
func runAllChecks(p probes) (checks []CheckResult, drv driver.Driver) {
	// ── Check 1: Platform ────────────────────────────────────────────────────
	platOK := p.goos == "linux"
	platCheck := CheckResult{
		Name:        "platform",
		Description: "operating system is Linux",
		OK:          platOK,
	}
	if platOK {
		platCheck.Detail = fmt.Sprintf("platform is %q — Cloud Hypervisor is supported", p.goos)
	} else {
		platCheck.Detail = fmt.Sprintf("platform %q is not supported; Cloud Hypervisor requires Linux", p.goos)
		platCheck.Remediation = "The nexus3-vzd daemon (macOS / other platforms) is not yet implemented. Run nexus3 on a Linux host."
	}
	checks = append(checks, platCheck)

	// ── Check 2: Binary ──────────────────────────────────────────────────────
	var binaryPath string
	binCheck := CheckResult{
		Name:        "binary",
		Description: "cloud-hypervisor executable in PATH",
	}
	if !platOK {
		binCheck.OK = false
		binCheck.Detail = "skipped (platform is not Linux)"
	} else {
		path, err := p.lookPath("cloud-hypervisor")
		if err == nil {
			binaryPath = path
			binCheck.OK = true
			binCheck.Detail = path
		} else {
			binCheck.OK = false
			binCheck.Detail = "cloud-hypervisor not found in PATH"
			binCheck.Remediation = "Install cloud-hypervisor (https://github.com/cloud-hypervisor/cloud-hypervisor/releases) and ensure it is on your PATH."
		}
	}
	checks = append(checks, binCheck)

	// ── Check 3: KVM ─────────────────────────────────────────────────────────
	kvmCheck := CheckResult{
		Name:        "kvm",
		Description: "/dev/kvm is present and openable by this user",
	}
	if !platOK {
		kvmCheck.OK = false
		kvmCheck.Detail = "skipped (platform is not Linux)"
	} else {
		err := p.openKVM()
		if err == nil {
			kvmCheck.OK = true
			kvmCheck.Detail = "/dev/kvm is present and openable by this user"
		} else if errors.Is(err, fs.ErrNotExist) {
			kvmCheck.OK = false
			kvmCheck.Detail = "/dev/kvm not found: KVM is not available on this kernel or host"
			kvmCheck.Remediation = "Enable KVM in the kernel (CONFIG_KVM) or run on a host with hardware virtualisation support (Intel VT-x or AMD-V)."
		} else if errors.Is(err, fs.ErrPermission) {
			kvmCheck.OK = false
			kvmCheck.Detail = "/dev/kvm not openable: permission denied — is your user in the kvm group?"
			kvmCheck.Remediation = "Add your user to the kvm group: sudo usermod -aG kvm $USER (then log out and back in, or run newgrp kvm)."
		} else {
			kvmCheck.OK = false
			kvmCheck.Detail = fmt.Sprintf("/dev/kvm not openable: %v", err)
			kvmCheck.Remediation = "Check KVM device permissions and kernel module status (lsmod | grep kvm)."
		}
	}
	checks = append(checks, kvmCheck)

	// ── Construct driver if all checks passed ─────────────────────────────────
	if platOK && binCheck.OK && kvmCheck.OK {
		// Resolve the per-sandbox disk directory so that sandbox start can find
		// <diskDir>/<id>.raw for sandboxes created by sandbox create --file.
		var diskDir string
		if storeRoot, derr := store.DefaultRoot(); derr == nil {
			diskDir = storeRoot + "/disks"
		}

		// ── Check 4: Kernel ──────────────────────────────────────────────────
		// Validate the kernel path before driver construction. kernelPathFor()
		// is intentionally NOT used here: it swallows the resolution error and
		// returns a best-effort non-existent path, which causes cloudhypervisor.New
		// to succeed (path is not validated at construction time) and defer the
		// failure to VM boot, where CH emits an opaque "Cannot open kernel file"
		// error instead of a legible NEXUS3_KERNEL_PATH message.
		//
		// cmd_doctor calls runAllChecks and reports every check — including this
		// one — without aborting, so adding it here is additive for doctor.
		// selectWith (→ SelectSubstrate → operational start/recover paths) uses
		// the returned drv; when kernelErr != nil we return (checks, nil) so
		// selectWith surfaces this check as a SubstrateError with the actionable
		// kernel message.
		kernelPath, kernelErr := resolveKernelPath()
		kernelCheck := CheckResult{
			Name:        "kernel",
			Description: "guest kernel image (vmlinux) exists",
		}
		if kernelErr != nil {
			kernelCheck.OK = false
			kernelCheck.Detail = kernelErr.Error()
			kernelCheck.Remediation = "Set NEXUS3_KERNEL_PATH to the vmlinux image path, or place the kernel image at images/kernel/vmlinux-x86_64 alongside the nexus3 binary."
			checks = append(checks, kernelCheck)
			return checks, nil // drv stays nil; selectWith will return a SubstrateError
		}
		kernelCheck.OK = true
		kernelCheck.Detail = kernelPath
		checks = append(checks, kernelCheck)

		// ── Check 5: Virtiofsd ───────────────────────────────────────────────
		// Informational only — virtiofsd is required only when --mount is used.
		// A host without virtiofsd can still create sandboxes without --mount.
		// Absence does NOT block driver construction or selectWith.
		virtiofsdCheck := CheckResult{
			Name:        "virtiofsd",
			Description: "virtiofsd binary for live directory mounts (--mount)",
		}
		if vp, verr := resolveVirtiofsdPath(); verr != nil {
			virtiofsdCheck.OK = false
			virtiofsdCheck.Detail = "not found"
			virtiofsdCheck.Remediation = "Set NEXUS3_VIRTIOFSD_PATH to the virtiofsd binary path, or install virtiofsd (https://gitlab.com/virtio-fs/virtiofsd). Required only for --mount; sandboxes without --mount continue to work."
		} else {
			virtiofsdCheck.OK = true
			virtiofsdCheck.Detail = vp
		}
		checks = append(checks, virtiofsdCheck)

		d, err := cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath: binaryPath,
			KernelPath: kernelPath,
			DiskDir:    diskDir,
		})
		if err != nil {
			checks = append(checks, CheckResult{
				Name:        "driver_init",
				Description: "cloud-hypervisor driver initialization",
				OK:          false,
				Detail:      fmt.Sprintf("failed to initialize cloud-hypervisor driver: %v", err),
				Remediation: "Check XDG_RUNTIME_DIR and ensure the socket directory path is short enough (Linux sun_path limit: 107 bytes).",
			})
		} else {
			drv = d
		}
	}

	return checks, drv
}

// selectWith is the testable substrate selection logic. Tests inject a probes
// struct instead of relying on the real OS environment.
func selectWith(p probes, envVal string) (driver.Driver, *SubstrateError) {
	switch envVal {
	case "", "cloudhypervisor":
		// fall through to capability detection

	case "none":
		return nil, &SubstrateError{
			Msg:         "substrate disabled by NEXUS3_SUBSTRATE=none",
			Remediation: "Unset NEXUS3_SUBSTRATE or set it to 'cloudhypervisor' to enable auto-detection.",
		}

	default:
		return nil, &SubstrateError{
			Msg: fmt.Sprintf(
				"NEXUS3_SUBSTRATE=%q is not a recognised value; accepted values: cloudhypervisor, none",
				envVal,
			),
			Remediation: "Set NEXUS3_SUBSTRATE to 'cloudhypervisor', 'none', or unset it for auto-detection. (\"fake\" is not accepted outside Go tests — inject the fake driver directly in Go test code.)",
		}
	}

	checks, drv := runAllChecks(p)
	if drv != nil {
		return drv, nil
	}

	// Return the first failed check as the SubstrateError so the message is
	// specific about which check failed. If somehow all checks passed but drv
	// is nil (driver_init check was appended), that last check is the failure.
	for _, c := range checks {
		if !c.OK {
			return nil, &SubstrateError{
				Msg:         c.Detail,
				Remediation: c.Remediation,
			}
		}
	}

	// Unreachable: drv is nil only when at least one check failed.
	return nil, &SubstrateError{
		Msg: "substrate unavailable: driver initialization failed (run nexus3 doctor for details)",
	}
}
