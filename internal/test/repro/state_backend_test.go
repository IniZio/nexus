package repro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLogFixture creates a temporary log file containing the given lines and
// returns its path. The file is automatically removed when the test ends.
func writeLogFixture(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "build.log")
	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("writeLogFixture: %v", err)
	}
	return p
}

// Real log-line fixtures — verbatim substrings from buildkit_linux.go.
const (
	// logLineExt4 mirrors buildkit_linux.go (full message from log.Printf).
	logLineExt4 = `2026/08/29 12:00:00 in-guest build: /var/lib/buildkit is a persistent ext4 mount — skipping tmpfs, layer cache will persist`

	// logLineVirtiofs mirrors buildkit_linux.go (full message from log.Printf).
	// %s → /var/lib/buildkit, %v → mount error string.
	logLineVirtiofs = `2026/08/29 12:00:00 in-guest build: WARNING: tmpfs on /var/lib/buildkit failed (operation not permitted); state will be on virtiofs`

	// logLineRamTmpfs mirrors buildkit_linux.go (full message from log.Printf).
	logLineRamTmpfs = `2026/08/29 12:00:00 in-guest build: WARNING: /var/lib/buildkit is a 4 GiB RAM tmpfs — no cache disk attached; layer cache will not persist and large COPYs are bounded by guest RAM`

	// logLineSentinel mirrors the manifest-channel sentinel used by ParseManifestStageA.
	// Included as surrounding noise to confirm we scan the full log correctly.
	logLineSentinel = `2026/08/29 12:00:00 in-guest build: manifest-channel: active`
)

// TestParseStateBackend_PersistentExt4 verifies detection of the ext4 line.
func TestParseStateBackend_PersistentExt4(t *testing.T) {
	logPath := writeLogFixture(t,
		"some preamble line",
		logLineExt4,
		logLineSentinel,
	)

	backend, probe := ParseStateBackend(logPath)

	if backend != PersistentExt4 {
		t.Errorf("backend: got %s, want PersistentExt4", backend)
	}
	if probe.Verdict != NoTruncationObserved {
		t.Errorf("probe.Verdict: got %s, want NoTruncationObserved", probe.Verdict)
	}
	if probe.Probe != "builder.state_backend" {
		t.Errorf("probe.Probe: got %q, want %q", probe.Probe, "builder.state_backend")
	}
}

// TestParseStateBackend_Virtiofs verifies detection of the virtiofs-fallback line.
func TestParseStateBackend_Virtiofs(t *testing.T) {
	logPath := writeLogFixture(t,
		"some preamble line",
		logLineVirtiofs,
		logLineSentinel,
	)

	backend, probe := ParseStateBackend(logPath)

	if backend != Virtiofs {
		t.Errorf("backend: got %s, want Virtiofs", backend)
	}
	if probe.Verdict != NoTruncationObserved {
		t.Errorf("probe.Verdict: got %s, want NoTruncationObserved", probe.Verdict)
	}
	if probe.Probe != "builder.state_backend" {
		t.Errorf("probe.Probe: got %q, want %q", probe.Probe, "builder.state_backend")
	}
}

// TestParseStateBackend_RamTmpfs verifies detection of the successful-tmpfs
// WARNING line. This line is emitted by buildkit_linux.go when mountTmpFS
// succeeds — the case most relevant to hollow-export diagnosis under memory
// pressure.
func TestParseStateBackend_RamTmpfs(t *testing.T) {
	logPath := writeLogFixture(t,
		"some preamble line",
		logLineRamTmpfs,
		logLineSentinel,
	)

	backend, probe := ParseStateBackend(logPath)

	if backend != RamTmpfs {
		t.Errorf("backend: got %s, want RamTmpfs", backend)
	}
	if probe.Verdict != NoTruncationObserved {
		t.Errorf("probe.Verdict: got %s, want NoTruncationObserved", probe.Verdict)
	}
	if probe.Probe != "builder.state_backend" {
		t.Errorf("probe.Probe: got %q, want %q", probe.Probe, "builder.state_backend")
	}
}

// TestParseStateBackend_NoBackendLine confirms that a log with none of the
// three backend lines yields Unknown → HIF.
func TestParseStateBackend_NoBackendLine(t *testing.T) {
	logPath := writeLogFixture(t,
		"some preamble line",
		logLineSentinel,
		"rootfs-size-manifest: 3 file(s) total",
	)

	backend, probe := ParseStateBackend(logPath)

	if backend != Unknown {
		t.Errorf("backend: got %s, want Unknown (no backend line present)", backend)
	}
	if probe.Verdict != HarnessIntegrityFailure {
		t.Errorf("probe.Verdict: got %s, want HarnessIntegrityFailure", probe.Verdict)
	}
	if probe.Detail != "no backend line in guest log" {
		t.Errorf("probe.Detail: got %q, want %q", probe.Detail, "no backend line in guest log")
	}
}

// TestParseStateBackend_MissingLog verifies HIF on a missing log file.
func TestParseStateBackend_MissingLog(t *testing.T) {
	backend, probe := ParseStateBackend("/tmp/does-not-exist-nexus3-repro-state-backend-test.log")

	if backend != Unknown {
		t.Errorf("backend: got %s, want Unknown", backend)
	}
	if probe.Verdict != HarnessIntegrityFailure {
		t.Errorf("probe.Verdict: got %s, want HarnessIntegrityFailure", probe.Verdict)
	}
	if probe.Probe != "builder.state_backend" {
		t.Errorf("probe.Probe: got %q, want %q", probe.Probe, "builder.state_backend")
	}
}

// ── Mutation proofs ───────────────────────────────────────────────────────────
//
// MUTATION: comment out the `strings.Contains(line, ext4LogFragment)` branch
// in state_backend.go → TestParseStateBackend_MutationProof_Ext4Detection FAILS:
//
//	--- FAIL: TestParseStateBackend_MutationProof_Ext4Detection (0.00s)
//	    state_backend_test.go: backend: got Unknown, want PersistentExt4
//	    state_backend_test.go: probe.Verdict: got HarnessIntegrityFailure, want NoTruncationObserved
//
// MUTATION: comment out the `strings.Contains(line, ramTmpfsFragment)` branch
// in state_backend.go → TestParseStateBackend_MutationProof_RamTmpfsDetection FAILS:
//
//	--- FAIL: TestParseStateBackend_MutationProof_RamTmpfsDetection (0.00s)
//	    state_backend_test.go: backend: got Unknown, want RamTmpfs
//	    state_backend_test.go: probe.Verdict: got HarnessIntegrityFailure, want NoTruncationObserved
//
// RESTORE the branch → PASS.

// TestParseStateBackend_MutationProof_Ext4Detection is the live test targeted
// by the ext4 mutation above.
func TestParseStateBackend_MutationProof_Ext4Detection(t *testing.T) {
	logPath := writeLogFixture(t, logLineExt4)

	backend, probe := ParseStateBackend(logPath)

	if backend != PersistentExt4 {
		t.Errorf("backend: got %s, want PersistentExt4", backend)
	}
	if probe.Verdict != NoTruncationObserved {
		t.Errorf("probe.Verdict: got %s, want NoTruncationObserved", probe.Verdict)
	}
}

// TestParseStateBackend_MutationProof_RamTmpfsDetection is the live test
// targeted by the ramTmpfs mutation above.
func TestParseStateBackend_MutationProof_RamTmpfsDetection(t *testing.T) {
	logPath := writeLogFixture(t, logLineRamTmpfs)

	backend, probe := ParseStateBackend(logPath)

	if backend != RamTmpfs {
		t.Errorf("backend: got %s, want RamTmpfs", backend)
	}
	if probe.Verdict != NoTruncationObserved {
		t.Errorf("probe.Verdict: got %s, want NoTruncationObserved", probe.Verdict)
	}
}

// ── Drift guard ───────────────────────────────────────────────────────────────
//
// TestParseStateBackend_RamTmpfsDriftGuard ensures that buildkit_linux.go
// still contains the exact substring that ramTmpfsFragment matches. If the log
// line in buildkit_linux.go is changed without updating ramTmpfsFragment (or
// vice-versa), this test fails with a clear message identifying the mismatch.
//
// This replaces the need for a cross-package import of internal/core/agent
// (which would pull linux-only unix dependencies into the repro package).
func TestParseStateBackend_RamTmpfsDriftGuard(t *testing.T) {
	// Walk up from this test file's directory to find buildkit_linux.go.
	// The repro package sits at internal/test/repro; the target is at
	// internal/core/agent/buildkit_linux.go — three levels up then back down.
	candidates := []string{
		"../../../internal/core/agent/buildkit_linux.go",    // relative from test working dir
		"internal/core/agent/buildkit_linux.go",            // from repo root
	}

	// Resolve via the module root: look for go.mod walking upward.
	repoRoot := findRepoRoot(t)
	target := filepath.Join(repoRoot, "internal", "core", "agent", "buildkit_linux.go")
	candidates = append([]string{target}, candidates...)

	var data []byte
	var readErr error
	for _, c := range candidates {
		data, readErr = os.ReadFile(c)
		if readErr == nil {
			break
		}
	}
	if readErr != nil {
		t.Fatalf("drift guard: could not read buildkit_linux.go from any candidate path: %v", readErr)
	}

	if !strings.Contains(string(data), ramTmpfsFragment) {
		t.Errorf("DRIFT: ramTmpfsFragment %q not found in buildkit_linux.go — "+
			"update the log.Printf in buildkit_linux.go or update ramTmpfsFragment in state_backend.go",
			ramTmpfsFragment)
	}
}

// TestParseStateBackend_SlogWrapped verifies that ParseStateBackend tolerates
// slog-wrapped log lines. ParseStateBackend uses strings.Contains so it is
// already tolerant — this test proves the invariant.
func TestParseStateBackend_SlogWrapped(t *testing.T) {
	// Verbatim slog-form line from conc-wave1-slot0-20260829-091808.log.
	slogLine := `time=2026-08-29T09:18:14.094Z level=INFO msg="in-guest build: /var/lib/buildkit is a persistent ext4 mount — skipping tmpfs, layer cache will persist"`
	logPath := writeLogFixture(t, slogLine)

	backend, probe := ParseStateBackend(logPath)

	if backend != PersistentExt4 {
		t.Errorf("backend: got %s, want PersistentExt4; slog-wrapped line not recognized", backend)
	}
	if probe.Verdict != NoTruncationObserved {
		t.Errorf("probe.Verdict: got %s, want NoTruncationObserved", probe.Verdict)
	}
}

// findRepoRoot walks upward from the current directory until it finds a go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("findRepoRoot: getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("findRepoRoot: go.mod not found walking up from %s", dir)
		}
		dir = parent
	}
}
