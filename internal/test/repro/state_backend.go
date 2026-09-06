package repro

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// StateBackend identifies the buildkit state storage backend used during a
// guest build. The value is inferred from log lines emitted by
// internal/core/agent/buildkit_linux.go.
type StateBackend int

const (
	// Unknown is the zero value — no backend line was found in the guest log,
	// or the log could not be read.
	Unknown StateBackend = iota
	// PersistentExt4 means /var/lib/buildkit was a pre-mounted ext4 volume;
	// layer cache persists across builds (production-parity configuration).
	PersistentExt4
	// RamTmpfs means a 4 GiB tmpfs was mounted on /var/lib/buildkit at build
	// start (fallback when no cache disk is provisioned). buildkit_linux.go
	// emits a WARNING log line on successful tmpfs mount; ParseStateBackend
	// detects it via ramTmpfsFragment.
	RamTmpfs
	// Virtiofs means the tmpfs mount failed; buildkit state landed on virtiofs.
	Virtiofs
)

func (b StateBackend) String() string {
	switch b {
	case PersistentExt4:
		return "PersistentExt4"
	case RamTmpfs:
		return "RamTmpfs"
	case Virtiofs:
		return "Virtiofs"
	default:
		return "Unknown"
	}
}

// ext4LogFragment is the unique substring in the persistent-ext4 log line
// (buildkit_linux.go). We match a fragment rather than the full line so minor
// message rewording does not silently break detection.
const ext4LogFragment = "persistent ext4 mount"

// virtiofsFallbackFragment is the unique substring in the virtiofs-fallback
// log line (buildkit_linux.go).
const virtiofsFallbackFragment = "state will be on virtiofs"

// ramTmpfsFragment is the unique substring in the successful-tmpfs log line
// (buildkit_linux.go). It must stay in sync with that file's log.Printf —
// state_backend_test.go greps buildkit_linux.go for this string to catch drift.
const ramTmpfsFragment = "4 GiB RAM tmpfs"

// ParseStateBackend scans the build log at logPath for the backend-selection
// log lines emitted by buildkit_linux.go and returns the backend that was used.
//
// Return rules:
//   - PersistentExt4 + probeOK  if the ext4-mount line is found.
//   - Virtiofs + probeOK         if the virtiofs-fallback line is found.
//   - RamTmpfs + probeOK         if the successful-tmpfs WARNING line is found.
//   - Unknown + probeHIF         if none of the three lines is present.
//   - Unknown + probeHIF         on any open or scan error.
//
// The returned ProbeResult uses the probe name "builder.state_backend".
// The caller is responsible for appending it to RunResult.Probes.
func ParseStateBackend(logPath string) (StateBackend, ProbeResult) {
	f, err := os.Open(logPath)
	if err != nil {
		return Unknown, probeHIF("builder.state_backend",
			fmt.Sprintf("open log: %v", err))
	}
	defer f.Close()

	// Raise scanner buffer to 4 MiB, matching ParseManifestStageA, so long
	// build-log lines (apt, runc, etc.) do not silently abort the scan.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, ext4LogFragment) {
			return PersistentExt4, probeOK("builder.state_backend", "PersistentExt4")
		}
		if strings.Contains(line, virtiofsFallbackFragment) {
			return Virtiofs, probeOK("builder.state_backend", "Virtiofs")
		}
		if strings.Contains(line, ramTmpfsFragment) {
			return RamTmpfs, probeOK("builder.state_backend", "RamTmpfs")
		}
	}
	if err := scanner.Err(); err != nil {
		return Unknown, probeHIF("builder.state_backend",
			fmt.Sprintf("scanner error: %v", err))
	}

	// No backend line found — the log is missing or the build was interrupted
	// before the backend-selection step. Backend is unverified → HIF.
	return Unknown, probeHIF("builder.state_backend", "no backend line in guest log")
}
