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
	resp, err := client.Copy(ctx, &agentpb.CopyRequest{
		Direction:   agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		GuestPath:   targetPath,
		IsDirectory: false,
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
