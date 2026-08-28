package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeFiles creates n regular files under dir. The first nonZero of them get
// one byte of content; the rest are zero-length. Enough parent structure is
// created to resemble a rootfs.
func writeFiles(t *testing.T, dir string, n, nonZero int) {
	t.Helper()
	sub := filepath.Join(dir, "usr", "bin")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		p := filepath.Join(sub, "f"+strconv.Itoa(i))
		var data []byte
		if i < nonZero {
			data = []byte{'x'}
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestVerifyRootfsPopulated_HollowExportFails reproduces the BUG-2 corruption
// signature — thousands of regular files, all but a couple zero-length — and
// asserts the guard rejects it. This is the bite: before the guard, such a tree
// was converted to an ext4 image, cached, and booted, failing only later with
// "exec format error".
func TestVerifyRootfsPopulated_HollowExportFails(t *testing.T) {
	dir := t.TempDir()
	// Mirror the observed ratio: 5023 files, 2 non-zero.
	writeFiles(t, dir, 5023, 2)

	err := verifyRootfsPopulated(dir)
	if err == nil {
		t.Fatal("expected hollow rootfs to be rejected, got nil")
	}
	var hollow *ErrRootfsHollow
	if !errors.As(err, &hollow) {
		t.Fatalf("expected *ErrRootfsHollow, got %T: %v", err, err)
	}
	if hollow.TotalFiles != 5023 || hollow.ZeroFiles != 5021 {
		t.Fatalf("unexpected counts: total=%d zero=%d", hollow.TotalFiles, hollow.ZeroFiles)
	}
}

// TestVerifyRootfsPopulated_HealthyExportPasses asserts a normal rootfs (the
// vast majority of files carry content) passes. Without this, a guard that
// simply always failed would also "pass" the bite test above — this is the
// mutation guard proving the check discriminates.
func TestVerifyRootfsPopulated_HealthyExportPasses(t *testing.T) {
	dir := t.TempDir()
	// 5000 files, only 20 legitimately empty (~0.4%): a realistic rootfs.
	writeFiles(t, dir, 5000, 4980)

	if err := verifyRootfsPopulated(dir); err != nil {
		t.Fatalf("expected healthy rootfs to pass, got: %v", err)
	}
}

// TestVerifyRootfsPopulated_TinyImageUnguarded asserts that images with fewer
// than the file-count floor are not judged, so a legitimately minimal base is
// never false-flagged even if a large fraction of its files are empty.
func TestVerifyRootfsPopulated_TinyImageUnguarded(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, 50, 1) // 49/50 zero, but below the floor

	if err := verifyRootfsPopulated(dir); err != nil {
		t.Fatalf("expected tiny image to be unguarded, got: %v", err)
	}
}

// TestVerifyRootfsPopulated_JustBelowThresholdPasses pins the boundary: a rootfs
// with a high-but-legitimate empty fraction (just under 90%) still passes, so
// the guard cannot creep into false positives.
func TestVerifyRootfsPopulated_JustBelowThresholdPasses(t *testing.T) {
	dir := t.TempDir()
	// 1000 files, 890 empty = 89% < 90%.
	writeFiles(t, dir, 1000, 110)

	if err := verifyRootfsPopulated(dir); err != nil {
		t.Fatalf("expected 89%% empty to pass, got: %v", err)
	}
}

// makeAgentFixture creates a source binary and an exported copy in a fake
// rootfs under rootfsDir/sbin/nexus3-agent. srcSize and dstSize control byte
// counts so callers can exercise truncation scenarios.
func makeAgentFixture(t *testing.T, rootfsDir string, srcSize, dstSize int) (srcPath string) {
	t.Helper()
	sbin := filepath.Join(rootfsDir, "sbin")
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPath = filepath.Join(t.TempDir(), "nexus3-agent-src")
	if err := os.WriteFile(srcPath, make([]byte, srcSize), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(sbin, "nexus3-agent")
	if err := os.WriteFile(dst, make([]byte, dstSize), 0o755); err != nil {
		t.Fatal(err)
	}
	return srcPath
}

// TestVerifyAgentIntegrity_TruncatedFails is the bite test: the exported agent
// is shorter than the source (the 32 MiB truncation signature). Without
// verifyAgentIntegrity this passes silently and bakes a segfaulting binary into
// the image. With it present the build is rejected with ErrRootfsTruncated.
func TestVerifyAgentIntegrity_TruncatedFails(t *testing.T) {
	rootfsDir := t.TempDir()
	// Source 1000 B, export 512 B — mimics the >32 MiB → 32 MiB truncation.
	srcPath := makeAgentFixture(t, rootfsDir, 1000, 512)

	err := verifyAgentIntegrity(rootfsDir, "/sbin/nexus3-agent", srcPath)
	if err == nil {
		t.Fatal("expected truncated agent to be rejected, got nil")
	}
	var truncErr *ErrRootfsTruncated
	if !errors.As(err, &truncErr) {
		t.Fatalf("expected *ErrRootfsTruncated, got %T: %v", err, err)
	}
	if truncErr.GotBytes != 512 || truncErr.WantBytes != 1000 {
		t.Fatalf("unexpected sizes: got=%d want=%d", truncErr.GotBytes, truncErr.WantBytes)
	}
}

// TestVerifyAgentIntegrity_MatchingPasses is the control: when the exported
// agent matches the source byte-for-byte the guard passes.
func TestVerifyAgentIntegrity_MatchingPasses(t *testing.T) {
	rootfsDir := t.TempDir()
	srcPath := makeAgentFixture(t, rootfsDir, 1000, 1000)

	if err := verifyAgentIntegrity(rootfsDir, "/sbin/nexus3-agent", srcPath); err != nil {
		t.Fatalf("expected matching agent to pass, got: %v", err)
	}
}

// TestVerifyAgentIntegrity_EmptySourceSkips asserts the guard is a no-op when
// agentSourcePath is empty (no agent was requested in the build).
func TestVerifyAgentIntegrity_EmptySourceSkips(t *testing.T) {
	rootfsDir := t.TempDir()
	if err := verifyAgentIntegrity(rootfsDir, "/sbin/nexus3-agent", ""); err != nil {
		t.Fatalf("expected skip on empty source, got: %v", err)
	}
}
