package selection

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

var firecrackerPreflightRunner = func(repo string, opts runtime.PreflightOptions) runtime.FirecrackerPreflightResult {
	return runtime.RunFirecrackerPreflight(repo, opts)
}

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

	if out, err := runtimeSetupRunCommandFn(ctx, binary, "init", "--project-root", repo); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("nexus init failed: %w", err)
		}
		return fmt.Errorf("nexus init failed: %w: %s", err, msg)
	}
	return nil
}

func SetPreflightSequenceForTest(sequence []runtime.FirecrackerPreflightResult) {
	seq := append([]runtime.FirecrackerPreflightResult(nil), sequence...)
	idx := 0
	firecrackerPreflightRunner = func(_ string, _ runtime.PreflightOptions) runtime.FirecrackerPreflightResult {
		if len(seq) == 0 {
			return runtime.FirecrackerPreflightResult{Status: runtime.PreflightPass}
		}
		if idx >= len(seq) {
			return seq[len(seq)-1]
		}
		result := seq[idx]
		idx++
		return result
	}
}

func ResetPreflightRunnerForTest() {
	firecrackerPreflightRunner = func(repo string, opts runtime.PreflightOptions) runtime.FirecrackerPreflightResult {
		return runtime.RunFirecrackerPreflight(repo, opts)
	}
}

func SetFirecrackerPreflightRunnerForTest(runner func(string, runtime.PreflightOptions) runtime.FirecrackerPreflightResult) {
	firecrackerPreflightRunner = runner
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

		if out, err := runtimeSetupRunCommandFn(ctx, binary, "init", "--project-root", repo); err != nil {
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
	return fmt.Errorf("firecracker runtime setup requires passwordless sudo or root access in non-interactive sessions\n\nmanual next steps:\n  sudo -E nexus init --project-root %s", repo)
}

func runtimePreflightFailure(result runtime.FirecrackerPreflightResult, setupErr error) *rpckit.RPCError {
	if setupErr != nil && strings.TrimSpace(result.SetupOutcome) == "" {
		result.SetupOutcome = fmt.Sprintf("failed: %v", setupErr)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime preflight failed: status=%s", result.Status)}
	}
	return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime preflight failed: %s", string(payload))}
}

func SelectBackend(ctx context.Context, repo string, requiredBackends []string, requiredCaps []string, factory *runtime.Factory) (string, *rpckit.RPCError) {
	preflightOpts := runtime.PreflightOptions{UseOverrides: internalPreflightOverrideEnabled()}
	preflight := firecrackerPreflightRunner(repo, preflightOpts)
	var setupErr error
	var selectedBackend string

	switch preflight.Status {
	case runtime.PreflightPass:
		selectedBackend = "firecracker"
	case runtime.PreflightInstallableMissing:
		setupErr = runtimeSetupRunner(ctx, repo, "firecracker")
		if setupErr != nil {
			preflight.SetupAttempted = true
			preflight.SetupOutcome = fmt.Sprintf("failed: %v", setupErr)
			return "", runtimePreflightFailure(preflight, setupErr)
		}
		postSetup := firecrackerPreflightRunner(repo, preflightOpts)
		postSetup.SetupAttempted = true
		postSetup.SetupOutcome = "succeeded"
		if postSetup.Status != runtime.PreflightPass {
			return "", runtimePreflightFailure(postSetup, setupErr)
		}
		selectedBackend = "firecracker"
	case runtime.PreflightUnsupportedNested:
		selectedBackend = "seatbelt"
	default:
		return "", runtimePreflightFailure(preflight, nil)
	}

	driver, err := factory.SelectDriver([]string{selectedBackend}, requiredCaps)
	if err != nil {
		return "", &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v (required=%v)", err, requiredBackends)}
	}
	return driver.Backend(), nil
}

func internalPreflightOverrideEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("NEXUS_INTERNAL_ENABLE_PREFLIGHT_OVERRIDE")))
	return value == "1" || value == "true" || value == "yes"
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
