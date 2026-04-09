package seatbelt

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

func TestCreateRequiresLimaForIsolation(t *testing.T) {
	d := NewDriver()
	oldLookPath := seatbeltLookPath
	old := d.bootstrapInstance
	t.Cleanup(func() {
		d.bootstrapInstance = old
		seatbeltLookPath = oldLookPath
	})
	seatbeltLookPath = func(file string) (string, error) { return "", errors.New("not found") }
	d.bootstrapInstance = func(ctx context.Context, instance, hostHome string) error { return nil }

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID:   "ws-1",
		WorkspaceName: "alpha",
		ProjectRoot:   t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected create to fail when limactl is unavailable")
	}
}

func TestCreateRunsBootstrapAndMount(t *testing.T) {
	d := NewDriver()
	oldLookPath := seatbeltLookPath
	t.Cleanup(func() { seatbeltLookPath = oldLookPath })
	seatbeltLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	calledBootstrap := false
	calledPrepare := false

	d.bootstrapInstance = func(ctx context.Context, instance, hostHome string) error {
		calledBootstrap = true
		if instance == "" {
			t.Fatal("expected non-empty instance")
		}
		return nil
	}
	d.prepareWorkspaceFS = func(ctx context.Context, instance, localPath string) error {
		calledPrepare = true
		if localPath == "" {
			t.Fatal("expected localPath")
		}
		return nil
	}

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID:   "ws-2",
		WorkspaceName: "beta",
		ProjectRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !calledBootstrap {
		t.Fatal("expected bootstrap to run")
	}
	if !calledPrepare {
		t.Fatal("expected workspace prepare to run")
	}
}

func TestCreateFailsWhenBootstrapFails(t *testing.T) {
	d := NewDriver()
	oldLookPath := seatbeltLookPath
	t.Cleanup(func() { seatbeltLookPath = oldLookPath })
	seatbeltLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	d.bootstrapInstance = func(ctx context.Context, instance, hostHome string) error {
		return errors.New("bootstrap failed")
	}

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID:   "ws-3",
		WorkspaceName: "gamma",
		ProjectRoot:   t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildSeatbeltBootstrapScriptIncludesIsolationAndForwarding(t *testing.T) {
	script := buildSeatbeltBootstrapScript("/Users/tester")

	for _, token := range []string{
		"unset DOCKER_HOST DOCKER_CONTEXT",
		"docker.io",
		"docker-compose-v2",
		"npm i -g opencode-ai @openai/codex @anthropic-ai/claude-code",
		"ln -sfn '/Users/tester'/.config/opencode ~/.config/opencode",
		"ln -sfn '/Users/tester'/.claude ~/.claude",
	} {
		if !strings.Contains(script, token) {
			t.Fatalf("expected script to include %q", token)
		}
	}
}

func TestShellOpenDefaultsToWorkspaceMountPath(t *testing.T) {
	d := NewDriver()
	root := t.TempDir()
	if err := d.Create(context.Background(), runtime.CreateRequest{WorkspaceID: "ws-open", WorkspaceName: "alpha", ProjectRoot: root}); err != nil {
		// bypass external dependencies for this protocol-focused test
		d.mu.Lock()
		d.workspaces["ws-open"] = &workspaceState{projectRoot: root, state: "created", instance: "nexus-seatbelt"}
		d.mu.Unlock()
	}

	called := make(chan struct{}, 1)
	var gotWorkdir, gotLocalPath string
	d.spawnShell = func(ctx context.Context, instanceName, workdir, localPath, shell string) (*exec.Cmd, *os.File, error) {
		gotWorkdir = workdir
		gotLocalPath = localPath
		cmd := exec.CommandContext(ctx, "bash", "-lc", "sleep 5")
		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 20, Cols: 80})
		if err != nil {
			return nil, nil, err
		}
		called <- struct{}{}
		return cmd, ptmx, nil
	}

	left, right := net.Pipe()
	defer left.Close()
	go d.serveShellProtocol(context.Background(), "ws-open", right)

	enc := json.NewEncoder(left)
	dec := json.NewDecoder(left)
	if err := enc.Encode(map[string]any{"id": "1", "type": "shell.open", "command": "bash", "workdir": ""}); err != nil {
		t.Fatalf("encode open: %v", err)
	}
	var res map[string]any
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("decode open result: %v", err)
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("spawnShell was not invoked")
	}

	if gotWorkdir != "/workspace" {
		t.Fatalf("expected workdir /workspace, got %q", gotWorkdir)
	}
	if gotLocalPath != root {
		t.Fatalf("expected localPath %q, got %q", root, gotLocalPath)
	}

	_ = enc.Encode(map[string]any{"id": "2", "type": "shell.close"})
}
