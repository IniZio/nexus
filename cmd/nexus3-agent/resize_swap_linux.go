//go:build linux

package main

// ZRAM swap safety net for the guest auto-resize subsystem (AR-GA-AC5,
// D-DC-21). Must be called before the workload starts.
//
// Normative MUST (D-DC-21): docs/site/concepts/execution-substrate.md §Resource limits.
// Every cloudhypervisor guest must boot with compressed swap enabled before the
// workload starts, because memory grow has irreducible actuation latency — a
// burst allocator can OOM the guest before vm.resize completes. ZRAM converts
// that kill into a recoverable reclaim stall.
//
// Ported from OLD packages/nexus/cmd/nexus-guest-agent/swap.go.

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	zramControlPath = "/sys/class/zram-control"
	swappinessPath  = "/proc/sys/vm/swappiness"

	// swapSafetyNetSwappiness keeps ZRAM a last-resort bridge, not a routine
	// offload: the kernel reclaims file cache first and only spills anonymous
	// pages to ZRAM under genuine pressure. That refault stall is precisely
	// what drives the memory governor to grow real RAM (spec-08 §2.4 + §3.3).
	// A low (but non-zero) value keeps swap a thin latency bridge rather than
	// the primary response, so growth — not swapping — does the heavy lifting.
	swapSafetyNetSwappiness = 10

	zramMinBytes int64 = 1 << 30 // 1 GiB floor (spec-08 §2.4)
	zramMaxBytes int64 = 4 << 30 // 4 GiB cap
)

// Injectable seams for ZRAM — real implementations by default; replaced in tests.
var (
	zramControlExistsFunc = func() bool {
		_, err := os.Stat(zramControlPath)
		return err == nil
	}
	zramSwapActiveFunc = swapAlreadyActive
	zramMeminfoPath    = "/proc/meminfo" // shared with resize_actuate_linux.go
	zramProcSwapsPath  = "/proc/swaps"
	zramExecFunc       = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
	zramWriteFileFunc = os.WriteFile
)

// setupZRAMSwap enables a compressed RAM-backed swap device so an allocation
// burst that outpaces virtio-mem grow becomes a recoverable reclaim stall
// rather than an instant OOM kill. Best-effort and idempotent: no-ops
// gracefully on kernels without ZRAM (CONFIG_ZRAM=n) and when swap is already
// active; never fails boot. nexus3's kernel has CONFIG_ZRAM=y
// (scripts/kernel/config-6.12.76:1564).
//
// Ported from OLD packages/nexus/cmd/nexus-guest-agent/swap.go:setupZramSwap.
func setupZRAMSwap(con *os.File) {
	// Best-effort modprobe: CONFIG_ZRAM=m needs this; CONFIG_ZRAM=y makes it a
	// harmless no-op; CONFIG_ZRAM=n causes failure which the Stat guard catches.
	if modprobe, err := exec.LookPath("modprobe"); err == nil {
		out, err := zramExecFunc(modprobe, "zram")
		if err != nil {
			consoleLog(con, "nexus3-agent: zram-swap: modprobe zram skipped (non-fatal): %v: %s\n",
				err, strings.TrimSpace(string(out)))
		}
	}

	if !zramControlExistsFunc() {
		consoleLog(con, "nexus3-agent: zram-swap: %s absent (CONFIG_ZRAM not set); swap safety net disabled\n",
			zramControlPath)
		return
	}
	if zramSwapActiveFunc() {
		consoleLog(con, "nexus3-agent: zram-swap: swap already active; skipping setup\n")
		return
	}

	// readMeminfoKB is defined in resize_actuate_linux.go (same build tag).
	// It returns values in kibibytes; multiply by 1024 to get bytes before
	// the half-and-clamp calculation.
	_, totalKB, err := readMeminfoKB(zramMeminfoPath)
	if err != nil || totalKB == 0 {
		consoleLog(con, "nexus3-agent: zram-swap: cannot read MemTotal (%v); skipping\n", err)
		return
	}
	// Size at half of boot RAM. At boot MemTotal == the virtio-mem floor
	// (MemMinBytes), so half stays comfortably ≤ MemMinBytes per spec §2.4.
	size := int64(totalKB) * 1024 / 2 //nolint:gosec // totalKB is always positive
	if size < zramMinBytes {
		size = zramMinBytes
	}
	if size > zramMaxBytes {
		size = zramMaxBytes
	}

	dev, err := createZramDevice(size)
	if err != nil {
		consoleLog(con, "nexus3-agent: zram-swap: create device failed (non-fatal): %v\n", err)
		return
	}

	if out, err := zramExecFunc("mkswap", dev); err != nil {
		consoleLog(con, "nexus3-agent: zram-swap: mkswap %s failed (non-fatal): %v: %s\n",
			dev, err, strings.TrimSpace(string(out)))
		return
	}
	// Priority 100 so ZRAM is preferred over any future disk swap.
	if out, err := zramExecFunc("swapon", "--priority", "100", dev); err != nil {
		consoleLog(con, "nexus3-agent: zram-swap: swapon %s failed (non-fatal): %v: %s\n",
			dev, err, strings.TrimSpace(string(out)))
		return
	}
	if err := zramWriteFileFunc(swappinessPath,
		[]byte(strconv.Itoa(swapSafetyNetSwappiness)), 0o644); err != nil {
		consoleLog(con, "nexus3-agent: zram-swap: set swappiness failed (non-fatal): %v\n", err)
	}
	consoleLog(con, "nexus3-agent: zram-swap: enabled %s (%d MiB, swappiness=%d)\n",
		dev, size>>20, swapSafetyNetSwappiness)
}

// createZramDevice allocates a ZRAM device of the given size in bytes,
// preferring the zstd compressor (best ratio) and falling back to the kernel
// default when zstd is unavailable. Returns the device path (e.g. /dev/zram0).
func createZramDevice(size int64) (string, error) {
	sz := strconv.FormatInt(size, 10)
	if dev, err := zramFind(sz, "zstd"); err == nil {
		return dev, nil
	}
	return zramFind(sz, "")
}

// zramFind runs `zramctl --find --size <size> [--algorithm <algo>]`, which
// claims a free ZRAM device, configures it, and prints its path.
func zramFind(size, algo string) (string, error) {
	args := []string{"--find", "--size", size}
	if algo != "" {
		args = append(args, "--algorithm", algo)
	}
	out, err := zramExecFunc("zramctl", args...)
	if err != nil {
		return "", zramExecErr("zramctl", args, err, out)
	}
	dev := strings.TrimSpace(string(out))
	if dev == "" {
		return "", &zramError{op: "zramctl", msg: "returned no device path"}
	}
	return dev, nil
}

// swapAlreadyActive reports whether any swap device is currently active.
// /proc/swaps has a header line, so > 1 lines means swap is active.
func swapAlreadyActive() bool {
	data, err := os.ReadFile(zramProcSwapsPath)
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return len(lines) > 1
}

// zramExecErr formats an exec error for ZRAM operations.
func zramExecErr(cmd string, args []string, err error, out []byte) error {
	return &zramError{
		op:  cmd + " " + strings.Join(args, " "),
		msg: err.Error() + ": " + strings.TrimSpace(string(out)),
	}
}

type zramError struct {
	op  string
	msg string
}

func (e *zramError) Error() string { return e.op + ": " + e.msg }
