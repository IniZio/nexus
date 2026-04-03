package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/compose"
	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/firecracker"
)

type options struct {
	projectRoot       string
	suite             string
	composeFile       string
	requiredHostPorts []int
	reportJSON        string
}

func main() {
	if len(os.Args) == 1 {
		printUsage()
		os.Exit(2)
	}

	command := os.Args[1]
	args := os.Args[2:]
	if strings.HasPrefix(command, "-") {
		command = "doctor"
		args = os.Args[1:]
	}

	switch command {
	case "setup":
		runSetupCommand(args)
		return
	case "doctor":
		// handled below
	default:
		printUsage()
		fmt.Fprintf(os.Stderr, "\nunknown subcommand: %s\n", command)
		os.Exit(2)
	}

	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectRoot := fs.String("project-root", "", "absolute path to downstream project repository")
	suite := fs.String("suite", "", "doctor suite name")
	composeFile := fs.String("compose-file", "docker-compose.yml", "compose file path relative to project root")
	requiredPorts := fs.String("required-host-ports", "", "comma-separated required published host ports (defaults to workspace config doctor.requiredHostPorts)")
	reportJSON := fs.String("report-json", "", "optional path to write doctor probe results as JSON")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *projectRoot == "" || *suite == "" {
		fmt.Fprintln(os.Stderr, "--project-root and --suite are required")
		os.Exit(2)
	}

	var ports []int
	if strings.TrimSpace(*requiredPorts) != "" {
		parsedPorts, err := parseRequiredPorts(*requiredPorts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		ports = parsedPorts
	}

	if err := run(options{
		projectRoot:       *projectRoot,
		suite:             *suite,
		composeFile:       *composeFile,
		requiredHostPorts: ports,
		reportJSON:        strings.TrimSpace(*reportJSON),
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  nexus doctor --project-root <abs-path> --suite <name> [--compose-file docker-compose.yml] [--required-host-ports 5173,5174,8000] [--report-json path]")
	fmt.Fprintln(os.Stderr, "  nexus setup firecracker")
}

// runSetupCommand dispatches the `nexus setup` subcommand.
func runSetupCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: nexus setup firecracker")
		os.Exit(2)
	}
	sub := args[0]
	if sub != "firecracker" {
		fmt.Fprintf(os.Stderr, "unknown setup subcommand: %s\n", sub)
		fmt.Fprintln(os.Stderr, "Usage: nexus setup firecracker")
		os.Exit(2)
	}
	if err := runSetupFirecracker(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if !filepath.IsAbs(opts.projectRoot) {
		return fmt.Errorf("project root must be absolute: %s", opts.projectRoot)
	}

	// Validate firecracker env contract before proceeding
	if err := config.ValidateFirecrackerEnv(); err != nil {
		return fmt.Errorf("firecracker configuration error: %w", err)
	}

	if opts.composeFile == "" {
		opts.composeFile = "docker-compose.yml"
	}

	requiredFiles := []string{
		filepath.Join(opts.projectRoot, ".nexus", "workspace.json"),
	}

	for _, p := range requiredFiles {
		if _, err := os.Stat(p); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("missing required file: %s", p)
			}
			return fmt.Errorf("stat %s: %w", p, err)
		}
	}

	if err := validateLifecycleEntrypoints(opts.projectRoot); err != nil {
		return err
	}

	if err := assertNoManualACP(filepath.Join(opts.projectRoot, ".nexus", "lifecycles")); err != nil {
		return err
	}

	if err := ensureDotEnv(opts.projectRoot); err != nil {
		return err
	}

	if os.Getenv("GLITCHTIP_DSN") == "" {
		_ = os.Setenv("GLITCHTIP_DSN", "placeholder")
	}

	workspaceConfig, _, err := config.LoadWorkspaceConfig(opts.projectRoot)
	if err != nil {
		return fmt.Errorf("invalid workspace config: %w", err)
	}

	probesToRun, testsToRun, warnings, err := resolveDoctorChecks(opts.projectRoot, workspaceConfig.Doctor.Probes, workspaceConfig.Doctor.Tests)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		fmt.Printf("doctor warning: %s\n", warning)
	}

	defer func() {
		if cleanupErr := runDoctorExecContextCleanup(); cleanupErr != nil {
			fmt.Printf("doctor warning: firecracker cleanup failed: %v\n", cleanupErr)
		}
	}()
	if err := bootstrapDoctorExecContext(opts.projectRoot); err != nil {
		return err
	}

	opts = applyDoctorConfigDefaults(opts, workspaceConfig.Doctor)

	publishedPorts := make([]compose.PublishedPort, 0)
	composePath := filepath.Join(opts.projectRoot, opts.composeFile)
	if _, err := os.Stat(composePath); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		publishedPorts, err = compose.DiscoverPublishedPorts(ctx, opts.projectRoot)
		if err != nil {
			return fmt.Errorf("compose discovery failed: %w", err)
		}
		if len(opts.requiredHostPorts) > 0 {
			missing := missingRequiredPorts(opts.requiredHostPorts, publishedPorts)
			if len(missing) > 0 {
				return fmt.Errorf("missing required host ports: %v", missing)
			}
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if len(opts.requiredHostPorts) > 0 {
			return fmt.Errorf("compose file not found but required host ports are configured: %s", composePath)
		}
		fmt.Printf("compose file not found, skipping compose port checks: %s\n", composePath)
	} else {
		return fmt.Errorf("stat compose file %s: %w", composePath, err)
	}

	probeResults, probeErr := runConfiguredProbes(opts, probesToRun)

	var allResults []checkResult

	if os.Getenv("NEXUS_DOCTOR_DISABLE_BUILTIN_CHECKS") != "1" {
		runtimeResult, runtimeErr := runBuiltInRuntimeBackendCheck()
		allResults = append(allResults, runtimeResult)
		probeErr = combineCheckErrors(runtimeErr, probeErr)
	}

	allResults = append(allResults, probeResults...)

	testResults, testErr := runConfiguredTests(opts, testsToRun)
	allResults = append(allResults, testResults...)

	if os.Getenv("NEXUS_DOCTOR_DISABLE_BUILTIN_CHECKS") != "1" {
		builtinResult, builtinErr := runBuiltInOpencodeSessionCheck(opts.projectRoot)
		allResults = append(allResults, builtinResult)
		testErr = combineCheckErrors(testErr, builtinErr)
	}

	if err := writeReport(opts.reportJSON, allResults); err != nil {
		return err
	}

	err = combineCheckErrors(probeErr, testErr)
	if err != nil {
		return err
	}

	fmt.Printf("doctor suite passed: %s (discovered %d compose ports)\n", opts.suite, len(publishedPorts))
	return nil
}

func applyDoctorConfigDefaults(opts options, doctorCfg config.DoctorConfig) options {
	if len(opts.requiredHostPorts) == 0 && len(doctorCfg.RequiredHostPorts) > 0 {
		opts.requiredHostPorts = append([]int(nil), doctorCfg.RequiredHostPorts...)
	}
	return opts
}

type checkResult struct {
	Name       string `json:"name"`
	Phase      string `json:"phase"`
	Status     string `json:"status"`
	Required   bool   `json:"required"`
	Attempts   int    `json:"attempts"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
}

type doctorExecContext struct {
	backend    string
	dockerHost string
	lxcName    string
	lxcExec    string
}

type firecrackerDoctorSession struct {
	workspaceID string
	manager     *firecracker.Manager
	vsockPath   string
	serialLog   string
}

var doctorCheckCommandRunner = runCheckCommandWithExecContext

var bootstrapInstallCommandRunner = runBootstrapInstallCommand

var firecrackerCheckCommandRunner = runFirecrackerCheckCommand

var firecrackerBootstrapRunner = bootstrapFirecrackerExecContextNative

var containerBootstrapRunner = bootstrapContainerExecContext

var doctorExecCleanup func() error

var firecrackerDoctorSessionState *firecrackerDoctorSession

var hostDockerSocketStat = os.Stat

var firecrackerHostBinaryLookup = exec.LookPath

var firecrackerHostStat = os.Stat

var firecrackerHostOpenFile = os.OpenFile

var firecrackerHostGOOS = runtime.GOOS

// firecrackerTapHelperValidator validates the tap helper binary.
// Overridable in tests.
var firecrackerTapHelperValidator = func() error { return validateFirecrackerTapHelper() }

// firecrackerBridgeValidator validates that nexusbr0 exists and is UP.
// Overridable in tests.
var firecrackerBridgeValidator = func() error { return validateFirecrackerBridge() }

func runBootstrapInstallCommand(ctx context.Context, projectRoot string, timeout time.Duration, execCtx doctorExecContext) (string, error) {
	installCmd := "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y bash docker.io docker-compose-v2 curl make python3 git nodejs npm || DEBIAN_FRONTEND=noninteractive apt-get install -y bash docker.io docker-compose-plugin curl make python3 git nodejs npm"
	return doctorCheckCommandRunner(ctx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "sh", []string{"-lc", installCmd}, execCtx)
}

func setDoctorExecContextCleanup(cleanup func() error) {
	doctorExecCleanup = cleanup
}

func runDoctorExecContextCleanup() error {
	if doctorExecCleanup == nil {
		return nil
	}
	cleanup := doctorExecCleanup
	doctorExecCleanup = nil
	return cleanup()
}

func setFirecrackerDoctorSession(session *firecrackerDoctorSession) {
	firecrackerDoctorSessionState = session
}

func clearFirecrackerDoctorSession() {
	firecrackerDoctorSessionState = nil
}

func getFirecrackerDoctorSession() (*firecrackerDoctorSession, error) {
	if firecrackerDoctorSessionState == nil {
		return nil, errors.New("firecracker execution context is not initialized")
	}
	return firecrackerDoctorSessionState, nil
}

func waitForFirecrackerAgent(vsockSocketPath string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	port := firecrackerAgentVSockPort()

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", vsockSocketPath, 1*time.Second)
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}

		if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
			_ = conn.Close()
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}

		resp, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			_ = conn.Close()
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}

		resp = strings.TrimSpace(resp)
		if !strings.HasPrefix(resp, "OK") {
			_ = conn.Close()
			lastErr = fmt.Errorf("vsock CONNECT failed: %s", resp)
			time.Sleep(25 * time.Millisecond)
			continue
		}

		return conn, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("agent was not ready after %s on vsock port %d: %w", timeout, port, lastErr)
	}
	return nil, fmt.Errorf("agent was not ready after %s on vsock port %d", timeout, port)
}

func firecrackerAgentVSockPort() uint32 {
	raw := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_AGENT_VSOCK_PORT"))
	if raw == "" {
		return firecracker.DefaultAgentVSockPort
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return firecracker.DefaultAgentVSockPort
	}

	return uint32(parsed)
}

func firecrackerRequestID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func readFileTail(path string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 4096
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	return strings.TrimSpace(string(data))
}

func validateFirecrackerHostPrerequisites(execCtx doctorExecContext) error {
	if execCtx.backend != "firecracker" {
		return nil
	}

	if firecrackerHostGOOS != "linux" {
		return fmt.Errorf("firecracker backend requires Linux with KVM; current host OS is %s (run doctor inside a Linux VM or CI)", firecrackerHostGOOS)
	}

	firecrackerBin := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_BIN"))
	if firecrackerBin == "" {
		firecrackerBin = "firecracker"
	}

	if _, err := firecrackerHostBinaryLookup(firecrackerBin); err != nil {
		return fmt.Errorf("firecracker binary %q not found in PATH; install Firecracker or set NEXUS_FIRECRACKER_BIN", firecrackerBin)
	}

	kernelPath := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_KERNEL"))
	if kernelPath == "" {
		return errors.New("missing NEXUS_FIRECRACKER_KERNEL for firecracker backend")
	}
	if _, err := firecrackerHostStat(kernelPath); err != nil {
		return fmt.Errorf("firecracker kernel not accessible at %q: %w", kernelPath, err)
	}

	rootfsPath := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_ROOTFS"))
	if rootfsPath == "" {
		return errors.New("missing NEXUS_FIRECRACKER_ROOTFS for firecracker backend")
	}
	if _, err := firecrackerHostStat(rootfsPath); err != nil {
		return fmt.Errorf("firecracker rootfs not accessible at %q: %w", rootfsPath, err)
	}

	kvmDevice := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_KVM_DEVICE"))
	if kvmDevice == "" {
		kvmDevice = "/dev/kvm"
	}

	fd, err := firecrackerHostOpenFile(kvmDevice, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("firecracker requires read/write access to %s; add current user to kvm group and re-login", kvmDevice)
		}
		return fmt.Errorf("firecracker KVM device check failed for %s: %w", kvmDevice, err)
	}
	_ = fd.Close()

	if err := firecrackerTapHelperValidator(); err != nil {
		return err
	}

	if err := firecrackerBridgeValidator(); err != nil {
		return err
	}

	return nil
}

// validateFirecrackerTapHelper verifies that nexus-tap-helper is installed and
// has cap_net_admin so Firecracker can create TAP devices without sudo.
func validateFirecrackerTapHelper() error {
	tapHelper := "nexus-tap-helper"
	path, err := firecrackerHostBinaryLookup(tapHelper)
	if err != nil {
		return fmt.Errorf(
			"%s not found in PATH\n\nRun the one-time host setup:\n  nexus setup firecracker",
			tapHelper,
		)
	}

	// Best-effort: verify cap_net_admin via getcap (skip if getcap unavailable).
	out, err := exec.Command("getcap", path).Output()
	if err != nil {
		// getcap not available — cannot verify, proceed and let runtime fail if needed.
		return nil
	}
	if !strings.Contains(string(out), "cap_net_admin") {
		return fmt.Errorf(
			"%s at %s lacks cap_net_admin\n\nRun the one-time host setup:\n  nexus setup firecracker",
			tapHelper, path,
		)
	}
	return nil
}

// validateFirecrackerBridge verifies that the nexusbr0 bridge exists and is UP
// so that TAP devices can be attached to it at VM spawn time.
func validateFirecrackerBridge() error {
	const bridge = "nexusbr0"
	out, err := exec.Command("ip", "link", "show", bridge).CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"bridge %s not found\n\nRun the one-time host setup:\n  nexus setup firecracker",
			bridge,
		)
	}
	if !strings.Contains(string(out), "UP") {
		return fmt.Errorf(
			"bridge %s exists but is not UP\n\nRun the one-time host setup:\n  nexus setup firecracker",
			bridge,
		)
	}
	return nil
}

func bootstrapFirecrackerExecContextNative(projectRoot string, execCtx doctorExecContext) error {
	if execCtx.backend != "firecracker" {
		return fmt.Errorf("invalid backend for firecracker bootstrap: %s", execCtx.backend)
	}

	if err := validateFirecrackerHostPrerequisites(execCtx); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	kernelPath := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_KERNEL"))
	rootfsPath := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_ROOTFS"))
	firecrackerBin := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_BIN"))
	if firecrackerBin == "" {
		firecrackerBin = "firecracker"
	}

	workDirRoot := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_FIRECRACKER_WORKDIR_ROOT"))
	if workDirRoot == "" {
		workDirRoot = filepath.Join(os.TempDir(), "nexus-firecracker-doctor")
	}
	if err := os.MkdirAll(workDirRoot, 0o755); err != nil {
		return fmt.Errorf("create firecracker workdir root: %w", err)
	}

	manager := firecracker.NewManager(firecracker.ManagerConfig{
		FirecrackerBin: firecrackerBin,
		KernelPath:     kernelPath,
		RootFSPath:     rootfsPath,
		WorkDirRoot:    workDirRoot,
	})

	workspaceID := fmt.Sprintf("doctor-%d", time.Now().UnixNano())
	instance, err := manager.Spawn(ctx, firecracker.SpawnSpec{
		WorkspaceID: workspaceID,
		ProjectRoot: projectRoot,
		MemoryMiB:   1024,
		VCPUs:       1,
	})
	if err != nil {
		return fmt.Errorf("bootstrap firecracker manager spawn failed: %w", err)
	}

	agentConn, err := waitForFirecrackerAgent(instance.VSockPath, 60*time.Second)
	if err != nil {
		logTail := readFileTail(instance.SerialLog, 262144)
		_ = manager.Stop(context.Background(), workspaceID)
		if logTail != "" {
			return fmt.Errorf("bootstrap firecracker agent connection failed: %w\nfirecracker serial log tail:\n%s", err, logTail)
		}
		return fmt.Errorf("bootstrap firecracker agent connection failed: %w", err)
	}

	session := &firecrackerDoctorSession{
		workspaceID: workspaceID,
		manager:     manager,
		vsockPath:   instance.VSockPath,
		serialLog:   instance.SerialLog,
	}
	setFirecrackerDoctorSession(session)
	_ = agentConn.Close()

	setDoctorExecContextCleanup(func() error {
		clearFirecrackerDoctorSession()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if session.manager != nil {
			if err := session.manager.Stop(stopCtx, session.workspaceID); err != nil {
				return fmt.Errorf("stop firecracker workspace %s: %w", session.workspaceID, err)
			}
		}
		return nil
	})

	return nil
}

func runFirecrackerCheckCommand(ctx context.Context, projectRoot, command string, args []string) (string, error) {
	session, err := getFirecrackerDoctorSession()
	if err != nil {
		return "", err
	}

	conn, err := waitForFirecrackerAgent(session.vsockPath, 10*time.Second)
	if err != nil {
		if logTail := readFileTail(session.serialLog, 262144); logTail != "" {
			return "", fmt.Errorf("%w\nfirecracker serial log tail:\n%s", err, logTail)
		}
		return "", err
	}
	defer conn.Close()

	agentClient := firecracker.NewAgentClient(conn)

	request := firecracker.ExecRequest{
		ID:      firecrackerRequestID(),
		Command: command,
		Args:    args,
		WorkDir: projectRoot,
		Env:     nil,
	}
	result, err := agentClient.Exec(ctx, request)
	if err != nil {
		if logTail := readFileTail(session.serialLog, 262144); logTail != "" {
			return "", fmt.Errorf("%w\nfirecracker serial log tail:\n%s", err, logTail)
		}
		return "", err
	}

	out := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if result.ID != "" && result.ID != request.ID {
		if out == "" {
			out = fmt.Sprintf("firecracker agent response id mismatch: got %q want %q", result.ID, request.ID)
		}
		return out, fmt.Errorf("firecracker agent response id mismatch: got %q want %q", result.ID, request.ID)
	}
	if result.ExitCode != 0 {
		if out == "" {
			out = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return out, fmt.Errorf("command failed with exit code %d", result.ExitCode)
	}
	return out, nil
}

func detectHostDockerSocket() string {
	candidates := make([]string, 0, 4)

	raw := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if strings.HasPrefix(raw, "unix://") {
		candidate := strings.TrimPrefix(raw, "unix://")
		if candidate != "" {
			candidates = append(candidates, candidate)
			if !strings.HasPrefix(candidate, "/var/lib/snapd/hostfs/") {
				candidates = append(candidates, "/var/lib/snapd/hostfs"+candidate)
			}
		}
	}

	candidates = append(candidates, "/var/lib/snapd/hostfs/var/run/docker.sock", "/var/run/docker.sock")

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if info, err := hostDockerSocketStat(candidate); err == nil && (info.Mode()&os.ModeSocket) != 0 {
			return candidate
		}
	}

	return ""
}

func bootstrapContainerExecContext(projectRoot string, execCtx doctorExecContext, backendLabel string, allowInstall bool) error {
	timeout := 5 * time.Minute
	hostProxyMode := execCtx.backend == "firecracker" && strings.EqualFold(strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")), "host-proxy")
	collectDockerDiagnostics := func() string {
		diagCmd := "set +e; echo '--- docker binary ---'; command -v docker || true; echo '--- docker version ---'; docker version || true; echo '--- docker info ---'; docker info || true; echo '--- dockerd ps ---'; ps -ef | grep '[d]ockerd' || true; echo '--- dockerd log ---'; cat /tmp/nexus-doctor-dockerd.log || true; if command -v systemctl >/dev/null 2>&1; then echo '--- systemctl status docker ---'; systemctl status docker --no-pager || true; fi"
		diagCtx, diagCancel := context.WithTimeout(context.Background(), 45*time.Second)
		diagOut, _ := doctorCheckCommandRunner(diagCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, 45*time.Second, "sh", []string{"-lc", diagCmd}, execCtx)
		diagCancel()
		return strings.TrimSpace(diagOut)
	}
	capabilityChecks := [][]string{{"docker", "info"}, {"docker", "compose", "version"}}
	runCapabilityChecks := func() (bool, string) {
		failures := make([]string, 0)
		for _, check := range capabilityChecks {
			checkCtx, checkCancel := context.WithTimeout(context.Background(), timeout)
			out, err := doctorCheckCommandRunner(checkCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, check[0], check[1:], execCtx)
			checkCancel()
			if err != nil {
				if strings.TrimSpace(out) != "" {
					failures = append(failures, strings.TrimSpace(out))
				} else {
					failures = append(failures, fmt.Sprintf("%s failed", strings.Join(check, " ")))
				}
			}
		}
		if len(failures) == 0 {
			return true, ""
		}
		return false, strings.Join(failures, "\n")
	}

	if ok, _ := runCapabilityChecks(); ok {
		return nil
	}

	if hostProxyMode {
		var verifyOut string
		for attempt := 1; attempt <= 6; attempt++ {
			if ok, out := runCapabilityChecks(); ok {
				return nil
			} else {
				verifyOut = out
			}
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
		diagnostics := collectDockerDiagnostics()
		if diagnostics != "" {
			return fmt.Errorf("bootstrap %s host-proxy docker mode unavailable: %s\n%s", backendLabel, strings.TrimSpace(verifyOut), diagnostics)
		}
		return fmt.Errorf("bootstrap %s host-proxy docker mode unavailable: %s", backendLabel, strings.TrimSpace(verifyOut))
	}

	startDockerCmd := "if command -v systemctl >/dev/null 2>&1; then systemctl enable docker >/dev/null 2>&1 || true; systemctl start docker >/dev/null 2>&1 || true; fi; if ! docker info >/dev/null 2>&1; then nohup dockerd --host=unix:///var/run/docker.sock --storage-driver=vfs --iptables=false --bridge=none --userland-proxy=false >/tmp/nexus-doctor-dockerd.log 2>&1 & sleep 5; fi"
	startCtx, startCancel := context.WithTimeout(context.Background(), timeout)
	startOut, startErr := doctorCheckCommandRunner(startCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "sh", []string{"-lc", startDockerCmd}, execCtx)
	startCancel()

	if startErr == nil {
		if ok, _ := runCapabilityChecks(); ok {
			return nil
		}
	}

	if !allowInstall {
		if ok, verifyOut := runCapabilityChecks(); !ok {
			diagnostics := collectDockerDiagnostics()
			if diagnostics != "" {
				return fmt.Errorf("bootstrap %s tooling verification failed: %s\n%s", backendLabel, strings.TrimSpace(verifyOut), diagnostics)
			}
			return fmt.Errorf("bootstrap %s tooling verification failed: %s", backendLabel, strings.TrimSpace(verifyOut))
		}
		return nil
	}

	installCtx, installCancel := context.WithTimeout(context.Background(), timeout)
	installOut, installErr := bootstrapInstallCommandRunner(installCtx, projectRoot, timeout, execCtx)
	installCancel()
	if installErr != nil {
		trimmedOut := strings.TrimSpace(installOut)
		if strings.Contains(trimmedOut, "Temporary failure resolving") ||
			strings.Contains(trimmedOut, "Failed to fetch") ||
			strings.Contains(trimmedOut, "Unable to locate package") ||
			strings.Contains(trimmedOut, "has no installation candidate") {
			fmt.Printf("bootstrap %s tooling: apt unavailable, continuing with existing runtime packages\n", backendLabel)
		} else {
			return fmt.Errorf("bootstrap %s tooling failed: %s", backendLabel, strings.TrimSpace(startOut+"\n"+installOut))
		}
	}

	startCtx, startCancel = context.WithTimeout(context.Background(), timeout)
	startOut, startErr = doctorCheckCommandRunner(startCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "sh", []string{"-lc", startDockerCmd}, execCtx)
	startCancel()
	if startErr != nil {
		diagnostics := collectDockerDiagnostics()
		if diagnostics != "" {
			return fmt.Errorf("bootstrap %s docker daemon startup failed: %s\n%s", backendLabel, strings.TrimSpace(startOut), diagnostics)
		}
		return fmt.Errorf("bootstrap %s docker daemon startup failed: %s", backendLabel, strings.TrimSpace(startOut))
	}

	if ok, verifyOut := runCapabilityChecks(); !ok {
		diagnostics := collectDockerDiagnostics()
		if diagnostics != "" {
			return fmt.Errorf("bootstrap %s tooling verification failed: %s\n%s", backendLabel, strings.TrimSpace(verifyOut), diagnostics)
		}
		return fmt.Errorf("bootstrap %s tooling verification failed: %s", backendLabel, strings.TrimSpace(verifyOut))
	}

	return nil
}

func bootstrapFirecrackerExecContext(projectRoot string, execCtx doctorExecContext) error {
	return firecrackerBootstrapRunner(projectRoot, execCtx)
}

func runConfiguredProbes(opts options, probes []config.DoctorCommandProbe) ([]checkResult, error) {
	results := make([]checkResult, 0, len(probes))
	requiredFailures := make([]string, 0)

	for _, probe := range probes {
		timeout := 10 * time.Minute
		if probe.TimeoutMs > 0 {
			timeout = time.Duration(probe.TimeoutMs) * time.Millisecond
		}
		attempts := probe.Retries + 1
		start := time.Now()
		lastErr := ""

		for attempt := 1; attempt <= attempts; attempt++ {
			probeCtx, cancel := context.WithTimeout(context.Background(), timeout)
			out, err := runCheckCommand(probeCtx, opts.projectRoot, "probe", probe.Name, attempt, attempts, timeout, probe.Command, probe.Args)
			cancel()

			if err == nil {
				fmt.Printf("probe passed: %s (attempt %d/%d)\n", probe.Name, attempt, attempts)
				results = append(results, checkResult{
					Name:       probe.Name,
					Phase:      "probe",
					Status:     "passed",
					Required:   probe.Required,
					Attempts:   attempt,
					DurationMs: time.Since(start).Milliseconds(),
				})
				lastErr = ""
				break
			}

			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			lastErr = msg
			if attempt < attempts {
				fmt.Printf("probe retrying: %s (attempt %d/%d)\n", probe.Name, attempt+1, attempts)
			}
		}

		if lastErr != "" {
			status := "failed_optional"
			if probe.Required {
				status = "failed_required"
				requiredFailures = append(requiredFailures, probe.Name)
			}
			results = append(results, checkResult{
				Name:       probe.Name,
				Phase:      "probe",
				Status:     status,
				Required:   probe.Required,
				Attempts:   attempts,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      lastErr,
			})
			if probe.Required {
				fmt.Printf("required probe failed: %s: %s\n", probe.Name, lastErr)
			} else {
				fmt.Printf("optional probe failed: %s: %s\n", probe.Name, lastErr)
			}
		}
	}

	if len(requiredFailures) > 0 {
		return results, fmt.Errorf("required probes failed: %s", strings.Join(requiredFailures, ", "))
	}

	return results, nil
}

func runConfiguredTests(opts options, tests []config.DoctorCommandCheck) ([]checkResult, error) {
	results := make([]checkResult, 0, len(tests))
	requiredFailures := make([]string, 0)

	for _, test := range tests {
		timeout := 10 * time.Minute
		if test.TimeoutMs > 0 {
			timeout = time.Duration(test.TimeoutMs) * time.Millisecond
		}
		attempts := test.Retries + 1
		start := time.Now()
		lastErr := ""

		for attempt := 1; attempt <= attempts; attempt++ {
			testCtx, cancel := context.WithTimeout(context.Background(), timeout)
			out, err := runCheckCommand(testCtx, opts.projectRoot, "test", test.Name, attempt, attempts, timeout, test.Command, test.Args)
			cancel()

			if err == nil {
				fmt.Printf("test passed: %s (attempt %d/%d)\n", test.Name, attempt, attempts)
				results = append(results, checkResult{
					Name:       test.Name,
					Phase:      "test",
					Status:     "passed",
					Required:   test.Required,
					Attempts:   attempt,
					DurationMs: time.Since(start).Milliseconds(),
				})
				lastErr = ""
				break
			}

			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			lastErr = msg
			if attempt < attempts {
				fmt.Printf("test retrying: %s (attempt %d/%d)\n", test.Name, attempt+1, attempts)
			}
		}

		if lastErr != "" {
			status := "failed_optional"
			if test.Required {
				status = "failed_required"
				requiredFailures = append(requiredFailures, test.Name)
			}
			results = append(results, checkResult{
				Name:       test.Name,
				Phase:      "test",
				Status:     status,
				Required:   test.Required,
				Attempts:   attempts,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      lastErr,
			})
			if test.Required {
				fmt.Printf("required test failed: %s: %s\n", test.Name, lastErr)
			} else {
				fmt.Printf("optional test failed: %s: %s\n", test.Name, lastErr)
			}
		}
	}

	if len(requiredFailures) > 0 {
		return results, fmt.Errorf("required tests failed: %s", strings.Join(requiredFailures, ", "))
	}

	return results, nil
}

func runBuiltInOpencodeSessionCheck(projectRoot string) (checkResult, error) {
	const checkName = "tooling-opencode-session"
	start := time.Now()

	if loadDoctorExecContext().backend == "lxc" {
		return checkResult{
			Name:       checkName,
			Phase:      "test",
			Status:     "not_run",
			Required:   true,
			Attempts:   0,
			DurationMs: time.Since(start).Milliseconds(),
			SkipReason: "opencode session check is skipped for lxc backend in CI",
		}, nil
	}

	result := checkResult{
		Name:     checkName,
		Phase:    "test",
		Attempts: 1,
	}

	if loadDoctorExecContext().backend != "firecracker" {
		if _, err := exec.LookPath("opencode"); err != nil {
			result.Status = "failed_required"
			result.Required = true
			result.DurationMs = time.Since(start).Milliseconds()
			result.Error = "opencode command not found in PATH"
			return result, fmt.Errorf("required tests failed: %s", checkName)
		}
	}

	versionTimeout := 30 * time.Second
	versionOut, versionErr := runCheckCommand(context.Background(), projectRoot, "test", checkName, 1, 1, versionTimeout, "opencode", []string{"--version"})
	if versionErr != nil {
		result.Status = "failed_required"
		result.Required = true
		result.DurationMs = time.Since(start).Milliseconds()
		result.Error = versionOut
		return result, fmt.Errorf("required tests failed: %s", checkName)
	}

	runHelpTimeout := 30 * time.Second
	runHelpOut, runHelpErr := runCheckCommand(context.Background(), projectRoot, "test", checkName, 1, 1, runHelpTimeout, "opencode", []string{"run", "--help"})
	if runHelpErr != nil {
		result.Status = "failed_required"
		result.Required = true
		result.DurationMs = time.Since(start).Milliseconds()
		result.Error = runHelpOut
		return result, fmt.Errorf("required tests failed: %s", checkName)
	}

	model := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_OPENCODE_MODEL"))

	prompt := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_OPENCODE_PROMPT"))
	if prompt == "" {
		prompt = "Respond with exactly: NEXUS_DOCTOR_OK"
	}

	expectedMarker := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_OPENCODE_EXPECTED_MARKER"))
	if expectedMarker == "" {
		expectedMarker = "NEXUS_DOCTOR_OK"
	}

	timeout := 3 * time.Minute
	if rawTimeout := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_OPENCODE_TIMEOUT_MS")); rawTimeout != "" {
		if ms, err := strconv.Atoi(rawTimeout); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	runArgs := []string{"run"}
	if model != "" {
		runArgs = append(runArgs, "--model", model)
	}
	runArgs = append(runArgs, prompt)

	opencodeCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := runCheckCommand(opencodeCtx, projectRoot, "test", checkName, 1, 1, timeout, "opencode", runArgs)

	result.Required = true
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "failed_required"
		result.Error = out
		return result, fmt.Errorf("required tests failed: %s", checkName)
	}

	if !strings.Contains(out, expectedMarker) {
		result.Status = "failed_required"
		result.Error = fmt.Sprintf("expected marker %q not found in opencode output", expectedMarker)
		return result, fmt.Errorf("required tests failed: %s", checkName)
	}

	result.Status = "passed"
	fmt.Printf("test passed: %s (attempt 1/1)\n", checkName)
	return result, nil
}

func runBuiltInRuntimeBackendCheck() (checkResult, error) {
	const checkName = "runtime-backend-capabilities"
	start := time.Now()
	result := checkResult{
		Name:     checkName,
		Phase:    "probe",
		Required: true,
		Attempts: 1,
	}

	timeout := 45 * time.Second
	if rawTimeout := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_RUNTIME_TIMEOUT_MS")); rawTimeout != "" {
		if ms, err := strconv.Atoi(rawTimeout); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	backend := strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_BACKEND"))
	if backend == "" {
		backend = "dind"
	}
	if backend == "firecracker" {
		result.Status = "passed"
		result.DurationMs = time.Since(start).Milliseconds()
		fmt.Printf("probe passed: %s (attempt 1/1)\n", checkName)
		return result, nil
	}
	execCtx := loadDoctorExecContext()

	if backend == "lxc" {
		lxcCtx, lxcCancel := context.WithTimeout(context.Background(), timeout)
		lxcOut, lxcErr := doctorCheckCommandRunner(lxcCtx, ".", "probe", checkName, 1, 1, timeout, "lxc", []string{"info"}, doctorExecContext{})
		lxcCancel()
		if lxcErr != nil {
			sudoCtx, sudoCancel := context.WithTimeout(context.Background(), timeout)
			sudoOut, sudoErr := doctorCheckCommandRunner(sudoCtx, ".", "probe", checkName, 1, 1, timeout, "sudo", []string{"-n", "lxc", "info"}, doctorExecContext{})
			sudoCancel()
			if sudoErr != nil {
				result.Status = "failed_required"
				result.DurationMs = time.Since(start).Milliseconds()
				result.Error = strings.TrimSpace(lxcOut + "\n" + sudoOut)
				return result, fmt.Errorf("required probes failed: %s", checkName)
			}
		}
	}

	checks := [][]string{{"docker", "info"}, {"docker", "compose", "version"}}

	for _, check := range checks {
		command := check[0]
		args := check[1:]
		cmdCtx, cancel := context.WithTimeout(context.Background(), timeout)
		out, err := doctorCheckCommandRunner(cmdCtx, ".", "probe", checkName, 1, 1, timeout, command, args, execCtx)
		cancel()
		if err != nil {
			result.Status = "failed_required"
			result.DurationMs = time.Since(start).Milliseconds()
			result.Error = out
			return result, fmt.Errorf("required probes failed: %s", checkName)
		}
	}

	result.Status = "passed"
	result.DurationMs = time.Since(start).Milliseconds()
	fmt.Printf("probe passed: %s (attempt 1/1)\n", checkName)
	return result, nil
}

func bootstrapDoctorExecContext(projectRoot string) error {
	setDoctorExecContextCleanup(nil)
	execCtx := loadDoctorExecContext()
	if execCtx.backend == "firecracker" {
		if err := bootstrapFirecrackerExecContext(projectRoot, execCtx); err != nil {
			return err
		}
		return containerBootstrapRunner(projectRoot, execCtx, "firecracker", true)
	}

	if execCtx.backend != "lxc" {
		return nil
	}
	if execCtx.lxcExec == "host" {
		return fmt.Errorf("backend \"lxc\" does not allow host execution mode; configure NEXUS_DOCTOR_LXC_INSTANCE")
	}
	if execCtx.lxcName == "" {
		return fmt.Errorf("backend \"lxc\" requires explicit execution context (set NEXUS_DOCTOR_LXC_INSTANCE)")
	}

	return containerBootstrapRunner(projectRoot, execCtx, "lxc", true)
}

func markChecksNotRun(tests []config.DoctorCommandCheck, skipReason string) []checkResult {
	results := make([]checkResult, 0, len(tests))
	for _, test := range tests {
		results = append(results, checkResult{
			Name:       test.Name,
			Phase:      "test",
			Status:     "not_run",
			Required:   test.Required,
			Attempts:   0,
			DurationMs: 0,
			SkipReason: skipReason,
		})
	}
	return results
}

func markProbesNotRun(probes []config.DoctorCommandProbe, skipReason string) []checkResult {
	results := make([]checkResult, 0, len(probes))
	for _, probe := range probes {
		results = append(results, checkResult{
			Name:       probe.Name,
			Phase:      "probe",
			Status:     "not_run",
			Required:   probe.Required,
			Attempts:   0,
			DurationMs: 0,
			SkipReason: skipReason,
		})
	}
	return results
}

func runCheckCommand(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string) (string, error) {
	execCtx := loadDoctorExecContext()
	return runCheckCommandWithExecContext(ctx, projectRoot, phase, name, attempt, attempts, timeout, command, args, execCtx)
}

func runCheckCommandWithExecContext(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext) (string, error) {
	if execCtx.backend == "lxc" && execCtx.lxcExec == "host" {
		msg := "backend \"lxc\" does not allow host execution mode; configure NEXUS_DOCTOR_LXC_INSTANCE"
		return msg, errors.New(msg)
	}

	if execCtx.backend == "lxc" && execCtx.lxcName == "" {
		msg := "backend \"lxc\" requires explicit execution context (set NEXUS_DOCTOR_LXC_INSTANCE)"
		return msg, errors.New(msg)
	}

	if execCtx.backend == "firecracker" {
		fmt.Printf("%s exec: %s (attempt %d/%d, timeout=%s, context=firecracker): %s\n", phase, name, attempt, attempts, timeout, formatCommand(command, args))
		return firecrackerCheckCommandRunner(ctx, projectRoot, command, args)
	}

	cmdName, cmdArgs, cmdEnv, contextLabel := resolveCheckCommand(projectRoot, command, args, execCtx)

	fmt.Printf("%s exec: %s (attempt %d/%d, timeout=%s, context=%s): %s\n", phase, name, attempt, attempts, timeout, contextLabel, formatCommand(cmdName, cmdArgs))

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), cmdEnv...)

	var output bytes.Buffer
	writer := io.MultiWriter(os.Stdout, &output)
	cmd.Stdout = writer
	cmd.Stderr = writer

	err := cmd.Run()
	out := strings.TrimSpace(output.String())
	if out == "" && err != nil {
		out = err.Error()
	}

	return out, err
}

func loadDoctorExecContext() doctorExecContext {
	backend := strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_BACKEND"))
	if backend == "" {
		backend = "dind"
	}
	return doctorExecContext{
		backend:    backend,
		dockerHost: strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_DIND_DOCKER_HOST")),
		lxcName:    strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_LXC_INSTANCE")),
		lxcExec:    strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_LXC_EXEC_MODE")),
	}
}

func resolveCheckCommand(projectRoot, command string, args []string, execCtx doctorExecContext) (string, []string, []string, string) {
	if execCtx.backend == "lxc" && execCtx.lxcName != "" {
		envPrefix := []string{
			"export", "NEXUS_RUNTIME_BACKEND=" + shellQuote(execCtx.backend),
			"NEXUS_DOCTOR_LXC_INSTANCE=" + shellQuote(execCtx.lxcName),
			"NEXUS_DOCTOR_LXC_EXEC_MODE=" + shellQuote(execCtx.lxcExec),
		}
		if execCtx.dockerHost != "" {
			envPrefix = append(envPrefix, "NEXUS_DOCTOR_DIND_DOCKER_HOST="+shellQuote(execCtx.dockerHost))
		}
		envPrefix = append(envPrefix, ";")

		innerParts := make([]string, 0, len(args)+2)
		innerParts = append(innerParts, envPrefix...)
		innerParts = append(innerParts, "cd", shellQuote(projectRoot), "&&", shellQuote(command))
		for _, arg := range args {
			innerParts = append(innerParts, shellQuote(arg))
		}
		inner := strings.Join(innerParts, " ")
		if execCtx.lxcExec == "sudo-lxc" {
			return "sudo", []string{"-n", "lxc", "exec", execCtx.lxcName, "--", "bash", "-lc", inner}, nil, "lxc-sudo"
		}
		return "lxc", []string{"exec", execCtx.lxcName, "--", "bash", "-lc", inner}, nil, "lxc"
	}

	if execCtx.backend == "dind" && execCtx.dockerHost != "" {
		extraEnv := []string{"DOCKER_HOST=" + execCtx.dockerHost}
		return command, args, extraEnv, "dind"
	}

	if execCtx.backend == "dind" {
		return command, args, nil, "dind"
	}

	return command, args, nil, "host"
}

func combineCheckErrors(probeErr, testErr error) error {
	if probeErr == nil {
		return testErr
	}
	if testErr == nil {
		return probeErr
	}
	return fmt.Errorf("%w; %v", probeErr, testErr)
}

func formatCommand(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(command))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.ContainsAny(value, " \t\n\r\"'`$\\") {
		return strconv.Quote(value)
	}
	return value
}

func writeReport(reportPath string, results []checkResult) error {
	if strings.TrimSpace(reportPath) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal doctor report: %w", err)
	}
	if err := os.WriteFile(reportPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write doctor report: %w", err)
	}
	fmt.Printf("doctor report written: %s\n", reportPath)
	return nil
}

func parseRequiredPorts(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	ports := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		port, err := strconv.Atoi(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid required host port %q", trimmed)
		}
		if port <= 0 || port > 65535 {
			return nil, fmt.Errorf("required host port out of range: %d", port)
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("no required host ports provided")
	}
	return ports, nil
}

func assertNoManualACP(lifecycleDir string) error {
	entries, err := os.ReadDir(lifecycleDir)
	if err != nil {
		return fmt.Errorf("read lifecycle dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(lifecycleDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read lifecycle script %s: %w", path, err)
		}
		if strings.Contains(string(data), "opencode serve") {
			return fmt.Errorf("manual ACP startup found in lifecycle scripts: %s", path)
		}
	}

	return nil
}

func validateLifecycleEntrypoints(projectRoot string) error {
	lifecycleDir := filepath.Join(projectRoot, ".nexus", "lifecycles")
	startPath := filepath.Join(lifecycleDir, "start.sh")

	startExists, err := isExecutableFile(startPath)
	if err != nil {
		return err
	}
	if !startExists && !hasMakeTarget(projectRoot, "start") {
		return fmt.Errorf("missing startup entrypoint: expected executable %s or Makefile target 'start'", startPath)
	}

	for _, name := range []string{"setup.sh", "teardown.sh"} {
		path := filepath.Join(lifecycleDir, name)
		_, err := isExecutableFile(path)
		if err != nil {
			return err
		}
	}

	return nil
}

func resolveDoctorChecks(projectRoot string, cfgProbes []config.DoctorCommandProbe, cfgTests []config.DoctorCommandCheck) ([]config.DoctorCommandProbe, []config.DoctorCommandCheck, []string, error) {
	probes, tests, warnings, err := discoverDoctorScripts(projectRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(probes) > 0 || len(tests) > 0 {
		return probes, tests, warnings, nil
	}

	fallbackWarnings := append([]string{}, warnings...)
	fallbackWarnings = append(fallbackWarnings, "no discovery scripts found under .nexus/probe or .nexus/check; falling back to workspace.json doctor.probes/tests")

	return cfgProbes, cfgTests, fallbackWarnings, nil
}

func discoverDoctorScripts(projectRoot string) ([]config.DoctorCommandProbe, []config.DoctorCommandCheck, []string, error) {
	probeDir := filepath.Join(projectRoot, ".nexus", "probe")
	checkDir := filepath.Join(projectRoot, ".nexus", "check")

	probeFiles, probeWarnings, err := collectDiscoveryScripts(probeDir)
	if err != nil {
		return nil, nil, nil, err
	}
	checkFiles, checkWarnings, err := collectDiscoveryScripts(checkDir)
	if err != nil {
		return nil, nil, nil, err
	}

	warnings := append(probeWarnings, checkWarnings...)

	probes := make([]config.DoctorCommandProbe, 0, len(probeFiles))
	for _, file := range probeFiles {
		probes = append(probes, config.DoctorCommandProbe{
			Name:     discoveryScriptName(file),
			Command:  "bash",
			Args:     []string{filepath.ToSlash(filepath.Join(".nexus", "probe", file))},
			Required: true,
		})
	}

	tests := make([]config.DoctorCommandCheck, 0, len(checkFiles))
	for _, file := range checkFiles {
		tests = append(tests, config.DoctorCommandCheck{
			Name:     discoveryScriptName(file),
			Command:  "bash",
			Args:     []string{filepath.ToSlash(filepath.Join(".nexus", "check", file))},
			Required: true,
		})
	}

	return probes, tests, warnings, nil
}

func collectDiscoveryScripts(dir string) ([]string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, []string{fmt.Sprintf("discovery directory not found (optional): %s", dir)}, nil
		}
		return nil, nil, fmt.Errorf("read discovery dir %s: %w", dir, err)
	}

	files := make([]string, 0)
	nonPrefixed := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sh") {
			continue
		}
		fullPath := filepath.Join(dir, name)
		execOK, execErr := isExecutableFile(fullPath)
		if execErr != nil {
			return nil, nil, execErr
		}
		if !execOK {
			continue
		}
		if !hasNumericPrefix(name) {
			nonPrefixed = append(nonPrefixed, name)
		}
		files = append(files, name)
	}

	sortDiscoveryScripts(files)

	warnings := make([]string, 0, len(nonPrefixed))
	for _, file := range nonPrefixed {
		warnings = append(warnings, fmt.Sprintf("discovery script without numeric prefix: %s", filepath.Join(dir, file)))
	}

	return files, warnings, nil
}

func hasNumericPrefix(name string) bool {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return regexp.MustCompile(`^\d+-`).MatchString(base)
}

func sortDiscoveryScripts(files []string) {
	sort.Slice(files, func(i, j int) bool {
		aPrefix, aNum := discoveryPrefix(files[i])
		bPrefix, bNum := discoveryPrefix(files[j])

		if aPrefix && bPrefix {
			if aNum != bNum {
				return aNum < bNum
			}
			return files[i] < files[j]
		}
		if aPrefix != bPrefix {
			return aPrefix
		}
		return files[i] < files[j]
	})
}

func discoveryPrefix(name string) (bool, int) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.SplitN(base, "-", 2)
	if len(parts) < 2 {
		return false, 0
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, 0
	}
	return true, n
}

func discoveryScriptName(file string) string {
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	if prefixed, _ := discoveryPrefix(base); prefixed {
		parts := strings.SplitN(base, "-", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}
	return base
}

func isExecutableFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return false, fmt.Errorf("lifecycle script is not executable: %s", path)
	}
	return true, nil
}

func hasMakeTarget(projectRoot, target string) bool {
	makefilePath := filepath.Join(projectRoot, "Makefile")
	contents, err := os.ReadFile(makefilePath)
	if err != nil {
		return false
	}
	pattern := fmt.Sprintf("(?m)^%s\\s*:", regexp.QuoteMeta(target))
	re := regexp.MustCompile(pattern)
	return re.Match(contents)
}

func ensureDotEnv(projectRoot string) error {
	dotEnvPath := filepath.Join(projectRoot, ".env")
	if _, err := os.Stat(dotEnvPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat .env: %w", err)
	}

	dotEnvExamplePath := filepath.Join(projectRoot, ".env.example")
	if _, err := os.Stat(dotEnvExamplePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat .env.example: %w", err)
	}

	data, err := os.ReadFile(dotEnvExamplePath)
	if err != nil {
		return fmt.Errorf("read .env.example: %w", err)
	}
	if err := os.WriteFile(dotEnvPath, data, 0o600); err != nil {
		return fmt.Errorf("write .env from .env.example: %w", err)
	}
	return nil
}

func missingRequiredPorts(required []int, discovered []compose.PublishedPort) []int {
	found := map[int]bool{}
	for _, p := range discovered {
		found[p.HostPort] = true
	}
	missing := make([]int, 0)
	for _, p := range required {
		if !found[p] {
			missing = append(missing, p)
		}
	}
	return missing
}
