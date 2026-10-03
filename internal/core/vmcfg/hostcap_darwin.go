//go:build darwin

package vmcfg

import "golang.org/x/sys/unix"

func darwinMemMiB() uint64 {
	b, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return b / (1024 * 1024)
}
