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
	"strings"
	"time"
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
	// Listen on TCP for testing purposes
	// In production, this would use vsock
	port := os.Getenv("AGENT_PORT")
	if port == "" {
		port = "8080"
	}

	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	log.Printf("Firecracker agent listening on port %s", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		go serveConn(conn)
	}
}
