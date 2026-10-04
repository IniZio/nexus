package sprites

import (
	"context"

	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
)

// BrokerExec adapts the driver's Sprites API to the broker's ExecFunc.
func (d *Driver) BrokerExec() broker.ExecFunc {
	return func(ctx context.Context, sprite string, req broker.ExecRequest) (int32, error) {
		return d.api.Exec(ctx, sprite, ExecRequest{
			Argv: req.Argv, Env: req.Env, Stdin: req.Stdin, Stdout: req.Stdout, Stderr: req.Stderr,
		})
	}
}
