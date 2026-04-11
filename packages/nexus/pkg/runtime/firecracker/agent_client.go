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
	Type    string   `json:"type,omitempty"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	WorkDir string   `json:"workdir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Stream  bool     `json:"stream,omitempty"`
}

// ExecResult represents the result of a command execution
type ExecResult struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type execEnvelope struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Stream   string `json:"stream,omitempty"`
	Data     string `json:"data,omitempty"`
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
	return c.ExecStreaming(ctx, req, nil)
}

// ExecStreaming executes a command on the guest and streams output chunks as
// they are emitted when the agent supports streaming responses.
func (c *AgentClient) ExecStreaming(ctx context.Context, req ExecRequest, onChunk func(stream, data string)) (ExecResult, error) {
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
	stdout := ""
	stderr := ""
	respDone := make(chan error, 1)
	respOut := make(chan ExecResult, 1)
	go func() {
		dec := json.NewDecoder(c.conn)
		for {
			var env execEnvelope
			if err := dec.Decode(&env); err != nil {
				respDone <- err
				return
			}

			if env.Type == "chunk" {
				if env.Stream == "stderr" {
					stderr += env.Data
				} else {
					stdout += env.Data
				}
				if onChunk != nil {
					onChunk(env.Stream, env.Data)
				}
				continue
			}

			if env.Type == "result" {
				if env.Stdout != "" {
					stdout = env.Stdout
				}
				if env.Stderr != "" {
					stderr = env.Stderr
				}
				respOut <- ExecResult{
					ID:       env.ID,
					ExitCode: env.ExitCode,
					Stdout:   stdout,
					Stderr:   stderr,
				}
				respDone <- nil
				return
			}

			// Legacy response format (single result object)
			respOut <- ExecResult{
				ID:       env.ID,
				ExitCode: env.ExitCode,
				Stdout:   env.Stdout,
				Stderr:   env.Stderr,
			}
			respDone <- nil
			return
		}
	}()

	select {
	case <-ctx.Done():
		return ExecResult{}, ctx.Err()
	case err := <-respDone:
		if err != nil {
			return ExecResult{}, err
		}
	}

	return <-respOut, nil
}
