//go:build !linux && !darwin

package main

import (
	"fmt"
	"os"
	"runtime"
)

var initRuntimeBootstrapRunner func(projectRoot, runtimeName string) error = runInitRuntimeBootstrapOther

var (
	initRuntimeBootstrapIsRootFn                   = func() bool { return false }
	initRuntimeBootstrapSudoOKFn                   = func() bool { return false }
	initRuntimeBootstrapIsTTYFn                    = isTerminalUnsupported
	initRuntimeBootstrapSkipFastFailFn func() bool = nil
)

func isTerminalUnsupported(_ *os.File) bool {
	return false
}

func runInitRuntimeBootstrapOther(projectRoot, runtimeName string) error {
	if runtimeName != "firecracker" {
		return nil
	}
	return fmt.Errorf("firecracker is only supported on Linux (with KVM) and macOS (with Lima); current platform is %s", runtime.GOOS)
}
