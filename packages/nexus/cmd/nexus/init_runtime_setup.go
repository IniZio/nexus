package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
)

var initRuntimeBootstrapRunner = runInitRuntimeBootstrap

var (
	initRuntimeBootstrapIsRootFn       = func() bool { return os.Geteuid() == 0 }
	initRuntimeBootstrapSudoOKFn       = func() bool { return exec.Command("sudo", "-n", "true").Run() == nil }
	initRuntimeBootstrapIsTTYFn        = isTerminal
	initRuntimeBootstrapSkipFastFailFn func() bool
)

func runInitRuntimeBootstrap(projectRoot, runtimeName string) error {
	if initRuntimeBootstrapRunner != nil {
		return initRuntimeBootstrapRunner(projectRoot, runtimeName)
	}

	switch runtime.GOOS {
	case "linux":
		return errors.New("initRuntimeBootstrapRunner not initialized (linux build may be missing)")
	case "darwin":
		return errors.New("initRuntimeBootstrapRunner not initialized (darwin build may be missing)")
	default:
		return runInitRuntimeBootstrapUnsupported(projectRoot, runtimeName)
	}
}

func runInitRuntimeBootstrapUnsupported(projectRoot, runtimeName string) error {
	if runtimeName != "firecracker" {
		return nil
	}

	if initRuntimeBootstrapSkipFastFailFn != nil && initRuntimeBootstrapSkipFastFailFn() {
		return runSetupFirecracker(io.Discard)
	}

	if initRuntimeBootstrapManualSetupRequired() {
		return initRuntimeBootstrapManualError(projectRoot)
	}

	if err := runSetupFirecracker(io.Discard); err != nil {
		return initRuntimeBootstrapWrapError(projectRoot, err)
	}

	return nil
}

func initRuntimeBootstrapManualSetupRequired() bool {
	if initRuntimeBootstrapIsRootFn() {
		return false
	}
	if initRuntimeBootstrapSudoOKFn() {
		return false
	}
	if initRuntimeBootstrapIsTTYFn(os.Stdin) {
		return false
	}
	return true
}

func initRuntimeBootstrapManualError(projectRoot string) error {
	return fmt.Errorf("firecracker runtime setup requires passwordless sudo or root access in non-interactive sessions\n\nmanual next steps:\n  sudo -E nexus init --project-root %s --runtime firecracker", projectRoot)
}

func initRuntimeBootstrapWrapError(projectRoot string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("firecracker runtime setup failed: %w\n\nmanual next steps:\n  sudo -E nexus init --project-root %s --runtime firecracker", err, projectRoot)
}
