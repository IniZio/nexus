//go:build darwin

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var initRuntimeBootstrapRunner func(projectRoot, runtimeName string) error = runInitRuntimeBootstrapDarwin

var (
	initRuntimeBootstrapIsRootFn                   = func() bool { return os.Geteuid() == 0 }
	initRuntimeBootstrapSudoOKFn                   = func() bool { return exec.Command("sudo", "-n", "true").Run() == nil }
	initRuntimeBootstrapIsTTYFn                    = isTerminalDarwin
	initRuntimeBootstrapSkipFastFailFn func() bool = nil

	limactlLookPathFn = exec.LookPath
	limactlRunFn      = func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	}
	limactlOutputFn = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
)

func runInitRuntimeBootstrapDarwin(projectRoot, runtimeName string) error {
	if runtimeName != "firecracker" {
		return nil
	}

	if _, err := limactlLookPathFn("limactl"); err != nil {
		if _, brewErr := limactlLookPathFn("brew"); brewErr == nil {
			_ = limactlRunFn("brew", "install", "lima")
		}
	}

	if _, err := limactlLookPathFn("limactl"); err != nil {
		return initRuntimeBootstrapDarwinWrapError(projectRoot, fmt.Errorf("limactl not found; run: brew install lima"))
	}

	templatePath := filepath.Join(moduleRoot(), "templates", "lima", "firecracker.yaml")
	if _, err := os.Stat(templatePath); err != nil {
		return initRuntimeBootstrapDarwinWrapError(projectRoot, fmt.Errorf("missing lima template at %s: %w", templatePath, err))
	}
	if err := ensurePersistentLimaInstance("nexus-firecracker", templatePath); err != nil {
		return initRuntimeBootstrapDarwinWrapError(projectRoot, err)
	}
	return nil
}

func ensurePersistentLimaInstance(instanceName, templatePath string) error {
	listOut, listErr := limactlOutputFn("limactl", "list", "--json", instanceName)
	trimmed := bytes.TrimSpace(listOut)
	if listErr == nil && len(trimmed) > 0 && string(trimmed) != "[]" {
		return nil
	}

	if err := limactlRunFn("limactl", "start", "--name", instanceName, templatePath); err != nil {
		return fmt.Errorf("failed to start lima instance %s: %w", instanceName, err)
	}

	return nil
}

func initRuntimeBootstrapDarwinWrapError(projectRoot string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("firecracker runtime setup failed on darwin: %w\n\nmanual next steps:\n  brew install lima\n  nexus init --project-root %s --runtime firecracker", err, projectRoot)
}

func isTerminalDarwin(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
