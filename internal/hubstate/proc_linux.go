package hubstate

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const bootIDPath = "/proc/sys/kernel/random/boot_id"

func BootID() (string, error) {
	b, err := os.ReadFile(bootIDPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ProcOf identifies a process by pid, starttime and boot_id.
func ProcOf(pid int) (Proc, error) {
	boot, err := BootID()
	if err != nil {
		return Proc{}, err
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return Proc{}, err
	}
	st, err := parseStarttime(string(b))
	if err != nil {
		return Proc{}, err
	}
	return Proc{PID: pid, Starttime: st, BootID: boot}, nil
}

// parseStarttime reads field 22 of /proc/<pid>/stat. comm (field 2) may
// contain spaces and parens, so fields are counted after the last ')'.
func parseStarttime(stat string) (uint64, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("hubstate: malformed stat: no ')'")
	}
	f := strings.Fields(stat[i+1:])
	// f[0] is field 3 (state); starttime is field 22 -> f[19].
	if len(f) < 20 {
		return 0, fmt.Errorf("hubstate: malformed stat: %d fields after comm", len(f))
	}
	return strconv.ParseUint(f[19], 10, 64)
}

// Alive reports whether p still names the same live process. A reused pid
// has a different starttime; a different boot_id means a reboot.
func Alive(p Proc) bool {
	if p.PID <= 0 || p.Starttime == 0 {
		return false
	}
	cur, err := ProcOf(p.PID)
	if err != nil {
		return false
	}
	return cur.BootID == p.BootID && cur.Starttime == p.Starttime
}

func realStatfsType(path string) (int64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return int64(s.Type), nil
}
