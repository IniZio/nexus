package provider

import (
	"context"
	"io"
)

type Session struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Status     string            `json:"status"`
	SSHPort    int               `json:"ssh_port"`
	BridgePort int               `json:"bridge_port"`
	Services   map[string]int    `json:"services"`
	Labels     map[string]string `json:"labels"`
}

type ExecOptions struct {
	Cmd          []string
	Env          []string
	Stdout       bool
	Stderr       bool
	StdoutWriter io.Writer
	StderrWriter io.Writer
}

type Provider interface {
	Name() string
	Create(ctx context.Context, sessionID string, workspacePath string, config interface{}) (*Session, error)
	Start(ctx context.Context, sessionID string) error
	Stop(ctx context.Context, sessionID string) error
	Destroy(ctx context.Context, sessionID string) error
	Exec(ctx context.Context, sessionID string, opts ExecOptions) error
	List(ctx context.Context) ([]Session, error)
}
