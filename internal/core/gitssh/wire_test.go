package gitssh_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"testing"

	"github.com/IniZio/nexus/internal/core/gitssh"
)

func TestWriteReadRequest_RoundTrip(t *testing.T) {
	req := gitssh.Request{
		Argv:        []string{"git@github.com", "git-receive-pack '/example-org/example-app.git'"},
		Cwd:         "/workspace/example-app",
		GitProtocol: "version=2",
	}

	var buf bytes.Buffer
	if err := gitssh.WriteRequest(&buf, req); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	got, err := gitssh.ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}

	if len(got.Argv) != len(req.Argv) {
		t.Fatalf("argv length: want %d, got %d", len(req.Argv), len(got.Argv))
	}
	for i, v := range req.Argv {
		if got.Argv[i] != v {
			t.Errorf("argv[%d]: want %q, got %q", i, v, got.Argv[i])
		}
	}
	if got.Cwd != req.Cwd {
		t.Errorf("cwd: want %q, got %q", req.Cwd, got.Cwd)
	}
	if got.GitProtocol != req.GitProtocol {
		t.Errorf("git_protocol: want %q, got %q", req.GitProtocol, got.GitProtocol)
	}
}

func TestWriteReadRequest_NoGitProtocol(t *testing.T) {
	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-upload-pack '/owner/repo.git'"},
		Cwd:  "/workspace/repo",
	}
	var buf bytes.Buffer
	if err := gitssh.WriteRequest(&buf, req); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	got, err := gitssh.ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if got.GitProtocol != "" {
		t.Errorf("git_protocol should be empty; got %q", got.GitProtocol)
	}
}

func TestWriteReadFrame_Stdout(t *testing.T) {
	data := []byte("hello from host\n")
	var buf bytes.Buffer
	if err := gitssh.WriteStdout(&buf, data); err != nil {
		t.Fatalf("WriteStdout: %v", err)
	}

	ft, payload, err := gitssh.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if ft != gitssh.FrameTypeStdout {
		t.Errorf("frame type: want FrameTypeStdout (0x01), got 0x%02x", ft)
	}
	if !bytes.Equal(payload, data) {
		t.Errorf("payload: want %q, got %q", data, payload)
	}
}

func TestWriteExitFrame_PropagatesCode(t *testing.T) {
	for _, code := range []int32{0, 1, 127, -1} {
		var buf bytes.Buffer
		if err := gitssh.WriteExitFrame(&buf, code); err != nil {
			t.Fatalf("WriteExitFrame(%d): %v", code, err)
		}
		ft, payload, err := gitssh.ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame (code=%d): %v", code, err)
		}
		if ft != gitssh.FrameTypeExit {
			t.Errorf("code=%d: frame type: want FrameTypeExit (0x02), got 0x%02x", code, ft)
		}
		if len(payload) != 4 {
			t.Fatalf("code=%d: exit payload length: want 4, got %d", code, len(payload))
		}
		got := int32(binary.BigEndian.Uint32(payload))
		if got != code {
			t.Errorf("code=%d: decoded exit code: want %d, got %d", code, code, got)
		}
	}
}

func TestPipeRoundTrip_StdinBridgeAndExitCode(t *testing.T) {
	shimConn, relayConn := net.Pipe()

	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-receive-pack '/foo/bar.git'"},
		Cwd:  "/workspace",
	}
	const stdinData = "pack-protocol-data\n"
	const wantExitCode int32 = 42

	relayDone := make(chan error, 1)
	go func() {
		defer relayConn.Close()
		gotReq, err := gitssh.ReadRequest(relayConn)
		if err != nil {
			relayDone <- fmt.Errorf("relay ReadRequest: %w", err)
			return
		}
		if len(gotReq.Argv) == 0 || gotReq.Argv[0] != req.Argv[0] {
			relayDone <- fmt.Errorf("relay: unexpected argv[0]: %q", gotReq.Argv[0])
			return
		}
		var stdinBuf [1024]byte
		n, _ := relayConn.Read(stdinBuf[:])
		if err := gitssh.WriteStdout(relayConn, stdinBuf[:n]); err != nil {
			relayDone <- fmt.Errorf("relay WriteStdout: %w", err)
			return
		}
		if err := gitssh.WriteExitFrame(relayConn, wantExitCode); err != nil {
			relayDone <- fmt.Errorf("relay WriteExitFrame: %w", err)
			return
		}
		relayDone <- nil
	}()

	if err := gitssh.WriteRequest(shimConn, req); err != nil {
		t.Fatalf("shim WriteRequest: %v", err)
	}
	if _, err := shimConn.Write([]byte(stdinData)); err != nil {
		t.Fatalf("shim write stdin: %v", err)
	}

	ft, payload, err := gitssh.ReadFrame(shimConn)
	if err != nil {
		t.Fatalf("shim ReadFrame(stdout): %v", err)
	}
	if ft != gitssh.FrameTypeStdout {
		t.Errorf("want FrameTypeStdout, got 0x%02x", ft)
	}
	if string(payload) != stdinData {
		t.Errorf("stdout payload: want %q, got %q", stdinData, payload)
	}

	ft, payload, err = gitssh.ReadFrame(shimConn)
	if err != nil {
		t.Fatalf("shim ReadFrame(exit): %v", err)
	}
	if ft != gitssh.FrameTypeExit {
		t.Errorf("want FrameTypeExit, got 0x%02x", ft)
	}
	gotCode := int32(binary.BigEndian.Uint32(payload))
	if gotCode != wantExitCode {
		t.Errorf("exit code: want %d, got %d", wantExitCode, gotCode)
	}

	shimConn.Close()
	if err := <-relayDone; err != nil {
		t.Fatalf("relay: %v", err)
	}
}
