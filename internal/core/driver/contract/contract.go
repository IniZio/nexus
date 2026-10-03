// Package contract is the backend-agnostic provider contract suite. Each
// backend wires a Harness and calls Run from its own (usually live-gated) test.
package contract

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

// Harness binds a backend to the suite.
type Harness struct {
	Backend string // registry name, for messages
	Driver  driver.Driver
	// Create provisions a sandbox bound to a fresh scratch git worktree with
	// egress allowing exactly allowHosts. It returns the id, the host-side
	// root dir of the sandbox worktree, and a destroy func.
	Create func(t *testing.T, allowHosts []string) (id domain.SandboxID, root string, destroy func())
	// GuestRoot is the guest-visible path of root; empty means same as root.
	GuestRoot string
	// Net enables the network egress checks.
	Net bool
	// HostShared means guest and host share a filesystem/home, so escape
	// attempts can be verified from the host.
	HostShared bool
	// DestroyKeepsRoot asserts root still exists after destroy; otherwise
	// root must be gone.
	DestroyKeepsRoot bool
}

const opTimeout = 60 * time.Second

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), opTimeout)
	t.Cleanup(cancel)
	return c
}

type result struct {
	code        int32
	out, errOut string
	err         error
}

func run(t *testing.T, d driver.Driver, id domain.SandboxID, o driver.ExecOptions) result {
	t.Helper()
	var so, se bytes.Buffer
	o.Stdout, o.Stderr = &so, &se
	code, err := d.Exec(ctx(t), id, o)
	return result{code, so.String(), se.String(), err}
}

func sh(t *testing.T, d driver.Driver, id domain.SandboxID, cwd, script string) result {
	t.Helper()
	return run(t, d, id, driver.ExecOptions{Argv: []string{"sh", "-c", script}, Cwd: cwd})
}

func mustOK(t *testing.T, r result) string {
	t.Helper()
	if r.err != nil || r.code != 0 {
		t.Fatalf("exec: code=%d err=%v stderr=%q", r.code, r.err, r.errOut)
	}
	return r.out
}

// Run executes the contract against h.
func Run(t *testing.T, h Harness) {
	guest := h.GuestRoot
	id, root, destroy := h.Create(t, []string{"github.com"})
	if guest == "" {
		guest = root
	}
	destroyed := false
	t.Cleanup(func() {
		if !destroyed {
			destroy()
		}
	})
	d := h.Driver

	t.Run("Lifecycle", func(t *testing.T) {
		o, err := d.Observe(ctx(t), id)
		if err != nil || o.State == driver.Running {
			t.Fatalf("pre-start Observe: state=%v err=%v", o.State, err)
		}
		if _, err := d.Start(ctx(t), driver.StartRequest{SandboxID: id}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		o, err = d.Observe(ctx(t), id)
		if err != nil || o.State != driver.Running {
			t.Fatalf("post-start Observe: state=%v err=%v", o.State, err)
		}
		if got := strings.TrimSpace(mustOK(t, sh(t, d, id, guest, "echo up"))); got != "up" {
			t.Fatalf("exec while running: %q", got)
		}
		for i := 0; i < 2; i++ {
			if err := d.Stop(ctx(t), id); err != nil {
				t.Fatalf("Stop #%d (must be idempotent): %v", i+1, err)
			}
		}
		if o, err = d.Observe(ctx(t), id); err != nil || o.State == driver.Running {
			t.Fatalf("post-stop Observe: state=%v err=%v", o.State, err)
		}
		if _, err := d.Start(ctx(t), driver.StartRequest{SandboxID: id}); err != nil {
			t.Fatalf("restart: %v", err)
		}
	})

	t.Run("Exec", func(t *testing.T) {
		if got := mustOK(t, run(t, d, id, driver.ExecOptions{Argv: []string{"echo", "hello"}, Cwd: guest})); strings.TrimSpace(got) != "hello" {
			t.Errorf("stdout = %q", got)
		}
		if r := sh(t, d, id, guest, "exit 7"); r.err != nil || r.code != 7 {
			t.Errorf("exit 7: code=%d err=%v", r.code, r.err)
		}
		r := run(t, d, id, driver.ExecOptions{Argv: []string{"cat"}, Cwd: guest, Stdin: strings.NewReader("piped-in")})
		if mustOK(t, r) != "piped-in" {
			t.Errorf("stdin passthrough = %q", r.out)
		}
		r = run(t, d, id, driver.ExecOptions{Argv: []string{"sh", "-c", "printf %s \"$NX_CONTRACT\""}, Cwd: guest, Env: map[string]string{"NX_CONTRACT": "v1"}})
		if mustOK(t, r) != "v1" {
			t.Errorf("env passthrough = %q", r.out)
		}
		want, _ := filepath.EvalSymlinks(guest)
		r = run(t, d, id, driver.ExecOptions{Argv: []string{"pwd", "-P"}, Cwd: guest})
		if got := strings.TrimSpace(mustOK(t, r)); got != want && got != guest {
			t.Errorf("cwd = %q, want %q", got, guest)
		}
	})

	t.Run("Copy", func(t *testing.T) {
		sz := int64(len("push-file"))
		gf := guest + "/contract-push.txt"
		err := d.Copy(ctx(t), id, driver.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH, GuestPath: gf,
			Src: strings.NewReader("push-file"), ExpectedBytes: &sz,
		})
		if err != nil {
			t.Fatalf("push file: %v", err)
		}
		if got := mustOK(t, sh(t, d, id, guest, "cat "+gf)); got != "push-file" {
			t.Errorf("pushed file = %q", got)
		}

		gd := guest + "/contract-pushdir"
		err = d.Copy(ctx(t), id, driver.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH, GuestPath: gd,
			IsDirectory: true, Src: tarOf(t, map[string]string{"a.txt": "A", "sub/b.txt": "B"}),
		})
		if err != nil {
			t.Fatalf("push dir: %v", err)
		}
		if got := mustOK(t, sh(t, d, id, guest, "cat "+gd+"/a.txt "+gd+"/sub/b.txt")); got != "AB" {
			t.Errorf("pushed dir contents = %q", got)
		}

		gp := guest + "/contract-pull.bin"
		mustOK(t, sh(t, d, id, guest, "printf 'pull-bytes' > "+gp))
		var dst bytes.Buffer
		err = d.Copy(ctx(t), id, driver.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PULL, GuestPath: gp, Dst: &dst,
		})
		if err != nil || dst.String() != "pull-bytes" {
			t.Errorf("pull: err=%v bytes=%q", err, dst.String())
		}

		esc := guest + "/../contract-escape.txt"
		err = d.Copy(ctx(t), id, driver.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH, GuestPath: esc,
			Src: strings.NewReader("x"), ExpectedBytes: ptr(int64(1)),
		})
		if err == nil {
			t.Errorf("traversal push to %s was accepted", esc)
			if h.HostShared {
				_ = os.Remove(filepath.Join(filepath.Dir(root), "contract-escape.txt"))
			}
		}
		err = d.Copy(ctx(t), id, driver.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH, GuestPath: gd,
			IsDirectory: true, Src: tarOf(t, map[string]string{"../contract-escape-dir.txt": "x"}),
		})
		if err == nil && h.HostShared {
			if _, serr := os.Stat(filepath.Join(filepath.Dir(gd), "contract-escape-dir.txt")); serr == nil {
				_ = os.Remove(filepath.Join(filepath.Dir(gd), "contract-escape-dir.txt"))
				t.Errorf("tar entry with .. escaped the destination")
			}
		}
	})

	t.Run("Isolation", func(t *testing.T) {
		mark := "$HOME/nexus-contract-outside"
		r := sh(t, d, id, guest, "touch "+mark)
		if r.err == nil && r.code == 0 {
			sh(t, d, id, guest, "rm -f "+mark)
			if d.Capabilities().Isolation == driver.IsolationGuest {
				if home, err := os.UserHomeDir(); err == nil {
					p := filepath.Join(home, "nexus-contract-outside")
					if _, err := os.Stat(p); err == nil {
						_ = os.Remove(p)
						t.Errorf("guest write leaked to host")
					}
				}
			} else {
				t.Errorf("write outside worktree succeeded")
			}
		}
		if h.HostShared {
			if home, err := os.UserHomeDir(); err == nil {
				if _, err := os.Stat(filepath.Join(home, "nexus-contract-outside")); err == nil {
					_ = os.Remove(filepath.Join(home, "nexus-contract-outside"))
					t.Errorf("file exists outside worktree on host")
				}
			}
		}
		if got := mustOK(t, sh(t, d, id, guest, "touch inside-ok && echo ok")); strings.TrimSpace(got) != "ok" {
			t.Errorf("write inside worktree failed")
		}
		home, err := os.UserHomeDir()
		if !h.HostShared || err != nil {
			return
		}
		tested := false
		for _, p := range []string{".ssh", ".aws"} {
			if _, err := os.Stat(filepath.Join(home, p)); err != nil {
				continue
			}
			tested = true
			r := sh(t, d, id, guest, "ls \"$HOME/"+p+"\"")
			if r.err == nil && r.code == 0 {
				t.Errorf("read of ~/%s allowed: %q", p, r.out)
			}
		}
		if !tested {
			t.Skip("no ~/.ssh or ~/.aws on host to probe denyRead")
		}
	})

	t.Run("Egress", func(t *testing.T) {
		if !h.Net {
			t.Skip("Net disabled for " + h.Backend)
		}
		curl := func(u string) result {
			return sh(t, d, id, guest, "curl -sS -m 20 -o /dev/null -w '%{http_code}' "+u)
		}
		r := curl("https://github.com")
		if r.err != nil || r.code != 0 || strings.TrimSpace(r.out) == "000" || strings.TrimSpace(r.out) == "" {
			t.Errorf("allowed host github.com blocked: code=%d out=%q err=%v stderr=%q", r.code, r.out, r.err, r.errOut)
		}
		r = curl("https://example.com")
		code := strings.TrimSpace(r.out)
		if r.err == nil && r.code == 0 && code != "000" && code != "403" {
			t.Errorf("non-allowed host example.com reachable: http %s", code)
		}
	})

	t.Run("Capabilities", func(t *testing.T) {
		c := d.Capabilities()
		got := driver.OptionalInterfaces(d)
		for name, pair := range map[string][2]bool{
			"Pause": {c.Pause, got.Pause}, "GuestDial": {c.GuestDial, got.GuestDial},
			"Snapshot": {c.Snapshot, got.Snapshot}, "Fork": {c.Fork, got.Fork},
			"SnapshotRemove": {c.SnapshotRemove, got.SnapshotRemove},
			"NetworkHook":    {c.NetworkHook, got.NetworkHook},
			"NetnsState":     {c.NetnsState, got.NetnsState},
			"SessionAttach":  {c.SessionAttach, got.SessionAttach},
		} {
			if pair[0] != pair[1] {
				t.Errorf("%s: declared=%v implemented=%v", name, pair[0], pair[1])
			}
		}
		if c.GuestOS == driver.GuestOSUnknown {
			t.Errorf("GuestOS undeclared")
		}
		if c.Egress == "" {
			t.Errorf("Egress level undeclared")
		}
		if h.Net && c.Egress == driver.EgressNone {
			t.Errorf("Net checks enabled but Egress=none")
		}
		if !c.Pause {
			if _, ok := d.(driver.PauseResumer); ok {
				t.Errorf("Pause not declared but PauseResumer implemented")
			}
		}
	})

	t.Run("Destroy", func(t *testing.T) {
		if err := d.Stop(ctx(t), id); err != nil {
			t.Fatalf("Stop before destroy: %v", err)
		}
		destroy()
		destroyed = true
		o, err := d.Observe(ctx(t), id)
		if err != nil || o.State == driver.Running {
			t.Errorf("post-destroy Observe: state=%v err=%v", o.State, err)
		}
		r := run(t, d, id, driver.ExecOptions{Argv: []string{"true"}, Cwd: guest})
		if r.err == nil && r.code == 0 {
			t.Errorf("exec succeeded after destroy")
		}
		_, serr := os.Stat(root)
		switch {
		case h.DestroyKeepsRoot && serr != nil:
			t.Errorf("root removed by destroy: %v", serr)
		case !h.DestroyKeepsRoot && !errors.Is(serr, os.ErrNotExist):
			t.Errorf("root survives destroy: %v", serr)
		}
	})
}

func ptr[T any](v T) *T { return &v }

func tarOf(t *testing.T, files map[string]string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}
