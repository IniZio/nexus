package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Request types
type execRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	WorkDir string   `json:"work_dir,omitempty"`
	Env     []string `json:"env,omitempty"`
}

type execResponse struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func handleExec(req execRequest) execResponse {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	}

	// Capture both stdout and stderr separately
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	exitCode := 0

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return execResponse{
		ID:       req.ID,
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
	}
}

func serveConn(conn net.Conn) {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		// Parse request
		var req execRequest
		if err := decoder.Decode(&req); err != nil {
			if err != io.EOF {
				log.Printf("Error decoding request: %v", err)
				// Try to send error response with request ID if available
				encoder.Encode(execResponse{ID: req.ID, ExitCode: 1, Stderr: fmt.Sprintf("decode error: %v", err)})
			}
			return
		}

		// Validate request ID is present
		if strings.TrimSpace(req.ID) == "" {
			log.Printf("Request missing ID field")
			encoder.Encode(execResponse{ExitCode: 1, Stderr: "request ID is required"})
			continue
		}

		// Handle request
		resp := handleExec(req)

		// Send response
		if err := encoder.Encode(resp); err != nil {
			log.Printf("Error encoding response: %v", err)
			return
		}
	}
}

func main() {
	if os.Getpid() == 1 {
		mountKernelFilesystems()
	}

	listener, transport, err := resolveListener()
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	log.Printf("Firecracker agent listening (%s)", transport)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		go serveConn(conn)
	}
}

func mountKernelFilesystems() {
	_ = os.MkdirAll("/proc", 0o755)
	_ = os.MkdirAll("/sys", 0o755)
	_ = os.MkdirAll("/dev", 0o755)
	_ = unix.Mount("proc", "/proc", "proc", 0, "")
	_ = unix.Mount("sysfs", "/sys", "sysfs", 0, "")
	_ = unix.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
}

func resolveListener() (net.Listener, string, error) {
	if os.Getpid() == 1 || os.Getenv("AGENT_REQUIRE_VSOCK") == "1" {
		var lastErr error
		for attempt := 1; attempt <= 120; attempt++ {
			listener, err := listenVsock()
			if err == nil {
				return listener, "vsock", nil
			}
			lastErr = err
			time.Sleep(500 * time.Millisecond)
		}
		return nil, "", fmt.Errorf("listen vsock (required) failed: %w", lastErr)
	}

	if os.Getenv("AGENT_FORCE_TCP") == "1" {
		listener, err := listenTCP()
		return listener, "tcp", err
	}

	listener, err := listenVsock()
	if err == nil {
		return listener, "vsock", nil
	}

	tcpListener, tcpErr := listenTCP()
	if tcpErr != nil {
		return nil, "", fmt.Errorf("listen vsock: %w; listen tcp fallback: %v", err, tcpErr)
	}
	return tcpListener, "tcp-fallback", nil
}

func listenTCP() (net.Listener, error) {
	port := os.Getenv("AGENT_PORT")
	if port == "" {
		port = "8080"
	}
	return net.Listen("tcp", ":"+port)
}

func listenVsock() (net.Listener, error) {
	port := uint32(1024)
	if raw := strings.TrimSpace(os.Getenv("AGENT_VSOCK_PORT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid AGENT_VSOCK_PORT %q", raw)
		}
		port = uint32(parsed)
	}

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	if err := unix.Listen(fd, 128); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	file := os.NewFile(uintptr(fd), "vsock-listener")
	defer file.Close()

	listener, err := net.FileListener(file)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return listener, nil
}
