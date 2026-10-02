package vmcfg

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// HostCapacity is the host size used to derive default sandbox ceilings.
// A zero field means "unknown"; Resolve then falls back to the fixed floors.
type HostCapacity struct {
	NCPU   uint32
	RAMMiB uint64
}

// HostCapacityFunc reports host capacity. Tests replace it to pin the host.
var HostCapacityFunc = detectHostCapacity

func detectHostCapacity() HostCapacity {
	hc := HostCapacity{}
	if n := runtime.NumCPU(); n > 0 {
		hc.NCPU = uint32(n) //nolint:gosec // G115: NumCPU is small and positive
	}
	switch runtime.GOOS {
	case "linux":
		hc.RAMMiB = linuxMemTotalMiB("/proc/meminfo")
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			if b, perr := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); perr == nil {
				hc.RAMMiB = b / (1024 * 1024)
			}
		}
	}
	return hc
}

func linuxMemTotalMiB(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kib / 1024
		}
	}
	return 0
}
