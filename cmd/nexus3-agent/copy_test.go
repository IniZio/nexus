package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
)

// isPipeGone reports whether err is a benign "receiver already closed" condition.
// When the guest finishes extracting all archive bytes before the host sends
// the trailing WriteExit, handleDataConn closes the connection first. The
// subsequent WriteExit write then returns io.ErrClosedPipe (bufconn) or a
// broken-pipe error (real sockets). That is not a test failure — the data was
// fully delivered.
func isPipeGone(err error) bool {
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "closed pipe") || strings.Contains(msg, "broken pipe")
}

// streamPushReader sends r as Data(Stdout) frames followed by a final Exit
// frame on w. It mirrors the host-side copyPush logic and is driven by the
// real agent.NewPushReader archive so the tar format agreement between host
// and guest is exercised end-to-end.
//
// WriteData errors are always fatal (payload not yet delivered). WriteExit
// errors that indicate the peer already closed the connection are tolerated:
// the guest closed its side after successfully extracting the full archive,
// so the trailing Exit frame is redundant.
func streamPushReader(t *testing.T, w *wire.Writer, r io.Reader) {
	t.Helper()
	buf := make([]byte, wire.MaxDataPayload)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if werr := w.WriteData(wire.StreamStdout, buf[:n]); werr != nil {
				t.Fatalf("streamPushReader: WriteData: %v", werr)
			}
		}
		if err == io.EOF {
			if werr := w.WriteExit(wire.Exit{Code: 0}); werr != nil && !isPipeGone(werr) {
				t.Fatalf("streamPushReader: WriteExit: %v", werr)
			}
			return
		}
		if err != nil {
			t.Fatalf("streamPushReader: read: %v", err)
		}
	}
}

// TestCopyPush_File proves that a host→guest push of a single file lands the
// correct bytes at the negotiated target path.
//
// Previously handleCopyTransfer dropped all incoming frames and silently
// reported success; this test asserts the bug is gone.
func TestCopyPush_File(t *testing.T) {
	// Prepare a real source file on "host" disk.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "src.txt")
	wantContent := []byte("nexus3 push round-trip\n")
	if err := os.WriteFile(srcFile, wantContent, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	targetPath := filepath.Join(t.TempDir(), "out.txt")

	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()
	eb := int64(len(wantContent))
	resp, err := client.Copy(ctx, &agentpb.CopyRequest{
		Direction:     agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		GuestPath:     targetPath,
		IsDirectory:   false,
		ExpectedBytes: &eb, // required: authoritative source size
	})
	if err != nil {
		t.Fatalf("Copy RPC: %v", err)
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: resp.TransferId}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: type=%v err=%v", ack.Type, err)
	}
	if ack.HandshakeAck.Status != wire.AckAlive {
		t.Fatalf("expected AckAlive, got %v", ack.HandshakeAck.Status)
	}

	// Use the real host archive helper so both sides exercise their code paths.
	src, err := agent.NewPushReader(srcFile, false)
	if err != nil {
		t.Fatalf("NewPushReader: %v", err)
	}
	defer src.Close()
	streamPushReader(t, w, src)

	// Guest sends Exit once the file is written; wait for it.
	frames := collectFrames(t, conn, r, 5*time.Second)
	gotExit, code := hasExitFrame(frames)
	if !gotExit || code != 0 {
		t.Fatalf("expected Exit{0} from guest, got exit=%v code=%v", gotExit, code)
	}

	// Verify bytes landed correctly (this would be empty before the fix).
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile target: %v", err)
	}
	if !bytes.Equal(got, wantContent) {
		t.Fatalf("file content mismatch: got %q, want %q", got, wantContent)
	}
}

// TestCopyPush_File_SizeMismatchFails is the bite for Defect 2: before the fix,
// a push with ExpectedBytes set to a larger value than what was transmitted
// returned Exit{0} (fail-open). With the fix the guest detects the truncation
// and returns Exit{1}, proving the authoritative-size check is active.
//
// Revert the pushFile size guard and this test fails (Exit code 0 instead of 1).
func TestCopyPush_File_SizeMismatchFails(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "truncated.bin")

	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()
	// Declare an expected size larger than what we will actually send.
	expectedSize := int64(1000)
	resp, err := client.Copy(ctx, &agentpb.CopyRequest{
		Direction:     agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		GuestPath:     targetPath,
		IsDirectory:   false,
		ExpectedBytes: &expectedSize,
	})
	if err != nil {
		t.Fatalf("Copy RPC: %v", err)
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: resp.TransferId}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: type=%v err=%v", ack.Type, err)
	}

	// Send only 500 bytes — half of expectedSize — then signal done.
	shortData := make([]byte, 500)
	if werr := w.WriteData(wire.StreamStdout, shortData); werr != nil {
		t.Fatalf("WriteData: %v", werr)
	}
	if werr := w.WriteExit(wire.Exit{Code: 0}); werr != nil && !isPipeGone(werr) {
		t.Fatalf("WriteExit: %v", werr)
	}

	// Guest must report a non-zero exit code because received < expectedSize.
	frames := collectFrames(t, conn, r, 5*time.Second)
	gotExit, code := hasExitFrame(frames)
	if !gotExit {
		t.Fatal("expected Exit frame from guest, got none")
	}
	if code == 0 {
		t.Fatalf("expected non-zero exit code for truncated push (received 500, expected %d), got 0 (fail-open)", expectedSize)
	}
}

// TestCopyPush_EmptyFile_OK proves that a legitimately empty file (declared size
// 0, zero bytes received) is accepted — distinguishing a valid &0 declaration
// from nil (undeclared). Before the optional-field fix, declared 0 in the proto
// was indistinguishable from absent and pushFile would return an error.
func TestCopyPush_EmptyFile_OK(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "empty.txt")

	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()
	eb := int64(0) // &0 = legitimately empty file declared
	resp, err := client.Copy(ctx, &agentpb.CopyRequest{
		Direction:     agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		GuestPath:     targetPath,
		IsDirectory:   false,
		ExpectedBytes: &eb,
	})
	if err != nil {
		t.Fatalf("Copy RPC: %v", err)
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: resp.TransferId}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: type=%v err=%v", ack.Type, err)
	}
	if ack.HandshakeAck.Status != wire.AckAlive {
		t.Fatalf("expected AckAlive, got %v", ack.HandshakeAck.Status)
	}

	// Send zero data bytes, then signal done.
	if werr := w.WriteExit(wire.Exit{Code: 0}); werr != nil && !isPipeGone(werr) {
		t.Fatalf("WriteExit: %v", werr)
	}

	// Guest must report exit 0 — empty file is valid.
	frames := collectFrames(t, conn, r, 5*time.Second)
	gotExit, code := hasExitFrame(frames)
	if !gotExit || code != 0 {
		t.Fatalf("expected Exit{0} for empty-file push, got exit=%v code=%v (regression: declared 0 rejected as undeclared)", gotExit, code)
	}

	// File must exist and be empty.
	fi, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat target: %v — empty file not written", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("expected empty file, got %d bytes", fi.Size())
	}
}

// TestCopyPush_Undeclared_Fails proves that a PUSH with no ExpectedBytes
// (nil = host did not declare the size) is rejected immediately — fail closed.
// Revert the nil-check in pushFile and this test passes with Exit{0}, proving
// the guard is necessary.
func TestCopyPush_Undeclared_Fails(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "undeclared.txt")

	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()
	// No ExpectedBytes field set → nil in the proto (optional absent).
	resp, err := client.Copy(ctx, &agentpb.CopyRequest{
		Direction:   agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		GuestPath:   targetPath,
		IsDirectory: false,
		// ExpectedBytes intentionally omitted — nil means undeclared.
	})
	if err != nil {
		t.Fatalf("Copy RPC: %v", err)
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: resp.TransferId}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: type=%v err=%v", ack.Type, err)
	}

	// Send some data and signal done — the guest may already have closed the
	// connection (fail-closed on nil expectedBytes), so pipe-gone is tolerated
	// on both writes.
	if werr := w.WriteData(wire.StreamStdout, []byte("some data")); werr != nil && !isPipeGone(werr) {
		t.Fatalf("WriteData: %v", werr)
	}
	if werr := w.WriteExit(wire.Exit{Code: 0}); werr != nil && !isPipeGone(werr) {
		t.Fatalf("WriteExit: %v", werr)
	}

	// Guest must report non-zero exit — undeclared size is fail closed.
	frames := collectFrames(t, conn, r, 5*time.Second)
	gotExit, code := hasExitFrame(frames)
	if !gotExit {
		t.Fatal("expected Exit frame from guest, got none")
	}
	if code == 0 {
		t.Fatal("expected non-zero exit code for undeclared ExpectedBytes (nil), got 0 — fail-open (mutation proof: remove nil-check in pushFile, this test fails)")
	}
}

// TestCopyPull_File proves the guest→host single-file PULL path:
//   - the agent stats the file and declares DeclaredBytes in the RPC response;
//   - the Data frames carry the correct file bytes;
//   - the host verifies the byte count against DeclaredBytes (round-trip proves
//     no silent truncation occurred).
func TestCopyPull_File(t *testing.T) {
	wantContent := []byte("nexus3 pull round-trip\n")
	srcFile := filepath.Join(t.TempDir(), "pull-src.txt")
	if err := os.WriteFile(srcFile, wantContent, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()
	resp, err := client.Copy(ctx, &agentpb.CopyRequest{
		Direction:   agentpb.CopyDirection_COPY_DIRECTION_PULL,
		GuestPath:   srcFile,
		IsDirectory: false,
	})
	if err != nil {
		t.Fatalf("Copy RPC: %v", err)
	}
	if resp.DeclaredBytes == nil || *resp.DeclaredBytes != int64(len(wantContent)) {
		var got interface{} = "<nil>"
		if resp.DeclaredBytes != nil {
			got = *resp.DeclaredBytes
		}
		t.Fatalf("DeclaredBytes=%v, want %d — agent must stat and declare for single-file PULL",
			got, len(wantContent))
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: resp.TransferId}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: type=%v err=%v", ack.Type, err)
	}
	if ack.HandshakeAck.Status != wire.AckAlive {
		t.Fatalf("expected AckAlive, got %v", ack.HandshakeAck.Status)
	}

	frames := collectFrames(t, conn, r, 5*time.Second)
	gotExit, code := hasExitFrame(frames)
	if !gotExit || code != 0 {
		t.Fatalf("expected Exit{0} from guest, got exit=%v code=%v", gotExit, code)
	}
	got := dataBytes(frames)
	if !bytes.Equal(got, wantContent) {
		t.Fatalf("content mismatch: got %q, want %q", got, wantContent)
	}
}

// TestCopyPush_Dir proves that a host→guest push of a directory tree extracts
// the full archive (created by agent.NewPushReader) under the target path.
func TestCopyPush_Dir(t *testing.T) {
	// Build a real source directory tree.
	srcRoot := t.TempDir()
	treeFiles := map[string][]byte{
		"alpha.txt":     []byte("alpha content"),
		"sub/beta.txt":  []byte("beta content"),
		"sub/gamma.txt": []byte("gamma content"),
	}
	for rel, content := range treeFiles {
		full := filepath.Join(srcRoot, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", rel, err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatalf("write %q: %v", rel, err)
		}
	}

	targetDir := filepath.Join(t.TempDir(), "extracted")

	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()
	resp, err := client.Copy(ctx, &agentpb.CopyRequest{
		Direction:   agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		GuestPath:   targetDir,
		IsDirectory: true,
	})
	if err != nil {
		t.Fatalf("Copy RPC: %v", err)
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: resp.TransferId}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: type=%v err=%v", ack.Type, err)
	}
	if ack.HandshakeAck.Status != wire.AckAlive {
		t.Fatalf("expected AckAlive, got %v", ack.HandshakeAck.Status)
	}

	src, err := agent.NewPushReader(srcRoot, true)
	if err != nil {
		t.Fatalf("NewPushReader: %v", err)
	}
	defer src.Close()
	streamPushReader(t, w, src)

	frames := collectFrames(t, conn, r, 5*time.Second)
	gotExit, code := hasExitFrame(frames)
	if !gotExit || code != 0 {
		t.Fatalf("expected Exit{0} from guest, got exit=%v code=%v", gotExit, code)
	}

	// Verify every file in the tree was extracted with correct content.
	for rel, wantContent := range treeFiles {
		full := filepath.Join(targetDir, rel)
		got, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("read %q: %v", rel, err)
			continue
		}
		if !bytes.Equal(got, wantContent) {
			t.Errorf("file %q: got %q, want %q", rel, got, wantContent)
		}
	}
}
