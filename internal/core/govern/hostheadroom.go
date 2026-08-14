package govern

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// HostHeadroomReader reports whether the host has enough free memory to
// absorb a proposed grow increment without pushing the host toward OOM.
//
// The interface is defined here (not embedded in the driver) so the governor
// can be unit-tested with a fake without reading /proc/meminfo.
type HostHeadroomReader interface {
	// HasHeadroom returns true if the host can absorb growBytes of additional
	// allocation. ctx carries a deadline for the underlying read.
	//
	// Fail-conservative contract: when the read fails, the caller MUST treat
	// the result as false (deny the grow). This is the inverse of OLD-nexus
	// HostPressureReader which is fail-open (memory_resize.go:24-27); see the
	// evaluate docstring for the rationale.
	HasHeadroom(ctx context.Context, growBytes int64) (bool, error)
}

const (
	// hostMemAvailFloorMin is the hard minimum headroom floor (1 GiB).
	// Source: OLD memory_resize.go:31 hostMemAvailFloorMin.
	hostMemAvailFloorMin int64 = 1 * 1024 * 1024 * 1024

	// hostMemAvailFloorPct is the percentage of host MemTotal kept as a
	// headroom floor: max(1 GiB, 5% of MemTotal).
	// Source: OLD memory_resize.go:798-806 hostMemAvailFloor.
	hostMemAvailFloorPct = 0.05
)

// hostMemAvailFloor returns the dynamic headroom floor: max(1 GiB, 5% of
// memTotal). The governor refuses a grow that would leave host MemAvailable
// below this floor.
//
// Source: OLD memory_resize.go:798-806.
func hostMemAvailFloor(memTotal int64) int64 {
	fivePct := int64(float64(memTotal) * hostMemAvailFloorPct)
	if fivePct > hostMemAvailFloorMin {
		return fivePct
	}
	return hostMemAvailFloorMin
}

// procfsHeadroom implements HostHeadroomReader by reading /proc/meminfo.
// It is the production implementation used by the supervisor. Tests inject a
// fake HostHeadroomReader instead.
type procfsHeadroom struct {
	path string // /proc/meminfo; overridden in tests via NewProcfsHeadroomAt
}

// NewProcfsHeadroom returns a HostHeadroomReader backed by /proc/meminfo.
func NewProcfsHeadroom() HostHeadroomReader {
	return &procfsHeadroom{path: "/proc/meminfo"}
}

// newProcfsHeadroomAt returns a HostHeadroomReader backed by an arbitrary
// meminfo path. Used in tests to inject a synthetic /proc/meminfo.
func newProcfsHeadroomAt(path string) HostHeadroomReader {
	return &procfsHeadroom{path: path}
}

// HasHeadroom reads /proc/meminfo to check whether the host can absorb
// growBytes without pushing MemAvailable below the dynamic floor.
//
// The floor is max(1 GiB, 5% of MemTotal). If MemAvailable − growBytes < floor,
// the grow is refused. If /proc/meminfo cannot be read, the error is returned
// and the caller treats it as false (fail-conservative).
func (h *procfsHeadroom) HasHeadroom(_ context.Context, growBytes int64) (bool, error) {
	avail, total, err := readMeminfo(h.path)
	if err != nil {
		return false, fmt.Errorf("govern: read host meminfo: %w", err)
	}
	floor := hostMemAvailFloor(total)
	// After the grow, at least floor bytes must remain available.
	return avail-growBytes >= floor, nil
}

// readMeminfo parses a Linux /proc/meminfo file and returns (MemAvailable,
// MemTotal) in bytes. Returns an error if either field is absent.
func readMeminfo(path string) (memAvailable, memTotal int64, err error) {
	f, err := os.Open(path)
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
		key = strings.TrimSpace(key)
		// Values are in kB; convert to bytes.
		switch key {
		case "MemAvailable":
			memAvailable, err = parseKB(val)
			if err != nil {
				return 0, 0, fmt.Errorf("govern: parse MemAvailable: %w", err)
			}
			gotAvail = true
		case "MemTotal":
			memTotal, err = parseKB(val)
			if err != nil {
				return 0, 0, fmt.Errorf("govern: parse MemTotal: %w", err)
			}
			gotTotal = true
		}
		if gotAvail && gotTotal {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("govern: scan meminfo: %w", err)
	}
	if !gotAvail {
		return 0, 0, fmt.Errorf("govern: MemAvailable not found in %s", path)
	}
	if !gotTotal {
		return 0, 0, fmt.Errorf("govern: MemTotal not found in %s", path)
	}
	return memAvailable, memTotal, nil
}

// parseKB parses a /proc/meminfo value like "  1234567 kB" and returns the
// value in bytes.
func parseKB(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " kB")
	s = strings.TrimSpace(s)
	kb, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return kb * 1024, nil
}
