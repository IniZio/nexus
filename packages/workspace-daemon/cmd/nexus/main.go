package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/compose"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/config"
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

	if command != "doctor" {
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
}

func run(opts options) error {
	if !filepath.IsAbs(opts.projectRoot) {
		return fmt.Errorf("project root must be absolute: %s", opts.projectRoot)
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

	probeResults, probeErr := runConfiguredProbes(opts, workspaceConfig.Doctor.Probes)

	var allResults []checkResult

	if os.Getenv("NEXUS_DOCTOR_DISABLE_BUILTIN_CHECKS") != "1" {
		runtimeResult, runtimeErr := runBuiltInRuntimeBackendCheck()
		allResults = append(allResults, runtimeResult)
		probeErr = combineCheckErrors(runtimeErr, probeErr)
	}

	allResults = append(allResults, probeResults...)

	testResults, testErr := runConfiguredTests(opts, workspaceConfig.Doctor.Tests)
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

	result := checkResult{
		Name:     checkName,
		Phase:    "test",
		Attempts: 1,
	}

	if _, err := exec.LookPath("opencode"); err != nil {
		result.Status = "failed_required"
		result.Required = true
		result.DurationMs = time.Since(start).Milliseconds()
		result.Error = "opencode command not found in PATH"
		return result, fmt.Errorf("required tests failed: %s", checkName)
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
	execCtx := loadDoctorExecContext()

	if backend == "lxc" {
		lxcCtx, lxcCancel := context.WithTimeout(context.Background(), timeout)
		lxcOut, lxcErr := runCheckCommandWithExecContext(lxcCtx, ".", "probe", checkName, 1, 1, timeout, "lxc", []string{"info"}, doctorExecContext{})
		lxcCancel()
		if lxcErr != nil {
			sudoCtx, sudoCancel := context.WithTimeout(context.Background(), timeout)
			sudoOut, sudoErr := runCheckCommandWithExecContext(sudoCtx, ".", "probe", checkName, 1, 1, timeout, "sudo", []string{"-n", "lxc", "info"}, doctorExecContext{})
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
		out, err := runCheckCommandWithExecContext(cmdCtx, ".", "probe", checkName, 1, 1, timeout, command, args, execCtx)
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
	execCtx := loadDoctorExecContext()
	if execCtx.backend != "lxc" {
		return nil
	}
	if execCtx.lxcExec == "host" {
		return fmt.Errorf("backend \"lxc\" does not allow host execution mode; configure NEXUS_DOCTOR_LXC_INSTANCE")
	}
	if execCtx.lxcName == "" {
		return fmt.Errorf("backend \"lxc\" requires explicit execution context (set NEXUS_DOCTOR_LXC_INSTANCE)")
	}

	timeout := 5 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	checkOut, checkErr := runCheckCommandWithExecContext(ctx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "docker", []string{"compose", "version"}, execCtx)
	cancel()
	if checkErr == nil {
		return nil
	}

	installCmd := "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-v2 curl make python3 git || DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-plugin curl make python3 git"
	installCtx, installCancel := context.WithTimeout(context.Background(), timeout)
	installOut, installErr := runCheckCommandWithExecContext(installCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "bash", []string{"-lc", installCmd}, execCtx)
	installCancel()
	if installErr != nil {
		return fmt.Errorf("bootstrap lxc tooling failed: %s", strings.TrimSpace(checkOut+"\n"+installOut))
	}

	startDockerCmd := "if command -v systemctl >/dev/null 2>&1; then systemctl enable docker >/dev/null 2>&1 || true; systemctl start docker >/dev/null 2>&1 || true; fi; if ! docker info >/dev/null 2>&1; then nohup dockerd >/tmp/nexus-doctor-dockerd.log 2>&1 & sleep 3; fi"
	startCtx, startCancel := context.WithTimeout(context.Background(), timeout)
	startOut, startErr := runCheckCommandWithExecContext(startCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "bash", []string{"-lc", startDockerCmd}, execCtx)
	startCancel()
	if startErr != nil {
		return fmt.Errorf("bootstrap lxc docker daemon startup failed: %s", strings.TrimSpace(startOut))
	}

	for _, verify := range [][]string{{"docker", "info"}, {"docker", "compose", "version"}} {
		verifyCtx, verifyCancel := context.WithTimeout(context.Background(), timeout)
		verifyOut, verifyErr := runCheckCommandWithExecContext(verifyCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, verify[0], verify[1:], execCtx)
		verifyCancel()
		if verifyErr != nil {
			return fmt.Errorf("bootstrap lxc tooling verification failed: %s", strings.TrimSpace(verifyOut))
		}
	}

	return nil
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
		innerParts := make([]string, 0, len(args)+2)
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
