package sprites

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/superfly/sprites-go"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/driver"
)

// localExecAPI runs copy commands on the host; the temp dir stands in for the sprite.
type localExecAPI struct {
	reqs []ExecRequest
	code int32
	msg  string
}

func (f *localExecAPI) CreateSprite(context.Context, string) error         { return nil }
func (f *localExecAPI) SpriteExists(context.Context, string) (bool, error) { return true, nil }
func (f *localExecAPI) DeleteSprite(context.Context, string) error         { return nil }
func (f *localExecAPI) SetNetworkPolicy(context.Context, string, *sdk.NetworkPolicy) error {
	return nil
}

func (f *localExecAPI) Exec(ctx context.Context, _ string, req ExecRequest) (int32, error) {
	f.reqs = append(f.reqs, req)
	if f.code != 0 {
		req.Stderr.Write([]byte(f.msg))
		return f.code, nil
	}
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = req.Stdin, req.Stdout, req.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return int32(ee.ExitCode()), nil
		}
		return 0, err
	}
	return 0, nil
}

func TestCopyViaExecFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deep", "f.bin")
	api := &localExecAPI{}
	n := int64(5)
	err := copyViaExec(context.Background(), api, "s", driver.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH, GuestPath: p,
		Src: strings.NewReader("hello"), ExpectedBytes: &n,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = copyViaExec(context.Background(), api, "s", driver.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PULL, GuestPath: p, Dst: &out,
	})
	if err != nil || out.String() != "hello" {
		t.Fatalf("pull = %q, %v", out.String(), err)
	}
	bad := int64(9)
	err = copyViaExec(context.Background(), api, "s", driver.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH, GuestPath: p,
		Src: strings.NewReader("hello"), ExpectedBytes: &bad,
	})
	if err == nil {
		t.Fatal("size mismatch accepted")
	}
}

func TestCopyViaExecDirRoundTrip(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range map[string]string{"a.txt": "A", "sub/b.txt": "B"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gd := filepath.Join(dir, "d")
	api := &localExecAPI{}
	err := copyViaExec(context.Background(), api, "s", driver.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH, GuestPath: gd, IsDirectory: true, Src: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(gd, "sub", "b.txt")); string(b) != "B" {
		t.Fatalf("pushed b.txt = %q", b)
	}
	var out bytes.Buffer
	err = copyViaExec(context.Background(), api, "s", driver.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PULL, GuestPath: gd, IsDirectory: true, Dst: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	tr := tar.NewReader(&out)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag == tar.TypeReg {
			var b bytes.Buffer
			b.ReadFrom(tr)
			got[filepath.Clean(h.Name)] = b.String()
		}
	}
	if got["a.txt"] != "A" || got["sub/b.txt"] != "B" {
		t.Fatalf("pulled entries = %v", got)
	}
}

func TestCopyViaExecNonZeroExit(t *testing.T) {
	api := &localExecAPI{code: 2, msg: strings.Repeat("x", 5000) + "boom"}
	err := copyViaExec(context.Background(), api, "s", driver.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PULL, GuestPath: "/nope", Dst: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "exit 2") || !strings.HasSuffix(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if len(err.Error()) > copyStderrCap+100 {
		t.Fatalf("stderr not capped: %d", len(err.Error()))
	}
}
