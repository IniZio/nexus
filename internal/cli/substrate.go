package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/hostbin"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// SubstrateError is returned by SelectSubstrate when no usable substrate
// driver can be configured. Unwrap returns service.ErrNoSubstrate so
// errors.Is maps this to no_substrate.
type SubstrateError struct {
	Msg         string
	Remediation string
}

func (e *SubstrateError) Error() string { return e.Msg }

func (e *SubstrateError) Unwrap() []error { return []error{service.ErrNoSubstrate} }

// CheckResult is the outcome of a single capability probe.
type CheckResult struct {
	Name        string
	Description string
	OK          bool
	Detail      string
	Remediation string
	// Optional marks a check whose failure disables a feature but does not
	// block core sandbox create/exec/--mount.
	Optional bool
}

type probes struct {
	goos              string
	lookPath          func(string) (string, error)
	openKVM           func() error
	listImages        func(context.Context) ([]domain.Image, error)
	registryReachable func(string) error
	listHerdrProcs    func(context.Context) ([]HerdrProc, error)
	getenv            func(string) string
	executable        func() (string, error)
	resolveHostBin    func(ctx context.Context, name string) (hostbin.Resolved, error)
	userns            func() error
	usernsNet         func() error
	resolveAgent      func() error
}

func defaultProbes() probes {
	return probes{
		goos:           runtime.GOOS,
		lookPath:       exec.LookPath,
		getenv:         os.Getenv,
		executable:     os.Executable,
		resolveHostBin: (&hostbin.Resolver{}).Resolve,
		userns:         usernsAvailable,
		usernsNet:      usernsNetAvailable,
		resolveAgent: func() error {
			_, err := (&hostbin.Resolver{}).ResolveAgent(nil)
			return err
		},
		openKVM: func() error {
			f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
			if err != nil {
				return err
			}
			return f.Close()
		},
		listImages: func(ctx context.Context) ([]domain.Image, error) {
			storeRoot, err := store.DefaultRoot()
			if err != nil {
				return nil, err
			}
			cache, err := image.NewCache(filepath.Join(storeRoot, "images"))
			if err != nil {
				return nil, err
			}
			return cache.List(ctx)
		},
		registryReachable: func(ref string) error {
			r, err := name.ParseReference(ref)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = remote.Head(r, remote.WithContext(ctx))
			return err
		},
		listHerdrProcs: ListHerdrProcesses,
	}
}

// SelectSubstrate selects and returns a usable driver.Driver based on
// capability probes and the NEXUS_SUBSTRATE environment variable.
func SelectSubstrate() (driver.Driver, *SubstrateError) {
	return selectWith(defaultProbes(), os.Getenv("NEXUS_SUBSTRATE"))
}

// runAllChecks runs every capability probe and returns all results.
// drv is non-nil only when all checks pass.
func runAllChecks(p probes) (checks []CheckResult, drv driver.Driver) {
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
		platCheck.Remediation = "The nexus-vzd daemon (macOS / other platforms) is not yet implemented. Run nexus on a Linux host."
	}
	checks = append(checks, platCheck)

	var binaryPath string
	binCheck := CheckResult{
		Name:        "binary",
		Description: "cloud-hypervisor executable (embedded in nexus)",
	}
	if !platOK {
		binCheck.OK = false
		binCheck.Detail = "skipped (platform is not Linux)"
	} else if p.resolveHostBin != nil {
		res, err := p.resolveHostBin(context.Background(), hostbin.CloudHypervisor)
		if err != nil {
			binCheck.OK = false
			binCheck.Detail = err.Error()
			binCheck.Remediation = "The nexus build lacks the embedded artifact or the download failed. Use a release build or set NEXUS_CLOUD_HYPERVISOR_PATH."
		} else {
			binaryPath = res.Path
			binCheck.OK = true
			binCheck.Detail = fmt.Sprintf("%s (source: %s)", res.Path, res.Source)
		}
	} else {
		// nil fallback: used when tests build probes literals without resolveHostBin.
		var envPath string
		if p.getenv != nil {
			envPath = p.getenv(hostbin.EnvVar(hostbin.CloudHypervisor))
		}
		if envPath != "" {
			binaryPath = envPath
			binCheck.OK = true
			binCheck.Detail = envPath
		} else if path, err := p.lookPath(hostbin.CloudHypervisor); err == nil {
			binaryPath = path
			binCheck.OK = true
			binCheck.Detail = path
		} else {
			binCheck.OK = false
			binCheck.Detail = "cloud-hypervisor not found in PATH"
			binCheck.Remediation = "Set NEXUS_CLOUD_HYPERVISOR_PATH to the binary path, or use a release build that embeds the artifact."
		}
	}
	checks = append(checks, binCheck)

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

	if platOK && binCheck.OK && kvmCheck.OK {
		var diskDir string
		if storeRoot, derr := store.DefaultRoot(); derr == nil {
			diskDir = storeRoot + "/disks"
		}

		kernelPath, kernelErr := resolveKernelPath()
		kernelCheck := CheckResult{
			Name:        "kernel",
			Description: "guest kernel image (vmlinux); auto-fetched on first use",
			Optional:    true,
		}
		if kernelErr != nil {
			kernelCheck.OK = false
			kernelCheck.Detail = kernelErr.Error()
			kernelCheck.Remediation = "run: nexus kernel install"
			checks = append(checks, kernelCheck)
			return appendHostChecks(checks, p, platOK), nil
		}
		kernelCheck.OK = true
		kernelCheck.Detail = kernelPath
		checks = append(checks, kernelCheck)

		if p.listImages != nil {
			imgs, _ := p.listImages(context.Background())
			var found bool
			for _, img := range imgs {
				if img.Ref == herdrDefaultImage && img.Size > 0 {
					found = true
					break
				}
			}
			baseImgCheck := CheckResult{
				Name:        "base_image",
				Description: "base sandbox image in local cache; pulled on first create",
				Optional:    true,
			}
			if found {
				baseImgCheck.OK = true
				baseImgCheck.Detail = herdrDefaultImage + " cached"
			} else {
				baseImgCheck.OK = false
				if p.registryReachable != nil && p.registryReachable(herdrDefaultImage) == nil {
					baseImgCheck.Detail = "not cached; registry reachable — first sandbox create will pull it"
					baseImgCheck.Remediation = "run: nexus sandbox create --image " + herdrDefaultImage + " (or first worktree-sandbox create pulls automatically)"
				} else {
					baseImgCheck.Detail = "not cached and registry unreachable"
					baseImgCheck.Remediation = "run: nexus sandbox create --image " + herdrDefaultImage + " when registry is available"
				}
			}
			checks = append(checks, baseImgCheck)
		}

		virtiofsdCheck := CheckResult{
			Name:        "virtiofsd",
			Description: "virtiofsd binary for live directory mounts (--mount)",
		}
		if vres, verr := resolveVirtiofsdPath(); verr != nil {
			virtiofsdCheck.OK = false
			virtiofsdCheck.Detail = "not found"
			virtiofsdCheck.Remediation = "Set NEXUS_VIRTIOFSD_PATH or ensure network access (embedded amd64 artifact auto-extracts on first use; arm64 requires manual install)."
		} else {
			virtiofsdCheck.OK = true
			virtiofsdCheck.Detail = vres.Path + " (source: " + string(vres.Source) + ")"
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

	return appendHostChecks(checks, p, platOK), drv
}

// appendHostChecks adds the embedded-tool, userns, agent and optional checks
// that do not depend on kernel resolution.
func appendHostChecks(checks []CheckResult, p probes, platOK bool) []CheckResult {
	if p.resolveHostBin != nil {
		for _, name := range []string{"mke2fs", "e2fsck", "resize2fs"} {
			chk := CheckResult{
				Name:        "tool_" + name,
				Description: name + " executable (hostbin)",
			}
			res, err := p.resolveHostBin(context.Background(), name)
			if err == nil {
				chk.OK = true
				chk.Detail = fmt.Sprintf("%s: %s", res.Source, res.Path)
			} else {
				chk.OK = false
				chk.Detail = err.Error()
				chk.Remediation = fmt.Sprintf("rebuild nexus with `make artifacts` or set %s", hostbin.EnvVar(name))
			}
			checks = append(checks, chk)
		}
	}

	if p.userns != nil && platOK {
		chk := CheckResult{
			Name:        "userns",
			Description: "unprivileged user namespaces (rootless virtiofsd for --mount)",
			OK:          true,
			Detail:      "available",
		}
		if err := p.userns(); err != nil {
			chk.OK = false
			chk.Detail = err.Error()
			chk.Remediation = "Enable user namespaces: set sysctl user.max_user_namespaces to a non-zero value."
			if _, serr := os.Stat("/proc/sys/kernel/unprivileged_userns_clone"); serr == nil {
				chk.Remediation += " Also set kernel.unprivileged_userns_clone=1."
			}
		}
		checks = append(checks, chk)
	}

	if p.usernsNet != nil && platOK {
		nerr := p.usernsNet()
		chk := CheckResult{
			Name:        "userns_net",
			Description: "tap networking in unprivileged userns",
			OK:          true,
			Detail:      "not restricted",
		}
		if nerr != nil {
			chk.Detail = "tap blocked; vhost-user will be used"
		}
		checks = append(checks, chk)
		checks = append(checks, netModeCheck(p, nerr))
	}

	if p.resolveAgent != nil {
		chk := CheckResult{
			Name:        "agent",
			Description: "nexus-agent guest binary (embedded)",
			OK:          true,
			Detail:      "resolved",
		}
		if err := p.resolveAgent(); err != nil {
			chk.OK = false
			chk.Detail = err.Error()
			chk.Remediation = fmt.Sprintf("rebuild nexus with `make artifacts` or set %s", hostbin.AgentEnvVar)
		}
		checks = append(checks, chk)
	}

	checks = append(checks, optionalToolChecks(p)...)

	if p.listHerdrProcs != nil {
		checks = append(checks, checkHerdrProcesses(context.Background(), p.listHerdrProcs))
	}

	return checks
}

// optionalTools maps host executables to the feature each one enables.
var optionalTools = []struct{ name, feature string }{
	{"git", "worktree sandboxes"},
	{"ssh", "git-over-ssh relay"},
	{"gh", "GitHub PR workflows"},
	{"lsof", "port discovery"},
	{"ss", "listening-socket inspection"},
	{"ps", "herdr process health"},
}

func optionalToolChecks(p probes) []CheckResult {
	if p.lookPath == nil {
		return nil
	}
	var out []CheckResult
	for _, t := range optionalTools {
		chk := CheckResult{
			Name:        t.name,
			Description: t.name + ": " + t.feature,
			Optional:    true,
		}
		if path, err := p.lookPath(t.name); err == nil {
			chk.OK = true
			chk.Detail = fmt.Sprintf("%s (enables %s)", path, t.feature)
		} else {
			chk.Detail = fmt.Sprintf("not found in PATH; %s unavailable", t.feature)
		}
		out = append(out, chk)
	}
	return out
}

func usernsAvailable() error {
	if b, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil && strings.TrimSpace(string(b)) == "0" {
		return errors.New("user.max_user_namespaces is 0")
	}
	if b, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil && strings.TrimSpace(string(b)) == "0" {
		return errors.New("kernel.unprivileged_userns_clone is 0")
	}
	return nil
}

// netModeCheck reports the net mode a new sandbox would get and why.
func netModeCheck(p probes, netErr error) CheckResult {
	chk := CheckResult{Name: "net_mode", Description: "network mode for new sandboxes", Optional: true, OK: true}
	env := ""
	if p.getenv != nil {
		env = p.getenv("NEXUS_NET_MODE")
	}
	switch {
	case env != "":
		chk.Detail = env + " (from NEXUS_NET_MODE)"
		if env == string(domain.NetModeTap) && netErr != nil {
			chk.OK = false
			chk.Detail += "; tap blocked by AppArmor userns restriction"
			chk.Remediation = "unset NEXUS_NET_MODE or set NEXUS_NET_MODE=vhost-user"
		}
	case netErr != nil:
		chk.Detail = "vhost-user (default; tap blocked by AppArmor userns restriction)"
	default:
		chk.Detail = "vhost-user (default; set NEXUS_NET_MODE=tap for tap)"
	}
	return chk
}

func usernsNetAvailable() error {
	if b, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil && strings.TrimSpace(string(b)) == "1" {
		return errors.New("kernel.apparmor_restrict_unprivileged_userns=1: tap/bridge creation in a user namespace is denied (EPERM)")
	}
	return nil
}

// selectWith is the testable substrate selection logic.
func selectWith(p probes, envVal string) (driver.Driver, *SubstrateError) {
	switch envVal {
	case "", "cloudhypervisor":

	case "none":
		return nil, &SubstrateError{
			Msg:         "substrate disabled by NEXUS_SUBSTRATE=none",
			Remediation: "Unset NEXUS_SUBSTRATE or set it to 'cloudhypervisor' to enable auto-detection.",
		}

	default:
		return nil, &SubstrateError{
			Msg: fmt.Sprintf(
				"NEXUS_SUBSTRATE=%q is not a recognised value; accepted values: cloudhypervisor, none",
				envVal,
			),
			Remediation: "Set NEXUS_SUBSTRATE to 'cloudhypervisor', 'none', or unset it for auto-detection. (\"fake\" is not accepted outside Go tests — inject the fake driver directly in Go test code.)",
		}
	}

	checks, drv := runAllChecks(p)
	if drv != nil {
		return drv, nil
	}

	for _, c := range checks {
		if !c.OK {
			return nil, &SubstrateError{
				Msg:         c.Detail,
				Remediation: c.Remediation,
			}
		}
	}

	return nil, &SubstrateError{
		Msg: "substrate unavailable: driver initialization failed (run nexus doctor for details)",
	}
}
