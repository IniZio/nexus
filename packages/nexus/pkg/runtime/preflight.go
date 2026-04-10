package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type PreflightStatus string

const (
	PreflightPass               PreflightStatus = "pass"
	PreflightInstallableMissing PreflightStatus = "installable_missing"
	PreflightUnsupportedNested  PreflightStatus = "unsupported_nested_virt"
	PreflightHardFail           PreflightStatus = "hard_fail"
)

type PreflightCheck struct {
	Name        string `json:"name"`
	OK          bool   `json:"ok"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
	Installable bool   `json:"installable,omitempty"`
}

type FirecrackerPreflightResult struct {
	Status         PreflightStatus  `json:"status"`
	Checks         []PreflightCheck `json:"checks"`
	SetupAttempted bool             `json:"setupAttempted"`
	SetupOutcome   string           `json:"setupOutcome"`
	Override       string           `json:"override,omitempty"`
}

type PreflightOptions struct {
	UseOverrides bool
}

var (
	preflightGOOS          = runtime.GOOS
	preflightLookPath      = exec.LookPath
	preflightStat          = os.Stat
	preflightCommandOutput = runPreflightCommand
	preflightGetenv        = os.Getenv
)

func RunFirecrackerPreflight(_ string, opts ...PreflightOptions) FirecrackerPreflightResult {
	options := PreflightOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}

	if options.UseOverrides {
		if forced, ok := preflightOverrideResult(strings.TrimSpace(preflightGetenv("NEXUS_INTERNAL_PREFLIGHT_OVERRIDE"))); ok {
			return forced
		}
	}

	checks := make([]PreflightCheck, 0, 4)
	macNestedVirtContext := false

	switch preflightGOOS {
	case "darwin":
		macNestedVirtContext = true
		checks = append(checks, checkDarwinNestedVirt())
		checks = append(checks, checkCommandInPath(
			"lima",
			"limactl",
			"brew install lima",
		))
	case "linux":
		checks = append(checks, checkLinuxKVM())
		checks = append(checks, checkCommandInPath(
			"firecracker",
			"firecracker",
			"install firecracker and ensure it is on PATH",
		))
	default:
		checks = append(checks, PreflightCheck{
			Name:        "platform",
			OK:          false,
			Message:     fmt.Sprintf("unsupported host platform: %s", preflightGOOS),
			Remediation: "use Linux (KVM) or macOS (Lima) for firecracker runtime",
		})
	}

	return ClassifyFirecrackerPreflight(checks, macNestedVirtContext)
}

func preflightOverrideResult(value string) (FirecrackerPreflightResult, bool) {
	makeForced := func(status PreflightStatus, override string) FirecrackerPreflightResult {
		return FirecrackerPreflightResult{
			Status:   status,
			Checks:   []PreflightCheck{{Name: "override", OK: true, Message: "forced preflight status for internal testing"}},
			Override: override,
		}
	}

	switch value {
	case "pass":
		return makeForced(PreflightPass, "pass"), true
	case "installable_missing":
		return makeForced(PreflightInstallableMissing, "installable_missing"), true
	case "unsupported_nested_virt":
		return makeForced(PreflightUnsupportedNested, "unsupported_nested_virt"), true
	case "hard_fail":
		return makeForced(PreflightHardFail, "hard_fail"), true
	default:
		return FirecrackerPreflightResult{}, false
	}
}

func checkDarwinNestedVirt() PreflightCheck {
	out, err := preflightCommandOutput("sysctl", "-n", "kern.hv_support")
	if err != nil {
		return PreflightCheck{
			Name:        "nested_virt",
			OK:          false,
			Message:     fmt.Sprintf("cannot determine Hypervisor.framework support: %v", err),
			Remediation: "run on a macOS host with Hypervisor.framework support",
		}
	}

	if strings.TrimSpace(out) == "1" {
		return PreflightCheck{Name: "nested_virt", OK: true}
	}

	return PreflightCheck{
		Name:        "nested_virt",
		OK:          false,
		Message:     "Hypervisor.framework unavailable (nested virtualization unsupported)",
		Remediation: "run on a bare-metal macOS host with virtualization enabled",
	}
}

func checkLinuxKVM() PreflightCheck {
	if _, err := preflightStat("/dev/kvm"); err == nil {
		return PreflightCheck{Name: "kvm", OK: true}
	}

	return PreflightCheck{
		Name:        "kvm",
		OK:          false,
		Message:     "/dev/kvm is not available",
		Remediation: "run on a Linux host with KVM enabled and accessible",
	}
}

func checkCommandInPath(name, binary, remediation string) PreflightCheck {
	if _, err := preflightLookPath(binary); err == nil {
		return PreflightCheck{Name: name, OK: true}
	}

	return PreflightCheck{
		Name:        name,
		OK:          false,
		Message:     fmt.Sprintf("%s not found in PATH", binary),
		Remediation: remediation,
		Installable: true,
	}
}

func runPreflightCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out", name)
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, msg)
	}

	return strings.TrimSpace(string(out)), nil
}

func ClassifyFirecrackerPreflight(checks []PreflightCheck, macNestedVirtContext bool) FirecrackerPreflightResult {
	result := FirecrackerPreflightResult{Checks: checks}

	status := PreflightPass
	for _, check := range checks {
		if check.OK {
			continue
		}

		candidate := PreflightHardFail
		if macNestedVirtContext && check.Name == "nested_virt" {
			candidate = PreflightUnsupportedNested
		} else if check.Installable {
			candidate = PreflightInstallableMissing
		}

		status = higherPreflightStatus(status, candidate)
	}

	result.Status = status
	return result
}

func higherPreflightStatus(current, candidate PreflightStatus) PreflightStatus {
	if preflightStatusRank(candidate) > preflightStatusRank(current) {
		return candidate
	}
	return current
}

func preflightStatusRank(status PreflightStatus) int {
	switch status {
	case PreflightHardFail:
		return 3
	case PreflightUnsupportedNested:
		return 2
	case PreflightInstallableMissing:
		return 1
	default:
		return 0
	}
}
