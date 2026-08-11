//go:build linux

package cloudhypervisor

import "syscall"

// setPdeathsig sets Pdeathsig=SIGKILL on attr so the child process is killed
// when its parent dies. Linux-only (POSIX has no equivalent).
func setPdeathsig(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGKILL
}
