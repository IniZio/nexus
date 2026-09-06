package repro

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	TruncationSentinel = int64(33554432) // 2^25 = exact truncation marker
	ExpRunProduced40m  = int64(41943040) // 40 MiB
)

var testFiles = []struct {
	name       string
	expected   int64
	hashVerify bool
}{
	{"file_8m", 8388608, false},
	{"file_31m", 32505856, false},
	{"file_32m", 33554433, false}, // 32 MiB + 1 byte — distinct from TruncationSentinel; size alone discriminates
	{"file_33m", 34603008, false},
	{"file_40m", 41943040, false},
	{"file_64m", 67108864, false},
	{"file_200m", 209715200, false},
	{"file_elf", 0, true}, // size set at runtime
}

// classifySize returns the probe verdict for a single measured file size.
// Priority order:
//
//	got == 0                    → probeHIF: zero-byte file signals corrupt export or ENOSPC
//	got == TruncationSentinel   → probeTrunc: the 32 MiB cap bug
//	expected <= 0               → probeOK: no fixed expected size; any non-zero non-sentinel OK
//	got == expected             → probeOK
//	else                        → probeHIF with "unexpected_size_not_sentinel: got N, expected M"
//
// This is the SINGLE point of truth for all Stage A and Stage B size-check sites.
// Every inline `if size == expected / elif size == TruncationSentinel / else` chain in
// ParseManifestStageA and StageBSizeProbes calls this function.
func classifySize(probe string, got, expected int64) ProbeResult {
	if got == 0 {
		return probeHIF(probe, fmt.Sprintf("unexpected_size_not_sentinel: got 0, expected %d", expected))
	}
	if got == TruncationSentinel {
		return probeTrunc(probe, "TRUNCATED_AT_32MiB")
	}
	if expected <= 0 || got == expected {
		return probeOK(probe, fmt.Sprintf("size=%d", got))
	}
	return probeHIF(probe, fmt.Sprintf("unexpected_size_not_sentinel: got %d, expected %d", got, expected))
}

// ErrDebugfsDead is returned when debugfs fails or produces invalid output.
type ErrDebugfsDead struct {
	Op  string // operation: "stat" or "dump"
	Err error
}

func (e ErrDebugfsDead) Error() string {
	return fmt.Sprintf("debugfs %s failed: %v", e.Op, e.Err)
}

// DebugfsSize runs `debugfs -R "stat <path>" <img>` and parses the Size field.
// Returns (size, nil) on success.
// Returns (0, ErrDebugfsDead{...}) if debugfs fails or output has no Size line.
// NEVER returns (0, nil) — zero-with-nil-error cannot be read as success.
func DebugfsSize(img, path string) (int64, error) {
	cmd := exec.Command("debugfs", "-R", fmt.Sprintf("stat %s", path), img)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, ErrDebugfsDead{Op: "stat", Err: err}
	}

	// Parse "Size: <n>" from debugfs stat output.
	//
	// debugfs stat emits the Size field on a compound line:
	//   "User:     0   Group:     0   Project:     0   Size: 8388608"
	// The field is NOT at the start of the line, so HasPrefix("Size:") misses
	// it. Search for the "Size:" token anywhere in the line, then parse the
	// first whitespace-delimited word that follows it.
	//
	// Buffer raised to 256 KiB to avoid silently aborting on unexpectedly long
	// lines (e.g. an extended attribute or xattr dump).
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_, after, found := strings.Cut(line, "Size:")
		if !found {
			continue
		}
		fields := strings.Fields(after)
		if len(fields) >= 1 {
			size, parseErr := strconv.ParseInt(fields[0], 10, 64)
			if parseErr == nil {
				return size, nil
			}
		}
	}
	// strings.NewReader never produces I/O errors, but check anyway for correctness.
	if err := scanner.Err(); err != nil {
		return 0, ErrDebugfsDead{Op: "stat", Err: fmt.Errorf("scanner: %w", err)}
	}
	return 0, ErrDebugfsDead{Op: "stat", Err: fmt.Errorf("no Size field found")}
}

// DebugfsDump runs `debugfs -R "dump <guestPath> <tmpPath>" <img>`.
// Returns nil on success (tmpPath exists and is non-empty).
// Returns ErrDebugfsDead if debugfs exits non-zero OR tmpPath is missing/empty.
// Note: debugfs exits 0 even for missing paths and creates no file — always check file existence.
func DebugfsDump(img, guestPath, tmpPath string) error {
	cmd := exec.Command("debugfs", "-R", fmt.Sprintf("dump %s %s", guestPath, tmpPath), img)
	if err := cmd.Run(); err != nil {
		return ErrDebugfsDead{Op: "dump", Err: err}
	}

	// Check if tmpPath exists and is non-empty
	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() == 0 {
		return ErrDebugfsDead{Op: "dump", Err: fmt.Errorf("file missing or empty")}
	}
	return nil
}

// ParseManifestStageA parses the build log at logPath and returns one ProbeResult
// per test file, plus run-produced-40m, docker-compose, and nexus3-agent.
// agentBinSize is the host binary size (0 = missing reference = HIF).
// elfSize is the expected size for file_elf (0 = use actual file_elf entry if available, still HIF if absent).
func ParseManifestStageA(logPath string, agentBinSize int64, elfSize int64) []ProbeResult {
	f, err := os.Open(logPath)
	if err != nil {
		return []ProbeResult{
			probeHIF("stageA.manifest", "NO_LOG"),
		}
	}
	defer f.Close()

	manifest := make(map[string]int64)
	sawSentinel := false // true when "manifest-channel: active" line is seen
	// Raise the scanner buffer to 4 MiB so long build-log lines (e.g. from apt or
	// runc) do not silently truncate the scan at the default 64 KiB limit. Without
	// this, scanner.Scan() returns false with ErrTooLong and the manifest is left
	// partially populated — a dead instrument reporting valid results on partial data.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		// Detect the manifest-channel sentinel emitted by guestBuild in
		// internal/core/builder/vmbuilder.go on the success path. When this
		// sentinel is present but no data entries are found, the manifest
		// emission (logRootfsSizeManifest) was suppressed — HarnessIntegrityFailure,
		// not not_collected.
		if strings.Contains(line, "manifest-channel: active") {
			sawSentinel = true
			continue
		}

		// Match "rootfs-size-manifest:   <relpath>  <size>" (3 spaces after colon = data).
		// strings.Cut avoids the double-search of Contains+Index.
		_, afterColon, found := strings.Cut(line, "rootfs-size-manifest:")
		if !found {
			continue
		}
		// Skip header line ("rootfs-size-manifest: N file(s) ...": 1 space then digit).
		// Data lines start with exactly 3 spaces before the relative path.
		if len(afterColon) < 3 || afterColon[0] != ' ' || afterColon[1] != ' ' || afterColon[2] != ' ' {
			continue
		}

		// Extract relpath and size from the remainder.
		// afterColon[3:] skips the 3 leading spaces; TrimSpace removes any trailing spaces.
		// TrimRight("\"") strips a trailing double-quote added when the in-guest slog
		// default handler wraps the message in msg="...": the entire original log.Printf
		// output becomes the msg value, so the closing " appears after the final digit.
		// Bare-form lines (no slog wrapper) are unaffected — they have no trailing ".
		rest := strings.TrimRight(strings.TrimSpace(afterColon[3:]), "\"")
		fields := strings.Fields(rest)
		if len(fields) >= 2 {
			path := fields[len(fields)-2]
			sizeStr := fields[len(fields)-1]
			size, parseErr := strconv.ParseInt(sizeStr, 10, 64)
			if parseErr == nil {
				manifest[path] = size
			}
		}
	}
	// scanner.Err() is non-nil for ErrTooLong (line exceeded buffer) and I/O errors.
	// Either leaves manifest partially populated — that is a dead instrument, not a
	// valid "no manifest lines found" result.
	if err := scanner.Err(); err != nil {
		return []ProbeResult{
			probeHIF("stageA.manifest", fmt.Sprintf("scanner error: %v", err)),
		}
	}

	if len(manifest) == 0 {
		if sawSentinel {
			// The manifest-channel sentinel ("manifest-channel: active") was present
			// in the log, meaning guestBuild forwarded the in-guest output; but no
			// rootfs-size-manifest data lines were found. This means logRootfsSizeManifest
			// was suppressed or did not emit any entries — a harness integrity failure,
			// not a valid "no truncation observed" result.
			return []ProbeResult{
				probeHIF("stageA.manifest",
					"manifest-channel active but no manifest data entries found; logRootfsSizeManifest may be suppressed"),
			}
		}
		// No sentinel: either the build used an old binary (pre-manifest-channel),
		// or the build failed before guestBuild reached the success path. The
		// manifest was not collected — this is a structural gap, not evidence of
		// correctness.
		return []ProbeResult{
			probeNotCollected("stageA.manifest",
				"manifest-channel sentinel absent; build used pre-channel binary or failed before success path"),
		}
	}

	var results []ProbeResult

	// Check each test file.
	// Manifest keys are rootfs-relative paths (e.g. "testfiles/file_8m").
	// file_elf has tf.expected==0, so classifySize treats any non-zero non-sentinel size as OK.
	for _, tf := range testFiles {
		key := "testfiles/" + tf.name
		if size, ok := manifest[key]; ok {
			results = append(results, classifySize("stageA."+tf.name, size, tf.expected))
		} else {
			results = append(results, probeHIF("stageA."+tf.name, "manifest_absent"))
		}
	}

	// Check run-produced-40m (rootfs path: usr/local/bin/run-produced-40m).
	if size, ok := manifest["usr/local/bin/run-produced-40m"]; ok {
		results = append(results, classifySize("stageA.run-produced-40m", size, ExpRunProduced40m))
	} else {
		results = append(results, probeHIF("stageA.run-produced-40m", "manifest_absent"))
	}

	// Check docker-compose (rootfs path: usr/libexec/docker/cli-plugins/docker-compose).
	if size, ok := manifest["usr/libexec/docker/cli-plugins/docker-compose"]; ok {
		results = append(results, classifySize("stageA.docker-compose", size, 0))
	} else {
		results = append(results, probeHIF("stageA.docker-compose", "manifest_absent"))
	}

	// Check nexus3-agent
	if agentBinSize == 0 {
		results = append(results, probeHIF("stageA.nexus3-agent", "no_host_ref"))
	} else if size, ok := manifest["usr/sbin/nexus3-agent"]; ok {
		results = append(results, classifySize("stageA.nexus3-agent", size, agentBinSize))
	} else {
		results = append(results, probeHIF("stageA.nexus3-agent", "manifest_absent"))
	}

	return results
}

// StageBSizeProbes probes all test files + run-produced-40m + docker-compose via debugfs stat.
// agentBinSize: host binary size for /sbin/nexus3-agent (0 → HIF).
// elfSize: expected size for file_elf (0 → only truncation-sentinel detection possible).
func StageBSizeProbes(img string, agentBinSize int64, elfSize int64) []ProbeResult {
	var results []ProbeResult

	// Probe test files.
	// The Containerfile uses "COPY testfiles/ /testfiles/" so all test files
	// live at /testfiles/<name> in the rootfs (not at /<name>).
	// file_elf has tf.expected==0; if elfSize>0, override to elfSize so the exact
	// size is verified. classifySize treats expected<=0 as "any non-zero non-sentinel OK".
	for _, tf := range testFiles {
		path := "/testfiles/" + tf.name
		size, err := DebugfsSize(img, path)
		if err != nil {
			results = append(results, probeHIF("stageB."+tf.name, fmt.Sprintf("debugfs error: %v", err)))
			continue
		}
		expected := tf.expected
		if tf.name == "file_elf" && elfSize > 0 {
			expected = elfSize
		}
		results = append(results, classifySize("stageB."+tf.name, size, expected))
	}

	// Probe run-produced-40m
	size, err := DebugfsSize(img, "/usr/local/bin/run-produced-40m")
	if err != nil {
		results = append(results, probeHIF("stageB.run-produced-40m", fmt.Sprintf("debugfs error: %v", err)))
	} else {
		results = append(results, classifySize("stageB.run-produced-40m", size, ExpRunProduced40m))
	}

	// Probe docker-compose
	size, err = DebugfsSize(img, "/usr/libexec/docker/cli-plugins/docker-compose")
	if err != nil {
		results = append(results, probeHIF("stageB.docker-compose", fmt.Sprintf("debugfs error: %v", err)))
	} else {
		results = append(results, classifySize("stageB.docker-compose", size, 0))
	}

	// Probe nexus3-agent
	if agentBinSize == 0 {
		results = append(results, probeHIF("stageB.nexus3-agent", "no_host_ref"))
	} else {
		size, err := DebugfsSize(img, "/sbin/nexus3-agent")
		if err != nil {
			results = append(results, probeHIF("stageB.nexus3-agent", fmt.Sprintf("debugfs error: %v", err)))
		} else {
			results = append(results, classifySize("stageB.nexus3-agent", size, agentBinSize))
		}
	}

	return results
}

// StageBHashProbes dumps file_32m and file_elf from the ext4 image to a temp dir,
// verifies sha256 against expectedHashes, returns one ProbeResult each.
// expectedHashes: map["file_32m"] = "<64-hex-sha256>", map["file_elf"] = "<64-hex-sha256>".
// nil map or missing entry in expectedHashes → probeNotCollected (SkipVerdict=true).
// DebugfsDump failure → HIF.
// sha256sum output not exactly 64 hex chars → HIF("sha256_failed").
// Hash mismatch → TruncationReproduced("HASH_FAIL").
// Hash match → NoTruncationObserved.
func StageBHashProbes(img string, expectedHashes map[string]string) []ProbeResult {
	var results []ProbeResult

	tmpDir, err := os.MkdirTemp("/tmp", "nexus3-repro-")
	if err != nil {
		return []ProbeResult{
			probeHIF("stageB.hash", fmt.Sprintf("tmpdir failed: %v", err)),
		}
	}
	defer os.RemoveAll(tmpDir)

	// Probe file_32m
	if expectedHash, ok := expectedHashes["file_32m"]; ok {
		dumpPath := filepath.Join(tmpDir, "file_32m")
		err := DebugfsDump(img, "/file_32m", dumpPath)
		if err != nil {
			results = append(results, probeHIF("stageB.file_32m.hash", fmt.Sprintf("dump failed: %v", err)))
		} else {
			hash, err := sha256File(dumpPath)
			if err != nil {
				results = append(results, probeHIF("stageB.file_32m.hash", fmt.Sprintf("sha256_failed: %v", err)))
			} else if !isValidHex(hash, 64) {
				results = append(results, probeHIF("stageB.file_32m.hash", "sha256_failed: invalid hex"))
			} else if hash == expectedHash {
				results = append(results, probeOK("stageB.file_32m.hash", hash[:16]+"..."))
			} else {
				results = append(results, probeTrunc("stageB.file_32m.hash", fmt.Sprintf("HASH_FAIL: got %s", hash[:16])))
			}
		}
	} else {
		results = append(results, probeNotCollected("stageB.file_32m.hash", "no_expected_hash: hash not configured"))
	}

	// Probe file_elf
	if expectedHash, ok := expectedHashes["file_elf"]; ok {
		dumpPath := filepath.Join(tmpDir, "file_elf")
		err := DebugfsDump(img, "/file_elf", dumpPath)
		if err != nil {
			results = append(results, probeHIF("stageB.file_elf.hash", fmt.Sprintf("dump failed: %v", err)))
		} else {
			hash, err := sha256File(dumpPath)
			if err != nil {
				results = append(results, probeHIF("stageB.file_elf.hash", fmt.Sprintf("sha256_failed: %v", err)))
			} else if !isValidHex(hash, 64) {
				results = append(results, probeHIF("stageB.file_elf.hash", "sha256_failed: invalid hex"))
			} else if hash == expectedHash {
				results = append(results, probeOK("stageB.file_elf.hash", hash[:16]+"..."))
			} else {
				results = append(results, probeTrunc("stageB.file_elf.hash", fmt.Sprintf("HASH_FAIL: got %s", hash[:16])))
			}
		}
	} else {
		results = append(results, probeNotCollected("stageB.file_elf.hash", "no_expected_hash: hash not configured"))
	}

	return results
}

// StageBRunIDProbe reads /.repro-run-id from the ext4 image via debugfs dump
// and verifies it matches expectedID. Returns HIF if absent, unreadable, or
// mismatched; OK if matched. A mismatched ID means the image being probed does
// not correspond to the build that produced it — a harness integrity failure.
//
// MUTATION (executed, W42): change this function to always return probeOK("stageB.run_id", "faked")
// → TestStageBRunIDProbe_Mismatch fails:
//
//	probes_test.go:401: MUTATION PROOF FAIL: expected HIF for mismatch, got NoTruncationObserved: faked
//
// RESTORED → PASS.
func StageBRunIDProbe(img, expectedID string) ProbeResult {
	if expectedID == "" {
		return probeHIF("stageB.run_id", "no_expected_id: harness did not inject run-id")
	}
	tmpPath := filepath.Join(os.TempDir(), "nexus3-runid-"+expectedID)
	defer os.Remove(tmpPath)

	if err := DebugfsDump(img, "/.repro-run-id", tmpPath); err != nil {
		return probeHIF("stageB.run_id", fmt.Sprintf("dump_failed: %v", err))
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return probeHIF("stageB.run_id", fmt.Sprintf("read_failed: %v", err))
	}

	got := strings.TrimSpace(string(data))
	if got == expectedID {
		return probeOK("stageB.run_id", "id="+got)
	}
	return probeHIF("stageB.run_id", fmt.Sprintf("ID_MISMATCH: got=%q expected=%q", got, expectedID))
}

// sha256File computes the SHA256 hash of a file and returns it as a hex string.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// isValidHex checks if a string is valid hex with the expected length.
func isValidHex(s string, expectedLen int) bool {
	if len(s) != expectedLen {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
