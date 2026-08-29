package repro

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeLogFile creates a temporary log file with the given content and returns its path.
func writeLogFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "build.log")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("writeLogFile: %v", err)
	}
	return p
}

// TestParseManifestStageA_SentinelPresentNoData is the mutation-proof test.
//
// It verifies that when the manifest-channel sentinel is present in the log
// (proving guestBuild ran the success-forward path) but no manifest data entries
// appear (simulating suppression of logRootfsSizeManifest in buildkit_linux.go),
// ParseManifestStageA returns a non-SkipVerdict HarnessIntegrityFailure — NOT
// a SkipVerdict not_collected probe.
//
// To run the mutation manually: comment out logRootfsSizeManifest(rootfsDir) in
// internal/core/agent/buildkit_linux.go, build a nexus3 binary, run the harness,
// and observe Stage A yields HIF. This unit test proves the same property
// against a synthetic log that matches what that mutation would produce.
func TestParseManifestStageA_SentinelPresentNoData(t *testing.T) {
	// Synthetic log: sentinel present (manifest-channel active), no data lines.
	// This matches what happens when logRootfsSizeManifest is suppressed while
	// guestBuild's forwarding code still runs.
	logContent := strings.Join([]string{
		"2026/08/29 10:00:00 INFO build-cache: miss — starting builder VM",
		"2026/08/29 10:01:00 in-guest build: manifest-channel: active",
		// No rootfs-size-manifest data lines — simulates suppressed emission.
		"2026/08/29 10:01:01 in-guest build: ext4 image at /dev/vdc",
	}, "\n") + "\n"

	logPath := writeLogFile(t, logContent)
	results := ParseManifestStageA(logPath, 0, 0)

	if len(results) == 0 {
		t.Fatal("expected at least one probe result, got none")
	}

	// The sentinel was seen but no data — must be HIF (non-SkipVerdict).
	found := false
	for _, r := range results {
		if r.Probe == "stageA.manifest" {
			found = true
			if r.SkipVerdict {
				t.Errorf("stageA.manifest SkipVerdict=true (not_collected) but sentinel was seen — "+
					"MUTATION PROOF FAIL: suppression of logRootfsSizeManifest must yield "+
					"non-skip HIF, not not_collected; detail=%q", r.Detail)
			} else if r.Verdict != HarnessIntegrityFailure {
				t.Errorf("stageA.manifest verdict=%v, want HarnessIntegrityFailure; detail=%q — "+
					"MUTATION PROOF FAIL", r.Verdict, r.Detail)
			} else {
				t.Logf("MUTATION PROOF PASS: sentinel present, no data → HIF detail=%q", r.Detail)
			}
		}
	}
	if !found {
		t.Errorf("no stageA.manifest probe in results: %v", results)
	}
}

// TestParseManifestStageA_NoSentinel_NotCollected verifies that when the
// manifest-channel sentinel is absent (old binary or pre-fix log), ParseManifestStageA
// returns a SkipVerdict probe (not_collected), not a non-skip HIF.
func TestParseManifestStageA_NoSentinel_NotCollected(t *testing.T) {
	logContent := strings.Join([]string{
		"2026/08/29 10:00:00 INFO build-cache: miss — starting builder VM",
		"2026/08/29 10:01:00 in-guest build: rootfs at /tmp/nexus3-...",
		// No sentinel, no manifest data.
	}, "\n") + "\n"

	logPath := writeLogFile(t, logContent)
	results := ParseManifestStageA(logPath, 0, 0)

	if len(results) == 0 {
		t.Fatal("expected at least one probe result, got none")
	}
	for _, r := range results {
		if r.Probe == "stageA.manifest" {
			if !r.SkipVerdict {
				t.Errorf("stageA.manifest SkipVerdict=false (non-skip HIF) but no sentinel present; "+
					"want not_collected (SkipVerdict=true); verdict=%v detail=%q", r.Verdict, r.Detail)
			} else {
				t.Logf("OK: no sentinel → not_collected (SkipVerdict=true) detail=%q", r.Detail)
			}
		}
	}
}

// TestParseManifestStageA_RealManifestLines verifies that correctly-formatted
// manifest data lines (3 spaces after colon, per logRootfsSizeManifest format)
// are parsed into per-file probes.
func TestParseManifestStageA_RealManifestLines(t *testing.T) {
	// The exact format from logRootfsSizeManifest:
	//   log.Printf("in-guest build: rootfs-size-manifest:   %-60s %d", relPath, size)
	// After "rootfs-size-manifest:" there are 3 spaces then a %-60s left-padded path.
	makeLine := func(relPath string, size int64) string {
		return fmt.Sprintf("2026/08/29 10:01:00 in-guest build: rootfs-size-manifest:   %-60s %d", relPath, size)
	}

	const agentSize = int64(45000000)
	lines := []string{
		"2026/08/29 10:00:00 INFO build-cache: miss",
		"2026/08/29 10:01:00 in-guest build: manifest-channel: active",
		// Header line (1 space, not 3) — must be skipped by parser.
		"2026/08/29 10:01:00 in-guest build: rootfs-size-manifest: 3 file(s) >= 1.0 MiB in /tmp/rootfs",
		makeLine("testfiles/file_33m", 34603008),
		makeLine("testfiles/file_64m", 67108864),
		makeLine("usr/sbin/nexus3-agent", agentSize),
	}
	logPath := writeLogFile(t, strings.Join(lines, "\n")+"\n")
	results := ParseManifestStageA(logPath, agentSize, 0)

	probeMap := make(map[string]ProbeResult)
	for _, r := range results {
		probeMap[r.Probe] = r
	}

	for _, tc := range []struct {
		probe string
	}{
		{"stageA.file_33m"},
		{"stageA.file_64m"},
		{"stageA.nexus3-agent"},
	} {
		r, ok := probeMap[tc.probe]
		if !ok {
			t.Errorf("missing probe %q; all probes: %v", tc.probe, probeMap)
			continue
		}
		if r.Verdict != NoTruncationObserved {
			t.Errorf("%s verdict=%v want NoTruncationObserved; detail=%q", tc.probe, r.Verdict, r.Detail)
		} else {
			t.Logf("OK: %s → NoTruncationObserved detail=%q", tc.probe, r.Detail)
		}
	}
}

// TestParseManifestStageA_TruncationDetected verifies a file at TruncationSentinel
// yields TruncationReproduced.
func TestParseManifestStageA_TruncationDetected(t *testing.T) {
	makeLine := func(relPath string, size int64) string {
		return fmt.Sprintf("2026/08/29 10:01:00 in-guest build: rootfs-size-manifest:   %-60s %d", relPath, size)
	}

	lines := []string{
		"2026/08/29 10:01:00 in-guest build: manifest-channel: active",
		makeLine("testfiles/file_64m", TruncationSentinel),
	}
	logPath := writeLogFile(t, strings.Join(lines, "\n")+"\n")
	results := ParseManifestStageA(logPath, 0, 0)

	probeMap := make(map[string]ProbeResult)
	for _, r := range results {
		probeMap[r.Probe] = r
	}
	r, ok := probeMap["stageA.file_64m"]
	if !ok {
		t.Fatalf("missing stageA.file_64m; all probes: %v", probeMap)
	}
	if r.Verdict != TruncationReproduced {
		t.Errorf("stageA.file_64m verdict=%v want TruncationReproduced; detail=%q", r.Verdict, r.Detail)
	} else {
		t.Logf("OK: truncated file_64m → TruncationReproduced detail=%q", r.Detail)
	}
}

// TestStageBHashProbesNilMap verifies that StageBHashProbes with a nil
// expectedHashes map returns only SkipVerdict probes (probeNotCollected),
// never a verdict-affecting HarnessIntegrityFailure.
//
// Mutation proof: temporarily revert the fix (make nil map → HIF) and this
// test fails; re-apply the fix and it passes.
func TestStageBHashProbesNilMap(t *testing.T) {
	// nil expectedHashes — no hash configured; no debugfs calls should occur.
	results := StageBHashProbes("", nil)

	if len(results) == 0 {
		t.Fatal("expected at least one probe result, got none")
	}
	for _, r := range results {
		if !r.SkipVerdict {
			t.Errorf("probe %q has SkipVerdict=false (verdict=%v detail=%q); "+
				"nil expectedHashes must yield only probeNotCollected (SkipVerdict=true)",
				r.Probe, r.Verdict, r.Detail)
		} else {
			t.Logf("OK: %s → SkipVerdict=true detail=%q", r.Probe, r.Detail)
		}
	}
}

// TestParseManifestStageA_SlogForm verifies that ParseManifestStageA correctly
// parses slog-wrapped manifest lines. The in-guest slog wraps log.Printf output
// inside a msg="..." quoted field, adding a trailing `"` that the fix strips via
// strings.TrimRight.
//
// MUTATION: comment out the TrimRight("\"") call in ParseManifestStageA → this
// test fails with stageA.file_200m missing or verdict HIF.
func TestParseManifestStageA_SlogForm(t *testing.T) {
	// Verbatim lines from conc-wave1-slot0-20260829-091808.log.
	// Sentinel is bare-form even in the slog log (comes from a different code path).
	logContent := strings.Join([]string{
		`2026/08/29 09:18:27 in-guest build: manifest-channel: active`,
		`time=2026-08-29T09:18:24.659Z level=INFO msg="in-guest build: rootfs-size-manifest: 24 file(s) >= 1.0 MiB in /var/lib/buildkit/nexus3-export/nexus3-inguestbuild-rootfs-879230242"`,
		`time=2026-08-29T09:18:24.659Z level=INFO msg="in-guest build: rootfs-size-manifest:   testfiles/file_200m                                          209715200"`,
		`time=2026-08-29T09:18:24.659Z level=INFO msg="in-guest build: rootfs-size-manifest:   testfiles/file_32m                                           33554433"`,
	}, "\n") + "\n"

	logPath := writeLogFile(t, logContent)
	results := ParseManifestStageA(logPath, 0, 0)

	if len(results) == 0 {
		t.Fatal("expected at least one probe result, got none")
	}

	probeMap := make(map[string]ProbeResult)
	for _, r := range results {
		probeMap[r.Probe] = r
	}

	for _, tc := range []struct{ probe string }{
		{"stageA.file_200m"},
		{"stageA.file_32m"},
	} {
		r, ok := probeMap[tc.probe]
		if !ok {
			t.Errorf("missing probe %q in slog-form parse; all probes: %v", tc.probe, probeMap)
			continue
		}
		if r.Verdict != NoTruncationObserved {
			t.Errorf("%s verdict=%v want NoTruncationObserved; detail=%q", tc.probe, r.Verdict, r.Detail)
		} else {
			t.Logf("OK: %s → NoTruncationObserved detail=%q", tc.probe, r.Detail)
		}
	}
}

// TestParseManifestStageA_BareForm verifies that bare-form (non-slog) manifest
// lines are still parsed correctly after the slog TrimRight fix did not break
// bare-form parsing.
func TestParseManifestStageA_BareForm(t *testing.T) {
	// Verbatim lines from baseline-20260829-074453.log.
	logContent := strings.Join([]string{
		`2026/08/29 07:45:11 in-guest build: manifest-channel: active`,
		`2026/08/29 07:45:07 in-guest build: rootfs-size-manifest: 24 file(s) >= 1.0 MiB in /var/lib/buildkit/nexus3-export/nexus3-inguestbuild-rootfs-3209933237`,
		`2026/08/29 07:45:07 in-guest build: rootfs-size-manifest:   testfiles/file_200m                                          209715200`,
		`2026/08/29 07:45:07 in-guest build: rootfs-size-manifest:   testfiles/file_32m                                           33554433`,
	}, "\n") + "\n"

	logPath := writeLogFile(t, logContent)
	results := ParseManifestStageA(logPath, 0, 0)

	if len(results) == 0 {
		t.Fatal("expected at least one probe result, got none")
	}

	probeMap := make(map[string]ProbeResult)
	for _, r := range results {
		probeMap[r.Probe] = r
	}

	for _, tc := range []struct{ probe string }{
		{"stageA.file_200m"},
		{"stageA.file_32m"},
	} {
		r, ok := probeMap[tc.probe]
		if !ok {
			t.Errorf("missing probe %q in bare-form parse; all probes: %v", tc.probe, probeMap)
			continue
		}
		if r.Verdict != NoTruncationObserved {
			t.Errorf("%s verdict=%v want NoTruncationObserved; detail=%q", tc.probe, r.Verdict, r.Detail)
		} else {
			t.Logf("OK: %s → NoTruncationObserved detail=%q", tc.probe, r.Detail)
		}
	}
}

// TestParseManifestStageA_DriftTest reads the newest *.log file from the
// runtime logs directory and verifies that ParseManifestStageA produces ≥1
// non-skip probe when the manifest-channel sentinel is present.
//
// If no log files exist or the newest log has no sentinel, the test skips with
// a clear message. Given that conc-wave1-slot0-20260829-091808.log exists and
// has the sentinel, this test should pass rather than skip.
func TestParseManifestStageA_DriftTest(t *testing.T) {
	repoRoot := findRepoRoot(t)
	reproDir := filepath.Join(repoRoot, "internal", "test", "repro")
	pattern := filepath.Join(reproDir, "logs", "*.log")

	entries, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %q: %v", pattern, err)
	}

	// Filter to regular files only.
	var logFiles []string
	for _, e := range entries {
		fi, statErr := os.Stat(e)
		if statErr == nil && fi.Mode().IsRegular() {
			logFiles = append(logFiles, e)
		}
	}

	if len(logFiles) == 0 {
		t.Skip("no log files present; skip drift test")
	}

	// Sort by ModTime descending; pick newest.
	sort.Slice(logFiles, func(i, j int) bool {
		si, _ := os.Stat(logFiles[i])
		sj, _ := os.Stat(logFiles[j])
		return si.ModTime().After(sj.ModTime())
	})
	newest := logFiles[0]
	t.Logf("drift test using newest log: %s", newest)

	// Scan for manifest-channel sentinel.
	data, readErr := os.ReadFile(newest)
	if readErr != nil {
		t.Fatalf("read %s: %v", newest, readErr)
	}
	if !strings.Contains(string(data), "manifest-channel: active") {
		t.Skipf("newest log %s has no manifest-channel sentinel; cannot test drift", newest)
	}

	results := ParseManifestStageA(newest, 0, 0)

	realProbes := 0
	for _, r := range results {
		if !r.SkipVerdict {
			realProbes++
		}
	}
	if realProbes == 0 {
		t.Errorf("newest log %s has manifest-channel sentinel but ParseManifestStageA returned 0 real probes "+
			"(all SkipVerdict); Stage A parsing may have regressed (slog or bare-form format drift?)", newest)
	} else {
		t.Logf("OK: drift test found %d real probes from %s", realProbes, newest)
	}
}

// TestFile32mFixtureNotSentinel verifies the file_32m expected size is not
// equal to TruncationSentinel so size alone discriminates correct vs truncated.
func TestFile32mFixtureNotSentinel(t *testing.T) {
	for _, tf := range testFiles {
		if tf.name == "file_32m" {
			if tf.expected == TruncationSentinel {
				t.Errorf("file_32m expected size %d == TruncationSentinel %d; "+
					"size cannot discriminate correct from truncated file. "+
					"Change expected to a non-sentinel size (e.g. 33554433 = 32 MiB + 1 byte).",
					tf.expected, TruncationSentinel)
			} else {
				t.Logf("OK: file_32m expected=%d, TruncationSentinel=%d (distinct)", tf.expected, TruncationSentinel)
			}
			return
		}
	}
	t.Fatal("file_32m not found in testFiles fixture")
}

// TestStageBRunIDProbe_Match verifies that StageBRunIDProbe returns OK
// when /.repro-run-id contains the expected ID.
func TestStageBRunIDProbe_Match(t *testing.T) {
	img := makeRunIDExt4(t, "9876543210987654321")
	r := StageBRunIDProbe(img, "9876543210987654321")
	if r.Verdict != NoTruncationObserved {
		t.Errorf("expected OK, got %v: %s", r.Verdict, r.Detail)
	}
}

// TestStageBRunIDProbe_Mismatch is the mutation proof.
//
// MUTATION: in probes.go StageBRunIDProbe, change the body to:
//
//	return probeOK("stageB.run_id", "faked")
//
// → test fails:
//
//	probes_test.go:NN: MUTATION PROOF FAIL: expected HIF for mismatch, got NoTruncationObserved: faked
//
// RESTORE: put original body back → PASS.
func TestStageBRunIDProbe_Mismatch(t *testing.T) {
	img := makeRunIDExt4(t, "9876543210987654321")
	r := StageBRunIDProbe(img, "wrong-id")
	if r.Verdict != HarnessIntegrityFailure {
		t.Errorf("MUTATION PROOF FAIL: expected HIF for mismatch, got %v: %s", r.Verdict, r.Detail)
	}
}

// makeRunIDExt4 creates a minimal ext4 image with /.repro-run-id = runID\n.
// Uses mke2fs -d to populate from a temp directory.
func makeRunIDExt4(t *testing.T, runID string) string {
	t.Helper()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, ".repro-run-id"), []byte(runID+"\n"), 0644); err != nil {
		t.Fatalf("write .repro-run-id: %v", err)
	}
	imgPath := filepath.Join(t.TempDir(), "fake.ext4")
	cmd := exec.Command("mke2fs", "-t", "ext4", "-d", srcDir, imgPath, "1M")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v: %s", err, out)
	}
	return imgPath
}
