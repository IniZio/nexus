package cloudhypervisor

import (
	"context"

	"github.com/IniZio/nexus/internal/core/domain"
)

// StopAfterHibernate implements driver.Hibernator: kill the VMM once the
// hibernated record is committed.
func (d *CHDriver) StopAfterHibernate(ctx context.Context, id domain.SandboxID) error {
	return d.StopVMM(ctx, id)
}
