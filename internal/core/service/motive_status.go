package service

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// sandboxProbeFn probes one sandbox for live state. It returns the supervisor
// process uptime in seconds (−1 means unknown/unavailable) and any error that
// signals the sandbox is unreachable. A non-nil error does NOT abort the
// overall batch — the caller records it in the row and continues.
type sandboxProbeFn func(sb domain.Sandbox) (uptimeSeconds int64, err error)

// SandboxStatusRow holds the per-sandbox status for one label-status row.
type SandboxStatusRow struct {
	Sandbox domain.Sandbox
	// AllocatedBytes is stat(2).Blocks*512 of the sandbox's .raw disk.
	// Zero when no disk file was found (disk not yet created, or already
	// removed from disk while the record survives).
	AllocatedBytes int64
	// UptimeSeconds is the estimated age of the supervisor process.
	// −1 means not determinable (no supervisor PID, PID unreadable, or
	// /proc unreadable).
	UptimeSeconds int64
	// Err is non-nil when the sandbox is unreachable or the per-sandbox
	// probe failed. The row is still present in the report; other rows
	// are unaffected.
	Err error
}

// LabelStatusReport aggregates the results of a LabelStatus call.
type LabelStatusReport struct {
	LabelKey   string
	LabelValue string
	Rows       []SandboxStatusRow
	// TotalAllocBytes is the sum of allocated disk bytes across all sandboxes
	// in the label group. Uses stat(2).Blocks*512 — never apparent size — to
	// avoid the 9× apparent-vs-allocated illusion on sparse ext4 images.
	TotalAllocBytes int64
	// LeakedCount is the number of ULID-keyed host resources (disks, sockets,
	// intent files) whose owning sandbox ID does not appear in the record
	// store. These are reclaimable via `nexus3 reap`. Sourced from
	// ResourceIndex (R1) without process probing.
	LeakedCount int
}

// LabelStatus returns a status report for all sandboxes matching key=value.
// Per-sandbox probe failures are recorded in each row and do not abort the
// call. The report is always non-nil when the returned error is nil.
func (s *Service) LabelStatus(ctx context.Context, key, value string) (*LabelStatusReport, error) {
	return s.labelStatus(ctx, key, value, NewResourceIndex(IndexConfig{}), probeSandboxLive)
}

// labelStatus is the testable core. Tests inject a custom *ResourceIndex
// (pointed at a temp dir) and a sandboxProbeFn (which can inject failures
// per sandbox without real processes).
func (s *Service) labelStatus(
	ctx context.Context,
	key, value string,
	idx *ResourceIndex,
	probe sandboxProbeFn,
) (*LabelStatusReport, error) {
	// 1. Fetch sandboxes matching the label key=value.
	sandboxes, err := s.store.GetByLabels(ctx, map[string]string{key: value})
	if err != nil {
		return nil, fmt.Errorf("label status %s=%q: fetch sandboxes: %w", key, value, err)
	}

	// 2. Enumerate all host resources for disk-size lookup and leak counting.
	resources, err := idx.List()
	if err != nil {
		return nil, fmt.Errorf("label status %s=%q: enumerate resources: %w", key, value, err)
	}

	// 3. Build allocated-bytes map: sandboxID → bytes on the primary .raw disk.
	// allocatedBytes uses stat(2).Blocks*512 — never os.Stat().Size() — so
	// sparse images report their real footprint, not their apparent size.
	diskBytes := make(map[domain.SandboxID]int64, len(resources))
	for _, r := range resources {
		if r.Kind == KindDiskRaw {
			diskBytes[r.OwnerID] = allocatedBytes(r.Path)
		}
	}

	// 4. Collect all known sandbox IDs for leak detection.
	allSandboxes, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("label status %s=%q: list all sandboxes: %w", key, value, err)
	}
	knownIDs := make(map[domain.SandboxID]bool, len(allSandboxes))
	for _, sb := range allSandboxes {
		knownIDs[sb.ID] = true
	}

	// 5. Count leaked resources: ULID-keyed resources with no store record.
	// Shadow disks are handle-keyed (OwnerID == zero value) and are excluded
	// from this count — their correlation requires a different mechanism.
	var leakedCount int
	for _, r := range resources {
		if r.OwnerID != (domain.SandboxID{}) && !knownIDs[r.OwnerID] {
			leakedCount++
		}
	}

	// 6. Build per-sandbox rows, collecting probe failures without aborting.
	rows := make([]SandboxStatusRow, 0, len(sandboxes))
	var totalAllocBytes int64
	for _, sb := range sandboxes {
		row := SandboxStatusRow{
			Sandbox:        sb,
			AllocatedBytes: diskBytes[sb.ID],
		}
		row.UptimeSeconds, row.Err = probe(sb)
		totalAllocBytes += row.AllocatedBytes
		rows = append(rows, row)
	}

	return &LabelStatusReport{
		LabelKey:        key,
		LabelValue:      value,
		Rows:            rows,
		TotalAllocBytes: totalAllocBytes,
		LeakedCount:     leakedCount,
	}, nil
}

// probeSandboxLive checks whether the sandbox's supervisor process is alive
// and estimates its uptime from /proc/<pid>/stat. This is the production
// implementation of sandboxProbeFn.
//
// Rules:
//   - No supervisor PID (in-process perimeter or pre-D-PP-01 sandboxes):
//     uptime = −1, err = nil. The sandbox may still be running without a
//     supervisor.
//   - Supervisor PID alive: uptime computed from /proc/<pid>/stat; err = nil.
//   - Supervisor PID dead (ESRCH) for a running/paused sandbox: the sandbox
//     is unreachable; err is set to flag the row. For stopped/created/error
//     sandboxes a dead PID is normal; err = nil, uptime = −1.
func probeSandboxLive(sb domain.Sandbox) (int64, error) {
	if sb.SupervisorPID <= 0 {
		return -1, nil
	}
	pid := sb.SupervisorPID
	if err := syscall.Kill(pid, 0); err != nil {
		if sb.State == domain.Running || sb.State == domain.Paused {
			return -1, fmt.Errorf("supervisor PID %d unreachable: %w", pid, err)
		}
		return -1, nil
	}
	u, err := pidUptimeSeconds(pid)
	if err != nil {
		// /proc unreadable: degrade gracefully.
		return -1, nil
	}
	return u, nil
}

// pidUptimeSeconds estimates how long PID has been alive using /proc/<pid>/stat
// (field 22, starttime in USER_HZ ticks since boot) and /proc/uptime (system
// uptime in seconds). Returns an error when either file is unreadable.
//
// USER_HZ is hardcoded to 100, matching the value returned by
// `getconf CLK_TCK` on all supported Linux platforms.
func pidUptimeSeconds(pid int) (int64, error) {
	// Read system uptime.
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("read /proc/uptime: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 1 {
		return 0, fmt.Errorf("unexpected /proc/uptime format: %q", string(raw))
	}
	sysUptimeSec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/uptime: %w", err)
	}

	// Read process starttime (field 22 in /proc/<pid>/stat).
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	f, err := os.Open(statPath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", statPath, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, fmt.Errorf("read %s: empty", statPath)
	}
	line := sc.Text()

	// The process name in field 2 can contain spaces and is enclosed in
	// parentheses. Strip everything up to and including the last ')' so
	// the remaining fields can be split safely.
	closeParen := strings.LastIndex(line, ")")
	if closeParen < 0 {
		return 0, fmt.Errorf("parse %s: missing closing parenthesis", statPath)
	}
	rest := strings.TrimSpace(line[closeParen+1:])
	parts := strings.Fields(rest)
	// After the closing ')' the fields are: state(3) ppid(4) pgrp(5) session(6)
	// tty_nr(7) tpgid(8) flags(9) minflt(10) cminflt(11) majflt(12) cmajflt(13)
	// utime(14) stime(15) cutime(16) cstime(17) priority(18) nice(19)
	// num_threads(20) itrealvalue(21) starttime(22)
	// → index 19 in parts (0-based).
	const startTimeIdx = 19
	if len(parts) <= startTimeIdx {
		return 0, fmt.Errorf("parse %s: too few fields (%d)", statPath, len(parts))
	}
	startTicks, err := strconv.ParseInt(parts[startTimeIdx], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s starttime: %w", statPath, err)
	}

	const userHz = 100
	startSec := float64(startTicks) / userHz
	uptimeSec := sysUptimeSec - startSec
	if uptimeSec < 0 {
		uptimeSec = 0
	}
	return int64(uptimeSec), nil
}

// FormatUptime formats an uptime duration (in seconds) as a compact human
// string (e.g. "2h3m", "45s"). Returns "−" for negative values, which
// indicates the uptime is unknown or unavailable.
func FormatUptime(seconds int64) string {
	if seconds < 0 {
		return "-"
	}
	d := time.Duration(seconds) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// FormatBytes formats an allocated byte count as a compact human string
// (GiB / MiB / KiB / B). Allocated bytes (stat.Blocks*512) are always
// smaller than apparent size for sparse images; never call this with
// os.FileInfo.Size() values to avoid reporting the illusion.
func FormatBytes(b int64) string {
	const (
		gib = 1 << 30
		mib = 1 << 20
		kib = 1 << 10
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	case b >= mib:
		return fmt.Sprintf("%.1f MiB", float64(b)/mib)
	case b >= kib:
		return fmt.Sprintf("%.1f KiB", float64(b)/kib)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
