//go:build linux

package cloudhypervisor

import (
	"os"
	"syscall"
)

// netnsChildAttr returns the SysProcAttr for the re-exec'd child.
//
//   - CLONE_NEWUSER | CLONE_NEWNET: new user+network namespace.
//   - UidMappings/GidMappings: in-ns uid/gid 0 → host uid/gid (full in-ns privileges).
//   - GidMappingsEnableSetgroups=false: writes setgroups=deny, which preserves
//     supplementary group membership (e.g. the kvm group) across the boundary
//     so the child can still open /dev/kvm.
//   - Setpgid=true: child becomes a process group leader (pgid == child.pid).
func netnsChildAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
		Setpgid:                    true,
	}
}
