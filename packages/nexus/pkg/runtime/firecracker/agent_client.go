package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
)

// DefaultAgentVSockPort is the guest-agent vsock port used for host<->guest exec.
const DefaultAgentVSockPort uint32 = 10789

// ExecRequest represents a command execution request to the agent
type ExecRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	WorkDir string   `json:"workdir,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// ExecResult represents the result of a command execution
type ExecResult struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// AgentClient is a client for communicating with the guest agent over vsock
type AgentClient struct {
	conn net.Conn
	mu   sync.Mutex
}

// NewAgentClient creates a new agent client with the given connection
func NewAgentClient(conn net.Conn) *AgentClient {
	return &AgentClient{conn: conn}
}

// Exec executes a command on the guest and returns the result
func (c *AgentClient) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if c.conn == nil {
		return ExecResult{}, errors.New("agent client: nil connection")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Send request
	done := make(chan error, 1)
	go func() {
		done <- json.NewEncoder(c.conn).Encode(req)
	}()

	select {
	case <-ctx.Done():
		return ExecResult{}, ctx.Err()
	case err := <-done:
		if err != nil {
			return ExecResult{}, err
		}
	}

	// Receive response
	var resp ExecResult
	respDone := make(chan error, 1)
	go func() {
		respDone <- json.NewDecoder(c.conn).Decode(&resp)
	}()

	select {
	case <-ctx.Done():
		return ExecResult{}, ctx.Err()
	case err := <-respDone:
		if err != nil {
			return ExecResult{}, err
		}
	}

	return resp, nil
}
