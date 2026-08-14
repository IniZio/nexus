//go:build linux

package main

// Guest-side auto-resize actuators: telemetry sample collection (from
// /proc and sysfs), disk.grow handler (resize2fs), and vCPU onliner.
//
// Ported / adapted from OLD-nexus:
//   - cpu_online.go  → startCPUOnliner / onlineOfflineCPUs
//   - memory_stats.go → readMeminfoKB, readMemoryPSI, readCPUPSI
//
// All injectable seams (sampleMeminfoPath, sampleStatfsFunc, resizeExecFunc,
// etc.) default to the real OS calls; tests replace them with fixtures and
// fake execs so the logic runs without root or a VM.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// Injectable seams — real implementations by default; replaced in unit tests.
var (
	sampleMeminfoPath = "/proc/meminfo"
	samplePSIMemPath  = "/proc/pressure/memory"
	samplePSICPUPath  = "/proc/pressure/cpu"
	sampleStatfsFunc  = func(path string, st *unix.Statfs_t) error { return unix.Statfs(path, st) }
	sampleCPUSysPath  = "/sys/devices/system/cpu"
	resizeExecFunc    = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
)

// collectSample reads the current guest state and returns a [resize.Sample]
// for the host governor. Called per telemetry poll (one vsock connection).
//
// PSI fields are set to zero with Supported=false when the PSI file is absent
// or unreadable. This is critical: zero pressure with Supported=true would
// look healthy and suppress a grow decision. Supported=false lets the governor
// fall back to the MemAvailable ratio alone (motive.md §Axis-1, item 4).
func collectSample(workspacePath string) (resize.Sample, error) {
	avail, total, err := readMeminfoKB(sampleMeminfoPath)
	if err != nil {
		return resize.Sample{}, fmt.Errorf("collectSample: meminfo: %w", err)
	}
	memSomeAvg10, memFullAvg10, memPSISupported := readMemoryPSI(samplePSIMemPath)
	cpuSomeAvg10, cpuPSISupported := readCPUPSI(samplePSICPUPath)
	diskUsed, diskTotal, diskSupported := readDiskStats(workspacePath)
	vcpuCount, vcpuOnline := readVCPUs(sampleCPUSysPath)

	return resize.Sample{
		Timestamp:         time.Now().UTC(),
		MemAvailableBytes: avail * 1024,
		MemTotalBytes:     total * 1024,
		MemPSISomeAvg10:   memSomeAvg10,
		MemPSIFullAvg10:   memFullAvg10,
		MemPSISupported:   memPSISupported,
		CPUPSISomeAvg10:   cpuSomeAvg10,
		CPUPSISupported:   cpuPSISupported,
		DiskUsedBytes:     diskUsed,
		DiskTotalBytes:    diskTotal,
		DiskSupported:     diskSupported,
		VCPUCount:         vcpuCount,
		VCPUOnline:        vcpuOnline,
	}, nil
}

// readMeminfoKB parses /proc/meminfo and returns MemAvailable and MemTotal in
// kibibytes. Returns an error if the file cannot be read or either key is absent.
func readMeminfoKB(path string) (avail, total uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	var foundAvail, foundTotal bool
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if v, ok := meminfoLineKB(line, "MemTotal"); ok {
			total = v
			foundTotal = true
		} else if v, ok := meminfoLineKB(line, "MemAvailable"); ok {
			avail = v
			foundAvail = true
		}
		if foundTotal && foundAvail {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if !foundTotal {
		return 0, 0, fmt.Errorf("MemTotal not found in %s", path)
	}
	if !foundAvail {
		return 0, 0, fmt.Errorf("MemAvailable not found in %s", path)
	}
	return avail, total, nil
}

// meminfoLineKB parses one "Key: N kB" line from /proc/meminfo.
// Returns the value in kB and true on a match, zero and false otherwise.
func meminfoLineKB(line, key string) (uint64, bool) {
	prefix := key + ":"
	if !strings.HasPrefix(line, prefix) {
		return 0, false
	}
	fields := strings.Fields(line[len(prefix):])
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// readMemoryPSI reads /proc/pressure/memory and returns the some/full avg10
// averages (percent, 0–100) and Supported=true. Returns zeros and
// Supported=false when the file is absent or unreadable.
//
// Intentionally does NOT return zeros with Supported=true on a missing file:
// zero pressure looks healthy and would suppress a governor grow decision.
func readMemoryPSI(path string) (someAvg10, fullAvg10 float64, supported bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false // PSI absent or disabled; not a healthy zero
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		switch {
		case strings.HasPrefix(line, "some "):
			someAvg10, _ = psiAvg10(line)
		case strings.HasPrefix(line, "full "):
			fullAvg10, _ = psiAvg10(line)
		}
	}
	return someAvg10, fullAvg10, true
}

// readCPUPSI reads /proc/pressure/cpu and returns the some avg10 average
// (percent) and Supported=true. CPU PSI has no "full" line. Returns zero and
// Supported=false when the file is absent or unreadable.
func readCPUPSI(path string) (someAvg10 float64, supported bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "some ") {
			someAvg10, _ = psiAvg10(line)
			break
		}
	}
	return someAvg10, true
}

// psiAvg10 extracts the avg10= field from a PSI pressure line.
// Format: "some avg10=X.XX avg60=X.XX avg300=X.XX total=N"
func psiAvg10(line string) (float64, error) {
	for _, field := range strings.Fields(line) {
		if val, ok := strings.CutPrefix(field, "avg10="); ok {
			return strconv.ParseFloat(val, 64)
		}
	}
	return 0, fmt.Errorf("avg10 not found in PSI line: %q", line)
}

// readDiskStats returns disk used/total bytes and whether disk telemetry is
// available. supported is false when workspacePath is empty or statfs fails —
// the host governor must not act on disk metrics when supported is false.
// Returning (0, 0, false) is unambiguously "no data"; returning (0, 0, true)
// would be "the disk is completely empty", which is a valid (if rare) state.
func readDiskStats(workspacePath string) (used, total uint64, supported bool) {
	if workspacePath == "" {
		return 0, 0, false
	}
	var st unix.Statfs_t
	if err := sampleStatfsFunc(workspacePath, &st); err != nil {
		return 0, 0, false
	}
	bsize := uint64(st.Bsize) //nolint:gosec // Bsize is always positive
	total = st.Blocks * bsize
	used = (st.Blocks - st.Bfree) * bsize
	return used, total, true
}

// cpuDirPattern matches cpuN sysfs entries (cpu0, cpu1, …).
// Deliberately excludes "cpuidle", "cpufreq", and similar non-CPU directories.
var cpuDirPattern = regexp.MustCompile(`^cpu[0-9]+$`)

// readVCPUs counts total and online vCPUs from /sys/devices/system/cpu/.
// cpu0 always counts as online (its online file is absent; it cannot be
// offlined). Returns (0, 0) on error.
func readVCPUs(sysPath string) (count, online int32) {
	entries, err := os.ReadDir(sysPath)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !cpuDirPattern.MatchString(e.Name()) {
			continue
		}
		count++
		onlinePath := sysPath + "/" + e.Name() + "/online"
		val, err := os.ReadFile(onlinePath)
		if err != nil {
			if os.IsNotExist(err) {
				online++ // cpu0: always online, no file
			}
			continue
		}
		if len(val) > 0 && val[0] == '1' {
			online++
		}
	}
	return count, online
}

// handleDiskGrow runs an online resize2fs against the workspace disk
// identified by req.DiskIndex. Device path is derived from the index:
// ExtraDisks[0]→/dev/vdb, [1]→/dev/vdc, … (matching the host's attachment
// order — workspace disk is LAST; extra disks start at /dev/vdb).
//
// An ext4 assertion is performed before resize2fs. A wrong index means
// resize2fs against the wrong filesystem: data loss, not a failed build
// (motive.md §HB — Gap 2, D-DC-15).
func handleDiskGrow(req resize.GrowRequest) resize.GrowResponse {
	if req.DiskIndex < 0 || req.DiskIndex > 25 {
		return resize.GrowResponse{
			Error: fmt.Sprintf("disk_index %d out of range [0,25]", req.DiskIndex),
		}
	}
	device := fmt.Sprintf("/dev/vd%c", rune('b'+req.DiskIndex))

	// Assert ext4 before resize2fs. blkid returns the filesystem type without
	// mounting; a mismatch means the index is wrong. Fail loud — data loss is
	// worse than a failed build.
	out, err := resizeExecFunc("blkid", "-o", "value", "-s", "TYPE", device)
	if err != nil {
		return resize.GrowResponse{
			Error: fmt.Sprintf("blkid %s: %v: %s", device, err, strings.TrimSpace(string(out))),
		}
	}
	fstype := strings.TrimSpace(string(out))
	if fstype != "ext4" {
		return resize.GrowResponse{
			Error: fmt.Sprintf(
				"device %s has fs type %q, want ext4; refusing resize2fs to prevent data loss",
				device, fstype,
			),
		}
	}

	// Run resize2fs online (-f forces even when last-fsck time is recent).
	out, err = resizeExecFunc("resize2fs", "-f", device)
	if err != nil {
		return resize.GrowResponse{
			Error: fmt.Sprintf("resize2fs %s: %v: %s", device, err, strings.TrimSpace(string(out))),
		}
	}
	resultBytes := parseResize2fsBytes(string(out), req.TargetBytes)
	return resize.GrowResponse{ResultBytes: resultBytes}
}

// parseResize2fsBytes extracts the new filesystem size in bytes from resize2fs
// output. resize2fs prints: "The filesystem on /dev/vdX is now NNNNN (Mk) blocks long."
// Falls back to targetBytes if parsing fails — ResultBytes is used only for logging.
func parseResize2fsBytes(output string, targetBytes int64) int64 {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "blocks long") {
			continue
		}
		fields := strings.Fields(line)
		// Locate the "(Nk)" token; the block count precedes it.
		for i, f := range fields {
			if i == 0 || !strings.HasPrefix(f, "(") || !strings.HasSuffix(f, ")") {
				continue
			}
			blocks, err := strconv.ParseInt(fields[i-1], 10, 64)
			if err != nil {
				break
			}
			sizeStr := strings.ToLower(f[1 : len(f)-1]) // e.g. "4k"
			var blockBytes int64
			if strings.HasSuffix(sizeStr, "k") {
				kb, err := strconv.ParseInt(strings.TrimSuffix(sizeStr, "k"), 10, 64)
				if err != nil {
					break
				}
				blockBytes = kb * 1024
			} else {
				b, err := strconv.ParseInt(sizeStr, 10, 64)
				if err != nil {
					break
				}
				blockBytes = b
			}
			return blocks * blockBytes
		}
	}
	return targetBytes
}

// startCPUOnliner spawns a panic-guarded goroutine that periodically brings
// hot-plugged vCPUs online. A hot-plugged vCPU appears as offline in
// /sys/devices/system/cpu/cpuN/online == "0"; writing "1" makes it schedulable.
//
// Ported from OLD packages/nexus/cmd/nexus-guest-agent/cpu_online.go (3 s ticker).
func startCPUOnliner(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "nexus3-agent: cpu-onliner: panic recovered: %v\n", r)
			}
		}()

		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()

		// Online any CPUs already hot-plugged before we started.
		onlineOfflineCPUs()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				onlineOfflineCPUs()
			}
		}
	}()
}

// onlineOfflineCPUs reads /sys/devices/system/cpu/, finds every cpuN entry
// with online == "0", and writes "1". cpu0 has no online file (it cannot be
// offlined) — its absence is silently ignored. All errors are best-effort.
func onlineOfflineCPUs() {
	entries, err := os.ReadDir(sampleCPUSysPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus3-agent: cpu-onliner: ReadDir %s: %v\n", sampleCPUSysPath, err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !cpuDirPattern.MatchString(name) {
			continue
		}
		onlinePath := sampleCPUSysPath + "/" + name + "/online"
		val, err := os.ReadFile(onlinePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // cpu0: no online file
			}
			fmt.Fprintf(os.Stderr, "nexus3-agent: cpu-onliner: read %s: %v\n", onlinePath, err)
			continue
		}
		if len(val) == 0 || val[0] != '0' {
			continue
		}
		if err := os.WriteFile(onlinePath, []byte("1"), 0); err != nil {
			fmt.Fprintf(os.Stderr, "nexus3-agent: cpu-onliner: online %s: %v\n", name, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "nexus3-agent: cpu-onliner: onlined %s\n", name)
	}
}
