package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/compose"
	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/firecracker"
)

type fakeSocketFileInfo struct {
	name string
}

func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("linux-only test")
	}
}

func addFakeSGToPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sg")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake sg: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (f fakeSocketFileInfo) Name() string       { return f.name }
func (f fakeSocketFileInfo) Size() int64        { return 0 }
func (f fakeSocketFileInfo) Mode() fs.FileMode  { return os.ModeSocket | 0o666 }
func (f fakeSocketFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeSocketFileInfo) IsDir() bool        { return false }
func (f fakeSocketFileInfo) Sys() any           { return nil }

func TestParseRequiredPorts(t *testing.T) {
	ports, err := parseRequiredPorts("5173, 5174,5173,8000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []int{5173, 5174, 8000}
	if !reflect.DeepEqual(ports, expected) {
		t.Fatalf("expected %v, got %v", expected, ports)
	}
}

func TestParseRequiredPortsInvalid(t *testing.T) {
	if _, err := parseRequiredPorts("abc"); err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestDetectHostDockerSocketPrefersSnapHostfsSocket(t *testing.T) {
	originalStat := hostDockerSocketStat
	t.Cleanup(func() {
		hostDockerSocketStat = originalStat
	})

	t.Setenv("DOCKER_HOST", "")

	hostDockerSocketStat = func(path string) (os.FileInfo, error) {
		switch path {
		case "/var/lib/snapd/hostfs/var/run/docker.sock", "/var/run/docker.sock":
			return fakeSocketFileInfo{name: filepath.Base(path)}, nil
		default:
			return nil, os.ErrNotExist
		}
	}

	got := detectHostDockerSocket()
	if got != "/var/lib/snapd/hostfs/var/run/docker.sock" {
		t.Fatalf("expected hostfs docker socket, got %q", got)
	}
}

func TestMissingRequiredPorts(t *testing.T) {
	required := []int{5173, 5174, 8000}
	discovered := []compose.PublishedPort{{HostPort: 5173}, {HostPort: 8000}}
	missing := missingRequiredPorts(required, discovered)
	expected := []int{5174}
	if !reflect.DeepEqual(missing, expected) {
		t.Fatalf("expected %v, got %v", expected, missing)
	}
}

func TestAssertNoManualACP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "start.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nset -euo pipefail\ndocker compose up -d\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := assertNoManualACP(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAssertNoManualACPFindsCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "start-acp.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nopencode serve --port 4096\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := assertNoManualACP(dir); err == nil {
		t.Fatal("expected error when manual ACP startup command exists")
	}
}

func TestEnsureDotEnvCopiesFromExample(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("FOO=bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureDotEnv(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "FOO=bar\n" {
		t.Fatalf("unexpected .env content: %q", string(data))
	}
}

func TestRunConfiguredProbesRequiredFailure(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "host")
	opts := options{projectRoot: t.TempDir()}
	_, err := runConfiguredProbes(opts, []config.DoctorCommandProbe{{
		Name:     "failing-required",
		Command:  "bash",
		Args:     []string{"-lc", "exit 1"},
		Required: true,
	}})
	if err == nil {
		t.Fatal("expected required probe failure")
	}
}

func TestRunConfiguredProbesOptionalFailure(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "host")
	opts := options{projectRoot: t.TempDir()}
	results, err := runConfiguredProbes(opts, []config.DoctorCommandProbe{{
		Name:     "failing-optional",
		Command:  "bash",
		Args:     []string{"-lc", "exit 1"},
		Required: false,
	}})
	if err != nil {
		t.Fatalf("did not expect optional probe error, got %v", err)
	}
	if len(results) != 1 || results[0].Status != "failed_optional" {
		t.Fatalf("expected one failed_optional result, got %+v", results)
	}
}

func TestRunConfiguredProbesRunsAllProbes(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "host")
	opts := options{projectRoot: t.TempDir()}
	results, err := runConfiguredProbes(opts, []config.DoctorCommandProbe{
		{Name: "first", Command: "bash", Args: []string{"-lc", "exit 0"}, Required: true},
		{Name: "second", Command: "bash", Args: []string{"-lc", "exit 0"}, Required: false},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 probe results, got %d", len(results))
	}
}

func TestMarkProbesNotRun(t *testing.T) {
	probes := []config.DoctorCommandProbe{
		{Name: "probe-a", Required: true},
		{Name: "probe-b", Required: false},
	}
	results := markProbesNotRun(probes, "skip reason")
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Status != "not_run" || results[0].Phase != "probe" || results[0].SkipReason != "skip reason" {
		t.Fatalf("unexpected first result: %+v", results[0])
	}
	if results[1].Status != "not_run" || results[1].Phase != "probe" || results[1].SkipReason != "skip reason" {
		t.Fatalf("unexpected second result: %+v", results[1])
	}
}

func TestRunBuiltInRuntimeBackendCheckFirecrackerPasses(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	result, err := runBuiltInRuntimeBackendCheck()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Status != "passed" {
		t.Fatalf("expected passed status, got %+v", result)
	}
}

func TestRunBuiltInOpencodeSessionCheckSkipsFirecracker(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	result, err := runBuiltInOpencodeSessionCheck(t.TempDir())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Status != "not_run" {
		t.Fatalf("expected not_run status, got %+v", result)
	}
	if !strings.Contains(result.SkipReason, "firecracker") {
		t.Fatalf("expected firecracker skip reason, got %+v", result)
	}
}

func TestRunDoctorLifecycleStartRunsLifecycleScript(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus", "lifecycles"), 0o755); err != nil {
		t.Fatalf("mkdir lifecycles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nexus", "lifecycles", "start.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write start.sh: %v", err)
	}

	called := false
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		called = true
		if command != "bash" {
			t.Fatalf("expected bash command, got %q", command)
		}
		if len(args) != 1 || args[0] != ".nexus/lifecycles/start.sh" {
			t.Fatalf("unexpected args: %v", args)
		}
		return "ok", nil
	}

	if err := runDoctorLifecycleStart(root, doctorExecContext{backend: "docker"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("expected lifecycle start command to be executed")
	}
}

func TestRunDoctorLifecycleStartFallsBackToCompose(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}

	called := false
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		called = true
		if command != "sh" {
			t.Fatalf("expected shell command, got %q", command)
		}
		if len(args) != 2 || args[0] != "-lc" {
			t.Fatalf("unexpected args: %v", args)
		}
		if !strings.Contains(args[1], "export UID=1000; export GID=1000;") {
			t.Fatalf("expected compose fallback to export UID/GID defaults, got %q", args[1])
		}
		if !strings.Contains(args[1], "docker compose build --progress=plain") || !strings.Contains(args[1], "docker compose up -d --no-build") {
			t.Fatalf("expected compose build+up commands in shell script, got %q", args[1])
		}
		return "ok", nil
	}

	if err := runDoctorLifecycleStart(root, doctorExecContext{backend: "firecracker"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("expected compose fallback to execute")
	}
}

func TestRunDoctorLifecycleStartPrefersMakeStartOverLifecycleScript(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus", "lifecycles"), 0o755); err != nil {
		t.Fatalf("mkdir lifecycles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nexus", "lifecycles", "start.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write start.sh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("start:\n\t@echo make-start\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	called := false
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		called = true
		if command != "make" {
			t.Fatalf("expected make command, got %q", command)
		}
		if len(args) != 1 || args[0] != "start" {
			t.Fatalf("expected make start args, got %v", args)
		}
		if name != "lifecycle-start-make" {
			t.Fatalf("expected lifecycle-start-make context, got %q", name)
		}
		return "ok", nil
	}

	if err := runDoctorLifecycleStart(root, doctorExecContext{backend: "firecracker"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("expected make start command to be executed")
	}
}

func TestRunDoctorLifecycleSetupSkipsWhenMakeStartTargetExists(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus", "lifecycles"), 0o755); err != nil {
		t.Fatalf("mkdir lifecycles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nexus", "lifecycles", "setup.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write setup.sh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("start:\n\t@echo make-start\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	called := false
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		called = true
		return "ok", nil
	}

	if err := runDoctorLifecycleSetup(root, doctorExecContext{backend: "firecracker"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if called {
		t.Fatal("expected lifecycle setup to be skipped when make start target exists")
	}
}

func TestResolveDoctorLifecycleStartCommandReturnsSummary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("start:\n\t@echo make-start\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	command, args, contextLabel, summary, found, err := resolveDoctorLifecycleStartCommand(root)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !found {
		t.Fatal("expected startup command to be resolved")
	}
	if command != "make" || len(args) != 1 || args[0] != "start" {
		t.Fatalf("unexpected command resolution: command=%q args=%v", command, args)
	}
	if contextLabel != "lifecycle-start-make" {
		t.Fatalf("unexpected context label: %q", contextLabel)
	}
	if summary != "make start" {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestRunDoctorLifecycleStartIncludesFirecrackerWorkspaceDiagnosticsOnFailure(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus", "lifecycles"), 0o755); err != nil {
		t.Fatalf("mkdir lifecycles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nexus", "lifecycles", "start.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write start.sh: %v", err)
	}

	calls := 0
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		calls++
		if calls == 1 {
			return "/usr/bin/bash: .nexus/lifecycles/start.sh: No such file or directory", errors.New("start missing")
		}
		if name != "lifecycle-start-workspace-diagnostics" {
			t.Fatalf("expected diagnostics runner name, got %q", name)
		}
		return "--- ls /workspace/.nexus/lifecycles ---\nls: cannot access '/workspace/.nexus/lifecycles': No such file or directory", nil
	}

	err := runDoctorLifecycleStart(root, doctorExecContext{backend: "firecracker"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "firecracker workspace diagnostics") {
		t.Fatalf("expected diagnostics in error, got: %v", err)
	}
}

func TestRunBootstrapInstallCommandDoesNotInstallOpencode(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	var gotCommand string
	var gotArgs []string
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		gotCommand = command
		gotArgs = append([]string(nil), args...)
		return "", nil
	}

	_, err := runBootstrapInstallCommand(context.Background(), t.TempDir(), 30*time.Second, doctorExecContext{backend: "firecracker"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if gotCommand != "sh" {
		t.Fatalf("expected command sh, got %q", gotCommand)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "-lc" {
		t.Fatalf("expected args [-lc <script>], got %v", gotArgs)
	}
	if strings.Contains(gotArgs[1], "npm i -g opencode-ai") {
		t.Fatalf("unexpected opencode install in bootstrap install command: %q", gotArgs[1])
	}
}

func TestRunBootstrapInstallCommandVerifiesMakeIsInstalled(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	var installScript string
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if command == "sh" && len(args) == 2 && args[0] == "-lc" {
			installScript = args[1]
		}
		return "", nil
	}

	_, err := runBootstrapInstallCommand(context.Background(), t.TempDir(), 30*time.Second, doctorExecContext{backend: "firecracker"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if installScript == "" {
		t.Fatal("expected install script to be captured")
	}
	if !strings.Contains(installScript, "command -v make >/dev/null 2>&1 || exit 1") {
		t.Fatalf("expected install script to verify make availability, got: %q", installScript)
	}
}

func TestBuildSetupScriptSeedsMakeBinaryIntoRootfs(t *testing.T) {
	requireLinux(t)

	script := buildSetupScript("/tmp/nexus-tap-helper", "/tmp/nexus-firecracker-agent")
	needle := "docker-init docker-proxy iptables ip6tables; do"
	if count := strings.Count(script, needle); count != 2 {
		t.Fatalf("expected setup script to seed runtime helpers in both binary copy loops, count=%d", count)
	}
}

func TestBootstrapContainerExecContextDindNoLongerSupported(t *testing.T) {
	err := bootstrapContainerExecContext(t.TempDir(), doctorExecContext{backend: "dind"}, "dind", true)
	if err == nil {
		t.Fatal("expected error when using dind backend")
	}
	if !strings.Contains(err.Error(), "unsupported runtime backend") {
		t.Fatalf("expected unsupported backend error, got: %v", err)
	}
}

func TestBootstrapContainerExecContextFirecrackerUsesGuestRoot(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	observedProjectRoot := ""
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if command == "docker" && observedProjectRoot == "" {
			observedProjectRoot = projectRoot
		}
		return "ok", nil
	}

	err := bootstrapContainerExecContext(t.TempDir(), doctorExecContext{backend: "firecracker"}, "firecracker", false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if observedProjectRoot != "/" {
		t.Fatalf("expected firecracker bootstrap checks to run from guest root '/', got %q", observedProjectRoot)
	}
}

func TestBootstrapContainerExecContextFirecrackerRequiresMakeWhenStartTargetExists(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("start:\n\t@echo make-start\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	seenMakeCheck := false
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if command == "make" && len(args) == 1 && args[0] == "--version" {
			seenMakeCheck = true
			return "GNU Make 4.3", nil
		}
		if command == "docker" {
			return "ok", nil
		}
		return "ok", nil
	}

	err := bootstrapContainerExecContext(root, doctorExecContext{backend: "firecracker"}, "firecracker", false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !seenMakeCheck {
		t.Fatal("expected make --version capability check when Makefile start target exists")
	}
}

func TestBootstrapContainerExecContextFirecrackerStartScriptMountsCgroups(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	checksSeen := 0
	startScript := ""
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if command == "docker" {
			checksSeen++
			if checksSeen <= 2 {
				return "docker unavailable", errors.New("docker unavailable")
			}
			return "ok", nil
		}

		if command == "sh" && len(args) == 2 && args[0] == "-lc" {
			script := args[1]
			if strings.Contains(script, "dockerd --host=unix:///var/run/docker.sock") {
				startScript = script
				return "started", nil
			}
		}

		return "", nil
	}

	err := bootstrapContainerExecContext(t.TempDir(), doctorExecContext{backend: "firecracker"}, "firecracker", false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if startScript == "" {
		t.Fatal("expected docker start script to be executed")
	}
	if !strings.Contains(startScript, "mount -t cgroup2 none /sys/fs/cgroup") {
		t.Fatalf("expected cgroup2 mount in docker start script, got: %s", startScript)
	}
	if !strings.Contains(startScript, "mount -t cgroup -o $s cgroup /sys/fs/cgroup/$s") {
		t.Fatalf("expected cgroup v1 controller mount fallback in docker start script, got: %s", startScript)
	}
}

func TestFirecrackerAgentVSockPortDefaultsToRuntimeConstant(t *testing.T) {
	t.Setenv("NEXUS_FIRECRACKER_AGENT_VSOCK_PORT", "")
	if got := firecrackerAgentVSockPort(); got != firecracker.DefaultAgentVSockPort {
		t.Fatalf("expected default agent vsock port %d, got %d", firecracker.DefaultAgentVSockPort, got)
	}
}

func TestFirecrackerAgentVSockPortHonorsEnvOverride(t *testing.T) {
	t.Setenv("NEXUS_FIRECRACKER_AGENT_VSOCK_PORT", "25000")
	if got := firecrackerAgentVSockPort(); got != 25000 {
		t.Fatalf("expected env override agent vsock port 25000, got %d", got)
	}
}

func TestFirecrackerAgentVSockPortInvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("NEXUS_FIRECRACKER_AGENT_VSOCK_PORT", "invalid")
	if got := firecrackerAgentVSockPort(); got != firecracker.DefaultAgentVSockPort {
		t.Fatalf("expected fallback agent vsock port %d, got %d", firecracker.DefaultAgentVSockPort, got)
	}
}

func TestDoctorFirecrackerMachineSpecDefaults(t *testing.T) {
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_MEMORY_MIB", "")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_VCPUS", "")
	memoryMiB, vcpus := doctorFirecrackerMachineSpec()
	if memoryMiB != 4096 || vcpus != 2 {
		t.Fatalf("expected defaults memory=4096 vcpus=2, got memory=%d vcpus=%d", memoryMiB, vcpus)
	}
}

func TestDoctorFirecrackerMachineSpecHonorsOverrides(t *testing.T) {
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_MEMORY_MIB", "6144")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_VCPUS", "4")
	memoryMiB, vcpus := doctorFirecrackerMachineSpec()
	if memoryMiB != 6144 || vcpus != 4 {
		t.Fatalf("expected overrides memory=6144 vcpus=4, got memory=%d vcpus=%d", memoryMiB, vcpus)
	}
}

func TestDoctorFirecrackerMachineSpecIgnoresInvalidValues(t *testing.T) {
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_MEMORY_MIB", "256")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_VCPUS", "0")
	memoryMiB, vcpus := doctorFirecrackerMachineSpec()
	if memoryMiB != 4096 || vcpus != 2 {
		t.Fatalf("expected fallback defaults memory=4096 vcpus=2, got memory=%d vcpus=%d", memoryMiB, vcpus)
	}
}

func TestValidateFirecrackerHostPrerequisitesSkipsNonFirecrackerBackend(t *testing.T) {
	if err := validateFirecrackerHostPrerequisites(doctorExecContext{backend: "dind"}); err != nil {
		t.Fatalf("expected non-firecracker backend to skip preflight, got %v", err)
	}
}

func TestValidateFirecrackerHostPrerequisitesRequiresLinuxHost(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
	})

	firecrackerHostGOOS = "darwin"
	err := validateFirecrackerHostPrerequisites(doctorExecContext{backend: "firecracker"})
	if err == nil {
		t.Fatal("expected darwin host to fail firecracker preflight")
	}
	if !strings.Contains(err.Error(), "requires Linux with KVM") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFirecrackerHostPrerequisitesFailsWhenBinaryMissing(t *testing.T) {
	originalLookup := firecrackerHostBinaryLookup
	originalGOOS := firecrackerHostGOOS
	t.Cleanup(func() {
		firecrackerHostBinaryLookup = originalLookup
		firecrackerHostGOOS = originalGOOS
	})

	firecrackerHostGOOS = "linux"
	firecrackerHostBinaryLookup = func(string) (string, error) {
		return "", errors.New("not found")
	}

	err := validateFirecrackerHostPrerequisites(doctorExecContext{backend: "firecracker"})
	if err == nil {
		t.Fatal("expected missing firecracker binary error")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFirecrackerHostPrerequisitesFailsWhenKVMInaccessible(t *testing.T) {
	originalLookup := firecrackerHostBinaryLookup
	originalStat := firecrackerHostStat
	originalOpen := firecrackerHostOpenFile
	originalGOOS := firecrackerHostGOOS
	t.Cleanup(func() {
		firecrackerHostBinaryLookup = originalLookup
		firecrackerHostStat = originalStat
		firecrackerHostOpenFile = originalOpen
		firecrackerHostGOOS = originalGOOS
	})

	t.Setenv("NEXUS_FIRECRACKER_KERNEL", "/kernel")
	t.Setenv("NEXUS_FIRECRACKER_ROOTFS", "/rootfs")

	firecrackerHostGOOS = "linux"
	firecrackerHostBinaryLookup = func(string) (string, error) {
		return "/usr/bin/firecracker", nil
	}
	firecrackerHostStat = func(path string) (os.FileInfo, error) {
		return fakeSocketFileInfo{name: filepath.Base(path)}, nil
	}
	firecrackerHostOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, os.ErrPermission
	}

	err := validateFirecrackerHostPrerequisites(doctorExecContext{backend: "firecracker"})
	if err == nil {
		t.Fatal("expected /dev/kvm permission failure")
	}
	if !strings.Contains(err.Error(), "kvm group") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFirecrackerHostPrerequisitesPassesWhenHostReady(t *testing.T) {
	originalLookup := firecrackerHostBinaryLookup
	originalStat := firecrackerHostStat
	originalOpen := firecrackerHostOpenFile
	originalGOOS := firecrackerHostGOOS
	originalTapHelper := firecrackerTapHelperValidator
	originalBridge := firecrackerBridgeValidator
	t.Cleanup(func() {
		firecrackerHostBinaryLookup = originalLookup
		firecrackerHostStat = originalStat
		firecrackerHostOpenFile = originalOpen
		firecrackerHostGOOS = originalGOOS
		firecrackerTapHelperValidator = originalTapHelper
		firecrackerBridgeValidator = originalBridge
	})

	t.Setenv("NEXUS_FIRECRACKER_KERNEL", "/kernel")
	t.Setenv("NEXUS_FIRECRACKER_ROOTFS", "/rootfs")

	firecrackerHostGOOS = "linux"
	firecrackerHostBinaryLookup = func(string) (string, error) {
		return "/usr/bin/firecracker", nil
	}
	firecrackerHostStat = func(path string) (os.FileInfo, error) {
		return fakeSocketFileInfo{name: filepath.Base(path)}, nil
	}
	firecrackerHostOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		f, err := os.CreateTemp(t.TempDir(), "kvm-probe")
		if err != nil {
			t.Fatalf("create temp file: %v", err)
		}
		return f, nil
	}
	firecrackerTapHelperValidator = func() error { return nil }
	firecrackerBridgeValidator = func() error { return nil }

	if err := validateFirecrackerHostPrerequisites(doctorExecContext{backend: "firecracker"}); err != nil {
		t.Fatalf("expected firecracker preflight to pass, got %v", err)
	}
}

func TestWriteReport(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "reports", "doctor.json")
	results := []checkResult{{Name: "runtime", Phase: "probe", Status: "passed", Required: true, Attempts: 1}}
	if err := writeReport(reportPath, results); err != nil {
		t.Fatalf("unexpected error writing report: %v", err)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("unable to read report: %v", err)
	}
	var parsed []checkResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid report JSON: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Name != "runtime" || parsed[0].Phase != "probe" {
		t.Fatalf("unexpected report contents: %+v", parsed)
	}
}

func TestValidateLifecycleEntrypointsRequiresStartWhenNoMakeTarget(t *testing.T) {
	root := t.TempDir()
	lifecycleDir := filepath.Join(root, ".nexus", "lifecycles")
	if err := os.MkdirAll(lifecycleDir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := validateLifecycleEntrypoints(root)
	if err == nil {
		t.Fatal("expected error when start entrypoint is missing")
	}
	if !strings.Contains(err.Error(), "missing startup entrypoint") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateLifecycleEntrypointsAllowsComposeWithoutLifecycleDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := validateLifecycleEntrypoints(root); err != nil {
		t.Fatalf("expected compose-only startup entrypoint to be allowed, got %v", err)
	}
}

func TestAssertNoManualACPIgnoresMissingLifecycleDir(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, ".nexus", "lifecycles")
	if err := assertNoManualACP(missing); err != nil {
		t.Fatalf("expected missing lifecycle directory to be ignored, got %v", err)
	}
}

func TestValidateLifecycleEntrypointsAllowsMakefileStartTarget(t *testing.T) {
	root := t.TempDir()
	lifecycleDir := filepath.Join(root, ".nexus", "lifecycles")
	if err := os.MkdirAll(lifecycleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("start:\n\t@echo start\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := validateLifecycleEntrypoints(root); err != nil {
		t.Fatalf("expected make start target to satisfy startup entrypoint, got %v", err)
	}
}

func TestValidateLifecycleEntrypointsAllowsMissingTeardownAndSetup(t *testing.T) {
	root := t.TempDir()
	lifecycleDir := filepath.Join(root, ".nexus", "lifecycles")
	if err := os.MkdirAll(lifecycleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	startPath := filepath.Join(lifecycleDir, "start.sh")
	if err := os.WriteFile(startPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := validateLifecycleEntrypoints(root); err != nil {
		t.Fatalf("expected optional setup/teardown to be allowed, got %v", err)
	}
}

func TestDiscoverDoctorScriptsOrdersNumericThenLexical(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".nexus", "probe"))
	mustMkdirAll(t, filepath.Join(root, ".nexus", "check"))

	mustWriteExec(t, filepath.Join(root, ".nexus", "probe", "10-z.sh"), "#!/usr/bin/env bash\nexit 0\n")
	mustWriteExec(t, filepath.Join(root, ".nexus", "probe", "01-a.sh"), "#!/usr/bin/env bash\nexit 0\n")
	mustWriteExec(t, filepath.Join(root, ".nexus", "probe", "misc.sh"), "#!/usr/bin/env bash\nexit 0\n")

	probes, checks, warnings, err := discoverDoctorScripts(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 0 {
		t.Fatalf("expected 0 checks, got %d", len(checks))
	}
	if len(probes) != 3 {
		t.Fatalf("expected 3 probes, got %d", len(probes))
	}

	got := []string{probes[0].Name, probes[1].Name, probes[2].Name}
	want := []string{"a", "z", "misc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected probe order: got %v want %v", got, want)
	}

	if len(warnings) == 0 {
		t.Fatal("expected warning for non-prefixed discovery script")
	}
}

func TestResolveDoctorChecksFallsBackToWorkspaceConfigWhenNoDiscoveredScripts(t *testing.T) {
	root := t.TempDir()

	cfgProbes := []config.DoctorCommandProbe{{
		Name:     "cfg-probe",
		Command:  "bash",
		Args:     []string{"-lc", "exit 0"},
		Required: true,
	}}
	cfgTests := []config.DoctorCommandCheck{{
		Name:     "cfg-test",
		Command:  "bash",
		Args:     []string{"-lc", "exit 0"},
		Required: true,
	}}

	probes, tests, warnings, err := resolveDoctorChecks(root, cfgProbes, cfgTests)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("expected warnings when discovery folders are absent")
	}
	if len(probes) != 1 || probes[0].Name != "cfg-probe" {
		t.Fatalf("unexpected probes: %+v", probes)
	}
	if len(tests) != 1 || tests[0].Name != "cfg-test" {
		t.Fatalf("unexpected tests: %+v", tests)
	}

}

func TestRunInitCreatesNexusWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	orig := initRuntimeBootstrapRunner
	t.Cleanup(func() { initRuntimeBootstrapRunner = orig })
	initRuntimeBootstrapRunner = func(projectRoot, runtimeName string) error {
		if runtimeName != "firecracker" {
			t.Fatalf("expected firecracker runtime, got %q", runtimeName)
		}
		return nil
	}

	if err := runInit(initOptions{projectRoot: root, runtime: "firecracker"}); err != nil {
		t.Fatalf("expected init success, got %v", err)
	}

	workspacePath := filepath.Join(root, ".nexus", "workspace.json")
	data, err := os.ReadFile(workspacePath)
	if err != nil {
		t.Fatalf("expected workspace config file, got %v", err)
	}
	if !strings.Contains(string(data), `"required": [`) || !strings.Contains(string(data), `"firecracker"`) {
		t.Fatalf("expected runtime requirement firecracker in workspace.json, got:\n%s", string(data))
	}

	for _, rel := range []string{
		".nexus/lifecycles/setup.sh",
		".nexus/lifecycles/start.sh",
		".nexus/lifecycles/teardown.sh",
		".nexus/probe/01-runtime-backend.sh",
		".nexus/check/20-tooling-runtime.sh",
		".nexus/e2e/run.sh",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("expected %s to be created: %v", rel, err)
		}
	}
}

func TestRunInitDoesNotOverwriteExistingFilesWithoutForce(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus", "lifecycles"), 0o755); err != nil {
		t.Fatal(err)
	}

	custom := filepath.Join(root, ".nexus", "lifecycles", "start.sh")
	if err := os.WriteFile(custom, []byte("#!/bin/sh\necho custom\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runInit(initOptions{projectRoot: root, runtime: "local"}); err != nil {
		t.Fatalf("expected init success, got %v", err)
	}

	data, err := os.ReadFile(custom)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "custom") {
		t.Fatalf("expected existing file content to be preserved, got:\n%s", string(data))
	}
}

func TestRunInitCallsRuntimeBootstrapForFirecracker(t *testing.T) {
	root := t.TempDir()
	called := false
	orig := initRuntimeBootstrapRunner
	t.Cleanup(func() { initRuntimeBootstrapRunner = orig })
	initRuntimeBootstrapRunner = func(projectRoot, runtimeName string) error {
		called = true
		if runtimeName != "firecracker" {
			t.Fatalf("expected firecracker runtime, got %q", runtimeName)
		}
		return nil
	}
	if err := runInit(initOptions{projectRoot: root, runtime: "firecracker"}); err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}
	if !called {
		t.Fatal("expected init runtime bootstrap runner to be called")
	}
}

func TestRunInitSkipsRuntimeBootstrapForLocal(t *testing.T) {
	root := t.TempDir()
	called := false
	orig := initRuntimeBootstrapRunner
	t.Cleanup(func() { initRuntimeBootstrapRunner = orig })
	initRuntimeBootstrapRunner = func(projectRoot, runtimeName string) error {
		called = true
		return nil
	}
	if err := runInit(initOptions{projectRoot: root, runtime: "local"}); err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}
	if called {
		t.Fatal("expected init runtime bootstrap runner to NOT be called for local runtime")
	}
}

func TestRunInitFirecrackerReturnsManualStepsInNonInteractiveMode(t *testing.T) {
	origRunner := initRuntimeBootstrapRunner
	t.Cleanup(func() { initRuntimeBootstrapRunner = origRunner })
	initRuntimeBootstrapRunner = func(projectRoot, runtimeName string) error {
		return fmt.Errorf("firecracker runtime setup failed: bootstrap setup failed\n\nmanual next steps:\n  sudo -E nexus init --project-root %s --runtime firecracker", projectRoot)
	}

	err := runInit(initOptions{projectRoot: t.TempDir(), runtime: "firecracker"})
	if err == nil {
		t.Fatal("expected init failure")
	}
	if !strings.Contains(err.Error(), "manual next steps") {
		t.Fatalf("expected error to contain 'manual next steps', got: %v", err)
	}
	if !strings.Contains(err.Error(), "sudo -E nexus init") {
		t.Fatalf("expected error to contain 'sudo -E nexus init' command, got: %v", err)
	}
}

func TestRunInitRuntimeBootstrapReturnsFastErrorInNonInteractiveNoSudoNonRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fast-fail bootstrap path is Linux-specific")
	}
	origIsRoot := initRuntimeBootstrapIsRootFn
	origSudoOK := initRuntimeBootstrapSudoOKFn
	origIsTTY := initRuntimeBootstrapIsTTYFn
	origSkipFastFail := initRuntimeBootstrapSkipFastFailFn
	t.Cleanup(func() {
		initRuntimeBootstrapIsRootFn = origIsRoot
		initRuntimeBootstrapSudoOKFn = origSudoOK
		initRuntimeBootstrapIsTTYFn = origIsTTY
		initRuntimeBootstrapSkipFastFailFn = origSkipFastFail
	})

	initRuntimeBootstrapIsRootFn = func() bool { return false }
	initRuntimeBootstrapSudoOKFn = func() bool { return false }
	initRuntimeBootstrapIsTTYFn = func(f *os.File) bool { return false }
	initRuntimeBootstrapSkipFastFailFn = nil

	err := runInitRuntimeBootstrap(t.TempDir(), "firecracker")
	if err == nil {
		t.Fatal("expected fast-fail error in non-interactive no-sudo non-root scenario")
	}
	if !strings.Contains(err.Error(), "manual next steps") {
		t.Fatalf("expected error to contain 'manual next steps', got: %v", err)
	}
	if !strings.Contains(err.Error(), "sudo -E nexus init") {
		t.Fatalf("expected error to contain 'sudo -E nexus init' command, got: %v", err)
	}
}
func TestRunInitRuntimeBootstrapIgnoresKVMRefreshNeededWhenPrivileged(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kvm refresh bootstrap behavior is Linux-specific")
	}

	origIsRoot := initRuntimeBootstrapIsRootFn
	origSudoOK := initRuntimeBootstrapSudoOKFn
	origIsTTY := initRuntimeBootstrapIsTTYFn
	origSkipFastFail := initRuntimeBootstrapSkipFastFailFn
	origVerify := setupVerifyFn
	origKVMReexec := setupKVMGroupReexecFn
	t.Cleanup(func() {
		initRuntimeBootstrapIsRootFn = origIsRoot
		initRuntimeBootstrapSudoOKFn = origSudoOK
		initRuntimeBootstrapIsTTYFn = origIsTTY
		initRuntimeBootstrapSkipFastFailFn = origSkipFastFail
		setupVerifyFn = origVerify
		setupKVMGroupReexecFn = origKVMReexec
	})

	initRuntimeBootstrapIsRootFn = func() bool { return true }
	initRuntimeBootstrapSudoOKFn = func() bool { return true }
	initRuntimeBootstrapIsTTYFn = func(f *os.File) bool { return false }
	initRuntimeBootstrapSkipFastFailFn = func() bool { return true }

	setupVerifyFn = func() error { return errKVMGroupRefreshNeeded }
	setupKVMGroupReexecFn = func(commandPath string) error { return errors.New("simulated sg failure") }

	err := runInitRuntimeBootstrap(t.TempDir(), "firecracker")
	if err != nil {
		t.Fatalf("expected nil error when privileged and only kvm refresh is needed, got: %v", err)
	}
}
func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setupDoctorTestWorkspace(t *testing.T, doctorConfig config.DoctorConfig) string {
	root := t.TempDir()
	nexusDir := filepath.Join(root, ".nexus")
	lifecycleDir := filepath.Join(nexusDir, "lifecycles")
	if err := os.MkdirAll(lifecycleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"setup.sh", "start.sh", "teardown.sh"} {
		path := filepath.Join(lifecycleDir, name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wsCfg := config.WorkspaceConfig{
		Version: 1,
		Runtime: config.RuntimeConfig{Required: []string{"firecracker"}},
		Doctor:  doctorConfig,
	}
	cfgData, err := json.Marshal(wsCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nexusDir, "workspace.json"), cfgData, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDoctor_StillRunsTestsWhenRequiredProbeFails(t *testing.T) {
	t.Setenv("NEXUS_DOCTOR_DISABLE_BUILTIN_CHECKS", "1")
	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")
	originalGOOS := firecrackerHostGOOS
	originalBootstrap := doctorExecBootstrapRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		doctorExecBootstrapRunner = originalBootstrap
	})
	firecrackerHostGOOS = "linux"
	doctorExecBootstrapRunner = func(projectRoot string) error { return nil }

	workspaceRoot := setupDoctorTestWorkspace(t, config.DoctorConfig{
		Probes: []config.DoctorCommandProbe{
			{Name: "failing-probe", Command: "bash", Args: []string{"-lc", "exit 1"}, Required: true},
		},
		Tests: []config.DoctorCommandCheck{
			{Name: "still-runs-and-fails", Command: "bash", Args: []string{"-lc", "exit 1"}, Required: true},
			{Name: "session-built-in-placeholder", Command: "bash", Args: []string{"-lc", "exit 0"}, Required: false},
		},
	})

	reportPath := filepath.Join(t.TempDir(), "report.json")
	err := run(options{
		projectRoot: workspaceRoot,
		suite:       "test-suite",
		reportJSON:  reportPath,
	})

	if err == nil {
		t.Fatal("expected error due to required probe failure")
	}
	if !strings.Contains(err.Error(), "required probes failed") {
		t.Fatalf("expected combined error to include required probe failure, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "required tests failed") {
		t.Fatalf("expected combined error to include required test failure, got %q", err.Error())
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("unable to read report: %v", err)
	}
	var results []checkResult
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("invalid report JSON: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results (configured probe + tests), got %d", len(results))
	}

	var probeResult, requiredTestResult checkResult
	for _, r := range results {
		if r.Phase == "probe" {
			probeResult = r
		} else if r.Phase == "test" && r.Name == "still-runs-and-fails" {
			requiredTestResult = r
		}
	}

	if probeResult.Status != "failed_required" {
		t.Fatalf("expected probe status 'failed_required', got %q", probeResult.Status)
	}
	if requiredTestResult.Status != "failed_required" {
		t.Fatalf("expected test status 'failed_required', got %q", requiredTestResult.Status)
	}
	if requiredTestResult.SkipReason != "" {
		t.Fatalf("expected test skipReason to be empty, got %q", requiredTestResult.SkipReason)
	}

}

func TestDoctor_ProbesPassThenTestsRun(t *testing.T) {
	t.Setenv("NEXUS_DOCTOR_DISABLE_BUILTIN_CHECKS", "1")
	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")
	originalGOOS := firecrackerHostGOOS
	originalBootstrap := doctorExecBootstrapRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		doctorExecBootstrapRunner = originalBootstrap
	})
	firecrackerHostGOOS = "linux"
	doctorExecBootstrapRunner = func(projectRoot string) error { return nil }

	workspaceRoot := setupDoctorTestWorkspace(t, config.DoctorConfig{
		Probes: []config.DoctorCommandProbe{
			{Name: "passing-probe", Command: "bash", Args: []string{"-lc", "exit 0"}, Required: true},
		},
		Tests: []config.DoctorCommandCheck{
			{Name: "passing-test", Command: "bash", Args: []string{"-lc", "exit 0"}, Required: true},
			{Name: "second-passing-test", Command: "bash", Args: []string{"-lc", "exit 0"}, Required: false},
		},
	})

	reportPath := filepath.Join(t.TempDir(), "report.json")
	err := run(options{
		projectRoot: workspaceRoot,
		suite:       "test-suite",
		reportJSON:  reportPath,
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("unable to read report: %v", err)
	}
	var results []checkResult
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("invalid report JSON: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results (configured probe + tests), got %d", len(results))
	}

	for _, r := range results {
		if r.Status != "passed" {
			t.Fatalf("expected status 'passed', got %q for %s", r.Status, r.Name)
		}
		if r.Phase == "" {
			t.Fatalf("expected non-empty phase, got empty for %s", r.Name)
		}
	}
}

func TestDoctor_RequiredTestFailureReturnsError(t *testing.T) {
	t.Setenv("NEXUS_DOCTOR_DISABLE_BUILTIN_CHECKS", "1")
	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")
	originalGOOS := firecrackerHostGOOS
	originalBootstrap := doctorExecBootstrapRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		doctorExecBootstrapRunner = originalBootstrap
	})
	firecrackerHostGOOS = "linux"
	doctorExecBootstrapRunner = func(projectRoot string) error { return nil }

	workspaceRoot := setupDoctorTestWorkspace(t, config.DoctorConfig{
		Probes: []config.DoctorCommandProbe{
			{Name: "passing-probe", Command: "bash", Args: []string{"-lc", "exit 0"}, Required: true},
		},
		Tests: []config.DoctorCommandCheck{
			{Name: "failing-test", Command: "bash", Args: []string{"-lc", "exit 1"}, Required: true},
			{Name: "passing-test", Command: "bash", Args: []string{"-lc", "exit 0"}, Required: false},
		},
	})

	reportPath := filepath.Join(t.TempDir(), "report.json")
	err := run(options{
		projectRoot: workspaceRoot,
		suite:       "test-suite",
		reportJSON:  reportPath,
	})

	if err == nil {
		t.Fatal("expected error due to required test failure")
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("unable to read report: %v", err)
	}
	var results []checkResult
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("invalid report JSON: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results (configured probe + tests), got %d", len(results))
	}

	var requiredTestResult checkResult
	for _, r := range results {
		if r.Phase == "test" && r.Name == "failing-test" {
			requiredTestResult = r
		}
	}

	if requiredTestResult.Status != "failed_required" {
		t.Fatalf("expected test status 'failed_required', got %q", requiredTestResult.Status)
	}
}

func TestCombineCheckErrors(t *testing.T) {
	probeErr := errors.New("required probes failed: startup")
	testErr := errors.New("required tests failed: auth")
	err := combineCheckErrors(probeErr, testErr)
	if err == nil {
		t.Fatal("expected combined error")
	}
	if !strings.Contains(err.Error(), probeErr.Error()) {
		t.Fatalf("expected probe error in combined error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), testErr.Error()) {
		t.Fatalf("expected test error in combined error, got %q", err.Error())
	}
}

func TestFormatCommand(t *testing.T) {
	actual := formatCommand("bash", []string{"-lc", "echo hi"})
	expected := "bash -lc \"echo hi\""
	if actual != expected {
		t.Fatalf("expected %q, got %q", expected, actual)
	}
}

func TestRunCheckCommandCapturesOutput(t *testing.T) {
	output, err := runCheckCommandWithExecContext(context.Background(), t.TempDir(), "probe", "example", 1, 1, 30*time.Second, "bash", []string{"-lc", "printf 'hello world'"}, doctorExecContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "hello world" {
		t.Fatalf("expected captured output, got %q", output)
	}
}

func TestResolveCheckCommandLXCNoLongerSupported(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "bash", []string{"-lc", "echo ok"}, doctorExecContext{
		backend: "lxc",
		lxcName: "nexus-ws",
	})

	if cmd != "bash" {
		t.Fatalf("expected bash command (lxc no longer supported), got %q", cmd)
	}
	if label != "host" {
		t.Fatalf("expected label host, got %q", label)
	}
	if len(env) != 0 {
		t.Fatalf("expected no extra env, got %v", env)
	}
	if len(args) != 2 {
		t.Fatalf("expected original args, got %v", args)
	}
}

func TestResolveCheckCommandFirecracker(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "bash", []string{"-lc", "echo ok"}, doctorExecContext{
		backend: "firecracker",
	})

	if cmd != "bash" {
		t.Fatalf("expected bash command, got %q", cmd)
	}
	if label != "firecracker" {
		t.Fatalf("expected label firecracker, got %q", label)
	}
	if len(env) != 0 {
		t.Fatalf("expected no extra env, got %v", env)
	}
	if len(args) != 2 {
		t.Fatalf("expected original args, got %v", args)
	}
}

func TestResolveCheckCommandDindNoLongerSupported(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "docker", []string{"compose", "ps"}, doctorExecContext{
		backend:    "dind",
		dockerHost: "unix:///var/run/docker.sock",
	})

	if cmd != "docker" {
		t.Fatalf("expected docker command (dind no longer supported), got %q", cmd)
	}
	if label != "host" {
		t.Fatalf("expected label host, got %q", label)
	}
	if !reflect.DeepEqual(args, []string{"compose", "ps"}) {
		t.Fatalf("unexpected args: %v", args)
	}
	if len(env) != 0 {
		t.Fatalf("expected empty env, got %v", env)
	}
}

func TestResolveCheckCommandDindWithoutDockerHostNoLongerSupported(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "docker", []string{"compose", "ps"}, doctorExecContext{
		backend: "dind",
	})

	if cmd != "docker" {
		t.Fatalf("expected docker command (dind no longer supported), got %q", cmd)
	}
	if label != "host" {
		t.Fatalf("expected label host, got %q", label)
	}
	if !reflect.DeepEqual(args, []string{"compose", "ps"}) {
		t.Fatalf("unexpected args: %v", args)
	}
	if len(env) != 0 {
		t.Fatalf("expected empty env, got %v", env)
	}
}

func TestRunCheckCommandWithExecContextHostExportsUIDAndGID(t *testing.T) {
	out, err := runCheckCommandWithExecContext(
		context.Background(),
		t.TempDir(),
		"probe",
		"uid-gid-export",
		1,
		1,
		30*time.Second,
		"bash",
		[]string{"-lc", "env | grep -E '^(UID|GID)=' | sort"},
		doctorExecContext{backend: "host"},
	)
	if err != nil {
		t.Fatalf("expected env probe to succeed, got: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, fmt.Sprintf("UID=%d", os.Getuid())) {
		t.Fatalf("expected UID export in command env, got %q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("GID=%d", os.Getgid())) {
		t.Fatalf("expected GID export in command env, got %q", out)
	}
}

func TestRunCheckCommandWithExecContextFirecrackerUsesNativeRunner(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	root := t.TempDir()
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	called := false
	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		called = true
		if projectRoot != root {
			t.Fatalf("expected firecracker checks to use project root %q, got %q", root, projectRoot)
		}
		if command != "bash" {
			t.Fatalf("expected command bash, got %q", command)
		}
		if !reflect.DeepEqual(args, []string{"-lc", "echo ok"}) {
			t.Fatalf("unexpected args: %v", args)
		}
		return "ok", nil
	}

	out, err := runCheckCommandWithExecContext(
		context.Background(),
		root,
		"probe",
		"example",
		1,
		1,
		30*time.Second,
		"bash",
		[]string{"-lc", "echo ok"},
		doctorExecContext{backend: "firecracker"},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("expected firecracker native runner to be called")
	}
	if out != "ok" {
		t.Fatalf("expected output ok, got %q", out)
	}
}

func TestRunCheckCommandWithExecContextFirecrackerPropagatesMissingWorkdirError(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	called := false
	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		called = true
		return "chdir " + projectRoot + ": no such file or directory", errors.New("guest command failed")
	}

	root := t.TempDir()
	out, err := runCheckCommandWithExecContext(
		context.Background(),
		root,
		"probe",
		"missing-workdir",
		1,
		1,
		30*time.Second,
		"bash",
		[]string{"-lc", "printf host-ok"},
		doctorExecContext{backend: "firecracker"},
	)
	if err == nil {
		t.Fatal("expected firecracker probe to fail when guest workdir is missing")
	}
	if !called {
		t.Fatal("expected firecracker runner to be called")
	}
	if !strings.Contains(out, "chdir") {
		t.Fatalf("expected original guest error output, got %q", out)
	}
}

func TestRunCheckCommandWithExecContextFirecrackerPropagatesMissingBinaryError(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	called := false
	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		called = true
		return "exec: \"docker\": executable file not found in $PATH", errors.New("guest command failed")
	}

	root := t.TempDir()
	out, err := runCheckCommandWithExecContext(
		context.Background(),
		root,
		"probe",
		"missing-binary",
		1,
		1,
		30*time.Second,
		"bash",
		[]string{"-lc", "printf host-ok"},
		doctorExecContext{backend: "firecracker"},
	)
	if err == nil {
		t.Fatal("expected firecracker probe to fail when guest binary is missing")
	}
	if !called {
		t.Fatal("expected firecracker runner to be called")
	}
	if !strings.Contains(out, "executable file not found") {
		t.Fatalf("expected original guest missing binary output, got %q", out)
	}
}

func TestRunCheckCommandWithExecContextFirecrackerExecReturnsGuestFailure(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		return "chdir " + projectRoot + ": no such file or directory", errors.New("guest command failed")
	}

	root := t.TempDir()
	out, err := runCheckCommandWithExecContext(
		context.Background(),
		root,
		"exec",
		"bash",
		1,
		1,
		30*time.Second,
		"bash",
		[]string{"-lc", "printf host-ok"},
		doctorExecContext{backend: "firecracker"},
	)
	if err == nil {
		t.Fatal("expected firecracker exec failure")
	}
	if !strings.Contains(out, "chdir") {
		t.Fatalf("expected guest error output, got %q", out)
	}
}

func TestVerifyFirecrackerWorkspaceReadyPasses(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		if projectRoot != "/workspace" {
			t.Fatalf("expected workspace verification at /workspace, got %q", projectRoot)
		}
		if command != "sh" {
			t.Fatalf("expected sh command, got %q", command)
		}
		return "", nil
	}

	if err := verifyFirecrackerWorkspaceReady(); err != nil {
		t.Fatalf("expected workspace verification to pass, got %v", err)
	}
}

func TestVerifyFirecrackerWorkspaceReadyProvidesSetupGuidanceOnMissingWorkspace(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		return "chdir /workspace: no such file or directory", errors.New("guest command failed")
	}

	err := verifyFirecrackerWorkspaceReady()
	if err == nil {
		t.Fatal("expected workspace verification error")
	}
	if !strings.Contains(err.Error(), "nexus init --project-root <abs-path> --runtime firecracker --force") {
		t.Fatalf("expected setup guidance, got %v", err)
	}
}

func TestRunExecHostBackendNoLongerSupported(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "host")

	root := t.TempDir()
	err := runExec(execOptions{
		projectRoot: root,
		timeout:     15 * time.Second,
		command:     "bash",
		args:        []string{"-lc", "printf exec-ok"},
	})
	if err == nil {
		t.Fatal("expected error when NEXUS_RUNTIME_BACKEND=host")
	}
	if !strings.Contains(err.Error(), "unsupported runtime backend") {
		t.Fatalf("expected unsupported backend error, got: %v", err)
	}
}

func TestRunExecFirecrackerUsesGuestWorkspaceWorkdir(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	originalGOOS := firecrackerHostGOOS
	root := t.TempDir()

	originalBootstrap := firecrackerBootstrapRunner
	originalFirecrackerRunner := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerBootstrapRunner = originalBootstrap
		firecrackerCheckCommandRunner = originalFirecrackerRunner
	})

	firecrackerHostGOOS = "linux"

	firecrackerBootstrapRunner = func(projectRoot string, execCtx doctorExecContext) error {
		return nil
	}

	called := false
	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		called = true
		if projectRoot != root {
			t.Fatalf("expected firecracker exec to use project root %q, got %q", root, projectRoot)
		}
		if command == "docker" {
			return "", nil
		}
		if command != "pwd" {
			t.Fatalf("expected command pwd (or docker bootstrap checks), got %q", command)
		}
		return "/workspace\n", nil
	}

	err := runExec(execOptions{
		projectRoot: root,
		timeout:     15 * time.Second,
		command:     "pwd",
		args:        nil,
	})
	if err != nil {
		t.Fatalf("expected runExec firecracker to succeed, got %v", err)
	}
	if !called {
		t.Fatal("expected firecracker runner to be called")
	}
}

func TestRunExecSelectsBackendFromWorkspaceRuntimeWhenEnvUnset(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "")
	originalGOOS := firecrackerHostGOOS

	originalBootstrap := firecrackerBootstrapRunner
	originalFirecrackerRunner := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerBootstrapRunner = originalBootstrap
		firecrackerCheckCommandRunner = originalFirecrackerRunner
	})

	firecrackerHostGOOS = "linux"

	root := t.TempDir()
	nexusDir := filepath.Join(root, ".nexus")
	if err := os.MkdirAll(nexusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nexusDir, "workspace.json"), []byte(`{"version":1,"runtime":{"required":["firecracker"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	calledFirecrackerBootstrap := false
	firecrackerBootstrapRunner = func(projectRoot string, execCtx doctorExecContext) error {
		calledFirecrackerBootstrap = true
		if execCtx.backend != "firecracker" {
			t.Fatalf("expected backend firecracker from workspace runtime.required, got %q", execCtx.backend)
		}
		return nil
	}
	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		if projectRoot != root {
			t.Fatalf("expected firecracker runExec to use project root %q, got %q", root, projectRoot)
		}
		return "/workspace\n", nil
	}

	err := runExec(execOptions{
		projectRoot: root,
		timeout:     15 * time.Second,
		command:     "pwd",
	})
	if err != nil {
		t.Fatalf("expected runExec to succeed, got %v", err)
	}
	if !calledFirecrackerBootstrap {
		t.Fatal("expected firecracker backend bootstrap from workspace runtime.required")
	}
}

func TestRunExecReexecsWithSGKVMOnKVMPermissionError(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	t.Setenv(execKVMGroupReexecEnv, "")
	originalGOOS := firecrackerHostGOOS
	addFakeSGToPath(t)

	originalBootstrap := firecrackerBootstrapRunner
	originalReexec := execKVMGroupReexecRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerBootstrapRunner = originalBootstrap
		execKVMGroupReexecRunner = originalReexec
	})

	firecrackerHostGOOS = "linux"

	firecrackerBootstrapRunner = func(projectRoot string, execCtx doctorExecContext) error {
		return errors.New("firecracker requires read/write access to /dev/kvm")
	}
	called := false
	var gotArgs []string
	execKVMGroupReexecRunner = func(commandPath string, args []string) error {
		called = true
		gotArgs = append([]string(nil), args...)
		return nil
	}

	err := runExec(execOptions{
		projectRoot: t.TempDir(),
		timeout:     15 * time.Second,
		command:     "pwd",
	})
	if err != nil {
		t.Fatalf("expected runExec to succeed via sg kvm reexec, got %v", err)
	}
	if !called {
		t.Fatal("expected sg kvm reexec to be attempted")
	}
	if len(gotArgs) < 7 || gotArgs[0] != "exec" {
		t.Fatalf("unexpected reexec args: %v", gotArgs)
	}
}

func TestRunExecDoesNotReexecWithSGKVMWhenAlreadyReexeced(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	t.Setenv(execKVMGroupReexecEnv, "1")

	originalBootstrap := firecrackerBootstrapRunner
	originalReexec := execKVMGroupReexecRunner
	t.Cleanup(func() {
		firecrackerBootstrapRunner = originalBootstrap
		execKVMGroupReexecRunner = originalReexec
	})

	firecrackerBootstrapRunner = func(projectRoot string, execCtx doctorExecContext) error {
		return errors.New("firecracker requires read/write access to /dev/kvm")
	}
	called := false
	execKVMGroupReexecRunner = func(commandPath string, args []string) error {
		called = true
		return nil
	}

	err := runExec(execOptions{
		projectRoot: t.TempDir(),
		timeout:     15 * time.Second,
		command:     "pwd",
	})
	if err == nil {
		t.Fatal("expected runExec to return original kvm error without reexec")
	}
	if called {
		t.Fatal("did not expect sg kvm reexec when already in reexec environment")
	}
}

func TestRunDoctorReexecsWithSGKVMOnKVMPermissionError(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	t.Setenv(execKVMGroupReexecEnv, "")
	t.Setenv("NEXUS_DOCTOR_DISABLE_BUILTIN_CHECKS", "1")
	addFakeSGToPath(t)

	workspaceRoot := setupDoctorTestWorkspace(t, config.DoctorConfig{})

	originalBootstrap := doctorExecBootstrapRunner
	originalReexec := execKVMGroupReexecRunner
	originalArgs := os.Args
	t.Cleanup(func() {
		doctorExecBootstrapRunner = originalBootstrap
		execKVMGroupReexecRunner = originalReexec
		os.Args = originalArgs
	})

	doctorExecBootstrapRunner = func(projectRoot string) error {
		return errors.New("firecracker requires read/write access to /dev/kvm")
	}

	called := false
	execKVMGroupReexecRunner = func(commandPath string, args []string) error {
		called = true
		if commandPath == "" {
			t.Fatal("expected command path")
		}
		if len(args) == 0 || args[0] != "doctor" {
			t.Fatalf("expected doctor reexec args, got %v", args)
		}
		return nil
	}

	os.Args = []string{"nexus", "doctor", "--project-root", workspaceRoot, "--suite", "local"}

	err := run(options{projectRoot: workspaceRoot, suite: "local"})
	if err != nil {
		t.Fatalf("expected run to return nil after successful sg kvm reexec, got %v", err)
	}
	if !called {
		t.Fatal("expected sg kvm reexec for doctor command")
	}
}

func TestVerifyFirecrackerGuestDockerRuntime(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		if projectRoot != "/workspace" {
			t.Fatalf("expected /workspace project root, got %q", projectRoot)
		}
		if command != "sh" {
			t.Fatalf("expected sh command, got %q", command)
		}
		return "", nil
	}

	if err := verifyFirecrackerGuestDockerRuntime(); err != nil {
		t.Fatalf("expected guest docker runtime verification to pass, got %v", err)
	}
}

func TestVerifyFirecrackerGuestDockerRuntimeFailureMessage(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		return "docker: not found", errors.New("guest command failed")
	}

	err := verifyFirecrackerGuestDockerRuntime()
	if err == nil {
		t.Fatal("expected guest docker runtime verification failure")
	}
	if !strings.Contains(err.Error(), "host docker is not used") {
		t.Fatalf("expected explicit host docker guidance, got %v", err)
	}
}

func TestVerifyFirecrackerGuestDockerRuntimePassesWithSudoDockerInfo(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = original
	})

	firecrackerHostGOOS = "linux"

	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		if command != "sh" {
			t.Fatalf("expected sh command, got %q", command)
		}
		if len(args) < 2 || args[0] != "-lc" {
			t.Fatalf("unexpected args: %v", args)
		}
		if !strings.Contains(args[1], "sudo -n docker info") {
			t.Fatalf("expected sudo fallback in verify command, got: %s", args[1])
		}
		return "sudo docker info ok", nil
	}

	if err := verifyFirecrackerGuestDockerRuntime(); err != nil {
		t.Fatalf("expected guest docker runtime verification to pass with sudo fallback, got %v", err)
	}
}

func TestRunFirecrackerDoctorFailsFastWhenGuestDockerUnavailable(t *testing.T) {
	t.Setenv("NEXUS_DOCTOR_DISABLE_BUILTIN_CHECKS", "1")
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")

	root := t.TempDir()
	nexusDir := filepath.Join(root, ".nexus")
	lifecycleDir := filepath.Join(nexusDir, "lifecycles")
	if err := os.MkdirAll(lifecycleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"setup.sh", "start.sh", "teardown.sh"} {
		if err := os.WriteFile(filepath.Join(lifecycleDir, name), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wsCfg := config.WorkspaceConfig{Version: 1, Runtime: config.RuntimeConfig{Required: []string{"firecracker"}}}
	data, err := json.Marshal(wsCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nexusDir, "workspace.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	origBootstrap := doctorExecBootstrapRunner
	origVerify := doctorFirecrackerRuntimeVerifier
	t.Cleanup(func() {
		doctorExecBootstrapRunner = origBootstrap
		doctorFirecrackerRuntimeVerifier = origVerify
	})

	doctorExecBootstrapRunner = func(projectRoot string) error { return nil }
	doctorFirecrackerRuntimeVerifier = func() error { return errors.New("docker unavailable in guest") }

	err = run(options{projectRoot: root, suite: "local"})
	if err == nil {
		t.Fatal("expected run to fail fast when guest docker is unavailable")
	}
	if !strings.Contains(err.Error(), "docker unavailable in guest") {
		t.Fatalf("expected guest docker error, got %v", err)
	}
}

func TestSelectRuntimeBackend(t *testing.T) {
	if got := selectRuntimeBackend([]string{"local"}); got != "" {
		t.Fatalf("expected local->empty (unsupported), got %q", got)
	}
	if got := selectRuntimeBackend([]string{"vm"}); got != "firecracker" {
		t.Fatalf("expected vm->firecracker, got %q", got)
	}
	if got := selectRuntimeBackend([]string{"firecracker"}); got != "firecracker" {
		t.Fatalf("expected firecracker->firecracker, got %q", got)
	}
	if got := selectRuntimeBackend([]string{"lxc"}); got != "" {
		t.Fatalf("expected lxc->empty (unsupported), got %q", got)
	}
}

func TestApplyRuntimeBackendFromWorkspaceRejectsDind(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "dind")

	root := t.TempDir()
	nexusDir := filepath.Join(root, ".nexus")
	if err := os.MkdirAll(nexusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nexusDir, "workspace.json"), []byte(`{"version":1,"runtime":{"required":["firecracker"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	applyErr := applyRuntimeBackendFromWorkspace(root)
	if applyErr == nil {
		t.Fatal("expected error when NEXUS_RUNTIME_BACKEND=dind")
	}
	if !strings.Contains(applyErr.Error(), "unsupported runtime backend") {
		t.Fatalf("expected unsupported backend error, got: %v", applyErr)
	}
}

func TestApplyFirecrackerAssetDefaultsSetsKernelAndRootfsWhenPresent(t *testing.T) {
	originalStat := firecrackerDefaultAssetStat
	originalKernelPath := firecrackerDefaultKernelPath
	originalRootfsPath := firecrackerDefaultRootFSPath
	t.Cleanup(func() {
		firecrackerDefaultAssetStat = originalStat
		firecrackerDefaultKernelPath = originalKernelPath
		firecrackerDefaultRootFSPath = originalRootfsPath
	})

	t.Setenv("NEXUS_FIRECRACKER_KERNEL", "")
	t.Setenv("NEXUS_FIRECRACKER_ROOTFS", "")

	firecrackerDefaultKernelPath = "/fake/kernel"
	firecrackerDefaultRootFSPath = "/fake/rootfs"
	firecrackerDefaultAssetStat = func(path string) (os.FileInfo, error) {
		if path == "/fake/kernel" || path == "/fake/rootfs" {
			return fakeSocketFileInfo{name: filepath.Base(path)}, nil
		}
		return nil, os.ErrNotExist
	}

	applyFirecrackerAssetDefaults()

	if got := os.Getenv("NEXUS_FIRECRACKER_KERNEL"); got != "/fake/kernel" {
		t.Fatalf("expected default kernel path, got %q", got)
	}
	if got := os.Getenv("NEXUS_FIRECRACKER_ROOTFS"); got != "/fake/rootfs" {
		t.Fatalf("expected default rootfs path, got %q", got)
	}
}

func TestApplyFirecrackerAssetDefaultsDoesNotOverrideExistingEnv(t *testing.T) {
	originalStat := firecrackerDefaultAssetStat
	originalKernelPath := firecrackerDefaultKernelPath
	originalRootfsPath := firecrackerDefaultRootFSPath
	t.Cleanup(func() {
		firecrackerDefaultAssetStat = originalStat
		firecrackerDefaultKernelPath = originalKernelPath
		firecrackerDefaultRootFSPath = originalRootfsPath
	})

	t.Setenv("NEXUS_FIRECRACKER_KERNEL", "/already/kernel")
	t.Setenv("NEXUS_FIRECRACKER_ROOTFS", "/already/rootfs")

	firecrackerDefaultKernelPath = "/fake/kernel"
	firecrackerDefaultRootFSPath = "/fake/rootfs"
	firecrackerDefaultAssetStat = func(path string) (os.FileInfo, error) {
		return fakeSocketFileInfo{name: filepath.Base(path)}, nil
	}

	applyFirecrackerAssetDefaults()

	if got := os.Getenv("NEXUS_FIRECRACKER_KERNEL"); got != "/already/kernel" {
		t.Fatalf("expected existing kernel env to be preserved, got %q", got)
	}
	if got := os.Getenv("NEXUS_FIRECRACKER_ROOTFS"); got != "/already/rootfs" {
		t.Fatalf("expected existing rootfs env to be preserved, got %q", got)
	}
}

func TestBuildSetupScriptUsesLocalKernelCacheBeforeDownload(t *testing.T) {
	requireLinux(t)

	script := buildSetupScript("/tmp/nexus-tap-helper", "/tmp/nexus-firecracker-agent")

	if !strings.Contains(script, "if [ -f /tmp/nexus-vmlinux.bin ]; then") {
		t.Fatalf("expected setup script to check local kernel cache first, got:\n%s", script)
	}
	if !strings.Contains(script, "cp /tmp/nexus-vmlinux.bin /var/lib/nexus/vmlinux.bin") {
		t.Fatalf("expected setup script to copy local kernel cache when present, got:\n%s", script)
	}
}

func TestBuildSetupScriptUsesLocalSquashfsCacheBeforeDownload(t *testing.T) {
	requireLinux(t)

	script := buildSetupScript("/tmp/nexus-tap-helper", "/tmp/nexus-firecracker-agent")

	if !strings.Contains(script, "if [ -f /tmp/nexus-ubuntu.squashfs ]; then") {
		t.Fatalf("expected setup script to check local squashfs cache first, got:\n%s", script)
	}
	if !strings.Contains(script, "cp /tmp/nexus-ubuntu.squashfs \"$SQUASHFS_TMP/rootfs.squashfs\"") {
		t.Fatalf("expected setup script to copy local squashfs cache when present, got:\n%s", script)
	}
}

func TestBuildSetupScriptEnsuresSudoUserInKVMGroup(t *testing.T) {
	requireLinux(t)

	script := buildSetupScript("/tmp/nexus-tap-helper", "/tmp/nexus-firecracker-agent")

	if !strings.Contains(script, "if [ -n \"${SUDO_USER:-}\" ]; then") {
		t.Fatalf("expected setup script to check sudo user before kvm group update, got:\n%s", script)
	}
	if !strings.Contains(script, "usermod -aG kvm \"$SUDO_USER\"") {
		t.Fatalf("expected setup script to add sudo user to kvm group, got:\n%s", script)
	}
}

func TestBuildSetupScriptNormalizesVMAssetOwnershipForSudoUser(t *testing.T) {
	requireLinux(t)

	script := buildSetupScript("/tmp/nexus-tap-helper", "/tmp/nexus-firecracker-agent")

	if !strings.Contains(script, "chown \"$SUDO_USER\":\"$SUDO_USER\" /var/lib/nexus/vmlinux.bin") {
		t.Fatalf("expected setup script to chown kernel to sudo user, got:\n%s", script)
	}
	if !strings.Contains(script, "chown \"$SUDO_USER\":\"$SUDO_USER\" /var/lib/nexus/rootfs.ext4") {
		t.Fatalf("expected setup script to chown rootfs to sudo user, got:\n%s", script)
	}
	if !strings.Contains(script, "chmod 600 /var/lib/nexus/rootfs.ext4") {
		t.Fatalf("expected setup script to restrict rootfs mode for user-owned rw access, got:\n%s", script)
	}
}

func TestBuildSetupScriptUpdatesExistingRootfsAgentPayload(t *testing.T) {
	requireLinux(t)

	script := buildSetupScript("/tmp/nexus-tap-helper", "/tmp/nexus-firecracker-agent")

	if !strings.Contains(script, "if [ \"$ROOTFS_REBUILD\" -eq 0 ]; then") {
		t.Fatalf("expected setup script to handle existing rootfs agent update, got:\n%s", script)
	}
	if !strings.Contains(script, "cp /tmp/nexus-firecracker-agent \"$ROOTFS_MOUNT/usr/local/bin/nexus-firecracker-agent\"") {
		t.Fatalf("expected setup script to copy fresh agent into existing rootfs, got:\n%s", script)
	}
	if !strings.Contains(script, "mkdir -p \"$ROOTFS_MOUNT/workspace\"") {
		t.Fatalf("expected setup script to ensure /workspace exists in rootfs, got:\n%s", script)
	}
	if !strings.Contains(script, "for candidate in docker dockerd containerd containerd-shim-runc-v2 ctr runc docker-init docker-proxy iptables ip6tables; do") {
		t.Fatalf("expected setup script to seed docker runtime binaries into rootfs, got:\n%s", script)
	}
	if !strings.Contains(script, "copy_bin_with_libs") {
		t.Fatalf("expected setup script to include copy_bin_with_libs helper, got:\n%s", script)
	}
	if !strings.Contains(script, "printf '#!/bin/sh\\nexec /usr/local/bin/nexus-firecracker-agent\\n' > \"$ROOTFS_MOUNT/sbin/init\"") {
		t.Fatalf("expected setup script to rewrite init inside existing rootfs, got:\n%s", script)
	}
}

func TestBuildSetupScriptChecksBridgeRouteLinkdownNotLinkState(t *testing.T) {
	requireLinux(t)

	script := buildSetupScript("/tmp/nexus-tap-helper", "/tmp/nexus-firecracker-agent")

	if !strings.Contains(script, "if ! ip route show dev nexusbr0 | grep -q 'linkdown'; then") {
		t.Fatalf("expected setup script to check route linkdown status, got:\n%s", script)
	}
	if !strings.Contains(script, "WARN: nexusbr0 route still linkdown after setup") {
		t.Fatalf("expected setup script to emit linkdown warning, got:\n%s", script)
	}
	if !strings.Contains(script, "ip rule add pref 5190 to 172.26.0.0/16 lookup main") {
		t.Fatalf("expected setup script to add high-priority to-subnet policy rule, got:\n%s", script)
	}
	if !strings.Contains(script, "ip rule add pref 5191 from 172.26.0.0/16 lookup main") {
		t.Fatalf("expected setup script to add high-priority from-subnet policy rule, got:\n%s", script)
	}
}

// ---- setup firecracker tests ----

// TestDetectPrivilegeModeRoot verifies that detectPrivilegeMode returns
// privilegeModeRoot when EUID is 0.
func TestDetectPrivilegeModeRoot(t *testing.T) {
	requireLinux(t)

	mode := detectPrivilegeMode(true, false, false)
	if mode != privilegeModeRoot {
		t.Fatalf("expected privilegeModeRoot for EUID==0, got %v", mode)
	}
}

// TestDetectPrivilegeModeSudoN verifies that detectPrivilegeMode returns
// privilegeModeSudoN when sudo -n would succeed (CI passwordless sudo).
func TestDetectPrivilegeModeSudoN(t *testing.T) {
	requireLinux(t)

	mode := detectPrivilegeMode(false, true, false)
	if mode != privilegeModeSudoN {
		t.Fatalf("expected privilegeModeSudoN for passwordless sudo, got %v", mode)
	}
}

// TestDetectPrivilegeModeInteractive verifies that detectPrivilegeMode returns
// privilegeModeInteractive when stdin is a TTY.
func TestDetectPrivilegeModeInteractive(t *testing.T) {
	requireLinux(t)

	mode := detectPrivilegeMode(false, false, true)
	if mode != privilegeModeInteractive {
		t.Fatalf("expected privilegeModeInteractive for TTY stdin, got %v", mode)
	}
}

// TestDetectPrivilegeModeFallback verifies that detectPrivilegeMode returns
// privilegeModeManual when no privilege escalation path is available.
func TestDetectPrivilegeModeFallback(t *testing.T) {
	requireLinux(t)

	mode := detectPrivilegeMode(false, false, false)
	if mode != privilegeModeManual {
		t.Fatalf("expected privilegeModeManual for non-interactive no-sudo, got %v", mode)
	}
}

// TestSetupFirecrackerNonInteractivePrintsAndErrors verifies that
// runSetupFirecracker in non-interactive mode (privilegeModeManual)
// falls back to printing a sudo command when auto-sudo fails.
func TestSetupFirecrackerNonInteractivePrintsAndErrors(t *testing.T) {
	requireLinux(t)

	origMode := setupPrivilegeModeOverride
	origEnabled := setupPrivilegeModeOverrideEnabled
	t.Cleanup(func() {
		setupPrivilegeModeOverride = origMode
		setupPrivilegeModeOverrideEnabled = origEnabled
	})
	setupPrivilegeModeOverride = privilegeModeManual
	setupPrivilegeModeOverrideEnabled = true

	origBuild := setupBuildTapHelperFn
	t.Cleanup(func() { setupBuildTapHelperFn = origBuild })
	buildCalled := false
	setupBuildTapHelperFn = func() (string, error) {
		buildCalled = true
		return "/tmp/nexus-tap-helper", nil
	}

	origAgent := setupExtractAgentFn
	t.Cleanup(func() { setupExtractAgentFn = origAgent })
	agentCalled := false
	setupExtractAgentFn = func() (string, error) {
		agentCalled = true
		return "/tmp/nexus-firecracker-agent", nil
	}

	origSudo := setupSudoReexecFn
	t.Cleanup(func() { setupSudoReexecFn = origSudo })
	sudoCalled := false
	setupSudoReexecFn = func(commandPath string) error {
		sudoCalled = true
		return errors.New("sudo unavailable")
	}

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	setupVerifyFn = func() error { return errors.New("not setup") }

	var buf strings.Builder
	err := runSetupFirecracker(&buf)
	if err == nil {
		t.Fatal("expected error in non-interactive / manual mode")
	}
	if !strings.Contains(err.Error(), "manual") {
		t.Fatalf("expected error to mention manual steps, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sudo") || !strings.Contains(out, "setup firecracker") {
		t.Fatalf("expected output to contain 'sudo <nexus> setup firecracker', got: %q", out)
	}
	if !sudoCalled {
		t.Fatal("expected manual mode to attempt auto-sudo before fallback")
	}
	if buildCalled {
		t.Fatal("expected manual mode to skip tap-helper extraction")
	}
	if agentCalled {
		t.Fatal("expected manual mode to skip agent extraction")
	}
}

func TestSetupFirecrackerManualModeAutoSudoSuccess(t *testing.T) {
	requireLinux(t)

	origMode := setupPrivilegeModeOverride
	origEnabled := setupPrivilegeModeOverrideEnabled
	t.Cleanup(func() {
		setupPrivilegeModeOverride = origMode
		setupPrivilegeModeOverrideEnabled = origEnabled
	})
	setupPrivilegeModeOverride = privilegeModeManual
	setupPrivilegeModeOverrideEnabled = true

	origBuild := setupBuildTapHelperFn
	t.Cleanup(func() { setupBuildTapHelperFn = origBuild })
	setupBuildTapHelperFn = func() (string, error) {
		t.Fatal("did not expect tap-helper extraction in parent process during manual auto-sudo")
		return "", nil
	}

	origAgent := setupExtractAgentFn
	t.Cleanup(func() { setupExtractAgentFn = origAgent })
	setupExtractAgentFn = func() (string, error) {
		t.Fatal("did not expect agent extraction in parent process during manual auto-sudo")
		return "", nil
	}

	origSudo := setupSudoReexecFn
	t.Cleanup(func() { setupSudoReexecFn = origSudo })
	sudoCalled := false
	setupSudoReexecFn = func(commandPath string) error {
		sudoCalled = true
		if strings.TrimSpace(commandPath) == "" {
			t.Fatal("expected non-empty setup command path")
		}
		return nil
	}

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	verifyCalls := 0
	setupVerifyFn = func() error {
		verifyCalls++
		if verifyCalls == 1 {
			return errors.New("not setup yet")
		}
		return nil
	}

	var buf strings.Builder
	if err := runSetupFirecracker(&buf); err != nil {
		t.Fatalf("expected setup to succeed when auto-sudo succeeds, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Requesting sudo") {
		t.Fatalf("expected output to show auto-sudo attempt, got: %q", out)
	}
	if !strings.Contains(out, "Firecracker host setup complete") {
		t.Fatalf("expected success output after auto-sudo, got: %q", out)
	}
	if !sudoCalled {
		t.Fatal("expected manual mode to call auto-sudo function")
	}
}

func TestSetupFirecrackerManualModeRefreshesKVMGroupViaSG(t *testing.T) {
	requireLinux(t)
	t.Setenv("NEXUS_SETUP_KVM_GROUP_REEXEC", "")

	origMode := setupPrivilegeModeOverride
	origEnabled := setupPrivilegeModeOverrideEnabled
	t.Cleanup(func() {
		setupPrivilegeModeOverride = origMode
		setupPrivilegeModeOverrideEnabled = origEnabled
	})
	setupPrivilegeModeOverride = privilegeModeManual
	setupPrivilegeModeOverrideEnabled = true

	origSudo := setupSudoReexecFn
	t.Cleanup(func() { setupSudoReexecFn = origSudo })
	setupSudoReexecFn = func(commandPath string) error { return nil }

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	verifyCalls := 0
	setupVerifyFn = func() error {
		verifyCalls++
		if verifyCalls == 1 {
			return errors.New("not setup yet")
		}
		return fmt.Errorf("%w: pending group refresh", errKVMGroupRefreshNeeded)
	}

	origKVMReexec := setupKVMGroupReexecFn
	t.Cleanup(func() { setupKVMGroupReexecFn = origKVMReexec })
	kvmReexecCalled := false
	setupKVMGroupReexecFn = func(commandPath string) error {
		kvmReexecCalled = true
		if strings.TrimSpace(commandPath) == "" {
			t.Fatal("expected non-empty setup command path for sg reexec")
		}
		return nil
	}

	var buf strings.Builder
	if err := runSetupFirecracker(&buf); err != nil {
		t.Fatalf("expected setup to succeed when sg kvm reexec succeeds, got: %v", err)
	}
	if !kvmReexecCalled {
		t.Fatal("expected sg kvm reexec to be invoked")
	}
	if !strings.Contains(buf.String(), "Refreshing kvm group") {
		t.Fatalf("expected output to mention kvm group refresh, got: %q", buf.String())
	}
}

// TestSetupFirecrackerMockedSucceeds verifies that runSetupFirecracker
// succeeds when the script execution and verify steps are mocked to pass.
func TestSetupFirecrackerMockedSucceeds(t *testing.T) {
	requireLinux(t)

	origMode := setupPrivilegeModeOverride
	origEnabled := setupPrivilegeModeOverrideEnabled
	t.Cleanup(func() {
		setupPrivilegeModeOverride = origMode
		setupPrivilegeModeOverrideEnabled = origEnabled
	})
	setupPrivilegeModeOverride = privilegeModeRoot
	setupPrivilegeModeOverrideEnabled = true

	origBuild := setupBuildTapHelperFn
	t.Cleanup(func() { setupBuildTapHelperFn = origBuild })
	setupBuildTapHelperFn = func() (string, error) { return "/tmp/nexus-tap-helper", nil }

	origAgent := setupExtractAgentFn
	t.Cleanup(func() { setupExtractAgentFn = origAgent })
	setupExtractAgentFn = func() (string, error) { return "/tmp/nexus-firecracker-agent", nil }

	origRunScript := setupRunScriptFn
	t.Cleanup(func() { setupRunScriptFn = origRunScript })
	setupRunScriptFn = func(mode privilegeMode, script string) error {
		return nil
	}

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	setupVerifyFn = func() error { return nil }

	var buf strings.Builder
	if err := runSetupFirecracker(&buf); err != nil {
		t.Fatalf("expected setup to succeed with mocked steps, got: %v", err)
	}
}

func TestSetupFirecrackerManualModeReturnsSuccessWhenAlreadyVerified(t *testing.T) {
	requireLinux(t)

	origMode := setupPrivilegeModeOverride
	origEnabled := setupPrivilegeModeOverrideEnabled
	t.Cleanup(func() {
		setupPrivilegeModeOverride = origMode
		setupPrivilegeModeOverrideEnabled = origEnabled
	})
	setupPrivilegeModeOverride = privilegeModeManual
	setupPrivilegeModeOverrideEnabled = true

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	setupVerifyFn = func() error { return nil }

	origBuild := setupBuildTapHelperFn
	t.Cleanup(func() { setupBuildTapHelperFn = origBuild })
	setupBuildTapHelperFn = func() (string, error) {
		t.Fatal("did not expect tap-helper extraction when manual verify already passes")
		return "", nil
	}

	origAgent := setupExtractAgentFn
	t.Cleanup(func() { setupExtractAgentFn = origAgent })
	setupExtractAgentFn = func() (string, error) {
		t.Fatal("did not expect agent extraction when manual verify already passes")
		return "", nil
	}

	var buf strings.Builder
	if err := runSetupFirecracker(&buf); err != nil {
		t.Fatalf("expected setup to report success when verify passes in manual mode, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Firecracker host setup complete") {
		t.Fatalf("expected success output, got: %q", out)
	}
}

func TestSetupFirecrackerInteractiveModeSkipsSudoWhenAlreadyVerified(t *testing.T) {
	requireLinux(t)

	origMode := setupPrivilegeModeOverride
	origEnabled := setupPrivilegeModeOverrideEnabled
	t.Cleanup(func() {
		setupPrivilegeModeOverride = origMode
		setupPrivilegeModeOverrideEnabled = origEnabled
	})
	setupPrivilegeModeOverride = privilegeModeInteractive
	setupPrivilegeModeOverrideEnabled = true

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	setupVerifyFn = func() error { return nil }

	origBuild := setupBuildTapHelperFn
	t.Cleanup(func() { setupBuildTapHelperFn = origBuild })
	setupBuildTapHelperFn = func() (string, error) {
		t.Fatal("did not expect tap-helper extraction when setup is already verified")
		return "", nil
	}

	origAgent := setupExtractAgentFn
	t.Cleanup(func() { setupExtractAgentFn = origAgent })
	setupExtractAgentFn = func() (string, error) {
		t.Fatal("did not expect agent extraction when setup is already verified")
		return "", nil
	}

	origRunScript := setupRunScriptFn
	t.Cleanup(func() { setupRunScriptFn = origRunScript })
	setupRunScriptFn = func(mode privilegeMode, script string) error {
		t.Fatal("did not expect sudo script execution when setup is already verified")
		return nil
	}

	var buf strings.Builder
	if err := runSetupFirecracker(&buf); err != nil {
		t.Fatalf("expected setup to return success when already configured, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Firecracker host setup complete") {
		t.Fatalf("expected success output, got: %q", out)
	}
}

func TestSetupFirecrackerForceRefreshBypassesAlreadyConfiguredShortCircuit(t *testing.T) {
	requireLinux(t)

	t.Setenv("NEXUS_SETUP_FIRECRACKER_FORCE", "1")

	origMode := setupPrivilegeModeOverride
	origEnabled := setupPrivilegeModeOverrideEnabled
	t.Cleanup(func() {
		setupPrivilegeModeOverride = origMode
		setupPrivilegeModeOverrideEnabled = origEnabled
	})
	setupPrivilegeModeOverride = privilegeModeRoot
	setupPrivilegeModeOverrideEnabled = true

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	verifyCalls := 0
	setupVerifyFn = func() error {
		verifyCalls++
		return nil
	}

	origBuild := setupBuildTapHelperFn
	t.Cleanup(func() { setupBuildTapHelperFn = origBuild })
	setupBuildTapHelperFn = func() (string, error) {
		f, err := os.CreateTemp("", "tap-helper-force-*")
		if err != nil {
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if err := os.Chmod(f.Name(), 0o755); err != nil {
			return "", err
		}
		return f.Name(), nil
	}

	origAgent := setupExtractAgentFn
	t.Cleanup(func() { setupExtractAgentFn = origAgent })
	setupExtractAgentFn = func() (string, error) {
		f, err := os.CreateTemp("", "agent-force-*")
		if err != nil {
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if err := os.Chmod(f.Name(), 0o755); err != nil {
			return "", err
		}
		return f.Name(), nil
	}

	ranScript := false
	origRunScript := setupRunScriptFn
	t.Cleanup(func() { setupRunScriptFn = origRunScript })
	setupRunScriptFn = func(mode privilegeMode, script string) error {
		ranScript = true
		return nil
	}

	var buf strings.Builder
	if err := runSetupFirecracker(&buf); err != nil {
		t.Fatalf("expected setup success, got: %v", err)
	}
	if !ranScript {
		t.Fatal("expected setup script execution when force-refresh is enabled")
	}
	if verifyCalls < 2 {
		t.Fatalf("expected verify to run before and after setup, got %d calls", verifyCalls)
	}
}

func TestSetupFirecrackerUsesUniqueTempAgentPath(t *testing.T) {
	requireLinux(t)

	origMode := setupPrivilegeModeOverride
	origEnabled := setupPrivilegeModeOverrideEnabled
	t.Cleanup(func() {
		setupPrivilegeModeOverride = origMode
		setupPrivilegeModeOverrideEnabled = origEnabled
	})
	setupPrivilegeModeOverride = privilegeModeRoot
	setupPrivilegeModeOverrideEnabled = true

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	setupVerifyFn = func() error { return errors.New("not configured") }

	origBuild := setupBuildTapHelperFn
	t.Cleanup(func() { setupBuildTapHelperFn = origBuild })
	setupBuildTapHelperFn = func() (string, error) {
		f, err := os.CreateTemp("", "tap-helper-test-*")
		if err != nil {
			return "", err
		}
		if _, err := f.WriteString("tap"); err != nil {
			_ = f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if err := os.Chmod(f.Name(), 0o755); err != nil {
			return "", err
		}
		return f.Name(), nil
	}

	origAgent := setupExtractAgentFn
	t.Cleanup(func() { setupExtractAgentFn = origAgent })
	setupExtractAgentFn = func() (string, error) {
		f, err := os.CreateTemp("", "agent-test-*")
		if err != nil {
			return "", err
		}
		if _, err := f.WriteString("agent"); err != nil {
			_ = f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if err := os.Chmod(f.Name(), 0o755); err != nil {
			return "", err
		}
		return f.Name(), nil
	}

	var seenScript string
	origRunScript := setupRunScriptFn
	t.Cleanup(func() { setupRunScriptFn = origRunScript })
	setupRunScriptFn = func(mode privilegeMode, script string) error {
		seenScript = script
		return nil
	}

	origFinalVerify := setupVerifyFn
	verifyCalls := 0
	setupVerifyFn = func() error {
		verifyCalls++
		if verifyCalls == 1 {
			return errors.New("not configured")
		}
		return nil
	}
	t.Cleanup(func() { setupVerifyFn = origFinalVerify })

	var buf strings.Builder
	if err := runSetupFirecracker(&buf); err != nil {
		t.Fatalf("expected setup success, got: %v", err)
	}

	if strings.Contains(seenScript, "/tmp/nexus-firecracker-agent") {
		t.Fatalf("expected unique temp agent path in setup script, got fixed path: %q", seenScript)
	}
}

func TestBootstrapDoctorExecContextFirecrackerUsesNativeBootstrap(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")

	original := firecrackerBootstrapRunner
	originalContainerBootstrap := containerBootstrapRunner
	t.Cleanup(func() {
		firecrackerBootstrapRunner = original
		containerBootstrapRunner = originalContainerBootstrap
	})

	called := false
	containerCalled := false
	firecrackerBootstrapRunner = func(projectRoot string, execCtx doctorExecContext) error {
		called = true
		if execCtx.backend != "firecracker" {
			t.Fatalf("expected firecracker backend, got %q", execCtx.backend)
		}
		if projectRoot == "" {
			t.Fatal("project root should not be empty")
		}
		return nil
	}
	containerBootstrapRunner = func(projectRoot string, execCtx doctorExecContext, backendLabel string, allowInstall bool) error {
		containerCalled = true
		if execCtx.backend != "firecracker" {
			t.Fatalf("expected firecracker container bootstrap backend, got %q", execCtx.backend)
		}
		if backendLabel != "firecracker" {
			t.Fatalf("expected backend label firecracker, got %q", backendLabel)
		}
		if !allowInstall {
			t.Fatal("expected allowInstall=true for firecracker runtime bootstrap")
		}
		return nil
	}

	if err := bootstrapDoctorExecContext(t.TempDir()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("expected firecracker bootstrap runner to be called")
	}
	if !containerCalled {
		t.Fatal("expected firecracker container bootstrap runner to be called")
	}
}

func TestBootstrapFirecrackerExecContextUsesDarwinBootstrap(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	originalDarwinFn := bootstrapFirecrackerExecContextDarwinFn
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		bootstrapFirecrackerExecContextDarwinFn = originalDarwinFn
	})

	firecrackerHostGOOS = "darwin"
	darwinCalled := false
	bootstrapFirecrackerExecContextDarwinFn = func(projectRoot string, execCtx doctorExecContext) error {
		darwinCalled = true
		return nil
	}

	err := bootstrapFirecrackerExecContext(t.TempDir(), doctorExecContext{backend: "firecracker"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !darwinCalled {
		t.Fatal("expected darwin bootstrap function to be called on darwin host")
	}
}

func TestBootstrapFirecrackerExecContextUsesNativeBootstrapOnLinux(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	originalBootstrapRunner := firecrackerBootstrapRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerBootstrapRunner = originalBootstrapRunner
	})

	firecrackerHostGOOS = "linux"
	nativeCalled := false
	firecrackerBootstrapRunner = func(projectRoot string, execCtx doctorExecContext) error {
		nativeCalled = true
		return nil
	}

	err := bootstrapFirecrackerExecContext(t.TempDir(), doctorExecContext{backend: "firecracker"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !nativeCalled {
		t.Fatal("expected native bootstrap function to be called on linux host")
	}
}

func TestRunFirecrackerCheckCommandForHostDarwin(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	originalLimaFn := runLimaCheckCommandFn
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		runLimaCheckCommandFn = originalLimaFn
	})

	firecrackerHostGOOS = "darwin"
	limaCalled := false
	runLimaCheckCommandFn = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		limaCalled = true
		if command != "bash" {
			t.Fatalf("expected bash command, got %q", command)
		}
		return "lima-output", nil
	}

	out, err := runFirecrackerCheckCommandForHost(context.Background(), "/workspace", "bash", []string{"-lc", "echo test"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !limaCalled {
		t.Fatal("expected lima check command function to be called on darwin host")
	}
	if out != "lima-output" {
		t.Fatalf("expected lima output, got %q", out)
	}
}

func TestRunFirecrackerCheckCommandForHostLinux(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	originalNativeFn := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		firecrackerCheckCommandRunner = originalNativeFn
	})

	firecrackerHostGOOS = "linux"
	nativeCalled := false
	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		nativeCalled = true
		if command != "bash" {
			t.Fatalf("expected bash command, got %q", command)
		}
		return "native-output", nil
	}

	out, err := runFirecrackerCheckCommandForHost(context.Background(), "/workspace", "bash", []string{"-lc", "echo test"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !nativeCalled {
		t.Fatal("expected native check command function to be called on linux host")
	}
	if out != "native-output" {
		t.Fatalf("expected native output, got %q", out)
	}
}

func TestRunCheckCommandWithExecContextUsesHostDispatchForFirecracker(t *testing.T) {
	originalGOOS := firecrackerHostGOOS
	originalLimaFn := runLimaCheckCommandFn
	t.Cleanup(func() {
		firecrackerHostGOOS = originalGOOS
		runLimaCheckCommandFn = originalLimaFn
	})

	firecrackerHostGOOS = "darwin"
	runLimaCheckCommandFn = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		if projectRoot != "/tmp/project" {
			t.Fatalf("expected project root /tmp/project, got %q", projectRoot)
		}
		if command != "bash" {
			t.Fatalf("expected bash command, got %q", command)
		}
		return "darwin-dispatch-output", nil
	}

	out, err := runCheckCommandWithExecContext(
		context.Background(),
		"/tmp/project",
		"probe",
		"dispatch-check",
		1,
		1,
		30*time.Second,
		"bash",
		[]string{"-lc", "echo ok"},
		doctorExecContext{backend: "firecracker"},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if out != "darwin-dispatch-output" {
		t.Fatalf("expected darwin dispatch output, got %q", out)
	}
}
