package selection

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
)

var (
	runtimeSetupGOOS     = goruntime.GOOS
	runtimeSetupIsRootFn = func() bool {
		return os.Geteuid() == 0
	}
	runtimeSetupSudoNOKFn = func() bool {
		return exec.Command("sudo", "-n", "true").Run() == nil
	}
	runtimeSetupIsTTYFn = func(f *os.File) bool {
		if f == nil {
			return false
		}
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return (fi.Mode() & os.ModeCharDevice) != 0
	}
	runtimeSetupResolveBinaryFn = resolveNexusBinaryPath
	runtimeSetupRunCommandFn    = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		return cmd.CombinedOutput()
	}
	darwinHasNestedVirtFn = func() bool {
		out, err := exec.Command("sysctl", "-n", "kern.hv_support").Output()
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(out)) == "1"
	}
)

var runtimeSetupRunner = func(ctx context.Context, repo, backend string) error {
	if strings.TrimSpace(backend) != "firecracker" {
		return nil
	}
	if strings.TrimSpace(repo) == "" {
		return fmt.Errorf("repo is required for runtime setup")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if runtimeSetupRequiresManualPrivilege() {
		return runtimeSetupManualPrivilegeError(repo)
	}

	binary, err := runtimeSetupResolveBinaryFn()
	if err != nil {
		return err
	}

	if out, err := runtimeSetupRunCommandFn(ctx, binary, "init", repo); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("nexus init failed: %w", err)
		}
		return fmt.Errorf("nexus init failed: %w: %s", err, msg)
	}
	return nil
}

func SetRuntimeSetupRunnerForTest(runner func(ctx context.Context, repo, backend string) error) {
	runtimeSetupRunner = runner
}

func ResetRuntimeSetupRunnerForTest() {
	runtimeSetupGOOS = goruntime.GOOS
	runtimeSetupIsRootFn = func() bool {
		return os.Geteuid() == 0
	}
	runtimeSetupSudoNOKFn = func() bool {
		return exec.Command("sudo", "-n", "true").Run() == nil
	}
	runtimeSetupIsTTYFn = func(f *os.File) bool {
		if f == nil {
			return false
		}
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return (fi.Mode() & os.ModeCharDevice) != 0
	}
	runtimeSetupResolveBinaryFn = resolveNexusBinaryPath
	runtimeSetupRunCommandFn = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		return cmd.CombinedOutput()
	}

	runtimeSetupRunner = func(ctx context.Context, repo, backend string) error {
		if strings.TrimSpace(backend) != "firecracker" {
			return nil
		}
		if strings.TrimSpace(repo) == "" {
			return fmt.Errorf("repo is required for runtime setup")
		}
		if ctx == nil {
			ctx = context.Background()
		}
		if runtimeSetupRequiresManualPrivilege() {
			return runtimeSetupManualPrivilegeError(repo)
		}

		binary, err := runtimeSetupResolveBinaryFn()
		if err != nil {
			return err
		}

		if out, err := runtimeSetupRunCommandFn(ctx, binary, "init", repo); err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				return fmt.Errorf("nexus init failed: %w", err)
			}
			return fmt.Errorf("nexus init failed: %w: %s", err, msg)
		}
		return nil
	}
}

func runtimeSetupRequiresManualPrivilege() bool {
	if runtimeSetupGOOS != "linux" {
		return false
	}
	if runtimeSetupIsRootFn() || runtimeSetupSudoNOKFn() || runtimeSetupIsTTYFn(os.Stdin) {
		return false
	}
	return true
}

func runtimeSetupManualPrivilegeError(repo string) error {
	return fmt.Errorf("firecracker runtime setup requires passwordless sudo or root access in non-interactive sessions\n\nmanual next steps:\n  sudo -E nexus init %s", repo)
}

// SelectBackend returns the driver name and mode for the given platform and config.
// Selection is deterministic from platform + config — no runtime probing.
func SelectBackend(platform string, cfg *config.WorkspaceConfig) (backend string, mode string, err error) {
	requestedLevel := ""
	requestedMode := ""
	level := "vm"
	vmMode := ""
	if cfg != nil {
		requestedLevel = strings.TrimSpace(cfg.Isolation.Level)
		requestedMode = strings.TrimSpace(cfg.Isolation.VM.Mode)
		if requestedLevel != "" {
			level = requestedLevel
		}
		vmMode = requestedMode
	}

	switch platform {
	case "linux":
		if level == "process" {
			return "process", "process", nil
		}
		if vmMode == "" {
			vmMode = "dedicated" // Linux default
		}
		return "firecracker", vmMode, nil

	case "darwin":
		if level == "process" {
			return "process", "process", nil
		}
		if vmMode == "" {
			vmMode = "dedicated"
		}
		if !darwinHasNestedVirtFn() {
			if requestedLevel == "vm" || requestedMode != "" {
				return "", "", fmt.Errorf("vm isolation requires nested virtualization on macOS; use process isolation instead")
			}
			return "process", "process", nil
		}
		return "lima", vmMode, nil

	default:
		return "", "", fmt.Errorf("unsupported platform: %s", platform)
	}
}

// DarwinHasNestedVirt reports whether nested virtualization is available on the
// current Darwin host by reading kern.hv_support. Returns false on non-darwin
// or if the sysctl is absent.
func DarwinHasNestedVirt() bool {
	return darwinHasNestedVirtFn()
}

func resolveNexusBinaryPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("NEXUS_CLI_PATH")); p != "" {
		clean := filepath.Clean(p)
		st, err := os.Stat(clean)
		if err != nil {
			return "", fmt.Errorf("resolve nexus binary: NEXUS_CLI_PATH %q: %w", clean, err)
		}
		if st.IsDir() {
			return "", fmt.Errorf("resolve nexus binary: NEXUS_CLI_PATH %q is a directory", clean)
		}
		return clean, nil
	}

	exe, exeErr := os.Executable()
	if exeErr == nil {
		name := "nexus"
		if goruntime.GOOS == "windows" {
			name = "nexus.exe"
		}
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}

	path, err := exec.LookPath("nexus")
	if err != nil {
		if exeErr != nil {
			return "", fmt.Errorf("resolve nexus binary: executable lookup failed: %w", exeErr)
		}
		return "", fmt.Errorf("resolve nexus binary: nexus not found next to %s or in PATH", exe)
	}
	return path, nil
}
