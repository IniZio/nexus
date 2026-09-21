package driver

import (
	"context"

	pb "github.com/IniZio/nexus/internal/openshell/computedriverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type fakeWatchStream struct {
	ctx    context.Context
	events chan *pb.WatchSandboxesEvent
}

func newFakeWatchStream(ctx context.Context) *fakeWatchStream {
	return &fakeWatchStream{ctx: ctx, events: make(chan *pb.WatchSandboxesEvent, 128)}
}

func (f *fakeWatchStream) Send(ev *pb.WatchSandboxesEvent) error {
	select {
	case f.events <- ev:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

func (f *fakeWatchStream) Context() context.Context     { return f.ctx }
func (f *fakeWatchStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeWatchStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeWatchStream) SetTrailer(metadata.MD)       {}
func (f *fakeWatchStream) SendMsg(m any) error          { return nil }
func (f *fakeWatchStream) RecvMsg(m any) error          { return nil }

var _ grpc.ServerStreamingServer[pb.WatchSandboxesEvent] = (*fakeWatchStream)(nil)
