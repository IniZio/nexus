package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/mdlayher/vsock"

	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/gitssh"
)

// hostCID is the vsock CID for the host from the guest's perspective.
const hostCID uint32 = 2

// gitSSHShimDial is the dial function used by runGitSSHShim.
// Replaced in tests with a net.Pipe-backed implementation.
var gitSSHShimDial = func() (net.Conn, error) {
	return vsock.Dial(hostCID, driver.GitSSHRelayPort, nil)
}

// runGitSSHShim is invoked when the agent binary is called as:
//
//	nexus3-agent git-ssh [-p port] [user@]host command
//
// This is the implementation of core.sshCommand wired into the guest gitconfig
// by SeedGitIdentity. Git passes the same argv it would give to ssh(1):
//
//	nexus3-agent git-ssh [user@]host git-receive-pack '/owner/repo.git'
//	nexus3-agent git-ssh -p 22 user@host git-upload-pack '/owner/repo.git'
//
// Does not return — calls os.Exit with the remote exit code on all paths.
func runGitSSHShim(args []string) {
	conn, err := gitSSHShimDial()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus3-agent git-ssh: dial host relay (vsock CID 2 port %d): %v\n",
			driver.GitSSHRelayPort, err)
		os.Exit(1)
	}
	code, err := execGitSSHShim(args, conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus3-agent git-ssh: %v\n", err)
		os.Exit(1)
	}
	os.Exit(int(code))
}

// execGitSSHShim implements the shim protocol over an already-connected conn.
// Returns the remote SSH process exit code. conn is always closed before
// returning.
//
// This function is the testable core: tests inject a net.Pipe connection and
// verify the request frame, stdin bridging, and exit-code propagation.
func execGitSSHShim(args []string, conn net.Conn) (int32, error) {
	defer conn.Close()

	cwd, _ := os.Getwd()
	req := gitssh.Request{
		Argv:        args,
		Cwd:         cwd,
		GitProtocol: os.Getenv("GIT_PROTOCOL"),
	}

	if err := gitssh.WriteRequest(conn, req); err != nil {
		return 1, fmt.Errorf("send request: %w", err)
	}

	var (
		exitCode int32
		once     sync.Once
		doneCh   = make(chan struct{})
	)

	// Frame reader: receives stdout data and the exit frame from the host relay.
	go func() {
		for {
			ft, payload, err := gitssh.ReadFrame(conn)
			if err != nil {
				if !isShimEOF(err) {
					fmt.Fprintf(os.Stderr, "nexus3-agent git-ssh: read frame: %v\n", err)
				}
				once.Do(func() { close(doneCh) })
				return
			}
			switch ft {
			case gitssh.FrameTypeStdout:
				if _, werr := os.Stdout.Write(payload); werr != nil {
					once.Do(func() { close(doneCh) })
					return
				}
			case gitssh.FrameTypeExit:
				if len(payload) == 4 {
					exitCode = int32(binary.BigEndian.Uint32(payload))
				}
				once.Do(func() { close(doneCh) })
				return
			default:
				fmt.Fprintf(os.Stderr, "nexus3-agent git-ssh: unknown frame type 0x%02x; ignoring\n", ft)
			}
		}
	}()

	// Stdin pump: stream os.Stdin to the connection.
	// Runs concurrently; we don't wait for it — when doneCh is closed
	// (FrameTypeExit received) we return regardless of pump state.
	go func() { _, _ = io.Copy(conn, os.Stdin) }()

	<-doneCh
	return exitCode, nil
}

// isShimEOF reports whether err is a normal connection-close signal.
func isShimEOF(err error) bool {
	if err == io.EOF {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}
