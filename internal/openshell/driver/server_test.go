package driver

import (
	"context"
	"testing"
	"time"

	pb "github.com/IniZio/nexus/internal/openshell/computedriverpb"
)

func newTestServer() *Server {
	return New(NewMemStore(), nil, "http://127.0.0.1:18800/", "", 0)
}

func TestServer_GetCapabilities(t *testing.T) {
	srv := newTestServer()
	resp, err := srv.GetCapabilities(context.Background(), &pb.GetCapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.DriverName != "nexus-ch" {
		t.Errorf("driver_name: %q", resp.DriverName)
	}
	if resp.SupportsSandboxAuthentication {
		t.Error("supports_sandbox_authentication must be false")
	}
}

func TestServer_AuthenticateSandbox_Unimplemented(t *testing.T) {
	srv := newTestServer()
	_, err := srv.AuthenticateSandbox(context.Background(), &pb.AuthenticateSandboxRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestServer_CreateGetListDelete(t *testing.T) {
	srv := newTestServer()
	ctx := context.Background()

	createResp, err := srv.CreateSandbox(ctx, &pb.CreateSandboxRequest{
		Sandbox: &pb.DriverSandbox{
			Id:   "s1",
			Name: "box1",
			Spec: &pb.DriverSandboxSpec{
				Template: &pb.DriverSandboxTemplate{
					Image: "myimage:test",
				},
			},
		},
	})
	if err != nil || createResp == nil {
		t.Fatalf("CreateSandbox: %v", err)
	}

	getResp, err := srv.GetSandbox(ctx, &pb.GetSandboxRequest{SandboxId: "s1"})
	if err != nil || getResp.Sandbox.Name != "box1" {
		t.Fatalf("GetSandbox: %v %+v", err, getResp)
	}

	getByName, err := srv.GetSandbox(ctx, &pb.GetSandboxRequest{Name: "box1"})
	if err != nil || getByName.Sandbox.Id != "s1" {
		t.Fatalf("GetSandbox by name: %v %+v", err, getByName)
	}

	list, err := srv.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if err != nil || len(list.Sandboxes) != 1 {
		t.Fatalf("ListSandboxes: %v len=%d", err, len(list.Sandboxes))
	}

	del, err := srv.DeleteSandbox(ctx, &pb.DeleteSandboxRequest{SandboxId: "s1"})
	if err != nil || !del.Deleted {
		t.Fatalf("DeleteSandbox: %v %+v", err, del)
	}

	list2, _ := srv.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if len(list2.Sandboxes) != 0 {
		t.Fatalf("list after delete: want 0, got %d", len(list2.Sandboxes))
	}
}

func TestServer_StopStart(t *testing.T) {
	srv := newTestServer()
	ctx := context.Background()

	_, _ = srv.CreateSandbox(ctx, &pb.CreateSandboxRequest{
		Sandbox: &pb.DriverSandbox{Id: "s2", Name: "box2"},
	})

	_, err := srv.StopSandbox(ctx, &pb.StopSandboxRequest{SandboxId: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := srv.store.Get("s2")
	if !ok || r.Phase != PhaseStopped {
		t.Fatalf("phase after stop: %v", r.Phase)
	}

	_, err = srv.StartSandbox(ctx, &pb.StartSandboxRequest{SandboxId: "s2"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServer_WatchSandboxes_InitialSnapshot(t *testing.T) {
	srv := newTestServer()
	ctx := context.Background()

	_ = srv.store.Put(SandboxRecord{ID: "pre1", Name: "existing", Phase: PhaseReady})

	ctx1, cancel1 := context.WithCancel(ctx)
	stream1 := newFakeWatchStream(ctx1)

	done := make(chan error, 1)
	go func() { done <- srv.WatchSandboxes(&pb.WatchSandboxesRequest{}, stream1) }()

	select {
	case ev := <-stream1.events:
		if ev.GetSandbox() == nil || ev.GetSandbox().Sandbox.Id != "pre1" {
			t.Errorf("initial snapshot wrong: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial snapshot")
	}
	cancel1()
	<-done
}

func TestServer_WatchSandboxes_TwoWatchers(t *testing.T) {
	srv := newTestServer()
	ctx := context.Background()

	ctx1, cancel1 := context.WithCancel(ctx)
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel1()
	defer cancel2()

	stream1 := newFakeWatchStream(ctx1)
	stream2 := newFakeWatchStream(ctx2)

	go func() { _ = srv.WatchSandboxes(&pb.WatchSandboxesRequest{}, stream1) }()
	go func() { _ = srv.WatchSandboxes(&pb.WatchSandboxesRequest{}, stream2) }()

	time.Sleep(20 * time.Millisecond)

	srv.broadcast(sandboxEvent(SandboxRecord{ID: "evt1", Name: "broadcast-test", Phase: PhaseProvisioning}))

	recv := func(name string, ch chan *pb.WatchSandboxesEvent) {
		t.Helper()
		select {
		case ev := <-ch:
			if ev.GetSandbox() == nil || ev.GetSandbox().Sandbox.Id != "evt1" {
				t.Errorf("%s: unexpected event %+v", name, ev)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: timeout", name)
		}
	}
	recv("watcher1", stream1.events)
	recv("watcher2", stream2.events)
}

func TestServer_WatchSandboxes_DeletedEvent(t *testing.T) {
	srv := newTestServer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newFakeWatchStream(ctx)
	go func() { _ = srv.WatchSandboxes(&pb.WatchSandboxesRequest{}, stream) }()
	time.Sleep(20 * time.Millisecond)

	srv.broadcast(deletedEvent("gone1"))

	select {
	case ev := <-stream.events:
		if ev.GetDeleted() == nil || ev.GetDeleted().SandboxId != "gone1" {
			t.Errorf("expected deleted event, got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestServer_LaunchAuthAndWorkloadIdentity_Preserved(t *testing.T) {
	srv := newTestServer()
	ctx := context.Background()

	_, err := srv.CreateSandbox(ctx, &pb.CreateSandboxRequest{
		Sandbox: &pb.DriverSandbox{
			Id:   "auth1",
			Name: "authbox",
			Spec: &pb.DriverSandboxSpec{
				LaunchAuthentication: []byte("opaque-auth-bytes"),
				WorkloadIdentity:     &pb.WorkloadIdentityRequest{User: "alice", Group: "eng"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	r, ok := srv.store.Get("auth1")
	if !ok {
		t.Fatal("not found")
	}
	if string(r.LaunchAuthentication) != "opaque-auth-bytes" {
		t.Errorf("launch_authentication: %q", r.LaunchAuthentication)
	}
	if r.WorkloadIdentity.User != "alice" || r.WorkloadIdentity.Group != "eng" {
		t.Errorf("workload_identity: %+v", r.WorkloadIdentity)
	}
}
