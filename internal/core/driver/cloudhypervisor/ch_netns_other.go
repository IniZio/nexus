//go:build !linux

package cloudhypervisor

import "syscall"

// netnsChildAttr off Linux returns a bare attr; the netns boot path errors via
// errUnsupportedPlatform before this is used in anger.
func netnsChildAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
