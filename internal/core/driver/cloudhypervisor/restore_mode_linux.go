package cloudhypervisor

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	uffdAPI                 = 0xAA
	uffdFeatureMissingShmem = 1 << 5
	// _IOWR(0xAA, 0x3F, struct uffdio_api{3 x u64})
	uffdioAPI = 0xC018AA3F
)

type uffdioAPIArg struct{ api, features, ioctls uint64 }

func probeOnDemandRestore(dev string) error {
	f, err := os.OpenFile(dev, os.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: ondemand restore unavailable: open %s: %w", dev, err)
	}
	defer f.Close()
	return uffdCheckShmem(f.Fd(), dev)
}

func uffdCheckShmem(fd uintptr, what string) error {
	arg := uffdioAPIArg{api: uffdAPI, features: uffdFeatureMissingShmem}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, uffdioAPI, uintptr(unsafe.Pointer(&arg))); e != 0 {
		return fmt.Errorf("cloudhypervisor: ondemand restore unavailable: %s lacks UFFD_FEATURE_MISSING_SHMEM: %w", what, e)
	}
	return nil
}

// probeOnDemandRestoreAny tries dev, then the userfaultfd(2) syscall (allowed
// by vm.unprivileged_userfaultfd=1 or CAP_SYS_PTRACE), since CH may use either.
func probeOnDemandRestoreAny(dev string) error {
	devErr := probeOnDemandRestore(dev)
	if devErr == nil {
		return nil
	}
	fd, _, e := unix.Syscall(unix.SYS_USERFAULTFD, uintptr(unix.O_CLOEXEC), 0, 0)
	if e != 0 {
		return fmt.Errorf("%w; userfaultfd(2) syscall: %v (set vm.unprivileged_userfaultfd=1 or grant CAP_SYS_PTRACE)", devErr, e)
	}
	defer unix.Close(int(fd))
	return uffdCheckShmem(fd, "userfaultfd(2)")
}
