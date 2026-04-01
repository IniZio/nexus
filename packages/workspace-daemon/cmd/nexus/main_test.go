package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/compose"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/config"
)

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

func TestBuiltInOpencodeSessionCheckFallbackWithoutModel(t *testing.T) {
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
		"if [ \"${1:-}\" = \"run\" ] && [ \"${2:-}\" = \"--model\" ] && [ \"${3:-}\" = \"bigpickle\" ]; then\n" +
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
		t.Fatalf("expected fallback model check to pass, got %v", err)
	}
	if result.Name != "tooling-opencode-session" {
		t.Fatalf("unexpected check name: %q", result.Name)
	}
	if result.Status != "passed" {
		t.Fatalf("expected status passed, got %q", result.Status)
	}
}
