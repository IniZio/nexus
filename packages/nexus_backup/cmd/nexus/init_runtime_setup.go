package main

import (
	"errors"
	"runtime"
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
	return &unsupportedPlatformError{goos: runtime.GOOS}
}

type unsupportedPlatformError struct {
	goos string
}

func (e *unsupportedPlatformError) Error() string {
	return "firecracker is only supported on Linux (with KVM) and macOS (with Lima); current platform is " + e.goos
}
