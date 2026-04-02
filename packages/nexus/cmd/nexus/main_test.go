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
	"strings"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/compose"
	"github.com/inizio/nexus/packages/nexus/pkg/config"
)

type fakeSocketFileInfo struct {
	name string
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
		Runtime: config.RuntimeConfig{Required: []string{"local"}},
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

func TestRunCheckCommandWithExecContextLXCNoInstanceFails(t *testing.T) {
	_, err := runCheckCommandWithExecContext(
		context.Background(),
		t.TempDir(),
		"probe",
		"example",
		1,
		1,
		30*time.Second,
		"bash",
		[]string{"-lc", "exit 0"},
		doctorExecContext{backend: "lxc", lxcExec: "host"},
	)
	if err == nil {
		t.Fatal("expected error when lxc backend has no instance")
	}
}

func TestResolveCheckCommandLXC(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "bash", []string{"-lc", "echo ok"}, doctorExecContext{
		backend: "lxc",
		lxcName: "nexus-ws",
	})

	if cmd != "lxc" {
		t.Fatalf("expected lxc command, got %q", cmd)
	}
	if label != "lxc" {
		t.Fatalf("expected label lxc, got %q", label)
	}
	if len(env) != 0 {
		t.Fatalf("expected no extra env, got %v", env)
	}
	if len(args) != 6 {
		t.Fatalf("expected wrapped lxc args, got %v", args)
	}
	if args[0] != "exec" || args[1] != "nexus-ws" {
		t.Fatalf("unexpected lxc prefix args: %v", args)
	}
	if args[5] == "" || !strings.Contains(args[5], "cd") || !strings.Contains(args[5], "/tmp/project") {
		t.Fatalf("unexpected wrapped shell command: %q", args[5])
	}
}

func TestResolveCheckCommandLXCSudo(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "bash", []string{"-lc", "echo ok"}, doctorExecContext{
		backend: "lxc",
		lxcName: "nexus-ws",
		lxcExec: "sudo-lxc",
	})

	if cmd != "sudo" {
		t.Fatalf("expected sudo command, got %q", cmd)
	}
	if label != "lxc-sudo" {
		t.Fatalf("expected label lxc-sudo, got %q", label)
	}
	if len(env) != 0 {
		t.Fatalf("expected no extra env, got %v", env)
	}
	if len(args) < 8 {
		t.Fatalf("expected wrapped sudo lxc args, got %v", args)
	}
	if args[0] != "-n" || args[1] != "lxc" || args[2] != "exec" || args[3] != "nexus-ws" {
		t.Fatalf("unexpected sudo lxc prefix args: %v", args)
	}
}

func TestResolveCheckCommandDind(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "docker", []string{"compose", "ps"}, doctorExecContext{
		backend:    "dind",
		dockerHost: "unix:///var/run/docker.sock",
	})

	if cmd != "docker" {
		t.Fatalf("expected docker command, got %q", cmd)
	}
	if label != "dind" {
		t.Fatalf("expected label dind, got %q", label)
	}
	if !reflect.DeepEqual(args, []string{"compose", "ps"}) {
		t.Fatalf("unexpected args: %v", args)
	}
	if !reflect.DeepEqual(env, []string{"DOCKER_HOST=unix:///var/run/docker.sock"}) {
		t.Fatalf("unexpected env: %v", env)
	}
}

func TestResolveCheckCommandDindWithoutDockerHost(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "docker", []string{"compose", "ps"}, doctorExecContext{
		backend: "dind",
	})

	if cmd != "docker" {
		t.Fatalf("expected docker command, got %q", cmd)
	}
	if label != "dind" {
		t.Fatalf("expected label dind, got %q", label)
	}
	if !reflect.DeepEqual(args, []string{"compose", "ps"}) {
		t.Fatalf("unexpected args: %v", args)
	}
	if len(env) != 0 {
		t.Fatalf("expected empty env, got %v", env)
	}
}

func TestRunCheckCommandWithExecContextFirecrackerRejectsHostContext(t *testing.T) {
	_, err := runCheckCommandWithExecContext(
		context.Background(),
		t.TempDir(),
		"probe",
		"example",
		1,
		1,
		30*time.Second,
		"bash",
		[]string{"-lc", "echo ok"},
		doctorExecContext{backend: "firecracker"},
	)
	if err == nil {
		t.Fatal("expected error when firecracker resolves to host context")
	}
	if !strings.Contains(err.Error(), "resolved to host execution context") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveCheckCommandFirecrackerUsesMicroVMContext(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "bash", []string{"-lc", "echo ok"}, doctorExecContext{
		backend: "firecracker",
		fcName:  "nexus-firecracker-ci",
		fcExec:  "sudo-lxc",
	})

	if cmd != "sudo" {
		t.Fatalf("expected sudo command, got %q", cmd)
	}
	if label != "firecracker-microvm" {
		t.Fatalf("expected firecracker-microvm label, got %q", label)
	}
	if len(env) != 0 {
		t.Fatalf("expected no extra env, got %v", env)
	}
	if len(args) < 8 {
		t.Fatalf("expected wrapped sudo lxc args, got %v", args)
	}
}

func TestBootstrapDoctorExecContextFirecrackerRequiresExplicitMicroVMContext(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE", "")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", "")

	err := bootstrapDoctorExecContext(t.TempDir())
	if err == nil {
		t.Fatal("expected bootstrap error when firecracker microVM context is missing")
	}
	if !strings.Contains(err.Error(), "requires explicit microVM execution context") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBootstrapDoctorExecContextFirecrackerAcceptsExplicitMicroVMContext(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	originalHostRunner := firecrackerHostCommandRunner
	originalInstallRunner := bootstrapInstallCommandRunner
	originalBinaryLookup := hostBinaryLookup
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
		firecrackerHostCommandRunner = originalHostRunner
		bootstrapInstallCommandRunner = originalInstallRunner
		hostBinaryLookup = originalBinaryLookup
		setDoctorExecContextCleanup(nil)
	})

	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE", "nexus-firecracker-ci")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", "sudo-lxc")

	hostToolDir := t.TempDir()
	binDir := filepath.Join(hostToolDir, "bin")
	mustMkdirAll(t, binDir)
	mustWriteExec(t, filepath.Join(binDir, "docker"), "#!/usr/bin/env sh\nexit 0\n")
	mustWriteExec(t, filepath.Join(binDir, "node"), "#!/usr/bin/env sh\nexit 0\n")
	mustWriteExec(t, filepath.Join(binDir, "opencode"), "#!/usr/bin/env sh\nexit 0\n")
	mustMkdirAll(t, filepath.Join(hostToolDir, "lib", "node_modules", "opencode-ai", "bin"))
	mustWriteExec(t, filepath.Join(hostToolDir, "lib", "node_modules", "opencode-ai", "bin", "opencode"), "#!/usr/bin/env sh\nexit 0\n")

	hostBinaryLookup = func(name string) (string, error) {
		switch name {
		case "docker", "node", "opencode":
			return filepath.Join(binDir, name), nil
		default:
			return "", errors.New("not found")
		}
	}

	firecrackerHostCommandRunner = func(ctx context.Context, execCtx doctorExecContext, args ...string) (string, error) {
		if execCtx.backend != "firecracker" {
			return "", fmt.Errorf("unexpected backend: %s", execCtx.backend)
		}
		if execCtx.fcName != "nexus-firecracker-ci" {
			return "", fmt.Errorf("unexpected firecracker instance: %s", execCtx.fcName)
		}
		if len(args) == 0 {
			return "", nil
		}
		switch args[0] {
		case "info", "delete", "launch", "config", "exec", "file":
			return "ok", nil
		default:
			return "", fmt.Errorf("unexpected host command: %v", args)
		}
	}

	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if execCtx.backend != "firecracker" {
			return "", fmt.Errorf("unexpected exec context: %+v", execCtx)
		}

		if command == "docker" && reflect.DeepEqual(args, []string{"info"}) {
			return "docker ok", nil
		}
		if command == "docker" && reflect.DeepEqual(args, []string{"compose", "version"}) {
			return "compose ok", nil
		}
		if command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "registry-1.docker.io") {
			return "401", nil
		}
		if command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "command -v opencode") {
			return "opencode-ai 1.0.0", nil
		}
		if command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "cat > /etc/resolv.conf") {
			return "dns configured", nil
		}

		return "ok", nil
	}

	err := bootstrapDoctorExecContext(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := runDoctorExecContextCleanup(); err != nil {
		t.Fatalf("unexpected cleanup error: %v", err)
	}
}

func TestBootstrapDoctorExecContextFirecrackerLaunchFailsWhenHostUnavailable(t *testing.T) {
	originalHostRunner := firecrackerHostCommandRunner
	t.Cleanup(func() {
		firecrackerHostCommandRunner = originalHostRunner
		setDoctorExecContextCleanup(nil)
	})

	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE", "nexus-firecracker-ci")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", "sudo-lxc")

	firecrackerHostCommandRunner = func(ctx context.Context, execCtx doctorExecContext, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "info" {
			return "sudo: a password is required", errors.New("host unavailable")
		}
		return "", nil
	}

	err := bootstrapDoctorExecContext(t.TempDir())
	if err == nil {
		t.Fatal("expected error when firecracker host is unavailable")
	}
	if !strings.Contains(err.Error(), "firecracker host bootstrap failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBootstrapDoctorExecContextFirecrackerFailsOnRegistryReadiness(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	originalHostRunner := firecrackerHostCommandRunner
	originalInstallRunner := bootstrapInstallCommandRunner
	originalBinaryLookup := hostBinaryLookup
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
		firecrackerHostCommandRunner = originalHostRunner
		bootstrapInstallCommandRunner = originalInstallRunner
		hostBinaryLookup = originalBinaryLookup
		setDoctorExecContextCleanup(nil)
	})

	t.Setenv("NEXUS_RUNTIME_BACKEND", "firecracker")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE", "nexus-firecracker-ci")
	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE", "sudo-lxc")

	hostToolDir := t.TempDir()
	binDir := filepath.Join(hostToolDir, "bin")
	mustMkdirAll(t, binDir)
	mustWriteExec(t, filepath.Join(binDir, "docker"), "#!/usr/bin/env sh\nexit 0\n")
	mustWriteExec(t, filepath.Join(binDir, "node"), "#!/usr/bin/env sh\nexit 0\n")
	mustWriteExec(t, filepath.Join(binDir, "opencode"), "#!/usr/bin/env sh\nexit 0\n")
	mustMkdirAll(t, filepath.Join(hostToolDir, "lib", "node_modules", "opencode-ai", "bin"))
	mustWriteExec(t, filepath.Join(hostToolDir, "lib", "node_modules", "opencode-ai", "bin", "opencode"), "#!/usr/bin/env sh\nexit 0\n")

	hostBinaryLookup = func(name string) (string, error) {
		switch name {
		case "docker", "node", "opencode":
			return filepath.Join(binDir, name), nil
		default:
			return "", errors.New("not found")
		}
	}

	firecrackerHostCommandRunner = func(ctx context.Context, execCtx doctorExecContext, args ...string) (string, error) {
		if len(args) == 0 {
			return "", nil
		}
		switch args[0] {
		case "info", "delete", "launch", "config", "exec", "file":
			return "ok", nil
		default:
			return "", fmt.Errorf("unexpected host command: %v", args)
		}
	}

	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if command == "docker" && reflect.DeepEqual(args, []string{"info"}) {
			return "docker ok", nil
		}
		if command == "docker" && reflect.DeepEqual(args, []string{"compose", "version"}) {
			return "compose ok", nil
		}
		if command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "registry-1.docker.io") {
			return "context deadline exceeded", errors.New("registry unavailable")
		}
		if command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "cat > /etc/resolv.conf") {
			return "dns configured", nil
		}
		if command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "--- /etc/resolv.conf ---") {
			return "--- /etc/resolv.conf ---\nnameserver 1.1.1.1", nil
		}
		return "ok", nil
	}

	err := bootstrapDoctorExecContext(t.TempDir())
	if err == nil {
		t.Fatal("expected firecracker registry readiness failure")
	}
	if !strings.Contains(err.Error(), "firecracker network readiness failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureFirecrackerRegistryReadinessUsesIPv4CurlFallback(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	var capturedCheckCmd string
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if name != "firecracker-network-readiness" {
			return "ok", nil
		}
		if command != "bash" || len(args) != 2 || args[0] != "-lc" {
			return "", fmt.Errorf("unexpected command: %s %v", command, args)
		}

		if strings.Contains(args[1], "curl") {
			capturedCheckCmd = args[1]
			return "401", nil
		}

		return "diagnostics", nil
	}

	err := ensureFirecrackerRegistryReadiness(t.TempDir(), doctorExecContext{backend: "firecracker", fcName: "nexus-firecracker-ci", fcExec: "sudo-lxc"})
	if err != nil {
		t.Fatalf("expected readiness check to pass, got %v", err)
	}
	if capturedCheckCmd == "" {
		t.Fatal("expected to capture readiness check command")
	}
	if !strings.Contains(capturedCheckCmd, "curl -4") {
		t.Fatalf("expected readiness check to include IPv4 curl fallback, got %q", capturedCheckCmd)
	}
}

func TestResolveCheckCommandHostFallback(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "bash", []string{"-lc", "echo ok"}, doctorExecContext{backend: "lxc"})
	if cmd != "bash" {
		t.Fatalf("expected host command fallback, got %q", cmd)
	}
	if label != "host" {
		t.Fatalf("expected host label, got %q", label)
	}
	if !reflect.DeepEqual(args, []string{"-lc", "echo ok"}) {
		t.Fatalf("unexpected args: %v", args)
	}
	if len(env) != 0 {
		t.Fatalf("expected no env in host fallback, got %v", env)
	}
}

func TestResolveCheckCommandLXCWithInstanceUsesLXCContext(t *testing.T) {
	cmd, args, env, label := resolveCheckCommand("/tmp/project", "bash", []string{"-lc", "echo ok"}, doctorExecContext{
		backend: "lxc",
		lxcName: "nexus-ws",
	})

	if cmd != "lxc" {
		t.Fatalf("expected lxc command, got %q", cmd)
	}
	if label != "lxc" {
		t.Fatalf("expected lxc label, got %q", label)
	}
	if len(env) != 0 {
		t.Fatalf("expected no env, got %v", env)
	}
	if len(args) < 6 {
		t.Fatalf("expected wrapped lxc args, got %v", args)
	}
}

func TestRunConfiguredProbesFailsWhenLXCExecContextMissing(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")
	t.Setenv("NEXUS_DOCTOR_LXC_INSTANCE", "")
	t.Setenv("NEXUS_DOCTOR_LXC_EXEC_MODE", "")

	opts := options{projectRoot: t.TempDir()}
	_, err := runConfiguredProbes(opts, []config.DoctorCommandProbe{{
		Name:     "probe-needs-lxc-context",
		Command:  "bash",
		Args:     []string{"-lc", "exit 0"},
		Required: true,
	}})
	if err == nil {
		t.Fatal("expected required probe failure when lxc execution context is missing")
	}
	if !strings.Contains(err.Error(), "required probes failed") {
		t.Fatalf("expected required probes failed error, got %q", err.Error())
	}
}

func TestBuiltInOpencodeSessionCheckRunsWithoutModelFlagWhenUnset(t *testing.T) {
	fakeBinDir := t.TempDir()
	fakeOpencodePath := filepath.Join(fakeBinDir, "opencode")
	fakeScript := "#!/usr/bin/env bash\n" +
		"set -euo pipefail\n" +
		"if [ \"${1:-}\" = \"--version\" ]; then\n" +
		"  echo 'fake-opencode 0.0.1'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"${1:-}\" = \"run\" ] && [ \"${2:-}\" = \"--help\" ]; then\n" +
		"  echo 'help output'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"${1:-}\" = \"run\" ] && [ \"${2:-}\" != \"--model\" ]; then\n" +
		"  echo 'NEXUS_DOCTOR_OK'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unexpected args' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeOpencodePath, []byte(fakeScript), 0o755); err != nil {
		t.Fatalf("failed to create fake opencode binary: %v", err)
	}

	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NEXUS_DOCTOR_OPENCODE_MODEL", "")

	result, err := runBuiltInOpencodeSessionCheck(t.TempDir())
	if err != nil {
		t.Fatalf("expected opencode check to pass without model, got %v", err)
	}
	if result.Name != "tooling-opencode-session" {
		t.Fatalf("unexpected check name: %q", result.Name)
	}
	if result.Status != "passed" {
		t.Fatalf("expected status passed, got %q", result.Status)
	}
}

func TestBuiltInOpencodeSessionCheckSkipsOnLXCBackend(t *testing.T) {
	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")

	result, err := runBuiltInOpencodeSessionCheck(t.TempDir())
	if err != nil {
		t.Fatalf("expected opencode check to skip on lxc backend, got %v", err)
	}
	if result.Status != "not_run" {
		t.Fatalf("expected status not_run, got %q", result.Status)
	}
	if result.SkipReason == "" {
		t.Fatal("expected skip reason for lxc backend")
	}
}

func TestBuiltInRuntimeBackendCheckLXCBackendRunsLXCInfoAndDockerChecks(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	type call struct {
		command string
		args    []string
		execCtx doctorExecContext
	}

	calls := make([]call, 0)
	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		calls = append(calls, call{command: command, args: append([]string{}, args...), execCtx: execCtx})
		switch {
		case command == "lxc" && reflect.DeepEqual(args, []string{"info"}):
			return "lxd ok", nil
		case command == "docker" && reflect.DeepEqual(args, []string{"info"}):
			return "docker ok", nil
		case command == "docker" && reflect.DeepEqual(args, []string{"compose", "version"}):
			return "compose ok", nil
		default:
			return "", fmt.Errorf("unexpected command: %s %v", command, args)
		}
	}

	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")
	t.Setenv("NEXUS_DOCTOR_LXC_INSTANCE", "nexus-ws")
	t.Setenv("NEXUS_DOCTOR_LXC_EXEC_MODE", "sudo-lxc")

	result, err := runBuiltInRuntimeBackendCheck()
	if err != nil {
		t.Fatalf("expected runtime backend check to pass on lxc backend, got %v", err)
	}
	if result.Name != "runtime-backend-capabilities" {
		t.Fatalf("unexpected check name: %q", result.Name)
	}
	if result.Status != "passed" {
		t.Fatalf("expected status passed, got %q", result.Status)
	}

	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}

	if calls[0].command != "lxc" || !reflect.DeepEqual(calls[0].args, []string{"info"}) {
		t.Fatalf("expected first call to be lxc info, got %s %v", calls[0].command, calls[0].args)
	}
	if calls[1].command != "docker" || !reflect.DeepEqual(calls[1].args, []string{"info"}) {
		t.Fatalf("expected second call to be docker info, got %s %v", calls[1].command, calls[1].args)
	}
	if calls[2].command != "docker" || !reflect.DeepEqual(calls[2].args, []string{"compose", "version"}) {
		t.Fatalf("expected third call to be docker compose version, got %s %v", calls[2].command, calls[2].args)
	}

	for i := 1; i < len(calls); i++ {
		if calls[i].execCtx.backend != "lxc" || calls[i].execCtx.lxcName != "nexus-ws" {
			t.Fatalf("expected lxc exec context for docker checks, got %+v", calls[i].execCtx)
		}
	}
}

func TestBootstrapDoctorExecContextLXCStartsDockerWhenInfoFails(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")
	t.Setenv("NEXUS_DOCTOR_LXC_INSTANCE", "nexus-ws")
	t.Setenv("NEXUS_DOCTOR_LXC_EXEC_MODE", "sudo-lxc")

	var calls []string
	dockerInfoCalls := 0
	startCalls := 0

	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		calls = append(calls, command+" "+strings.Join(args, " "))
		if execCtx.backend != "lxc" || execCtx.lxcName != "nexus-ws" {
			return "", fmt.Errorf("unexpected exec context: %+v", execCtx)
		}

		switch {
		case command == "docker" && reflect.DeepEqual(args, []string{"info"}):
			dockerInfoCalls++
			if dockerInfoCalls == 1 {
				return "Cannot connect to the Docker daemon", errors.New("docker daemon unavailable")
			}
			return "docker ok", nil
		case command == "docker" && reflect.DeepEqual(args, []string{"compose", "version"}):
			return "compose ok", nil
		case command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "systemctl enable docker"):
			startCalls++
			return "started", nil
		default:
			return "", fmt.Errorf("unexpected command: %s %v", command, args)
		}
	}

	if err := bootstrapDoctorExecContext(t.TempDir()); err != nil {
		t.Fatalf("expected bootstrapDoctorExecContext to recover docker daemon, got %v", err)
	}

	if startCalls != 1 {
		t.Fatalf("expected one docker startup attempt, got %d", startCalls)
	}
	if dockerInfoCalls != 2 {
		t.Fatalf("expected two docker info attempts, got %d", dockerInfoCalls)
	}

	for _, call := range calls {
		if strings.Contains(call, "apt-get update") {
			t.Fatalf("did not expect apt-get install path, got call %q", call)
		}
	}
}

func TestBootstrapContainerExecContextFirecrackerHostProxyDoesNotFallbackToGuestDockerd(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
	})

	t.Setenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE", "host-proxy")

	startCalls := 0
	hostProxyChecks := 0

	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if execCtx.backend != "firecracker" {
			return "", fmt.Errorf("unexpected backend: %s", execCtx.backend)
		}

		if command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "systemctl enable docker") {
			startCalls++
			return "guest start attempted", nil
		}

		if command == "docker" && reflect.DeepEqual(args, []string{"info"}) {
			hostProxyChecks++
			return "ERROR: error during connect", errors.New("proxy unavailable")
		}

		if command == "docker" && reflect.DeepEqual(args, []string{"compose", "version"}) {
			return "compose ok", nil
		}

		return "ok", nil
	}

	err := bootstrapContainerExecContext(t.TempDir(), doctorExecContext{backend: "firecracker", fcName: "nexus-firecracker-ci", fcExec: "sudo-lxc"}, "firecracker", false)
	if err == nil {
		t.Fatal("expected host-proxy bootstrap failure when docker info stays unavailable")
	}
	if !strings.Contains(err.Error(), "host-proxy docker mode unavailable") {
		t.Fatalf("expected host-proxy unavailable error, got %v", err)
	}
	if hostProxyChecks == 0 {
		t.Fatal("expected host-proxy checks to run")
	}
	if startCalls != 0 {
		t.Fatalf("expected no guest dockerd startup attempts, got %d", startCalls)
	}
}

func TestBootstrapDoctorExecContextLXCInstallsWhenStartDoesNotRecover(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	originalInstallRunner := bootstrapInstallCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
		bootstrapInstallCommandRunner = originalInstallRunner
	})

	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")
	t.Setenv("NEXUS_DOCTOR_LXC_INSTANCE", "nexus-ws")
	t.Setenv("NEXUS_DOCTOR_LXC_EXEC_MODE", "sudo-lxc")

	dockerInfoCalls := 0
	startCalls := 0
	installCalls := 0

	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if execCtx.backend != "lxc" || execCtx.lxcName != "nexus-ws" {
			return "", fmt.Errorf("unexpected exec context: %+v", execCtx)
		}

		switch {
		case command == "docker" && reflect.DeepEqual(args, []string{"info"}):
			dockerInfoCalls++
			if dockerInfoCalls < 3 {
				return "Cannot connect to the Docker daemon", errors.New("docker daemon unavailable")
			}
			return "docker ok", nil
		case command == "docker" && reflect.DeepEqual(args, []string{"compose", "version"}):
			return "compose ok", nil
		case command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "apt-get update"):
			installCalls++
			return "installed", nil
		case command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "systemctl enable docker"):
			startCalls++
			return "started", nil
		default:
			return "", fmt.Errorf("unexpected command: %s %v", command, args)
		}
	}

	if err := bootstrapDoctorExecContext(t.TempDir()); err != nil {
		t.Fatalf("expected bootstrapDoctorExecContext to install and recover docker daemon, got %v", err)
	}

	if installCalls != 1 {
		t.Fatalf("expected one install attempt, got %d", installCalls)
	}
	if startCalls != 2 {
		t.Fatalf("expected two start attempts, got %d", startCalls)
	}
	if dockerInfoCalls != 3 {
		t.Fatalf("expected three docker info attempts, got %d", dockerInfoCalls)
	}
}

func TestBootstrapDoctorExecContextLXCToleratesAPTDNSFailure(t *testing.T) {
	originalRunner := doctorCheckCommandRunner
	originalInstallRunner := bootstrapInstallCommandRunner
	t.Cleanup(func() {
		doctorCheckCommandRunner = originalRunner
		bootstrapInstallCommandRunner = originalInstallRunner
	})

	t.Setenv("NEXUS_RUNTIME_BACKEND", "lxc")
	t.Setenv("NEXUS_DOCTOR_LXC_INSTANCE", "nexus-ws")
	t.Setenv("NEXUS_DOCTOR_LXC_EXEC_MODE", "sudo-lxc")

	dockerInfoCalls := 0
	startCalls := 0
	installCalls := 0

	doctorCheckCommandRunner = func(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
		if execCtx.backend != "lxc" || execCtx.lxcName != "nexus-ws" {
			return "", fmt.Errorf("unexpected exec context: %+v", execCtx)
		}

		switch {
		case command == "docker" && reflect.DeepEqual(args, []string{"info"}):
			dockerInfoCalls++
			if dockerInfoCalls < 3 {
				return "Cannot connect to the Docker daemon", errors.New("docker daemon unavailable")
			}
			return "docker ok", nil
		case command == "docker" && reflect.DeepEqual(args, []string{"compose", "version"}):
			if dockerInfoCalls < 3 {
				return "compose unavailable", errors.New("compose unavailable")
			}
			return "compose ok", nil
		case command == "bash" && len(args) == 2 && args[0] == "-lc" && strings.Contains(args[1], "systemctl enable docker"):
			startCalls++
			return "started", nil
		default:
			return "", fmt.Errorf("unexpected command: %s %v", command, args)
		}
	}

	bootstrapInstallCommandRunner = func(ctx context.Context, projectRoot string, timeout time.Duration, execCtx doctorExecContext) (string, error) {
		installCalls++
		return "W: Failed to fetch http://archive.ubuntu.com/ubuntu/dists/noble/InRelease  Temporary failure resolving 'archive.ubuntu.com'\nE: Package 'docker.io' has no installation candidate", errors.New("apt failed")
	}

	if err := bootstrapDoctorExecContext(t.TempDir()); err != nil {
		t.Fatalf("expected bootstrapDoctorExecContext to tolerate apt dns failure when runtime recovers, got %v", err)
	}

	if installCalls != 1 {
		t.Fatalf("expected one install attempt, got %d", installCalls)
	}
	if startCalls != 2 {
		t.Fatalf("expected two start attempts, got %d", startCalls)
	}
	if dockerInfoCalls != 3 {
		t.Fatalf("expected three docker info attempts, got %d", dockerInfoCalls)
	}
}
