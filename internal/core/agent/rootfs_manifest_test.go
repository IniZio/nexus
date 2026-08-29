//go:build linux

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeManifestFixture creates a rootfs directory containing files of specified
// sizes. files is a map of relative path → size in bytes. Intermediate
// directories are created automatically.
func makeManifestFixture(t *testing.T, files map[string]int64) string {
	t.Helper()
	dir := t.TempDir()
	for rel, size := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(full)
		if err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if err := f.Truncate(size); err != nil {
				f.Close()
				t.Fatal(err)
			}
		}
		f.Close()
	}
	return dir
}

// TestLogRootfsSizeManifest_LargeFileAppears is the BITE test.
//
// It asserts that a file >= rootfsManifestMinBytes (1 MiB) appears in the
// returned manifest. Removing or neutering the append inside
// logRootfsSizeManifest makes this test fail because entries will be empty.
func TestLogRootfsSizeManifest_LargeFileAppears(t *testing.T) {
	// 34 MiB — above the 32 MiB truncation boundary and above the 1 MiB threshold.
	const largeSize = 34 * 1 << 20
	dir := makeManifestFixture(t, map[string]int64{
		"usr/bin/bigfile": largeSize,
	})

	entries := logRootfsSizeManifest(dir)

	if len(entries) == 0 {
		t.Fatal("manifest returned no entries; expected bigfile to appear")
	}
	found := false
	for _, e := range entries {
		if e.RelPath == "usr/bin/bigfile" {
			found = true
			if e.Size != largeSize {
				t.Errorf("bigfile size = %d, want %d", e.Size, largeSize)
			}
		}
	}
	if !found {
		t.Errorf("usr/bin/bigfile not found in manifest; entries = %v", entries)
	}
}

// TestLogRootfsSizeManifest_SmallFileExcluded is the threshold guard test.
//
// A file below rootfsManifestMinBytes (1 MiB) must NOT appear in the manifest.
// If the threshold filter is removed, this test fails because the small file
// would appear in entries. Together with the bite test above, this pair proves
// the threshold discriminates — a manifest that always returns everything, or
// always returns nothing, cannot satisfy both.
func TestLogRootfsSizeManifest_SmallFileExcluded(t *testing.T) {
	// 512 KiB — below the 1 MiB threshold.
	const smallSize = 512 * 1024
	dir := makeManifestFixture(t, map[string]int64{
		"usr/lib/smallfile": smallSize,
	})

	entries := logRootfsSizeManifest(dir)

	for _, e := range entries {
		if e.RelPath == "usr/lib/smallfile" {
			t.Errorf("smallfile (%d bytes) should not appear in manifest (threshold %d); got entry %+v",
				smallSize, rootfsManifestMinBytes, e)
		}
	}
}

// TestLogRootfsSizeManifest_TruncatedFileSurfaced is the scenario-proof test.
//
// It synthesizes the exact truncation signature: a file that SHOULD be 34 MiB
// but was written as exactly 33554432 bytes (2^25 = 32 MiB). The manifest
// must record the on-disk size (33554432) so an operator can compare it against
// the expected source size and identify the truncation. This test fails if the
// manifest logic is removed (no entries returned) or if it records the wrong
// size.
func TestLogRootfsSizeManifest_TruncatedFileSurfaced(t *testing.T) {
	const truncatedAt = 33554432 // exactly 2^25 bytes — the observed truncation cap
	dir := makeManifestFixture(t, map[string]int64{
		"sbin/nexus3-agent": truncatedAt,
	})

	entries := logRootfsSizeManifest(dir)

	if len(entries) == 0 {
		t.Fatal("manifest returned no entries; expected truncated nexus3-agent to appear")
	}
	for _, e := range entries {
		if e.RelPath == "sbin/nexus3-agent" {
			if e.Size != truncatedAt {
				t.Errorf("manifest recorded size %d, want %d (the truncation cap)", e.Size, truncatedAt)
			}
			return
		}
	}
	t.Errorf("sbin/nexus3-agent not found in manifest; entries = %v", entries)
}

// TestLogRootfsSizeManifest_EmptyDirReturnsNone ensures the function is
// non-fatal and returns an empty slice on a directory with no large files.
// This is the non-corruption baseline: a rootfs of only small files produces
// no manifest entries.
func TestLogRootfsSizeManifest_EmptyDirReturnsNone(t *testing.T) {
	dir := makeManifestFixture(t, map[string]int64{
		"etc/hostname": 12,
	})

	entries := logRootfsSizeManifest(dir)

	if len(entries) != 0 {
		t.Errorf("expected no manifest entries for all-small files, got %d: %v", len(entries), entries)
	}
}

// TestLogRootfsSizeManifest_MultipleFilesAllSurfaced verifies that when
// multiple large files exist, all of them appear in the manifest — the walk
// does not short-circuit after the first hit.
func TestLogRootfsSizeManifest_MultipleFilesAllSurfaced(t *testing.T) {
	const sz = 2 * 1 << 20 // 2 MiB each
	dir := makeManifestFixture(t, map[string]int64{
		"usr/bin/a": sz,
		"usr/bin/b": sz,
		"usr/bin/c": sz,
	})

	entries := logRootfsSizeManifest(dir)

	if len(entries) != 3 {
		t.Errorf("expected 3 manifest entries, got %d: %v", len(entries), entries)
	}
}

// TestManifestBeforeIntegrityGate_OrderingProtection enforces call-site ordering
// in buildkit_linux.go by parsing the source file and asserting that
// logRootfsSizeManifest appears before BOTH verifyRootfsPopulated AND
// verifyAgentIntegrity.
//
// Rationale: both verify* functions are fail-closed — they return an error that
// aborts the build. A manifest placed after either gate would never execute on
// the truncation builds it exists to diagnose.
//
// This test provides MECHANICAL enforcement: deleting the logRootfsSizeManifest
// call, moving it after either gate, or placing it between the two gates in the
// wrong order all turn this test RED. It is not possible to silently reintroduce
// the ordering bug without also making this test fail.
//
// The _ import of "errors" is retained below to keep the unused-import linter
// satisfied; errors is used by TestManifestBeforeIntegrityGate_ScenarioProof.
func TestManifestBeforeIntegrityGate_OrderingProtection(t *testing.T) {
	// Go test CWD is the package directory, so the relative path resolves correctly.
	src, err := os.ReadFile("buildkit_linux.go")
	if err != nil {
		t.Fatalf("cannot read buildkit_linux.go: %v", err)
	}

	// findActivePos returns the byte offset of the first ACTIVE (non-commented)
	// occurrence of needle. A line is considered commented when its first non-space
	// token is "//". This prevents a commented-out call from satisfying the check.
	findActivePos := func(needle string) int {
		offset := 0
		for _, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "//") {
				if idx := strings.Index(line, needle); idx >= 0 {
					return offset + idx
				}
			}
			offset += len(line) + 1 // +1 for the '\n' consumed by Split
		}
		return -1
	}

	manifestPos := findActivePos("logRootfsSizeManifest(")
	populatedPos := findActivePos("verifyRootfsPopulated(")
	integrityPos := findActivePos("verifyAgentIntegrity(")

	if manifestPos < 0 {
		t.Fatal("logRootfsSizeManifest( not found in buildkit_linux.go on any active (non-commented) line — call was removed or commented out")
	}
	if populatedPos < 0 {
		t.Fatal("verifyRootfsPopulated( not found in buildkit_linux.go")
	}
	if integrityPos < 0 {
		t.Fatal("verifyAgentIntegrity( not found in buildkit_linux.go")
	}

	if manifestPos > populatedPos {
		t.Errorf("ordering violation: logRootfsSizeManifest (pos %d) appears AFTER "+
			"verifyRootfsPopulated (pos %d); the manifest must run before the fail-closed "+
			"gate or it never diagnoses truncated builds",
			manifestPos, populatedPos)
	}
	if manifestPos > integrityPos {
		t.Errorf("ordering violation: logRootfsSizeManifest (pos %d) appears AFTER "+
			"verifyAgentIntegrity (pos %d); the manifest must run before the fail-closed "+
			"gate or it never diagnoses truncated builds",
			manifestPos, integrityPos)
	}

	if !t.Failed() {
		t.Logf("ordering OK: logRootfsSizeManifest@%d < verifyRootfsPopulated@%d, verifyAgentIntegrity@%d",
			manifestPos, populatedPos, integrityPos)
	}
}

// TestManifestBeforeIntegrityGate_ScenarioProof verifies that in the truncation
// scenario the manifest surfaces the file AND the integrity gate rejects it —
// the two mechanisms that must work together for the ordering to matter.
func TestManifestBeforeIntegrityGate_ScenarioProof(t *testing.T) {
	const truncatedAt = 33554432 // 2^25 bytes — the observed truncation cap
	const sourceSize = 40 * 1 << 20

	rootfsDir := t.TempDir()
	sbin := filepath.Join(rootfsDir, "sbin")
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(sbin, "nexus3-agent"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(truncatedAt); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	srcPath := filepath.Join(t.TempDir(), "nexus3-agent-src")
	sf, err := os.Create(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sf.Truncate(sourceSize); err != nil {
		sf.Close()
		t.Fatal(err)
	}
	sf.Close()

	entries := logRootfsSizeManifest(rootfsDir)
	found := false
	for _, e := range entries {
		if e.RelPath == "sbin/nexus3-agent" {
			found = true
			if e.Size != truncatedAt {
				t.Errorf("manifest recorded size %d, want %d", e.Size, truncatedAt)
			}
		}
	}
	if !found {
		t.Errorf("manifest did not surface sbin/nexus3-agent; entries = %v", entries)
	}

	gateErr := verifyAgentIntegrity(rootfsDir, "/sbin/nexus3-agent", srcPath)
	if gateErr == nil {
		t.Fatal("expected verifyAgentIntegrity to fail for truncated agent, got nil")
	}
	var truncErr *ErrRootfsTruncated
	if !errors.As(gateErr, &truncErr) {
		t.Fatalf("expected *ErrRootfsTruncated, got %T: %v", gateErr, gateErr)
	}
	t.Logf("scenario proof: manifest entry size=%d, gate error=%v", truncErr.GotBytes, gateErr)
}
