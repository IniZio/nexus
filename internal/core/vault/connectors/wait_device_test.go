package connectors

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func nopSleep(_ time.Duration) {}

type seqConnector struct {
	*vaulttest.FakeConnector
	results []seqResult
	idx     int
}

type seqResult struct {
	rec vault.Record
	err error
}

func (c *seqConnector) PollDevice(ctx context.Context, _ string) (vault.Record, error) {
	if c.idx >= len(c.results) {
		return c.FakeConnector.PollDevice(ctx, "")
	}
	r := c.results[c.idx]
	c.idx++
	return r.rec, r.err
}

func okRecord() vault.Record {
	return vault.Record{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}
}

func TestWaitDevice_PendingThenSuccess(t *testing.T) {
	da := vault.DeviceAuth{DeviceCode: "code", Interval: 0}
	conn := &seqConnector{
		FakeConnector: vaulttest.NewFakeConnector("gh"),
		results: []seqResult{
			{err: ErrAuthorizationPending},
			{err: ErrAuthorizationPending},
			{rec: okRecord()},
		},
	}
	rec, err := WaitDevice(context.Background(), conn, da, nopSleep)
	if err != nil {
		t.Fatalf("WaitDevice: %v", err)
	}
	if rec.AccessToken != "tok" {
		t.Fatalf("AccessToken = %q, want %q", rec.AccessToken, "tok")
	}
}

func TestWaitDevice_SlowDownIncreasesInterval(t *testing.T) {
	da := vault.DeviceAuth{DeviceCode: "code", Interval: 0}
	var intervals []time.Duration
	capSleep := func(d time.Duration) { intervals = append(intervals, d) }
	conn := &seqConnector{
		FakeConnector: vaulttest.NewFakeConnector("gh"),
		results: []seqResult{
			{err: ErrSlowDown},
			{rec: okRecord()},
		},
	}
	if _, err := WaitDevice(context.Background(), conn, da, capSleep); err != nil {
		t.Fatalf("WaitDevice: %v", err)
	}
	if len(intervals) != 2 {
		t.Fatalf("expected 2 sleep calls, got %d", len(intervals))
	}
	if intervals[1] <= intervals[0] {
		t.Fatalf("interval after SlowDown not increased: [0]=%v [1]=%v", intervals[0], intervals[1])
	}
}

func TestWaitDevice_NonPendingErrorReturns(t *testing.T) {
	da := vault.DeviceAuth{DeviceCode: "code", Interval: 0}
	want := errors.New("access_denied")
	conn := &seqConnector{
		FakeConnector: vaulttest.NewFakeConnector("gh"),
		results:       []seqResult{{err: want}},
	}
	_, err := WaitDevice(context.Background(), conn, da, nopSleep)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestWaitDevice_CtxCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	da := vault.DeviceAuth{DeviceCode: "code", Interval: 0}
	conn := vaulttest.NewFakeConnector("gh")
	_, err := WaitDevice(ctx, conn, da, nopSleep)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
