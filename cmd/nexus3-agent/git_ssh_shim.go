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

const hostCID uint32 = 2 // vsock CID for host (from guest)

var gitSSHShimDial = func() (net.Conn, error) { // replaced in tests
	return vsock.Dial(hostCID, driver.GitSSHRelayPort, nil)
}

// runGitSSHShim implements GIT_SSH_COMMAND (wired by SeedGitIdentity). Does not return.
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

// execGitSSHShim runs the shim protocol over conn and returns the remote exit code.
// Testable core: tests inject a net.Pipe connection.
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

	go func() { // frame reader: stdout data and exit frame from host relay
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

	// We don't wait for the stdin pump — return as soon as FrameTypeExit arrives.
	go func() { _, _ = io.Copy(conn, os.Stdin) }()

	<-doneCh
	return exitCode, nil
}

func isShimEOF(err error) bool {
	if err == io.EOF {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}
