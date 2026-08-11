//go:build !linux

package cloudhypervisor

import "syscall"

// setPdeathsig is a no-op on non-Linux platforms (Pdeathsig is Linux-only).
func setPdeathsig(_ *syscall.SysProcAttr) {}
