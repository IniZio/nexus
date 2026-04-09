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
	script := buildSeatbeltBootstrapScript("/Users/tester", hostCLIAvailability{Opencode: true, Codex: true, Claude: true})

	for _, token := range []string{
		"unset DOCKER_HOST DOCKER_CONTEXT",
		"docker.io",
		"docker-compose-v2",
		"npm i -g opencode-ai @openai/codex @anthropic-ai/claude-code",
		"ln -sfn '/Users/tester'/.config/opencode ~/.config/opencode",
		"ln -sfn '/Users/tester'/.codex ~/.codex",
		"ln -sfn '/Users/tester'/.claude ~/.claude",
		"npm bin -g",
	} {
		if !strings.Contains(script, token) {
			t.Fatalf("expected script to include %q", token)
		}
	}
}

func TestBuildSeatbeltBootstrapScriptInstallsOnlyHostAvailableCLIs(t *testing.T) {
	script := buildSeatbeltBootstrapScript("/Users/tester", hostCLIAvailability{Opencode: true, Codex: false, Claude: true})
	if !strings.Contains(script, "npm i -g opencode-ai @anthropic-ai/claude-code") {
		t.Fatalf("expected selective install command, got %q", script)
	}
	if strings.Contains(script, "@openai/codex") {
		t.Fatalf("did not expect codex package install when host codex is unavailable, got %q", script)
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

func TestStartLimaShellSkipsUnavailableCandidatesWhenPreparingWorkspaceMount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Setenv("NEXUS_RUNTIME_SEATBELT_INSTANCE", "")

	origEnsure := ensureLimaInstanceRunningFn
	origPrepare := prepareWorkspaceMountFn
	origList := listLimaInstancesFn
	defer func() {
		ensureLimaInstanceRunningFn = origEnsure
		prepareWorkspaceMountFn = origPrepare
		listLimaInstancesFn = origList
	}()

	listLimaInstancesFn = func(context.Context) ([]string, error) {
		return []string{"default"}, nil
	}
	ensureLimaInstanceRunningFn = func(_ context.Context, instance string) error {
		if instance != "default" {
			return errors.New("instance does not exist")
		}
		return nil
	}

	called := make([]string, 0)
	prepareWorkspaceMountFn = func(_ context.Context, instance, localPath string) error {
		called = append(called, instance+":"+localPath)
		return nil
	}

	origSpawn := ptyStartWithSizeFn
	defer func() { ptyStartWithSizeFn = origSpawn }()
	ptyStartWithSizeFn = func(*exec.Cmd, *pty.Winsize) (*os.File, error) {
		return nil, errors.New("stop after observe")
	}

	_, _, _ = startLimaShell(ctx, "nexus-seatbelt", "/workspace", "/tmp/repo", "bash")
	if len(called) != 1 || called[0] != "default:/tmp/repo" {
		t.Fatalf("expected prepareWorkspaceMount called only for default candidate, got %v", called)
	}
}

func TestEnsureLimaInstanceRunningCreatesMissingInstance(t *testing.T) {
	origOutput := limactlOutputFn
	origCombined := limactlCombinedOutputFn
	defer func() {
		limactlOutputFn = origOutput
		limactlCombinedOutputFn = origCombined
	}()

	calledStart := false
	limactlOutputFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 3 && args[0] == "list" && args[1] == "--json" && args[2] == "nexus-seatbelt" {
			return []byte("[]"), nil
		}
		return nil, errors.New("unexpected limactl output args")
	}
	limactlCombinedOutputFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 5 && args[0] == "start" && args[1] == "--yes" && args[2] == "--name" && args[3] == "nexus-seatbelt" && args[4] == "template:default" {
			calledStart = true
			return []byte(""), nil
		}
		return nil, errors.New("unexpected limactl start args")
	}

	err := ensureLimaInstanceRunning(context.Background(), "nexus-seatbelt")
	if err != nil {
		t.Fatalf("ensureLimaInstanceRunning: %v", err)
	}
	if !calledStart {
		t.Fatal("expected limactl start --name nexus-seatbelt to be called")
	}
}

func TestIsTransientLimaShellError(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{name: "kex exchange", message: "kex_exchange_identification: read: Connection reset by peer", want: true},
		{name: "mux refusal", message: "mux_client_request_session: session request failed: Session open refused by peer", want: true},
		{name: "plain failure", message: "permission denied", want: false},
		{name: "empty", message: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientLimaShellError(tc.message)
			if got != tc.want {
				t.Fatalf("isTransientLimaShellError(%q)=%v, want %v", tc.message, got, tc.want)
			}
		})
	}
}
