package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/compose"
	"github.com/inizio/nexus/packages/nexus/pkg/config"
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
	fcName     string
	fcExec     string
}

var doctorCheckCommandRunner = runCheckCommandWithExecContext

var bootstrapInstallCommandRunner = runBootstrapInstallCommand

var doctorExecCleanup func() error

var firecrackerHostCommandRunner = runFirecrackerHostCommand

var hostBinaryLookup = exec.LookPath

var hostDockerSocketStat = os.Stat

func runBootstrapInstallCommand(ctx context.Context, projectRoot string, timeout time.Duration, execCtx doctorExecContext) (string, error) {
	installCmd := "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-v2 curl make python3 git nodejs npm || DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-plugin curl make python3 git nodejs npm"
	return doctorCheckCommandRunner(ctx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "bash", []string{"-lc", installCmd}, execCtx)
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

func runFirecrackerHostCommand(ctx context.Context, execCtx doctorExecContext, args ...string) (string, error) {
	cmdName := "lxc"
	cmdArgs := append([]string(nil), args...)
	if execCtx.fcExec == "sudo-lxc" {
		cmdName = "sudo"
		cmdArgs = append([]string{"-n", "lxc"}, args...)
	}

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	var output bytes.Buffer
	writer := io.MultiWriter(os.Stdout, &output)
	cmd.Stdout = writer
	cmd.Stderr = writer
	err := cmd.Run()
	return strings.TrimSpace(output.String()), err
}

func writeExecutableScriptInExecContext(projectRoot string, execCtx doctorExecContext, checkName, targetPath, scriptContent string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(scriptContent))
	cmd := "printf %s " + shellQuote(encoded) + " | base64 -d > " + shellQuote(targetPath) + " && chmod +x " + shellQuote(targetPath)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := doctorCheckCommandRunner(ctx, projectRoot, "probe", checkName, 1, 1, 45*time.Second, "bash", []string{"-lc", cmd}, execCtx)
	if err != nil {
		return fmt.Errorf("write executable %s failed: %s", targetPath, strings.TrimSpace(out))
	}
	return nil
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

func seedFirecrackerDockerTooling(projectRoot string, execCtx doctorExecContext) error {
	binaryCandidates := []string{"docker", "dockerd", "containerd", "containerd-shim-runc-v2", "ctr", "runc"}
	for _, binName := range binaryCandidates {
		binPath, err := hostBinaryLookup(binName)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		out, pushErr := firecrackerHostCommandRunner(ctx, execCtx, "file", "push", binPath, execCtx.fcName+binPath)
		cancel()
		if pushErr != nil {
			return fmt.Errorf("seed firecracker %s binary failed: %s", binName, strings.TrimSpace(out))
		}
	}

	dockerInit := "/usr/bin/docker-init"
	if info, err := os.Stat(dockerInit); err == nil && !info.IsDir() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		out, pushErr := firecrackerHostCommandRunner(ctx, execCtx, "file", "push", dockerInit, execCtx.fcName+dockerInit)
		cancel()
		if pushErr != nil {
			return fmt.Errorf("seed firecracker docker-init binary failed: %s", strings.TrimSpace(out))
		}
	}

	composePluginCandidates := []string{
		"/usr/libexec/docker/cli-plugins/docker-compose",
		"/usr/lib/docker/cli-plugins/docker-compose",
		"/usr/local/lib/docker/cli-plugins/docker-compose",
	}
	composePlugin := ""
	for _, candidate := range composePluginCandidates {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			composePlugin = candidate
			break
		}
	}
	if composePlugin != "" {
		mkdirCtx, mkdirCancel := context.WithTimeout(context.Background(), 45*time.Second)
		_, _ = firecrackerHostCommandRunner(mkdirCtx, execCtx, "exec", execCtx.fcName, "--", "mkdir", "-p", "/usr/libexec/docker/cli-plugins")
		mkdirCancel()

		pluginCtx, pluginCancel := context.WithTimeout(context.Background(), 45*time.Second)
		pluginOut, pluginErr := firecrackerHostCommandRunner(pluginCtx, execCtx, "file", "push", composePlugin, execCtx.fcName+"/usr/libexec/docker/cli-plugins/docker-compose")
		pluginCancel()
		if pluginErr != nil {
			return fmt.Errorf("seed firecracker docker compose plugin failed: %s", strings.TrimSpace(pluginOut))
		}

		chmodCtx, chmodCancel := context.WithTimeout(context.Background(), 45*time.Second)
		_, _ = firecrackerHostCommandRunner(chmodCtx, execCtx, "exec", execCtx.fcName, "--", "chmod", "+x", "/usr/libexec/docker/cli-plugins/docker-compose")
		chmodCancel()
	}

	hostSocket := detectHostDockerSocket()
	if hostSocket != "" {
		_ = os.Setenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE", "host-proxy")
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 45*time.Second)
		_, _ = firecrackerHostCommandRunner(rmCtx, execCtx, "config", "device", "remove", execCtx.fcName, "docker-sock")
		rmCancel()

		addCtx, addCancel := context.WithTimeout(context.Background(), 45*time.Second)
		addOut, addErr := firecrackerHostCommandRunner(addCtx, execCtx,
			"config", "device", "add", execCtx.fcName, "docker-sock", "proxy",
			"listen=unix:/tmp/nexus-host-docker.sock",
			"connect=unix:"+hostSocket,
			"bind=container",
			"uid=0",
			"gid=0",
			"mode=0660",
		)
		addCancel()
		if addErr != nil {
			return fmt.Errorf("configure firecracker docker socket proxy failed: %s", strings.TrimSpace(addOut))
		}

		dockerWrapper := strings.Join([]string{
			"#!/usr/bin/env sh",
			"if [ \"${NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE:-}\" = \"host-proxy\" ] && [ -z \"${DOCKER_HOST:-}\" ] && [ -S /tmp/nexus-host-docker.sock ]; then",
			"  export DOCKER_HOST=unix:///tmp/nexus-host-docker.sock",
			"fi",
			"exec /usr/bin/docker \"$@\"",
		}, "\n") + "\n"
		if err := writeExecutableScriptInExecContext(projectRoot, execCtx, "runtime-backend-capabilities", "/usr/local/bin/docker", dockerWrapper); err != nil {
			return fmt.Errorf("configure firecracker docker wrapper failed: %w", err)
		}
	}
	if hostSocket == "" {
		_ = os.Setenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE", "")
	}

	return nil
}

func seedFirecrackerOpencodeTooling(projectRoot string, execCtx doctorExecContext) error {
	nodeBin, nodeErr := hostBinaryLookup("node")
	if nodeErr != nil {
		return fmt.Errorf("host node binary not found in PATH")
	}
	opencodeBin, opencodeErr := hostBinaryLookup("opencode")
	if opencodeErr != nil {
		return fmt.Errorf("host opencode binary not found in PATH")
	}

	moduleDir := filepath.Clean(filepath.Join(filepath.Dir(opencodeBin), "..", "lib", "node_modules", "opencode-ai"))
	if info, err := os.Stat(moduleDir); err != nil || !info.IsDir() {
		return fmt.Errorf("host opencode module directory not found: %s", moduleDir)
	}

	mkdirCtx, mkdirCancel := context.WithTimeout(context.Background(), 45*time.Second)
	_, mkdirErr := firecrackerHostCommandRunner(mkdirCtx, execCtx, "exec", execCtx.fcName, "--", "mkdir", "-p", "/opt/nexus-node/bin", "/usr/local/lib/node_modules", "/usr/local/bin")
	mkdirCancel()
	if mkdirErr != nil {
		return fmt.Errorf("create firecracker tooling directories failed")
	}

	nodeCtx, nodeCancel := context.WithTimeout(context.Background(), 45*time.Second)
	nodeOut, pushNodeErr := firecrackerHostCommandRunner(nodeCtx, execCtx, "file", "push", nodeBin, execCtx.fcName+"/opt/nexus-node/bin/node")
	nodeCancel()
	if pushNodeErr != nil {
		return fmt.Errorf("seed firecracker node binary failed: %s", strings.TrimSpace(nodeOut))
	}

	chmodCtx, chmodCancel := context.WithTimeout(context.Background(), 45*time.Second)
	_, _ = firecrackerHostCommandRunner(chmodCtx, execCtx, "exec", execCtx.fcName, "--", "chmod", "+x", "/opt/nexus-node/bin/node")
	chmodCancel()

	moduleCtx, moduleCancel := context.WithTimeout(context.Background(), 90*time.Second)
	moduleOut, pushModuleErr := firecrackerHostCommandRunner(moduleCtx, execCtx, "file", "push", "-r", moduleDir, execCtx.fcName+"/usr/local/lib/node_modules/")
	moduleCancel()
	if pushModuleErr != nil {
		return fmt.Errorf("seed firecracker opencode module failed: %s", strings.TrimSpace(moduleOut))
	}

	opencodeWrapper := strings.Join([]string{
		"#!/usr/bin/env sh",
		"exec /opt/nexus-node/bin/node /usr/local/lib/node_modules/opencode-ai/bin/opencode \"$@\"",
	}, "\n") + "\n"
	if err := writeExecutableScriptInExecContext(projectRoot, execCtx, "firecracker-opencode-tooling", "/usr/local/bin/opencode", opencodeWrapper); err != nil {
		return fmt.Errorf("configure firecracker opencode wrapper failed: %w", err)
	}

	linkCtx, linkCancel := context.WithTimeout(context.Background(), 45*time.Second)
	linkOut, linkErr := doctorCheckCommandRunner(linkCtx, projectRoot, "probe", "firecracker-opencode-tooling", 1, 1, 45*time.Second, "bash", []string{"-lc", "ln -sf /opt/nexus-node/bin/node /usr/local/bin/node"}, execCtx)
	linkCancel()
	if linkErr != nil {
		return fmt.Errorf("configure firecracker node symlink failed: %s", strings.TrimSpace(linkOut))
	}

	return nil
}

func collectHostDNSServers() []string {
	paths := []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"}
	servers := make([]string, 0)
	seen := map[string]bool{}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "nameserver ") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			host := strings.TrimSpace(parts[1])
			if host == "" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" {
				continue
			}
			if !seen[host] {
				seen[host] = true
				servers = append(servers, host)
			}
		}
	}

	if len(servers) == 0 {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	return servers
}

func configureFirecrackerDNS(projectRoot string, execCtx doctorExecContext) error {
	dnsServers := collectHostDNSServers()
	printfParts := make([]string, 0, len(dnsServers)+1)
	for _, server := range dnsServers {
		printfParts = append(printfParts, "'nameserver "+server+"'")
	}
	printfParts = append(printfParts, "'options timeout:2 attempts:5'")

	script := "if [ -L /etc/resolv.conf ]; then rm -f /etc/resolv.conf || true; fi; printf '%s\\n' " + strings.Join(printfParts, " ") + " > /etc/resolv.conf"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := doctorCheckCommandRunner(ctx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, 30*time.Second, "bash", []string{"-lc", script}, execCtx)
	if err != nil {
		return fmt.Errorf("configure firecracker dns failed: %s", strings.TrimSpace(out))
	}
	return nil
}

func bootstrapContainerExecContext(projectRoot string, execCtx doctorExecContext, backendLabel string, allowInstall bool) error {
	timeout := 5 * time.Minute
	hostProxyMode := execCtx.backend == "firecracker" && strings.EqualFold(strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")), "host-proxy")
	collectDockerDiagnostics := func() string {
		diagCmd := "set +e; echo '--- docker binary ---'; command -v docker || true; echo '--- docker version ---'; docker version || true; echo '--- docker info ---'; docker info || true; echo '--- dockerd ps ---'; ps -ef | grep '[d]ockerd' || true; echo '--- dockerd log ---'; cat /tmp/nexus-doctor-dockerd.log || true; if command -v systemctl >/dev/null 2>&1; then echo '--- systemctl status docker ---'; systemctl status docker --no-pager || true; fi"
		diagCtx, diagCancel := context.WithTimeout(context.Background(), 45*time.Second)
		diagOut, _ := doctorCheckCommandRunner(diagCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, 45*time.Second, "bash", []string{"-lc", diagCmd}, execCtx)
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
	startOut, startErr := doctorCheckCommandRunner(startCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "bash", []string{"-lc", startDockerCmd}, execCtx)
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
	startOut, startErr = doctorCheckCommandRunner(startCtx, projectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "bash", []string{"-lc", startDockerCmd}, execCtx)
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

func ensureFirecrackerRegistryReadiness(projectRoot string, execCtx doctorExecContext) error {
	checkCmd := "set -euo pipefail; getent ahostsv4 registry-1.docker.io >/dev/null 2>&1 || getent hosts registry-1.docker.io >/dev/null; status=$(curl -4 -sS -o /dev/null -w '%{http_code}' --max-time 20 https://registry-1.docker.io/v2/ || true); if [ \"$status\" != \"200\" ] && [ \"$status\" != \"401\" ]; then status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 https://registry-1.docker.io/v2/ || true); fi; [ \"$status\" = \"200\" ] || [ \"$status\" = \"401\" ]"
	var lastOut string
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		out, err := doctorCheckCommandRunner(ctx, projectRoot, "probe", "firecracker-network-readiness", attempt, 3, 45*time.Second, "bash", []string{"-lc", checkCmd}, execCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastOut = strings.TrimSpace(out)
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}

	diagCmd := "set +e; echo '--- /etc/resolv.conf ---'; cat /etc/resolv.conf || true; echo '--- route ---'; ip route || true; echo '--- getent registry-1.docker.io ---'; getent hosts registry-1.docker.io || true"
	diagCtx, diagCancel := context.WithTimeout(context.Background(), 30*time.Second)
	diagOut, _ := doctorCheckCommandRunner(diagCtx, projectRoot, "probe", "firecracker-network-readiness", 1, 1, 30*time.Second, "bash", []string{"-lc", diagCmd}, execCtx)
	diagCancel()

	combined := strings.TrimSpace(lastOut + "\n" + diagOut)
	if combined == "" {
		combined = "registry endpoint unreachable"
	}
	return fmt.Errorf("firecracker network readiness failed: %s", combined)
}

func ensureFirecrackerOpencodeTooling(projectRoot string, execCtx doctorExecContext) error {
	checkTimeout := 30 * time.Second
	checkCtx, checkCancel := context.WithTimeout(context.Background(), checkTimeout)
	_, checkErr := doctorCheckCommandRunner(checkCtx, projectRoot, "probe", "firecracker-opencode-tooling", 1, 1, checkTimeout, "bash", []string{"-lc", "command -v opencode >/dev/null 2>&1 && opencode --version >/dev/null 2>&1"}, execCtx)
	checkCancel()
	if checkErr == nil {
		return nil
	}

	installTimeout := 6 * time.Minute
	installCmd := "set -euo pipefail; command -v npm >/dev/null 2>&1; npm i -g opencode-ai"
	installCtx, installCancel := context.WithTimeout(context.Background(), installTimeout)
	installOut, installErr := doctorCheckCommandRunner(installCtx, projectRoot, "probe", "firecracker-opencode-tooling", 1, 1, installTimeout, "bash", []string{"-lc", installCmd}, execCtx)
	installCancel()
	if installErr != nil {
		return fmt.Errorf("firecracker opencode install failed: %s", strings.TrimSpace(installOut))
	}

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), checkTimeout)
	verifyOut, verifyErr := doctorCheckCommandRunner(verifyCtx, projectRoot, "probe", "firecracker-opencode-tooling", 1, 1, checkTimeout, "bash", []string{"-lc", "command -v opencode >/dev/null 2>&1 && opencode --version"}, execCtx)
	verifyCancel()
	if verifyErr != nil {
		return fmt.Errorf("firecracker opencode verification failed: %s", strings.TrimSpace(verifyOut))
	}
	return nil
}

func bootstrapFirecrackerExecContext(projectRoot string, execCtx doctorExecContext) error {
	// Native firecracker execution requires manager+agent adapter
	// Legacy LXC-based bootstrap is removed in native firecracker cutover
	// Firecracker backend now requires native runtime support through the daemon
	return fmt.Errorf("backend \"firecracker\" requires native runtime support; use workspace daemon with firecracker driver instead of doctor bootstrap")
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
		return bootstrapFirecrackerExecContext(projectRoot, execCtx)
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

	return bootstrapContainerExecContext(projectRoot, execCtx, "lxc", true)
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
	if execCtx.backend == "firecracker" && contextLabel == "host" {
		msg := "backend \"firecracker\" resolved to host execution context; refusing to run doctor checks outside microVM"
		return msg, errors.New(msg)
	}

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
		fcName:     strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_FIRECRACKER_INSTANCE")),
		fcExec:     strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE")),
	}
}

func resolveCheckCommand(projectRoot, command string, args []string, execCtx doctorExecContext) (string, []string, []string, string) {
	// Firecracker backend no longer uses LXC execution - native manager+agent style
	// When firecracker backend is set but native execution isn't wired, it falls through
	// to host context which will be rejected by runCheckCommandWithExecContext

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
