package limafirecracker

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

const defaultLimaInstance = "nexus"

type Driver struct {
	inner runtime.Driver
}

func NewDriver(inner runtime.Driver) *Driver {
	return &Driver{inner: inner}
}

func (d *Driver) Backend() string { return "firecracker" }

func (d *Driver) Create(ctx context.Context, req runtime.CreateRequest) error {
	if d.inner == nil {
		return fmt.Errorf("inner driver is required")
	}
	if req.Options == nil {
		req.Options = map[string]string{}
	}
	if strings.TrimSpace(req.Options["lima.instance"]) == "" {
		req.Options["lima.instance"] = defaultLimaInstance
	}
	return d.inner.Create(ctx, req)
}

func (d *Driver) Start(ctx context.Context, workspaceID string) error {
	if d.inner == nil {
		return fmt.Errorf("inner driver is required")
	}
	return d.inner.Start(ctx, workspaceID)
}

func (d *Driver) Stop(ctx context.Context, workspaceID string) error {
	if d.inner == nil {
		return fmt.Errorf("inner driver is required")
	}
	return d.inner.Stop(ctx, workspaceID)
}

func (d *Driver) Restore(ctx context.Context, workspaceID string) error {
	if d.inner == nil {
		return fmt.Errorf("inner driver is required")
	}
	return d.inner.Restore(ctx, workspaceID)
}

func (d *Driver) Pause(ctx context.Context, workspaceID string) error {
	if d.inner == nil {
		return fmt.Errorf("inner driver is required")
	}
	return d.inner.Pause(ctx, workspaceID)
}

func (d *Driver) Resume(ctx context.Context, workspaceID string) error {
	if d.inner == nil {
		return fmt.Errorf("inner driver is required")
	}
	return d.inner.Resume(ctx, workspaceID)
}

func (d *Driver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	if d.inner == nil {
		return fmt.Errorf("inner driver is required")
	}
	return d.inner.Fork(ctx, workspaceID, childWorkspaceID)
}

func (d *Driver) Destroy(ctx context.Context, workspaceID string) error {
	if d.inner == nil {
		return fmt.Errorf("inner driver is required")
	}
	return d.inner.Destroy(ctx, workspaceID)
}

func (d *Driver) AgentConn(ctx context.Context, workspaceID string) (net.Conn, error) {
	if d.inner == nil {
		return nil, fmt.Errorf("inner driver is required")
	}
	if connector, ok := d.inner.(interface {
		AgentConn(context.Context, string) (net.Conn, error)
	}); ok {
		return connector.AgentConn(ctx, workspaceID)
	}
	return nil, fmt.Errorf("firecracker runtime does not support agent connection")
}
