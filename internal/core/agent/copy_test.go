package agent

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent/wire"
)

// buildWirePullStream writes payloads as Data(Stdout) frames followed by an
// Exit frame into a bytes.Buffer and returns a wire.Reader over it. It
// mirrors the guest-side pullFile streaming logic.
func buildWirePullStream(t *testing.T, payloads [][]byte) *wire.Reader {
	t.Helper()
	var buf bytes.Buffer
	wr := wire.NewWriter(&buf)
	for _, p := range payloads {
		for len(p) > 0 {
			chunk := p
			if len(chunk) > wire.MaxDataPayload {
				chunk = p[:wire.MaxDataPayload]
			}
			if err := wr.WriteData(wire.StreamStdout, chunk); err != nil {
				t.Fatalf("buildWirePullStream: WriteData: %v", err)
			}
			p = p[len(chunk):]
		}
	}
	if err := wr.WriteExit(wire.Exit{Code: 0}); err != nil {
		t.Fatalf("buildWirePullStream: WriteExit: %v", err)
	}
	return wire.NewReader(&buf)
}

// ptrInt64 is a convenience helper for constructing a *int64 inline.
func ptrInt64(v int64) *int64 { return &v }

// TestCopyPull_OkFile proves the happy path: correct bytes land in dst and
// no error is returned when declaredBytes matches the received count.
func TestCopyPull_OkFile(t *testing.T) {
	payload := []byte("nexus3 pull unit test")
	rd := buildWirePullStream(t, [][]byte{payload})
	var dst bytes.Buffer
	if err := copyPull(rd, &dst, ptrInt64(int64(len(payload))), false, "/guest/src.txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("content mismatch: got %q, want %q", dst.Bytes(), payload)
	}
}

// TestCopyPull_EmptyFile_OK proves that a legitimately empty file (declared
// size 0, zero bytes received) is accepted — distinguishing a valid 0-byte
// declaration from an absent declaration (nil).
//
// Before the optional-field fix, declared 0 was treated as "no declaration"
// and this test would fail with a false-positive error.
func TestCopyPull_EmptyFile_OK(t *testing.T) {
	// No payload — empty file.
	rd := buildWirePullStream(t, nil)
	var dst bytes.Buffer
	// ptrInt64(0): agent declared size = 0, received = 0, must succeed.
	if err := copyPull(rd, &dst, ptrInt64(0), false, "/guest/empty.txt"); err != nil {
		t.Fatalf("unexpected error pulling 0-byte file: %v", err)
	}
	if dst.Len() != 0 {
		t.Fatalf("expected empty dst, got %d bytes", dst.Len())
	}
}

// TestCopyPull_SizeMismatch_Fails is mutation proof #1:
//
//	Removing the `if n != *declaredBytes` host comparison in copyPull lets this
//	test return nil instead of an error — the test then fails, proving the
//	guard is necessary.
func TestCopyPull_SizeMismatch_Fails(t *testing.T) {
	// Declare 10 bytes but send only 5 — simulates transport truncation.
	rd := buildWirePullStream(t, [][]byte{[]byte("hello")}) // 5 bytes
	var dst bytes.Buffer
	err := copyPull(rd, &dst, ptrInt64(10), false, "/guest/src.txt")
	if err == nil {
		t.Fatal("expected error for byte-count mismatch (received 5, declared 10), got nil — " +
			"remove the `if n != *declaredBytes` comparison and this test fails (mutation proof #1)")
	}
}

// TestCopyPull_NoDeclaredBytes_Fails is mutation proof #2:
//
//	nil declaredBytes means the agent did not set the optional field — the
//	host must fail closed. Removing the agent's `declaredBytes = &size`
//	declaration causes the host to receive nil, triggering this guard. If the
//	guard is also removed the test returns nil and fails, proving both the
//	agent-side declaration and the host-side nil-check are necessary.
func TestCopyPull_NoDeclaredBytes_Fails(t *testing.T) {
	// nil = field absent (agent did not declare) — distinct from ptrInt64(0).
	rd := buildWirePullStream(t, [][]byte{[]byte("hello")})
	var dst bytes.Buffer
	err := copyPull(rd, &dst, nil, false, "/guest/src.txt")
	if err == nil {
		t.Fatal("expected fail-closed error when DeclaredBytes is nil for single-file PULL, got nil — " +
			"remove the agent's DeclaredBytes declaration and this test fails (mutation proof #2)")
	}
}

// TestCopyPull_Dir_TruncatedTar_Fails proves that archive/tar surfaces
// truncated entry bodies:
//
//	A tar header declares Size=100 but no body bytes are sent. archive/tar
//	returns io.ErrUnexpectedEOF when it cannot read the full declared body,
//	which propagates through validateTarStream and out of copyPull as an error.
func TestCopyPull_Dir_TruncatedTar_Fails(t *testing.T) {
	// Build a tar that declares 100 bytes of body but supplies none.
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "data.bin",
		Typeflag: tar.TypeReg,
		Size:     100,
		Mode:     0o644,
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	// Intentionally omit the 100 body bytes and do not call tw.Close().
	// tarBuf now holds exactly one 512-byte header block with no body.

	rd := buildWirePullStream(t, [][]byte{tarBuf.Bytes()})
	var dst bytes.Buffer
	err := copyPull(rd, &dst, nil, true, "/guest/dir")
	if err == nil {
		t.Fatal("expected error for truncated tar entry body, got nil — " +
			"archive/tar should return io.ErrUnexpectedEOF on a short body")
	}
}

// TestCopyPull_Dir_ValidTar_Ok proves that a well-formed tar archive passes
// the inline validation without error.
func TestCopyPull_Dir_ValidTar_Ok(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	content := []byte("hello from guest dir")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "hello.txt",
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o644,
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("tar Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}

	rd := buildWirePullStream(t, [][]byte{tarBuf.Bytes()})
	var dst bytes.Buffer
	if err := copyPull(rd, &dst, nil, true, "/guest/dir"); err != nil {
		t.Fatalf("unexpected error for valid tar: %v", err)
	}
	if !bytes.Equal(dst.Bytes(), tarBuf.Bytes()) {
		t.Fatal("dst content does not match original tar bytes")
	}
}

// TestValidateTarStream_Truncated confirms validateTarStream returns a
// non-nil error when a tar body is short — isolating the archive/tar
// detection from the frame-reading machinery.
func TestValidateTarStream_Truncated(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{
		Name:     "x",
		Typeflag: tar.TypeReg,
		Size:     50,
		Mode:     0o644,
	})
	// Write only 10 of the declared 50 bytes.
	_, _ = io.CopyN(tw, bytes.NewReader(make([]byte, 10)), 10)
	// Do not close — the archive is intentionally corrupt.

	err := validateTarStream(&tarBuf)
	if err == nil {
		t.Fatal("expected error from validateTarStream for truncated tar body, got nil")
	}
}
