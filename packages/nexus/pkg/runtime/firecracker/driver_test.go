package firecracker

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

type fakeRunner struct {
	calls []call
	err   error
}

type call struct {
	dir  string
	cmd  string
	args []string
}

func (f *fakeRunner) Run(ctx context.Context, dir string, cmd string, args ...string) error {
	f.calls = append(f.calls, call{dir: dir, cmd: cmd, args: append([]string(nil), args...)})
	return f.err
}

func TestFirecrackerDriver_Backend(t *testing.T) {
	d := NewDriver(&fakeRunner{})
	if d.Backend() != "firecracker" {
		t.Fatalf("expected backend firecracker, got %q", d.Backend())
	}
}

func TestFirecrackerDriver_CreateRequiresProjectRoot(t *testing.T) {
	d := NewDriver(&fakeRunner{})
	err := d.Create(context.Background(), runtime.CreateRequest{WorkspaceID: "ws-1"})
	if err == nil {
		t.Fatal("expected error when project root is empty")
	}
}

func TestFirecrackerDriver_CreateStartPauseResumeForkDestroy(t *testing.T) {
	r := &fakeRunner{}
	d := NewDriver(r)

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if err := d.Start(context.Background(), "ws-1"); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := d.Pause(context.Background(), "ws-1"); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	if err := d.Resume(context.Background(), "ws-1"); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if err := d.Fork(context.Background(), "ws-1", "ws-2"); err != nil {
		t.Fatalf("fork failed: %v", err)
	}
	if err := d.Destroy(context.Background(), "ws-1"); err != nil {
		t.Fatalf("destroy failed: %v", err)
	}

	if len(r.calls) != 6 {
		t.Fatalf("expected 6 calls, got %d", len(r.calls))
	}

	assertCall := func(i int, dir string, cmd string, args []string) {
		t.Helper()
		c := r.calls[i]
		if c.dir != dir || c.cmd != cmd || !reflect.DeepEqual(c.args, args) {
			t.Fatalf("call %d mismatch: got dir=%q cmd=%q args=%v; want dir=%q cmd=%q args=%v", i, c.dir, c.cmd, c.args, dir, cmd, args)
		}
	}

	assertCall(0, "/projects/ws-1", "vmctl-firecracker", []string{"create", "--id", "ws-1", "--balloon", "off"})
	assertCall(1, "/projects/ws-1", "vmctl-firecracker", []string{"start", "--id", "ws-1"})
	assertCall(2, "/projects/ws-1", "vmctl-firecracker", []string{"pause", "--id", "ws-1"})
	assertCall(3, "/projects/ws-1", "vmctl-firecracker", []string{"resume", "--id", "ws-1"})
	assertCall(4, "/projects/ws-1", "vmctl-firecracker", []string{"fork", "--id", "ws-1", "--child-id", "ws-2"})
	assertCall(5, "/projects/ws-1", "vmctl-firecracker", []string{"destroy", "--id", "ws-1"})
}

func TestFirecrackerDriver_CreateWrapsCommandOnDarwin(t *testing.T) {
	r := &fakeRunner{}
	d := NewDriver(r, WithHostOS("darwin"), WithLimaInstance("nexus-firecracker"))

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}

	c := r.calls[0]
	if c.cmd != "limactl" {
		t.Fatalf("expected limactl, got %q", c.cmd)
	}
	want := []string{"shell", "nexus-firecracker", "vmctl-firecracker", "create", "--id", "ws-1", "--balloon", "off"}
	if !reflect.DeepEqual(c.args, want) {
		t.Fatalf("unexpected args: got %v want %v", c.args, want)
	}
}

func TestFirecrackerDriver_CreateAddsMemMiBWhenProvided(t *testing.T) {
	r := &fakeRunner{}
	d := NewDriver(r)

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
		Options:     map[string]string{"mem_mib": "1024"},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	want := []string{"create", "--id", "ws-1", "--mem-mib", "1024", "--balloon", "off"}
	if !reflect.DeepEqual(r.calls[0].args, want) {
		t.Fatalf("unexpected args: got %v want %v", r.calls[0].args, want)
	}
}

func TestFirecrackerDriver_ErrorsPropagate(t *testing.T) {
	r := &fakeRunner{err: errors.New("boom")}
	d := NewDriver(r)
	_ = d.Create(context.Background(), runtime.CreateRequest{WorkspaceID: "ws-1", ProjectRoot: "/projects/ws-1"})
	err := d.Start(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected error from runner")
	}
}

func TestFirecrackerDriver_DestroyUnknownWorkspaceNoOp(t *testing.T) {
	r := &fakeRunner{}
	d := NewDriver(r)
	if err := d.Destroy(context.Background(), "missing"); err != nil {
		t.Fatalf("destroy unknown should be no-op, got %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("expected no runner calls, got %d", len(r.calls))
	}
}
