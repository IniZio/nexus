//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

var initRuntimeBootstrapRunner func(projectRoot, runtimeName string) error = runInitRuntimeBootstrapLinux

var (
	initRuntimeBootstrapIsRootFn       = func() bool { return os.Geteuid() == 0 }
	initRuntimeBootstrapSudoOKFn       = func() bool { return exec.Command("sudo", "-n", "true").Run() == nil }
	initRuntimeBootstrapIsTTYFn        = isTerminal
	initRuntimeBootstrapSkipFastFailFn func() bool
)

func runInitRuntimeBootstrapLinux(projectRoot, runtimeName string) error {
	if runtimeName != "firecracker" {
		return nil
	}

	if initRuntimeBootstrapSkipFastFailFn != nil && initRuntimeBootstrapSkipFastFailFn() {
		err := runSetupFirecracker(io.Discard)
		if errors.Is(err, errKVMGroupRefreshNeeded) && (initRuntimeBootstrapIsRootFn() || initRuntimeBootstrapSudoOKFn()) {
			return nil
		}
		return err
	}

	if initRuntimeBootstrapManualSetupRequired() {
		return initRuntimeBootstrapManualError(projectRoot)
	}

	if err := runSetupFirecracker(io.Discard); err != nil {
		if errors.Is(err, errKVMGroupRefreshNeeded) && (initRuntimeBootstrapIsRootFn() || initRuntimeBootstrapSudoOKFn()) {
			return nil
		}
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
	return fmt.Errorf("firecracker runtime setup requires passwordless sudo or root access in non-interactive sessions\n\nmanual next steps:\n  cd %s\n  sudo -E nexus init --force", projectRoot)
}

func initRuntimeBootstrapWrapError(projectRoot string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("firecracker runtime setup failed: %w\n\nmanual next steps:\n  cd %s\n  sudo -E nexus init --force", err, projectRoot)
}
