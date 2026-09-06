package builder_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
)

// TestExportAndUnpack_FailClosed_TruncatedBody proves that a tar entry whose
// body is shorter than hdr.Size causes a hard error rather than a silently
// truncated file. This is Mutation Proof 1: if the unpack path were replaced
// with a naïve io.Copy(io.Discard, r), this test would return nil — masking
// data corruption. The solveFn writes only the 512-byte header block for a
// 4096-byte file; no body bytes follow, so archive.Apply sees unexpected EOF.
func TestExportAndUnpack_FailClosed_TruncatedBody(t *testing.T) {
	// Build a valid tar header for a 4096-byte file, then truncate to just the
	// 512-byte header block — no body bytes present in the stream.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := bytes.Repeat([]byte("x"), 4096)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "bigfile",
		Size:     int64(len(body)),
		Mode:     0644,
		ModTime:  time.Now(),
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("Write body: %v", err)
	}
	// Take only the header block; body is intentionally absent.
	truncated := buf.Bytes()[:512]

	outDir := t.TempDir()
	solveFn := func(_ context.Context, pw io.WriteCloser) error {
		_, err := pw.Write(truncated)
		return err
	}

	err := builder.ExportAndUnpack(context.Background(), outDir, solveFn)
	if err == nil {
		t.Fatal("expected non-nil error for truncated tar body, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestExportAndUnpack_UnpackError_Propagates proves that an unpack-side error
// arising from a structurally COMPLETE tar stream (no truncation) is propagated
// back to the caller even though the solve goroutine returned nil. This is
// Mutation Proof 2: if eg.Wait() were removed (fire-and-forget unpack), the
// function would return nil and the caller would see a spuriously successful
// build.
//
// This proof is intentionally distinct from Proof 1: the tar stream here is
// complete and well-formed (size=0 entry, no truncated body). The unpack fails
// because the hardlink target does not exist in outDir, causing archive.Apply
// to return an ENOENT error from os.Link — NOT an unexpected-EOF error from a
// short body. A proof relying only on truncation would not exercise the errgroup
// join on non-truncation unpack failures.
func TestExportAndUnpack_UnpackError_Propagates(t *testing.T) {
	// Build a well-formed, complete tar with a hardlink whose target does not
	// exist in outDir. archive.Apply calls os.Link(target, name) → ENOENT.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeLink,
		Name:     "link_to_nowhere",
		Linkname: "nonexistent_target",
		Mode:     0644,
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar.Close: %v", err)
	}
	tarBytes := buf.Bytes()

	outDir := t.TempDir()
	solveFn := func(_ context.Context, pw io.WriteCloser) error {
		// Write the complete tar and return nil — solve "succeeded".
		_, err := pw.Write(tarBytes)
		return err
	}

	err := builder.ExportAndUnpack(context.Background(), outDir, solveFn)
	if err == nil {
		t.Fatal("expected non-nil error propagated from unpack side, got nil " +
			"(likely eg.Wait() was removed)")
	}
	// The unpack goroutine wraps its error with "unpack tar".
	if !strings.Contains(err.Error(), "unpack tar") {
		t.Fatalf("expected error origin \"unpack tar\", got: %v", err)
	}
	t.Logf("got expected error: %v", err)
}

// TestExportAndUnpack_Fidelity verifies that ExportAndUnpack preserves parity
// with the old ExporterLocal path: regular file content, modes, symlinks, and
// hardlinks survive the unpack. Ownership is NOT verified for xattrs or device
// nodes: rootless buildkitd strips both from exported tars (confirmed by
// inspection: 0 TypeChar/Block entries, 0 security.* PAX headers in a real
// alpine export). Owner parity IS verified: WithNoSameOwner() means all
// extracted files are owned by the current user, matching the old path which
// rewrote every uid/gid to the builder uid via session/filesync/diffcopy.go.
func TestExportAndUnpack_Fidelity(t *testing.T) {
	fileContent := []byte("hello fidelity")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Directory entry before its children.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "data/",
		Mode:     0755,
		ModTime:  time.Now(),
	}); err != nil {
		t.Fatalf("WriteHeader data/: %v", err)
	}

	// Regular file.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "data/hello.txt",
		Size:     int64(len(fileContent)),
		Mode:     0644,
		ModTime:  time.Now(),
	}); err != nil {
		t.Fatalf("WriteHeader data/hello.txt: %v", err)
	}
	if _, err := tw.Write(fileContent); err != nil {
		t.Fatalf("Write data/hello.txt body: %v", err)
	}

	// Hardlink to the regular file. Mode must match the target so Apply does
	// not chmod the shared inode to 0000 (the zero-value default).
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeLink,
		Name:     "data/hardlink",
		Linkname: "data/hello.txt",
		Mode:     0644,
	}); err != nil {
		t.Fatalf("WriteHeader data/hardlink: %v", err)
	}

	// Symlink.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "link",
		Linkname: "data/hello.txt",
	}); err != nil {
		t.Fatalf("WriteHeader link: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar.Close: %v", err)
	}
	tarBytes := buf.Bytes()

	outDir := t.TempDir()
	solveFn := func(_ context.Context, pw io.WriteCloser) error {
		_, err := pw.Write(tarBytes)
		return err
	}

	if err := builder.ExportAndUnpack(context.Background(), outDir, solveFn); err != nil {
		t.Fatalf("ExportAndUnpack: %v", err)
	}

	// Regular file content.
	got, err := os.ReadFile(outDir + "/data/hello.txt")
	if err != nil {
		t.Fatalf("ReadFile data/hello.txt: %v", err)
	}
	if string(got) != string(fileContent) {
		t.Fatalf("data/hello.txt content = %q, want %q", got, fileContent)
	}

	// File mode (0644).
	fi, err := os.Stat(outDir + "/data/hello.txt")
	if err != nil {
		t.Fatalf("Stat data/hello.txt: %v", err)
	}
	if fi.Mode().Perm() != 0644 {
		t.Fatalf("data/hello.txt mode = %04o, want 0644", fi.Mode().Perm())
	}

	// Hardlink: same content as original.
	gotHL, err := os.ReadFile(outDir + "/data/hardlink")
	if err != nil {
		t.Fatalf("ReadFile data/hardlink: %v", err)
	}
	if string(gotHL) != string(fileContent) {
		t.Fatalf("data/hardlink content = %q, want %q", gotHL, fileContent)
	}

	// Symlink: points to data/hello.txt.
	fi, err = os.Lstat(outDir + "/link")
	if err != nil {
		t.Fatalf("Lstat link: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link: expected symlink, got mode %v", fi.Mode())
	}
	target, err := os.Readlink(outDir + "/link")
	if err != nil {
		t.Fatalf("Readlink link: %v", err)
	}
	if target != "data/hello.txt" {
		t.Fatalf("link target = %q, want %q", target, "data/hello.txt")
	}

	// Owner parity: WithNoSameOwner() skips lchown, so files are owned by the
	// current user — matching the old ExporterLocal path (diffcopy.go rewrote
	// every uid/gid to os.Getuid()/os.Getgid() before writing to disk).
	fi, err = os.Stat(outDir + "/data/hello.txt")
	if err != nil {
		t.Fatalf("Stat data/hello.txt for owner check: %v", err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		wantUID := uint32(os.Getuid())
		wantGID := uint32(os.Getgid())
		if st.Uid != wantUID || st.Gid != wantGID {
			t.Fatalf("data/hello.txt owner uid:gid = %d:%d, want %d:%d (WithNoSameOwner parity)",
				st.Uid, st.Gid, wantUID, wantGID)
		}
	} else {
		t.Logf("skipping owner check: Sys() is not *syscall.Stat_t on this platform")
	}
}
