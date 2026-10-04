package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
)

// SetClock sets the guest CLOCK_REALTIME to unixNanos and returns the guest
// wall clock (unix nanos) as it was immediately before the set.
func (c *Client) SetClock(ctx context.Context, unixNanos int64) (int64, error) {
	stub, cc, err := c.controlClient(ctx)
	if err != nil {
		return 0, fmt.Errorf("agent: set clock: dial: %w", err)
	}
	defer cc.Close()
	resp, err := stub.SetClock(ctx, &agentpb.SetClockRequest{UnixNanos: unixNanos})
	if err != nil {
		return 0, fmt.Errorf("agent: set clock: %w", err)
	}
	return resp.GetPreviousUnixNanos(), nil
}

// SetGuestClock pushes now to the guest and returns the measured skew in ms:
// guest time before the set minus host time. Negative means the guest lagged.
func SetGuestClock(ctx context.Context, c *Client, now time.Time) (int64, error) {
	prev, err := c.SetClock(ctx, now.UnixNano())
	if err != nil {
		return 0, err
	}
	return (prev - now.UnixNano()) / int64(time.Millisecond), nil
}
