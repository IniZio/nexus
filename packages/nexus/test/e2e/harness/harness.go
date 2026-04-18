//go:build e2e

package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

var moduleRoot string

func init() {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			moduleRoot = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

type Harness struct {
	t          *testing.T
	socketPath string
	client     *Client
	cmd        *exec.Cmd
}

type Option func(*config)

type config struct {
	firecrackerBin    string
	firecrackerKernel string
	firecrackerRootfs string
	nodeName          string
}

func WithFirecracker(bin, kernel, rootfs string) Option {
	return func(c *config) {
		c.firecrackerBin = bin
		c.firecrackerKernel = kernel
		c.firecrackerRootfs = rootfs
	}
}

func WithNodeName(name string) Option {
	return func(c *config) {
		c.nodeName = name
	}
}

func New(t *testing.T, opts ...Option) *Harness {
	t.Helper()

	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	dbPath := TempDB(t)
	socketPath := TempSocket(t)
	workdir := TempWorkdir(t)

	binPath := os.Getenv("NEXUS_E2E_BINARY")
	if binPath == "" {
		tmp, err := os.MkdirTemp("", "nexus-e2e-bin-*")
		if err != nil {
			t.Fatalf("harness: mktemp for binary: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(tmp) })

		binPath = filepath.Join(tmp, "nexusd")
		build := exec.Command("go", "build", "-o", binPath, "./cmd/nexusd/")
		build.Dir = moduleRoot
		build.Stderr = os.Stderr
		if out, err := build.Output(); err != nil {
			t.Fatalf("harness: build nexusd: %v\n%s", err, out)
		}
	}

	args := []string{
		"-db", dbPath,
		"-socket", socketPath,
		"-workdir-root", workdir,
	}
	if cfg.firecrackerBin != "" {
		args = append(args,
			"-firecracker",
			"-firecracker-bin", cfg.firecrackerBin,
			"-kernel", cfg.firecrackerKernel,
			"-rootfs", cfg.firecrackerRootfs,
		)
	}
	if cfg.nodeName != "" {
		args = append(args, "-node-name", cfg.nodeName)
	}

	cmd := exec.Command(binPath, args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness: start nexusd: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var client *Client
	for time.Now().Before(deadline) {
		c, err := Dial(socketPath)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if err := c.Call("node.info", nil, nil); err != nil {
			c.Close()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		client = c
		break
	}
	if client == nil {
		_ = cmd.Process.Kill()
		t.Fatalf("harness: nexusd did not become ready within 10s")
	}

	h := &Harness{
		t:          t,
		socketPath: socketPath,
		client:     client,
		cmd:        cmd,
	}

	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = client.Close()
	})

	return h
}

func (h *Harness) Call(method string, params, out any) error {
	return h.client.Call(method, params, out)
}

func (h *Harness) MustCall(method string, params, out any) {
	h.t.Helper()
	if err := h.Call(method, params, out); err != nil {
		h.t.Fatalf("MustCall %s: %v", method, err)
	}
}

func (h *Harness) SocketPath() string {
	return h.socketPath
}
