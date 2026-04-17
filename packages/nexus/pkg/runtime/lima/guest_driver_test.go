package lima

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/drivers/shared"
)

func TestGuestWorkdir_ScopedPerWorkspaceID(t *testing.T) {
	d := NewGuestDriver()
	if got := d.GuestWorkdir("ws-feat-1"); got != "/workspace/ws-feat-1" {
		t.Fatalf("expected /workspace/ws-feat-1, got %q", got)
	}
	if got := d.GuestWorkdir("a/b"); got != "/workspace/a-b" {
		t.Fatalf("expected sanitized segment, got %q", got)
	}
}

func TestWorkspaceBindMountScript_includesGitPermissionRepair(t *testing.T) {
	s := workspaceBindMountScript("/workspace/ws-1", "/Users/me/proj")
	if !strings.Contains(s, "CUR_CANON") || !strings.Contains(s, "!=") {
		t.Fatal("expected remount-only-when-source-differs logic")
	}
	if strings.Contains(s, "exit 0") {
		t.Fatal("mount script should not exit before bind is complete")
	}
}

func TestWorkspaceBindMountScriptNoChmod(t *testing.T) {
	script := workspaceBindMountScript("/workspace/test-id", "/host/project")
	if strings.Contains(script, "chmod") {
		t.Error("bind mount script should not contain chmod — UID matching makes it unnecessary")
	}
}

func TestCreateRequiresLimaForIsolation(t *testing.T) {
	d := NewGuestDriver()
	oldLookPath := guestLookPath
	old := d.bootstrapInstance
	t.Cleanup(func() {
		d.bootstrapInstance = old
		guestLookPath = oldLookPath
	})
	guestLookPath = func(file string) (string, error) { return "", errors.New("not found") }
	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error { return nil }

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
	d := NewGuestDriver()
	oldLookPath := guestLookPath
	t.Cleanup(func() { guestLookPath = oldLookPath })
	guestLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	calledBootstrap := false
	calledPrepare := false

	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error {
		calledBootstrap = true
		if instance == "" {
			t.Fatal("expected non-empty instance")
		}
		return nil
	}
	d.prepareWorkspaceFS = func(ctx context.Context, instance, targetPath, localPath string) error {
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

func TestCreateAppliesConfigBundleViaApplyHook(t *testing.T) {
	d := NewGuestDriver()
	oldLookPath := guestLookPath
	t.Cleanup(func() { guestLookPath = oldLookPath })
	guestLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error { return nil }
	d.prepareWorkspaceFS = func(ctx context.Context, instance, targetPath, localPath string) error { return nil }

	applied := false
	d.applyConfigBundle = func(ctx context.Context, instance, configBundle string) error {
		applied = strings.TrimSpace(configBundle) != ""
		return nil
	}

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID:   "ws-cfg",
		WorkspaceName: "cfg",
		ProjectRoot:   t.TempDir(),
		ConfigBundle:  "QUJD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Fatal("expected applyConfigBundle to be called with non-empty bundle")
	}
}

func TestCreateFallsBackToDefaultInstanceWhenMountPrepareFails(t *testing.T) {
	d := NewGuestDriver()
	oldLookPath := guestLookPath
	t.Cleanup(func() { guestLookPath = oldLookPath })
	guestLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	d.instanceEnv = "nexus-ci-alt"
	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error { return nil }

	seen := make([]string, 0)
	d.prepareWorkspaceFS = func(ctx context.Context, instance, targetPath, localPath string) error {
		seen = append(seen, instance)
		if instance == "nexus-ci-alt" {
			return errors.New("instance does not exist")
		}
		if instance == "nexus" {
			return nil
		}
		return errors.New("unexpected instance")
	}

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID:   "ws-fallback",
		WorkspaceName: "fallback",
		ProjectRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected fallback create to succeed, got %v", err)
	}

	if len(seen) < 2 || seen[0] != "nexus-ci-alt" || seen[1] != "nexus" {
		t.Fatalf("expected prepare sequence [nexus-ci-alt nexus], got %v", seen)
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	ws, ok := d.workspaces["ws-fallback"]
	if !ok {
		t.Fatal("expected workspace to be tracked")
	}
	if ws.instance != "nexus" {
		t.Fatalf("expected workspace instance to switch to nexus, got %q", ws.instance)
	}
}

func TestCreateExistingWorkspaceRefreshesMountPath(t *testing.T) {
	d := NewGuestDriver()
	oldLookPath := guestLookPath
	t.Cleanup(func() { guestLookPath = oldLookPath })
	guestLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	oldPath := t.TempDir()
	newPath := t.TempDir()
	d.workspaces["ws-refresh"] = &workspaceState{
		projectRoot: oldPath,
		state:       "created",
		instance:    "nexus",
	}

	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error { return nil }

	var seenLocalPath string
	d.prepareWorkspaceFS = func(ctx context.Context, instance, targetPath, localPath string) error {
		seenLocalPath = localPath
		return nil
	}

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID:   "ws-refresh",
		WorkspaceName: "refresh",
		ProjectRoot:   newPath,
	})
	if err != nil {
		t.Fatalf("expected create refresh to succeed, got %v", err)
	}
	if seenLocalPath != newPath {
		t.Fatalf("expected prepareWorkspaceFS localPath %q, got %q", newPath, seenLocalPath)
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	if got := d.workspaces["ws-refresh"].projectRoot; got != newPath {
		t.Fatalf("expected stored projectRoot %q, got %q", newPath, got)
	}
}

func TestCreateFailsWhenBootstrapFails(t *testing.T) {
	d := NewGuestDriver()
	oldLookPath := guestLookPath
	t.Cleanup(func() { guestLookPath = oldLookPath })
	guestLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error {
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

func TestCheckpointForkUsesBtrfsSubvolumeSnapshot(t *testing.T) {
	d := NewGuestDriver()

	var capturedInstance, capturedScript string
	d.forkSSH = func(_ context.Context, instance, script string) ([]byte, error) {
		capturedInstance = instance
		capturedScript = script
		return nil, nil
	}

	d.workspaces["ws-parent"] = &workspaceState{
		projectRoot: "/host/path/unused",
		state:       "running",
		instance:    "nexus",
	}

	snapshotID, err := d.CheckpointFork(context.Background(), "ws-parent", "ws-child")
	if err != nil {
		t.Fatalf("CheckpointFork failed: %v", err)
	}
	if snapshotID != "" {
		t.Errorf("expected snapshotID to be empty for virtiofs-backed fork, got %q", snapshotID)
	}
	if capturedInstance != "nexus" {
		t.Errorf("expected instance %q, got %q", "nexus", capturedInstance)
	}
	if !strings.Contains(capturedScript, "btrfs subvolume snapshot") {
		t.Errorf("expected btrfs subvolume snapshot in script, got: %q", capturedScript)
	}
	if !strings.Contains(capturedScript, guestWorkdirForID("ws-parent")) {
		t.Errorf("script should reference parent guest path, got: %q", capturedScript)
	}
	if !strings.Contains(capturedScript, guestWorkdirForID("ws-child")) {
		t.Errorf("script should reference child guest path, got: %q", capturedScript)
	}
}

func TestCreateRestoresLineageSnapshotIntoProjectRoot(t *testing.T) {
	d := NewGuestDriver()
	d.snapshotRoot = t.TempDir()
	oldLookPath := guestLookPath
	t.Cleanup(func() { guestLookPath = oldLookPath })
	guestLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }
	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error { return nil }
	d.prepareWorkspaceFS = func(ctx context.Context, instance, targetPath, localPath string) error { return nil }
	d.applyConfigBundle = func(ctx context.Context, instance, configBundle string) error { return nil }

	snapshotID := "snap-test"
	snapshotPath := d.snapshotPath(snapshotID)
	if err := os.MkdirAll(snapshotPath, 0o755); err != nil {
		t.Fatalf("mkdir snapshot path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotPath, "YOLO.txt"), []byte("from snapshot"), 0o644); err != nil {
		t.Fatalf("write snapshot file: %v", err)
	}

	projectRoot := t.TempDir()
	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID:   "ws-restore",
		WorkspaceName: "restore",
		ProjectRoot:   projectRoot,
		Options: map[string]string{
			"lineage_snapshot_id": snapshotID,
		},
	})
	if err != nil {
		t.Fatalf("create with snapshot failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(projectRoot, "YOLO.txt"))
	if err != nil {
		t.Fatalf("expected restored file: %v", err)
	}
	if string(data) != "from snapshot" {
		t.Fatalf("unexpected restored file contents: %q", string(data))
	}
}

func TestBuildLimaGuestBootstrapScriptIncludesIsolationAndForwarding(t *testing.T) {
	script := buildLimaGuestBootstrapScript("")

	for _, token := range []string{
		"unset DOCKER_HOST DOCKER_CONTEXT",
		"docker.io",
		"docker-compose-v2",
	} {
		if !strings.Contains(script, token) {
			t.Fatalf("expected script to include %q", token)
		}
	}

	for _, forbidden := range []string{"ln -sfn", "hostHome"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("script must not contain %q (symlinks removed for remote-first design)", forbidden)
		}
	}
}

func TestBuildLimaGuestBootstrapScriptInstallsRegistryPackages(t *testing.T) {
	script := buildLimaGuestBootstrapScript("")
	for _, pkg := range agentprofile.AllInstallPkgs() {
		if !strings.Contains(script, pkg) {
			t.Fatalf("bootstrap script missing install package %q", pkg)
		}
	}
}

func TestBuildLimaGuestBootstrapScriptChecksRegistryBinaries(t *testing.T) {
	script := buildLimaGuestBootstrapScript("")
	for _, bin := range agentprofile.AllBinaries() {
		if !strings.Contains(script, bin) {
			t.Fatalf("bootstrap script missing binary check for %q", bin)
		}
	}
}

func TestBuildLimaGuestBootstrapScriptExtractsBundleWhenProvided(t *testing.T) {
	script := buildLimaGuestBootstrapScript("QUJDREVGRw==")
	if !strings.Contains(script, "/tmp/nexus-auth.tar.gz.b64") {
		t.Fatal("bootstrap script must write auth bundle payload to temp file")
	}
	if !strings.Contains(script, "base64") {
		t.Fatal("bootstrap script must contain base64 decode step")
	}
	if !strings.Contains(script, "nexus-auth.tar.gz") {
		t.Fatal("bootstrap script must contain tar extraction")
	}
}

func TestBuildLimaGuestBootstrapScriptNoBundleWhenEmpty(t *testing.T) {
	script := buildLimaGuestBootstrapScript("")
	if strings.Contains(script, "nexus-auth.tar.gz") {
		t.Fatal("bootstrap script must not contain tar extraction when bundle path is empty")
	}
}

func TestShellOpenDefaultsToWorkspaceMountPath(t *testing.T) {
	d := NewGuestDriver()
	root := t.TempDir()
	d.mu.Lock()
	d.workspaces["ws-open"] = &workspaceState{projectRoot: root, state: "created", instance: "nexus-ci-alt"}
	d.mu.Unlock()

	bootstrapCalls := 0
	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error {
		bootstrapCalls++
		if instance != "nexus-ci-alt" {
			t.Fatalf("expected bootstrap instance nexus-ci-alt, got %q", instance)
		}
		return nil
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

	wantGuest := "/workspace/ws-open"
	if gotWorkdir != wantGuest {
		t.Fatalf("expected workdir %q, got %q", wantGuest, gotWorkdir)
	}
	if gotLocalPath != root {
		t.Fatalf("expected localPath %q, got %q", root, gotLocalPath)
	}
	if bootstrapCalls != 1 {
		t.Fatalf("expected bootstrap to run once during shell open, got %d", bootstrapCalls)
	}

	_ = enc.Encode(map[string]any{"id": "2", "type": "shell.close"})
}

func TestStartLimaShellSkipsUnavailableCandidatesWhenPreparingWorkspaceMount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Setenv("NEXUS_RUNTIME_LIMAGUEST_INSTANCE", "")
	t.Setenv("NEXUS_RUNTIME_SEATBELT_INSTANCE", "")

	origEnsure := ensureLimaInstanceRunningFn
	origPrepare := prepareWorkspacePathFn
	origList := listLimaInstancesFn
	defer func() {
		ensureLimaInstanceRunningFn = origEnsure
		prepareWorkspacePathFn = origPrepare
		listLimaInstancesFn = origList
	}()

	listLimaInstancesFn = func(context.Context) ([]string, error) {
		return []string{"nexus"}, nil
	}
	ensureLimaInstanceRunningFn = func(_ context.Context, instance string) error {
		if instance != "nexus" {
			return errors.New("instance does not exist")
		}
		return nil
	}

	called := make([]string, 0)
	prepareWorkspacePathFn = func(_ context.Context, instance, targetPath, localPath string) error {
		called = append(called, instance+":"+localPath)
		return nil
	}

	origSpawn := ptyStartWithSizeFn
	defer func() { ptyStartWithSizeFn = origSpawn }()
	ptyStartWithSizeFn = func(*exec.Cmd, *pty.Winsize) (*os.File, error) {
		return nil, errors.New("stop after observe")
	}

	_, _, err := startLimaShell(ctx, "nexus-ci-alt", "/nexus/ws/test-ws", "/tmp/repo", "bash")
	if err == nil {
		t.Fatal("expected startLimaShell to fail once pty start is stubbed")
	}
	if len(called) != 1 || called[0] != "nexus:/tmp/repo" {
		t.Fatalf("expected prepareWorkspacePath called only for nexus candidate, got %v", called)
	}
}

func TestEnsureLimaInstanceRunningReturnsErrorForMissingInstance(t *testing.T) {
	origOutput := limactlOutputFn
	origCombined := limactlCombinedOutputFn
	defer func() {
		limactlOutputFn = origOutput
		limactlCombinedOutputFn = origCombined
	}()

	limactlOutputFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 3 && args[0] == "list" && args[1] == "--json" && args[2] == "nexus-ci-alt" {
			return []byte("[]"), nil
		}
		return nil, errors.New("unexpected limactl output args")
	}
	limactlCombinedOutputFn = func(_ context.Context, args ...string) ([]byte, error) {
		return nil, errors.New("unexpected limactl start args")
	}

	err := ensureLimaInstanceRunning(context.Background(), "nexus-ci-alt")
	if err == nil {
		t.Fatal("expected ensureLimaInstanceRunning to fail for missing instance")
	}
	if !strings.Contains(err.Error(), "lima instance nexus-ci-alt is missing") {
		t.Fatalf("expected missing instance error, got: %v", err)
	}
}

func TestCreateReturnsErrWorkspaceMountFailedWhenAllMountsFail(t *testing.T) {
	d := NewGuestDriver()
	oldLookPath := guestLookPath
	t.Cleanup(func() { guestLookPath = oldLookPath })
	guestLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	d.bootstrapInstance = func(ctx context.Context, instance, configBundle string) error { return nil }
	d.prepareWorkspaceFS = func(ctx context.Context, instance, targetPath, localPath string) error {
		return errors.New("prepare /workspace mount failed: instance unreachable")
	}

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-mount-fail",
		ProjectRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error when all mount candidates fail")
	}
	if !errors.Is(err, runtime.ErrWorkspaceMountFailed) {
		t.Fatalf("expected ErrWorkspaceMountFailed sentinel, got: %v", err)
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
			got := shared.IsTransientLimaShellError(tc.message)
			if got != tc.want {
				t.Fatalf("IsTransientLimaShellError(%q)=%v, want %v", tc.message, got, tc.want)
			}
		})
	}
}

func TestStartLimaShellDoesNotWrapNamespace(t *testing.T) {
	d := &GuestDriver{}
	cmd := d.buildRemoteShellCmd("ws-abc123")
	if strings.Contains(cmd, "unshare") {
		t.Error("shell command must not use unshare")
	}
}
