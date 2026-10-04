package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mdlayher/vsock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
)

func peerCtx(addr net.Addr) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: addr})
}

func stubClock(t *testing.T, set func(time.Time) error, now time.Time) {
	t.Helper()
	oSet, oNow := setClockFn, nowFn
	setClockFn, nowFn = set, func() time.Time { return now }
	t.Cleanup(func() { setClockFn, nowFn = oSet, oNow })
}

func TestSetClock_HostPeerSetsClock(t *testing.T) {
	want := time.Unix(2_000_000_000, 5)
	var got time.Time
	stubClock(t, func(x time.Time) error { got = x; return nil }, time.Unix(1_000_000_000, 0))
	cs := newControlServer(&Agent{})
	resp, err := cs.SetClock(peerCtx(&vsock.Addr{ContextID: vsockCIDHost}), &agentpb.SetClockRequest{UnixNanos: want.UnixNano()})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("set %v want %v", got, want)
	}
	if resp.PreviousUnixNanos != time.Unix(1_000_000_000, 0).UnixNano() {
		t.Errorf("previous = %d", resp.PreviousUnixNanos)
	}
}

func TestSetClock_RejectsNonHost(t *testing.T) {
	called := false
	stubClock(t, func(time.Time) error { called = true; return nil }, time.Now())
	cs := newControlServer(&Agent{})
	for name, ctx := range map[string]context.Context{
		"guest cid": peerCtx(&vsock.Addr{ContextID: 3}),
		"tcp":       peerCtx(&net.TCPAddr{}),
		"no peer":   context.Background(),
	} {
		_, err := cs.SetClock(ctx, &agentpb.SetClockRequest{UnixNanos: 1})
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s: code = %v", name, status.Code(err))
		}
	}
	if called {
		t.Error("setter called for unauthorized caller")
	}
}

func TestSetClock_SetterErrorAndBadInput(t *testing.T) {
	stubClock(t, func(time.Time) error { return errors.New("EPERM") }, time.Now())
	cs := newControlServer(&Agent{})
	ctx := peerCtx(&vsock.Addr{ContextID: vsockCIDHost})
	if _, err := cs.SetClock(ctx, &agentpb.SetClockRequest{UnixNanos: 1}); status.Code(err) != codes.Internal {
		t.Errorf("setter err code = %v", status.Code(err))
	}
	if _, err := cs.SetClock(ctx, &agentpb.SetClockRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("zero code = %v", status.Code(err))
	}
}
