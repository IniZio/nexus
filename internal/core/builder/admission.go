package builder

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

const (
	admitFloorMin int64   = 1 * 1024 * 1024 * 1024
	admitFloorPct float64 = 0.05
)

func admitFloor(total int64) int64 {
	pct := int64(float64(total) * admitFloorPct)
	if pct > admitFloorMin {
		return pct
	}
	return admitFloorMin
}

func AdmitBuilderBoot(bootMemMiB uint32, readMem func() (avail, total int64, err error)) error {
	avail, total, err := readMem()
	if err != nil {
		log.Printf("builder: admission: cannot read host meminfo (%v); allowing boot", err)
		return nil
	}
	floor := admitFloor(total)
	need := int64(bootMemMiB)*1024*1024 + floor
	if avail < need {
		availMiB := avail / (1024 * 1024)
		needMiB := need / (1024 * 1024)
		floorMiB := floor / (1024 * 1024)
		return fmt.Errorf("builder: host has %d MiB available, need %d MiB (boot %d MiB + floor %d MiB) — stop or pause a sandbox and retry",
			availMiB, needMiB, bootMemMiB, floorMiB)
	}
	return nil
}

// GuestMemCeiling reports the RAM ceiling a nexus guest was booted with, from
// the --mem-ceiling=<bytes> argument the outer nexus puts on the kernel
// cmdline for its guest agent. ok is false outside a nexus guest.
func GuestMemCeiling(readCmdline func() ([]byte, error)) (ceiling int64, ok bool) {
	raw, err := readCmdline()
	if err != nil {
		return 0, false
	}
	for _, f := range strings.Fields(string(raw)) {
		if v, found := strings.CutPrefix(f, "--mem-ceiling="); found {
			n, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil || n <= 0 {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}

// ElasticMeminfo is the readMem for admission INSIDE a nexus guest. A guest's
// MemTotal is only its current balloon size: the outer governor grows it on
// PSI pressure up to --mem-ceiling, so measuring against MemAvailable at one
// instant refuses builds the guest could run a second later ("host has 559
// MiB available, need 3072 MiB" with 3 GiB of ceiling unused, 2026-09-19).
// Here avail = ceiling - used and total = ceiling, so admission bounds only
// what the governor could never provide. Outside a guest it is ProcfsMeminfo.
func ElasticMeminfo() (avail, total int64, err error) {
	avail, total, err = ProcfsMeminfo()
	if err != nil {
		return 0, 0, err
	}
	ceiling, ok := GuestMemCeiling(func() ([]byte, error) { return os.ReadFile("/proc/cmdline") })
	if !ok || ceiling <= total {
		return avail, total, nil
	}
	used := total - avail
	return ceiling - used, ceiling, nil
}

func ProcfsMeminfo() (avail, total int64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var gotAvail, gotTotal bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "MemAvailable":
			avail, err = parseMemKB(val)
			if err != nil {
				return 0, 0, err
			}
			gotAvail = true
		case "MemTotal":
			total, err = parseMemKB(val)
			if err != nil {
				return 0, 0, err
			}
			gotTotal = true
		}
		if gotAvail && gotTotal {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if !gotAvail || !gotTotal {
		return 0, 0, fmt.Errorf("builder: MemAvailable or MemTotal missing from /proc/meminfo")
	}
	return avail, total, nil
}

func parseMemKB(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " kB")
	s = strings.TrimSpace(s)
	kb, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return kb * 1024, nil
}
