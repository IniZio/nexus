package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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

func TestRunCheckCommandWithExecContextFirecrackerUsesNativeRunner(t *testing.T) {
	original := firecrackerCheckCommandRunner
	t.Cleanup(func() {
		firecrackerCheckCommandRunner = original
	})

	called := false
	firecrackerCheckCommandRunner = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		called = true
		if projectRoot == "" {
			t.Fatal("project root should not be empty")
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

// ---- setup firecracker tests ----

// TestDetectPrivilegeModeRoot verifies that detectPrivilegeMode returns
// privilegeModeRoot when EUID is 0.
func TestDetectPrivilegeModeRoot(t *testing.T) {
	mode := detectPrivilegeMode(true, false, false)
	if mode != privilegeModeRoot {
		t.Fatalf("expected privilegeModeRoot for EUID==0, got %v", mode)
	}
}

// TestDetectPrivilegeModeSudoN verifies that detectPrivilegeMode returns
// privilegeModeSudoN when sudo -n would succeed (CI passwordless sudo).
func TestDetectPrivilegeModeSudoN(t *testing.T) {
	mode := detectPrivilegeMode(false, true, false)
	if mode != privilegeModeSudoN {
		t.Fatalf("expected privilegeModeSudoN for passwordless sudo, got %v", mode)
	}
}

// TestDetectPrivilegeModeInteractive verifies that detectPrivilegeMode returns
// privilegeModeInteractive when stdin is a TTY.
func TestDetectPrivilegeModeInteractive(t *testing.T) {
	mode := detectPrivilegeMode(false, false, true)
	if mode != privilegeModeInteractive {
		t.Fatalf("expected privilegeModeInteractive for TTY stdin, got %v", mode)
	}
}

// TestDetectPrivilegeModeFallback verifies that detectPrivilegeMode returns
// privilegeModeManual when no privilege escalation path is available.
func TestDetectPrivilegeModeFallback(t *testing.T) {
	mode := detectPrivilegeMode(false, false, false)
	if mode != privilegeModeManual {
		t.Fatalf("expected privilegeModeManual for non-interactive no-sudo, got %v", mode)
	}
}

// TestSetupFirecrackerNonInteractivePrintsAndErrors verifies that
// runSetupFirecracker in non-interactive mode (privilegeModeManual)
// prints the manual commands and returns a non-nil error.
func TestSetupFirecrackerNonInteractivePrintsAndErrors(t *testing.T) {
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
	setupBuildTapHelperFn = func() (string, error) { return "/tmp/nexus-tap-helper", nil }

	var buf strings.Builder
	err := runSetupFirecracker(&buf)
	if err == nil {
		t.Fatal("expected error in non-interactive / manual mode")
	}
	if !strings.Contains(err.Error(), "manual") {
		t.Fatalf("expected error to mention manual steps, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sudo") {
		t.Fatalf("expected output to contain sudo commands, got: %q", out)
	}
}

// TestSetupFirecrackerMockedSucceeds verifies that runSetupFirecracker
// succeeds when all privileged steps are mocked to pass.
func TestSetupFirecrackerMockedSucceeds(t *testing.T) {
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

	origPrivileged := setupRunPrivilegedFn
	t.Cleanup(func() { setupRunPrivilegedFn = origPrivileged })
	setupRunPrivilegedFn = func(w interface{ Write([]byte) (int, error) }, mode privilegeMode, args ...string) error {
		return nil
	}

	origWriteFile := setupWriteFileFn
	t.Cleanup(func() { setupWriteFileFn = origWriteFile })
	setupWriteFileFn = func(mode privilegeMode, dest string, content []byte) error { return nil }

	origVerify := setupVerifyFn
	t.Cleanup(func() { setupVerifyFn = origVerify })
	setupVerifyFn = func() error { return nil }

	var buf strings.Builder
	if err := runSetupFirecracker(&buf); err != nil {
		t.Fatalf("expected setup to succeed with mocked steps, got: %v", err)
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
			t.Fatalf("expected container bootstrap firecracker backend, got %q", execCtx.backend)
		}
		if backendLabel != "firecracker" {
			t.Fatalf("expected backend label firecracker, got %q", backendLabel)
		}
		if !allowInstall {
			t.Fatal("expected allowInstall=true for firecracker bootstrap")
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
