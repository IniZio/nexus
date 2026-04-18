//go:build darwin

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var bootstrapFirecrackerExecContextDarwinFn = bootstrapFirecrackerExecContextDarwin

var runLimaCheckCommandFn = runLimaCheckCommand

var doctorLimaInstanceName string

func bootstrapFirecrackerExecContextDarwin(projectRoot string, execCtx doctorExecContext) error {
	if execCtx.backend != "firecracker" {
		return fmt.Errorf("invalid backend for lima bootstrap on darwin: %s", execCtx.backend)
	}

	if _, err := limactlLookPathFn("limactl"); err != nil {
		return fmt.Errorf("limactl not found; brew install lima: %w", err)
	}

	templatePath, cleanupTemplate, err := writeEmbeddedLimaTemplate()
	if err != nil {
		return fmt.Errorf("failed to write lima template: %w", err)
	}
	defer cleanupTemplate()

	instanceName := fmt.Sprintf("nexus-doctor-%d", time.Now().UnixNano())
	doctorLimaInstanceName = instanceName

	if err := limactlRunFn("limactl", "start", "--name", instanceName, templatePath); err != nil {
		doctorLimaInstanceName = ""
		return fmt.Errorf("failed to start lima instance %s: %w", instanceName, err)
	}

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer verifyCancel()
	if _, err := runLimaCheckCommandFn(verifyCtx, projectRoot, "sh", []string{"-lc", "pwd >/dev/null"}); err != nil {
		doctorLimaInstanceName = ""
		_ = limactlRunFn("limactl", "delete", "-f", instanceName)
		return fmt.Errorf("lima workspace readiness check failed for %s: %w", instanceName, err)
	}

	setDoctorExecContextCleanup(func() error {
		doctorLimaInstanceName = ""
		if err := limactlRunFn("limactl", "delete", "-f", instanceName); err != nil {
			return fmt.Errorf("failed to delete lima doctor instance %s: %w", instanceName, err)
		}
		return nil
	})

	return nil
}

func runLimaCheckCommand(ctx context.Context, projectRoot, command string, args []string) (string, error) {
	if doctorLimaInstanceName == "" {
		return "", fmt.Errorf("lima doctor session not initialized")
	}

	fullCmd := fmt.Sprintf("cd %s && %s", shellQuote(projectRoot), formatCommand(command, args))
	out, err := runLimaShellCommand(ctx, projectRoot, fullCmd, false)
	if err == nil {
		return out, nil
	}

	if !shouldRetryLimaCommandWithSudo(out) {
		return out, err
	}

	sudoOut, sudoErr := runLimaShellCommand(ctx, projectRoot, fullCmd, true)
	if sudoErr == nil {
		return sudoOut, nil
	}
	if strings.TrimSpace(sudoOut) != "" {
		return sudoOut, sudoErr
	}
	return out, err
}

func runLimaShellCommand(ctx context.Context, projectRoot, fullCmd string, withSudo bool) (string, error) {
	commandToRun := fullCmd
	if withSudo {
		commandToRun = fmt.Sprintf("if [ \"$(id -u)\" -eq 0 ]; then %s; elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then sudo -n sh -lc %s; else echo 'sudo not available for privileged command'; exit 126; fi", fullCmd, shellQuote(fullCmd))
	}

	cmd := exec.CommandContext(ctx, "limactl", "shell", doctorLimaInstanceName, "--", "sh", "-lc", commandToRun)

	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	out := strings.TrimSpace(output.String())
	if err != nil {
		if out == "" {
			out = err.Error()
		}
		return out, fmt.Errorf("lima command failed: %w", err)
	}

	return out, nil
}

func shouldRetryLimaCommandWithSudo(output string) bool {
	lower := strings.ToLower(strings.TrimSpace(output))
	if lower == "" {
		return false
	}

	markers := []string{
		"permission denied",
		"operation not permitted",
		"are you root",
		"requires root",
		"superuser",
		"could not open lock file",
		"unable to acquire the dpkg frontend lock",
		"this command has to be run as root",
		"permission denied while trying to connect to the docker daemon socket",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	return false
}
