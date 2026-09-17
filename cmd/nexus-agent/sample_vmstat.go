package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sampleVmstatPath is the /proc/vmstat location; tests point it at a fixture.
var sampleVmstatPath = "/proc/vmstat"

// sampleSwapsPath is the /proc/swaps location; tests point it at a fixture.
var sampleSwapsPath = "/proc/swaps"

// sampleBlockSysDir is the /sys/class/block root under which the swap
// device's stat file lives (<dir>/<dev>/stat); tests point it at a fixture.
var sampleBlockSysDir = "/sys/class/block"

// sectorsPerPage converts block-layer 512-byte sectors into 4 KiB pages. A
// swap-in on a block-backed swap device is one page-sized read bio, so read
// sectors / 8 == pages swapped in.
const sectorsPerPage = 8

// readSwapInPages returns the cumulative pages-swapped-in counter for
// Sample.SwapInPages. The shipped guest kernel has CONFIG_VM_EVENT_COUNTERS
// unset, so /proc/vmstat carries only nr_* gauges and never pswpin; the
// primary source is therefore the block-layer read-sector counter of the
// active swap device (/sys/class/block/<dev>/stat field 3, monotonic), scaled
// to pages. When no block swap device is active (swap file, no swap) the
// vmstat pswpin counter is used so a kernel with event counters still works.
// Any failure yields 0 and never an error: the host governor treats a zero
// delta as "not paging", which is the safe default.
func readSwapInPages(swapsPath, blockSysDir, vmstatPath string) uint64 {
	if dev := swapBlockDevice(swapsPath); dev != "" {
		if sectors, ok := readBlockReadSectors(filepath.Join(blockSysDir, dev, "stat")); ok {
			return sectors / sectorsPerPage
		}
	}
	return readVmstatPswpin(vmstatPath)
}

// swapBlockDevice returns the basename of the first active block-device
// swap entry in /proc/swaps (e.g. "zram0" for /dev/zram0), or "" when there
// is none or the file is unreadable. Swap files (no /dev/ prefix) have no
// per-device block stats, so they are skipped.
func swapBlockDevice(swapsPath string) string {
	f, err := os.Open(swapsPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 1 || !strings.HasPrefix(fields[0], "/dev/") {
			continue
		}
		return filepath.Base(fields[0])
	}
	return ""
}

// readBlockReadSectors parses the block-layer stat file at path (Documentation/
// admin-guide/iostats.rst: read I/Os, read merges, read sectors, ...) and
// returns field 3, the cumulative sectors read. ok is false when the file is
// missing or malformed.
func readBlockReadSectors(path string) (sectors uint64, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return 0, false
	}
	v, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readVmstatPswpin returns the cumulative pswpin (pages swapped in) counter
// from path. Any failure — missing file, missing key, unparsable value —
// yields 0 and never an error.
func readVmstatPswpin(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "pswpin" {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return v
	}
	return 0
}
