package cloudhypervisor

import (
	"context"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

var (
	_ driver.Driver          = (*CHDriver)(nil)
	_ driver.SessionAttacher = (*CHDriver)(nil)
)

// Exec runs a process in the guest through the vsock guest agent.
func (d *CHDriver) Exec(ctx context.Context, id domain.SandboxID, opts driver.ExecOptions) (int32, error) {
	return agent.NewClient(d, id).Exec(ctx, opts)
}

// Copy transfers a tar stream to or from the guest through the vsock guest agent.
func (d *CHDriver) Copy(ctx context.Context, id domain.SandboxID, opts driver.CopyOptions) error {
	return agent.NewClient(d, id).Copy(ctx, opts)
}

// Attach reattaches to a running guest session.
func (d *CHDriver) Attach(ctx context.Context, id domain.SandboxID, opts driver.AttachOptions) (int32, error) {
	return agent.NewClient(d, id).Attach(ctx, opts)
}

// Capabilities reports the optional interfaces CHDriver implements.
func (d *CHDriver) Capabilities() driver.CapabilitySet {
	c := driver.OptionalInterfaces(d)
	c.GuestOS = driver.GuestOSLinux
	c.Egress = driver.EgressEnforced
	c.Isolation = driver.IsolationWorktree
	return c
}
