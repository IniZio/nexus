package connectors

import (
	"context"
	"errors"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
)

// WaitDevice polls c.PollDevice in a loop until authorized, ctx is done, or a
// non-retriable error occurs. sleep is called between attempts; pass time.Sleep
// for production or a no-op for tests.
func WaitDevice(ctx context.Context, c vault.Connector, da vault.DeviceAuth, sleep func(time.Duration)) (vault.Record, error) {
	interval := time.Duration(da.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return vault.Record{}, err
		}
		sleep(interval)
		if err := ctx.Err(); err != nil {
			return vault.Record{}, err
		}
		rec, err := c.PollDevice(ctx, da.DeviceCode)
		if errors.Is(err, ErrAuthorizationPending) {
			continue
		}
		if errors.Is(err, ErrSlowDown) {
			interval += 5 * time.Second
			continue
		}
		return rec, err
	}
}
