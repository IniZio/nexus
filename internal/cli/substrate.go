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
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
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
}

type probes struct {
	goos               string
	lookPath           func(string) (string, error)
	openKVM            func() error
	listImages         func(context.Context) ([]domain.Image, error)
	registryReachable  func(string) error
	listHerdrProcs     func(context.Context) ([]HerdrProc, error)
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
// capability probes and the NEXUS3_SUBSTRATE environment variable.
func SelectSubstrate() (driver.Driver, *SubstrateError) {
	return selectWith(defaultProbes(), os.Getenv("NEXUS3_SUBSTRATE"))
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
		platCheck.Remediation = "The nexus3-vzd daemon (macOS / other platforms) is not yet implemented. Run nexus3 on a Linux host."
	}
	checks = append(checks, platCheck)

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
			Description: "guest kernel image (vmlinux) exists",
		}
		if kernelErr != nil {
			kernelCheck.OK = false
			kernelCheck.Detail = kernelErr.Error()
			kernelCheck.Remediation = "run: nexus3 kernel install"
			checks = append(checks, kernelCheck)
			if p.listHerdrProcs != nil {
				checks = append(checks, checkHerdrProcesses(context.Background(), p.listHerdrProcs))
			}
			return checks, nil
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
				Description: "base sandbox image in local cache",
			}
			if found {
				baseImgCheck.OK = true
				baseImgCheck.Detail = herdrDefaultImage + " cached"
			} else {
				baseImgCheck.OK = false
				if p.registryReachable != nil && p.registryReachable(herdrDefaultImage) == nil {
					baseImgCheck.Detail = "not cached; registry reachable — first sandbox create will pull it"
					baseImgCheck.Remediation = "run: nexus3 sandbox create --image " + herdrDefaultImage + " (or first worktree-sandbox create pulls automatically)"
				} else {
					baseImgCheck.Detail = "not cached and registry unreachable"
					baseImgCheck.Remediation = "run: nexus3 sandbox create --image " + herdrDefaultImage + " when registry is available"
				}
			}
			checks = append(checks, baseImgCheck)
		}

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

	if p.listHerdrProcs != nil {
		checks = append(checks, checkHerdrProcesses(context.Background(), p.listHerdrProcs))
	}

	return checks, drv
}

// selectWith is the testable substrate selection logic.
func selectWith(p probes, envVal string) (driver.Driver, *SubstrateError) {
	switch envVal {
	case "", "cloudhypervisor":

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

	for _, c := range checks {
		if !c.OK {
			return nil, &SubstrateError{
				Msg:         c.Detail,
				Remediation: c.Remediation,
			}
		}
	}

	return nil, &SubstrateError{
		Msg: "substrate unavailable: driver initialization failed (run nexus3 doctor for details)",
	}
}
