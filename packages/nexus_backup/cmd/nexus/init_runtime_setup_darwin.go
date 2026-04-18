//go:build darwin

package main

import (
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
)

func runInitRuntimeBootstrapDarwin(projectRoot, runtimeName string) error {
	return fmt.Errorf("firecracker runtime no longer available on darwin; connect to a remote Linux daemon instead")
}

func isTerminalDarwin(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func writeNexusInitEnv(projectRoot string, kvPairs map[string]string) error {
	runDir := filepath.Join(projectRoot, ".nexus", "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("create nexus run dir: %w", err)
	}
	return nil
}

var limactlLookPathFn = exec.LookPath

func limactlRunFn(name string, args ...string) error {
	return fmt.Errorf("lima not supported")
}

func limactlOutputFn(name string, args ...string) ([]byte, error) {
	return nil, fmt.Errorf("lima not supported")
}

func writeEmbeddedLimaTemplate() (string, func(), error) {
	return "", func() {}, fmt.Errorf("lima not supported")
}

func ensurePersistentLimaInstance(instanceName, templatePath string) error {
	return fmt.Errorf("lima not supported")
}
