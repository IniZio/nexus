//go:build linux

package main

// TestNoTmpfsAssumptionOnSlashTmp is a standing sweep that fails when any
// guest-facing source file asserts that /tmp is a tmpfs mount.
//
// Background: Wave-2 of the guest-scratch-disk motive will mount /tmp as an
// ext4 block device for workspace sandboxes.  Code that independently assumes
// tmpfs semantics at /tmp (type-check assertions, df/findmnt filtering,
// grow-only sizing on a tmpfs cap) breaks silently under ext4.
//
// The test scans two surfaces:
//
//  1. All .go files in cmd/nexus3-agent/ for non-comment lines that either:
//     (a) contain the identifier "tmpfsMagic" — the Linux statfs type
//         constant (0x01021994), flagged immediately; OR
//     (b) contain the string literal "tmpfs" within a PROXIMITY WINDOW of
//         a line containing "/tmp" (window N=3 source lines, bidirectional).
//
//     Proximity rule: a line with "tmpfs" and a line with "/tmp" are flagged
//     if their source-line numbers differ by ≤ 3.  Source line numbers are
//     used (not active-line indices) so blank and comment lines between the
//     two do not deflate the gap artificially.  N=3 is chosen to cover the
//     common Go error-handling pattern — type check on one line, error
//     message with the path on the next (distance=1) — while staying narrow
//     enough to avoid pairing unrelated mentions separated by real code.
//
//     A same-line match (distance=0) is the degenerate case and is caught by
//     the same rule.
//
//  2. Image-baked scripts and Containerfiles for shell lines that test
//     whether /tmp is a tmpfs via findmnt(8) or df(1).
//
// Explicitly out of scope (benign; do not re-investigate):
//
//   - resize_tmp_linux.go: checks st.Type == tmpfsMagic to decide whether to
//     resize; no-ops on non-tmpfs mounts — that is the correct ext4-safe
//     guard, not an assertion that /tmp must be a tmpfs.
//
//   - resize_test.go: unit-test helpers that construct fake Statfs_t values
//     for the resizer tests; no production assertion.
//
//   - internal/core/service/buildkit_linux.go:56: xattr constraint on
//     /var/lib/buildkit (virtiofs transport), unrelated to /tmp.
//
// Wave-2-owned items (see exemptGoLines below) that will be updated alongside
// the production change — not flagged by this sweep:
//
//   - cmd/nexus3-agent/main.go:436 tryMount("tmpfs", "/tmp", …): the call
//     that currently creates the tmpfs mount; Wave-2 changes this.
//
//   - cmd/nexus3-agent/mount_linux_test.go:36 c.fstype == "tmpfs" for /tmp:
//     the behavioral test that verifies the above; must change with it.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// exemptGoLines marks specific lines in otherwise-non-exempt files as
// Wave-2-owned. The scanner still inspects the rest of each file, so a NEW
// assertion added anywhere else in the same file is caught.
//
// Values are distinctive substrings unique to the target line; they must be
// updated if the surrounding code is refactored. These two lines are the
// ONLY known /tmp-as-tmpfs uses that are not already covered by an
// exemptGoFiles entry; blanket-exempting their files would hide future
// violations added elsewhere in those files.
var exemptGoLines = map[string][]string{
	// main.go:436 — the call that creates the tmpfs mount; Wave-2 replaces
	// this with a mount of an ext4 block device.
	"main.go": {`tryMount("tmpfs", "/tmp"`},
	// mount_linux_test.go:36 — behavioral test for the above call; must
	// change in Wave-2 together with main.go.
	"mount_linux_test.go": {`c.target == "/tmp" && c.fstype == "tmpfs"`},
}

func TestNoTmpfsAssumptionOnSlashTmp(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate source tree")
	}
	// thisFile = …/cmd/nexus3-agent/tmpfs_assumption_sweep_test.go
	agentDir := filepath.Dir(thisFile)
	repoRoot := filepath.Join(agentDir, "..", "..")

	var violations []string

	// ── 1. Guest-agent Go source ──────────────────────────────────────────────
	//
	// Exempted files are the resizer and its companion test; every other .go
	// file in the package is scanned.
	exemptGoFiles := map[string]bool{
		"resize_tmp_linux.go":            true,
		"resize_test.go":                 true,
		"tmpfs_assumption_sweep_test.go": true, // self: scanner source contains the literals
	}
	goFiles, err := filepath.Glob(filepath.Join(agentDir, "*.go"))
	if err != nil {
		t.Fatalf("glob Go files: %v", err)
	}
	// Defect 2 guard: a renamed or moved package directory silently produces
	// zero files, which would make the sweep pass while scanning nothing.
	if len(goFiles) == 0 {
		t.Fatalf("glob returned no .go files in %s — scan target missing or build tag mismatch; the sweep is scanning nothing", agentDir)
	}
	for _, gf := range goFiles {
		if exemptGoFiles[filepath.Base(gf)] {
			continue
		}
		violations = append(violations, scanGoForTmpfsMagic(t, gf)...)
	}

	// ── 2. Image-baked scripts and Containerfiles ─────────────────────────────
	//
	// spike/ and docs/ are not baked into the image and are excluded.
	imageFiles := []string{
		filepath.Join(repoRoot, "images", "base", "Containerfile"),
		filepath.Join(repoRoot, ".nexus", "Containerfile"),
		filepath.Join(repoRoot, "scripts", "fetch-boot-artifacts.sh"),
	}
	for _, sf := range imageFiles {
		violations = append(violations, scanScriptForTmpfsTypeAssert(t, sf)...)
	}

	if len(violations) > 0 {
		t.Errorf(
			"/tmp-is-tmpfs assumption(s) detected — these break when /tmp is ext4 (%d hit(s)):\n%s\n\n"+
				"If the hit is a new legitimate use, add it to the exemptGoFiles map or exemptGoLines "+
				"with a comment explaining why it is ext4-safe.",
			len(violations), strings.Join(violations, "\n"),
		)
	}
}

// TestMutationProof verifies that both scanner functions catch realistic
// tmpfs-at-/tmp assertions, including the split-line form that the previous
// same-line scanner missed.
//
// Subtest (a) covers two Go forms:
//   - same-line: "tmpfs" and /tmp on one line (distance=0)
//   - split-line: type check on one line, error message with path on the next
//     (distance=1) — the exact shape the coordinator's real-package probe used
//
// Subtest (b) covers the shell findmnt assertion form.
//
// These temp-dir subtests are UNIT coverage of the scanner function.  The
// real-package RED→GREEN proof (writing a probe into cmd/nexus3-agent/ and
// running TestNoTmpfsAssumptionOnSlashTmp against the live package) is shown
// in the slice report and cannot be automated inside the test itself without
// mutating the package under test.
func TestMutationProof(t *testing.T) {
	t.Run("go_string_form_caught", func(t *testing.T) {
		// Two realistic Go shapes that must go RED.
		//
		// Shape 1: same-line (distance=0) — "tmpfs" and /tmp on one line.
		// Shape 2: split-line (distance=1) — the common Go error-handling pattern
		//   that the former same-line scanner missed: type check on one line,
		//   error message referencing the path on the next.
		bad := `package main

import "fmt"

// same-line form
func checkSameLine(fstype, path string) bool {
	return fstype == "tmpfs" && path == "/tmp"
}

// split-line form — realistic Go error handling (the probe that escaped the
// old same-line scanner)
func checkSplitLine(fstype string) error {
	if fstype != "tmpfs" {
		return fmt.Errorf("/tmp must be a tmpfs, got %s", fstype)
	}
	return nil
}
`
		dir := t.TempDir()
		badFile := filepath.Join(dir, "mutation_go_test.go")
		if err := os.WriteFile(badFile, []byte(bad), 0o644); err != nil {
			t.Fatalf("write mutation file: %v", err)
		}
		hits := scanGoForTmpfsMagic(t, badFile)
		if len(hits) == 0 {
			t.Fatal("RED check failed: scanner did not detect either same-line or split-line \"tmpfs\"+\"/tmp\" assertion")
		}

		// Clean variant must produce no violations.
		clean := `package main

func checkFstype(fstype, path string) bool {
	return fstype == "ext4" && path == "/data"
}
`
		cleanFile := filepath.Join(dir, "clean_go_test.go")
		if err := os.WriteFile(cleanFile, []byte(clean), 0o644); err != nil {
			t.Fatalf("write clean file: %v", err)
		}
		if hits := scanGoForTmpfsMagic(t, cleanFile); len(hits) > 0 {
			t.Fatalf("GREEN check failed: false positive on clean Go file: %v", hits)
		}
	})

	t.Run("shell_findmnt_caught", func(t *testing.T) {
		// Mutation (b): realistic shell assertion using findmnt to verify /tmp
		// is a tmpfs — the form a script author would write when guarding
		// tmpfs-specific behaviour.
		bad := `#!/bin/sh
set -e
# Ensure /tmp is backed by tmpfs before proceeding.
findmnt -t tmpfs /tmp || exit 1
echo "tmpfs confirmed"
`
		dir := t.TempDir()
		badFile := filepath.Join(dir, "mutation_check.sh")
		if err := os.WriteFile(badFile, []byte(bad), 0o644); err != nil {
			t.Fatalf("write mutation shell file: %v", err)
		}
		hits := scanScriptForTmpfsTypeAssert(t, badFile)
		if len(hits) == 0 {
			t.Fatal("RED check failed: scanner did not detect findmnt -t tmpfs /tmp shell assertion")
		}

		// Clean variant must produce no violations.
		clean := `#!/bin/sh
set -e
echo "nothing to check"
`
		cleanFile := filepath.Join(dir, "clean_check.sh")
		if err := os.WriteFile(cleanFile, []byte(clean), 0o644); err != nil {
			t.Fatalf("write clean shell file: %v", err)
		}
		if hits := scanScriptForTmpfsTypeAssert(t, cleanFile); len(hits) > 0 {
			t.Fatalf("GREEN check failed: false positive on clean shell file: %v", hits)
		}
	})
}

// scanGoForTmpfsMagic returns file:line entries for non-comment lines in a
// .go source file that assert /tmp is a tmpfs.  Two forms are flagged:
//
//  1. The identifier "tmpfsMagic" — the Linux statfs constant (0x01021994).
//     Its presence outside the two exempted resizer files signals a runtime
//     assertion that a path has that specific filesystem type.  Flagged
//     immediately; no proximity check needed.
//
//  2. The string literal "tmpfs" in PROXIMITY to "/tmp" — within N=3 source
//     lines of each other (bidirectional).  Source line numbers are used, not
//     active-line indices, so blank lines and comments between the two do not
//     reduce the measured distance.
//
//     N=3 covers the dominant real-world pattern: a type check on one line
//     and an error message or second clause referencing the path on the next
//     (distance=1), with slack for a short comment or blank line between them.
//     A same-line match (distance=0) is the degenerate case and is caught by
//     the same rule.
//
//     The former same-line-only rule was proven insufficient by a real-package
//     probe that split the check and the path across two consecutive lines:
//
//         if fstype != "tmpfs" {                        // "tmpfs", no /tmp
//             return fmt.Errorf("/tmp must be …", …)   // /tmp, no "tmpfs"
//         }
//
//     That probe escaped the old scanner; the proximity window catches it.
//
// Lines listed in exemptGoLines for this file's basename are skipped even
// if they would otherwise match.  This allows Wave-2-owned callers to be
// tracked explicitly without blanket-exempting their entire files.
func scanGoForTmpfsMagic(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		// A missing scan target is a configuration error, not a benign skip.
		t.Fatalf("open %s: %v", path, err)
		return nil
	}

	allLines := strings.Split(string(raw), "\n")
	exemptLines := exemptGoLines[filepath.Base(path)]

	type lineEntry struct {
		n    int
		text string // trimmed
	}

	var immediatHits []string  // tmpfsMagic — flagged without proximity check
	var tmpfsLitLines []lineEntry // lines with "tmpfs" string literal
	var slashTmpLines []lineEntry // lines with /tmp

	for i, line := range allLines {
		n := i + 1
		trimmed := strings.TrimSpace(line)
		// Skip blank lines and pure line-comment lines.
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Check Wave-2-owned exemptions; the scanner still checks the rest of
		// the file so new assertions elsewhere are caught.
		if isExemptLine(trimmed, exemptLines) {
			continue
		}
		// Form 1: tmpfsMagic constant — immediate flag, no proximity needed.
		if strings.Contains(line, "tmpfsMagic") {
			immediatHits = append(immediatHits, fmt.Sprintf("%s:%d: %s", path, n, trimmed))
			continue
		}
		// Collect lines for the proximity check.
		if strings.Contains(line, `"tmpfs"`) {
			tmpfsLitLines = append(tmpfsLitLines, lineEntry{n, trimmed})
		}
		if strings.Contains(line, "/tmp") {
			slashTmpLines = append(slashTmpLines, lineEntry{n, trimmed})
		}
	}

	hits := append([]string(nil), immediatHits...)

	// Proximity check: flag any pair where "tmpfs" literal and /tmp are within
	// proximityN source lines of each other.  Both lines in the pair are
	// reported so the engineer sees the full context.
	const proximityN = 3
	reported := make(map[int]bool)
	for _, tl := range tmpfsLitLines {
		for _, sl := range slashTmpLines {
			dist := tl.n - sl.n
			if dist < 0 {
				dist = -dist
			}
			if dist <= proximityN {
				if !reported[tl.n] {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", path, tl.n, tl.text))
					reported[tl.n] = true
				}
				if !reported[sl.n] {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", path, sl.n, sl.text))
					reported[sl.n] = true
				}
			}
		}
	}
	return hits
}

// isExemptLine reports whether trimmed matches any of the exempt substrings.
func isExemptLine(trimmed string, exempts []string) bool {
	for _, ex := range exempts {
		if strings.Contains(trimmed, ex) {
			return true
		}
	}
	return false
}

// scanScriptForTmpfsTypeAssert returns file:line entries for lines in a shell
// script or Containerfile that assert /tmp's filesystem type is tmpfs via
// findmnt(8) or df(1).
//
// Patterns caught:
//
//	findmnt -t tmpfs /tmp   (or --types, any argument order)
//	df -t tmpfs /tmp
func scanScriptForTmpfsTypeAssert(t *testing.T, path string) []string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		// Defect 2 fix: a missing scan target is a configuration error.  If a
		// script is renamed or moved, the sweep must fail loudly — not silently
		// continue scanning nothing.  This matches the documented history in
		// MEMORY.md of assertion↔mechanism drift: a fixture that does not
		// assert its own premise hides the gap.
		t.Fatalf("scan target missing or inaccessible — sweep is scanning nothing for %s: %v", path, err)
		return nil
	}
	if info.Size() == 0 {
		t.Fatalf("scan target is empty — sweep is scanning nothing for %s", path)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
		return nil
	}
	defer f.Close()

	var hits []string
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		// Skip comment lines (shell: #, Containerfile: # on a bare line).
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		lower := strings.ToLower(line)
		hasFindmnt := strings.Contains(lower, "findmnt") &&
			strings.Contains(lower, "tmpfs") &&
			strings.Contains(lower, "/tmp")
		hasDf := strings.Contains(lower, "df ") &&
			strings.Contains(lower, "-t") &&
			strings.Contains(lower, "tmpfs") &&
			strings.Contains(lower, "/tmp")
		if hasFindmnt || hasDf {
			hits = append(hits, fmt.Sprintf("%s:%d: %s", path, n, trimmed))
		}
	}
	if err := sc.Err(); err != nil {
		t.Errorf("scan %s: %v", path, err)
	}
	return hits
}
