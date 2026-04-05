//go:build darwin

package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

//go:embed templates/lima/firecracker.yaml
var embeddedLimaTemplate string

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

	templatePath, cleanupTemplate, err := writeEmbeddedLimaTemplate()
	if err != nil {
		return initRuntimeBootstrapDarwinWrapError(projectRoot, err)
	}
	defer cleanupTemplate()

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

func writeEmbeddedLimaTemplate() (string, func(), error) {
	content := strings.TrimSpace(embeddedLimaTemplate)
	if content == "" {
		return "", func() {}, fmt.Errorf("embedded lima template is empty")
	}

	tmp, err := os.CreateTemp("", "nexus-lima-firecracker-*.yaml")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp lima template: %w", err)
	}

	path := tmp.Name()
	if _, err := tmp.WriteString(content + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return "", func() {}, fmt.Errorf("write embedded lima template: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", func() {}, fmt.Errorf("close temp lima template: %w", err)
	}

	return path, func() { _ = os.Remove(path) }, nil
}

func isTerminalDarwin(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
