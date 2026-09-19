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

// InNexusGuest reports whether this process runs inside a nexus guest, which
// is exactly when the kernel cmdline carries --mem-ceiling.
func InNexusGuest() bool {
	_, ok := GuestMemCeiling(func() ([]byte, error) { return os.ReadFile("/proc/cmdline") })
	return ok
}

// AdmitBuilderBootHere is AdmitBuilderBoot against this machine's meminfo,
// except inside a nexus guest, where admission is skipped: the guest's
// MemTotal is its ceiling and MemAvailable is whatever the balloon has left
// it at this instant (77 MiB of 8 GiB was observed idle, 2026-09-19), and the
// balloon size is not visible from inside. The outer governor grows the guest
// on PSI pressure, so the builder is admitted and memory follows demand; the
// ceiling remains the hard bound, enforced by the outer host, not by a guess
// made here. Nested builds therefore rely on resource governance, not
// admission (operator decision 2026-09-19).
func AdmitBuilderBootHere(bootMemMiB uint32) error {
	if InNexusGuest() {
		log.Printf("builder: admission: inside a nexus guest; relying on the outer governor to grow memory (boot %d MiB)", bootMemMiB)
		return nil
	}
	return AdmitBuilderBoot(bootMemMiB, ProcfsMeminfo)
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
