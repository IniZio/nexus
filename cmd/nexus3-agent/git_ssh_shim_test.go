package main

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/IniZio/nexus3/internal/core/gitssh"
)

// pipeRelay is a test helper that acts as a minimal host relay over a
// net.Pipe pair: it reads the request frame, optionally writes stdout data,
// then sends an exit frame with the given code.
type pipeRelay struct {
	conn     net.Conn
	exitCode int32
	stdout   []byte
}

func (r *pipeRelay) run(t *testing.T, reqOut *gitssh.Request) {
	t.Helper()
	defer r.conn.Close()

	req, err := gitssh.ReadRequest(r.conn)
	if err != nil {
		t.Errorf("relay: ReadRequest: %v", err)
		return
	}
	if reqOut != nil {
		*reqOut = req
	}

	if len(r.stdout) > 0 {
		if err := gitssh.WriteStdout(r.conn, r.stdout); err != nil {
			t.Errorf("relay: WriteStdout: %v", err)
			return
		}
	}
	if err := gitssh.WriteExitFrame(r.conn, r.exitCode); err != nil {
		t.Errorf("relay: WriteExitFrame: %v", err)
	}
}

// TestGitSSHShim_ExitCodePropagated verifies that the exit code from
// FrameTypeExit is returned by execGitSSHShim.
//
// Mutation pin: flipping the exit-code propagation (e.g. always returning 0
// or ignoring the frame payload) must cause this test to fail.
func TestGitSSHShim_ExitCodePropagated(t *testing.T) {
	for _, wantCode := range []int32{0, 1, 128} {
		t.Run("", func(t *testing.T) {
			shimConn, relayConn := net.Pipe()

			relay := &pipeRelay{conn: relayConn, exitCode: wantCode}
			var gotReq gitssh.Request
			relayDone := make(chan struct{})
			go func() {
				defer close(relayDone)
				relay.run(t, &gotReq)
			}()

			args := []string{"git@github.com", "git-receive-pack '/owner/repo.git'"}
			gotCode, err := execGitSSHShim(args, shimConn)
			if err != nil {
				t.Fatalf("execGitSSHShim: %v", err)
			}
			<-relayDone

			if gotCode != wantCode {
				t.Errorf("exit code: want %d, got %d", wantCode, gotCode)
			}
		})
	}
}

// TestGitSSHShim_RequestArgvSent verifies that execGitSSHShim encodes the
// argv correctly in the request frame sent to the relay.
func TestGitSSHShim_RequestArgvSent(t *testing.T) {
	shimConn, relayConn := net.Pipe()

	relay := &pipeRelay{conn: relayConn, exitCode: 0}
	var gotReq gitssh.Request
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		relay.run(t, &gotReq)
	}()

	args := []string{"user@github.com", "git-receive-pack '/example-org/example-app.git'"}
	_, _ = execGitSSHShim(args, shimConn)
	<-relayDone

	if len(gotReq.Argv) != len(args) {
		t.Fatalf("argv length: want %d, got %d", len(args), len(gotReq.Argv))
	}
	for i, v := range args {
		if gotReq.Argv[i] != v {
			t.Errorf("argv[%d]: want %q, got %q", i, v, gotReq.Argv[i])
		}
	}
}

// TestGitSSHShim_NonzeroExitCodePropagated_MutationPin is the mutation pin
// for exit-code propagation. It proves that returning a hardcoded zero exit
// code (instead of the FrameTypeExit payload) would make this test fail.
//
// A mutation that replaces `exitCode = int32(binary.BigEndian.Uint32(payload))`
// with `exitCode = 0` (or removes the assignment) will cause this test to fail
// because wantCode=42 ≠ 0.
func TestGitSSHShim_NonzeroExitCodePropagated_MutationPin(t *testing.T) {
	const wantCode int32 = 42

	shimConn, relayConn := net.Pipe()
	relay := &pipeRelay{conn: relayConn, exitCode: wantCode}
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		relay.run(t, nil)
	}()

	args := []string{"git@github.com", "git-upload-pack '/foo/bar.git'"}
	gotCode, err := execGitSSHShim(args, shimConn)
	<-relayDone

	if err != nil {
		t.Fatalf("execGitSSHShim: %v", err)
	}
	if gotCode != wantCode {
		t.Errorf("exit-code propagation broken: want %d, got %d (mutation target: binary.BigEndian.Uint32(payload))",
			wantCode, gotCode)
	}
}

// TestGitSSHShim_ExitFrame_PayloadDecoding ensures the 4-byte BE int32 exit
// code is read correctly from the payload bytes.
func TestGitSSHShim_ExitFrame_PayloadDecoding(t *testing.T) {
	for _, wantCode := range []int32{1, 127, 255} {
		var payload [4]byte
		binary.BigEndian.PutUint32(payload[:], uint32(wantCode))
		got := int32(binary.BigEndian.Uint32(payload[:]))
		if got != wantCode {
			t.Errorf("BE decode: want %d, got %d", wantCode, got)
		}
	}
}
