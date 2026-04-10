//go:build !linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	setupPrivilegeModeOverride        privilegeMode
	setupPrivilegeModeOverrideEnabled bool
	setupBuildTapHelperFn             = func() (string, error) { return "", fmt.Errorf("not available on non-linux") }
	setupExtractAgentFn               = func() (string, error) { return "", fmt.Errorf("not available on non-linux") }
	setupRunScriptFn                  = func(mode privilegeMode, script string) error { return fmt.Errorf("not available on non-linux") }
	setupVerifyFn                     = func() error { return fmt.Errorf("not available on non-linux") }
	setupSudoReexecFn                 = func(commandPath string) error { return fmt.Errorf("not available on non-linux") }
	setupKVMGroupReexecFn             = func(commandPath string) error { return fmt.Errorf("not available on non-linux") }
)

var errKVMGroupRefreshNeeded = errors.New("kvm group refresh not applicable on non-linux")

func setupCommandPath() string {
	if exe, err := os.Executable(); err == nil {
		exe = strings.TrimSpace(exe)
		if exe != "" {
			return exe
		}
	}
	if len(os.Args) > 0 {
		arg0 := strings.TrimSpace(os.Args[0])
		if arg0 != "" {
			if filepath.IsAbs(arg0) {
				return arg0
			}
			if lp, err := exec.LookPath(arg0); err == nil {
				return lp
			}
			return arg0
		}
	}
	return "nexus"
}

func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func runSetupFirecracker(w io.Writer) error {
	return fmt.Errorf("firecracker host setup is only supported on Linux; on macOS use 'limactl' directly")
}

type privilegeMode int

const (
	privilegeModeRoot privilegeMode = iota
	privilegeModeSudoN
	privilegeModeInteractive
	privilegeModeManual
)

func detectPrivilegeMode(isRoot, sudoNOK, stdinIsTTY bool) privilegeMode {
	return privilegeModeManual
}

func resolvePrivilegeMode() privilegeMode {
	return privilegeModeManual
}

func buildSetupScript(tapHelperSrc, agentSrc string) string {
	return "# non-linux stub"
}
