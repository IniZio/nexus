package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/buildinfo"
	"github.com/inizio/nexus/packages/nexus/pkg/compose"
	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/credsbundle"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/firecracker"
	"github.com/inizio/nexus/packages/nexus/pkg/update"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
	"github.com/spf13/cobra"
)

type options struct {
	projectRoot string
	reportJSON  string
}

type execOptions struct {
	projectRoot string
	timeout     time.Duration
	command     string
	args        []string
}

type initOptions struct {
	projectRoot string
	force       bool
}

const execKVMGroupReexecEnv = "NEXUS_EXEC_KVM_GROUP_REEXEC"

var rootCmd = &cobra.Command{
	Use:           "nexus",
	SilenceUsage:  true,
	SilenceErrors: true,
}

var doctorReportJSON string

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run workspace health checks",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		projectRoot, err := filepath.Abs(".")
		if err != nil {
			return fmt.Errorf("resolve project root: %w", err)
		}
		return run(options{
			projectRoot: projectRoot,
			reportJSON:  strings.TrimSpace(doctorReportJSON),
		})
	},
}

var initForce bool

var initCmd = &cobra.Command{
	Use:   "init [project-root]",
	Short: "Scaffold .nexus in a git repository",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		projectRoot := "."
		if len(args) == 1 {
			projectRoot = args[0]
		}
		abs, err := filepath.Abs(strings.TrimSpace(projectRoot))
		if err != nil {
			return fmt.Errorf("resolve project root: %w", err)
		}
		return runInit(initOptions{projectRoot: abs, force: initForce})
	},
}

var runBackend string
var runTimeout time.Duration
var updateCheckOnly bool
var updateForce bool
var updateRollback bool
var updateJSON bool
var versionJSON bool
var githubReleaseAPIBaseURL = "https://api.github.com"

var runCmd = &cobra.Command{
	Use:   "run [--backend name] [--timeout dur] -- <command> [args...]",
	Short: "Run a command in a new ephemeral workspace",
	Args:  cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.ArgsLenAtDash() == -1 {
			return fmt.Errorf("usage: nexus run [--backend <name>] [--timeout <dur>] -- <command> [args...]")
		}
		if len(args) == 0 {
			return fmt.Errorf("command required after --")
		}
		return runRun(strings.TrimSpace(runBackend), runTimeout, args)
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show CLI, daemon, and latest release version",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runVersion(versionJSON)
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for updates and apply latest release",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUpdate(updateCheckOnly, updateForce, updateRollback, updateJSON)
	},
}

func init() {
	doctorCmd.Flags().StringVar(&doctorReportJSON, "report-json", "", "optional path to write doctor probe results as JSON")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite existing .nexus files")
	runCmd.Flags().StringVar(&runBackend, "backend", "", "runtime backend override")
	runCmd.Flags().DurationVar(&runTimeout, "timeout", 10*time.Minute, "max time for the workspace run")
	versionCmd.Flags().BoolVar(&versionJSON, "json", false, "render machine-readable output")
	updateCmd.Flags().BoolVar(&updateCheckOnly, "check", false, "check latest version without applying update")
	updateCmd.Flags().BoolVar(&updateForce, "force", false, "ignore update check interval and re-evaluate latest release")
	updateCmd.Flags().BoolVar(&updateRollback, "rollback", false, "rollback CLI and daemon binaries to previous version")
	updateCmd.Flags().BoolVar(&updateJSON, "json", false, "render machine-readable output")
	rootCmd.AddCommand(doctorCmd, initCmd, runCmd, versionCmd, updateCmd)
}

func main() {
	tryBackgroundAutoUpdate()
	args := os.Args[1:]
	for len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		printUsage()
		os.Exit(2)
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-h" && args[0] != "--help" {
		rootCmd.SetArgs(append([]string{"doctor"}, args...))
	} else {
		rootCmd.SetArgs(args)
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func tryBackgroundAutoUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_, _ = update.AutoUpdate(ctx, update.Options{
		ReleaseBaseURL:     releaseBaseURL(),
		PublicKeyBase64:    strings.TrimSpace(buildinfo.UpdatePublicKeyBase64),
		CheckInterval:      4 * time.Hour,
		BadVersionCooldown: 24 * time.Hour,
		CurrentVersion:     buildinfo.CLI().Version,
		CurrentUpdater:     buildinfo.CLI().Version,
		AutoApply:          true,
		Force:              false,
	})
}

func runVersion(asJSON bool) error {
	info := buildinfo.CLI()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	status, _ := update.Check(ctx, update.Options{
		ReleaseBaseURL:     releaseBaseURL(),
		PublicKeyBase64:    strings.TrimSpace(buildinfo.UpdatePublicKeyBase64),
		CheckInterval:      0,
		BadVersionCooldown: 24 * time.Hour,
		CurrentVersion:     info.Version,
		CurrentUpdater:     info.Version,
		AutoApply:          false,
		Force:              true,
	})
	daemonVersion := fetchDaemonVersion()
	payload := map[string]any{
		"cli":    info,
		"daemon": daemonVersion,
		"update": status,
		"channel": map[string]string{
			"name": channelName(),
			"repo": channelRepo(),
		},
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(payload)
	}
	fmt.Printf("CLI:     %s\n", info.Version)
	if daemonVersion != "" {
		fmt.Printf("Daemon:  %s\n", daemonVersion)
	} else {
		fmt.Printf("Daemon:  unavailable\n")
	}
	if strings.TrimSpace(status.LatestVersion) != "" {
		fmt.Printf("Latest:  %s\n", status.LatestVersion)
	}
	if strings.TrimSpace(status.LastFailure) != "" {
		fmt.Printf("Update:  %s\n", status.LastFailure)
	} else if status.UpdateReady {
		fmt.Printf("Update:  available\n")
	} else {
		fmt.Printf("Update:  up to date\n")
	}
	fmt.Printf("Channel: %s (%s)\n", channelName(), channelRepo())
	return nil
}

func runUpdate(checkOnly, force, rollback, asJSON bool) error {
	if rollback {
		err := update.Rollback(context.Background())
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"rolledBack": err == nil,
				"error":      errString(err),
			})
		}
		if err != nil {
			return err
		}
		fmt.Println("rollback completed")
		return nil
	}
	opts := update.Options{
		ReleaseBaseURL:     releaseBaseURL(),
		PublicKeyBase64:    strings.TrimSpace(buildinfo.UpdatePublicKeyBase64),
		CheckInterval:      0,
		BadVersionCooldown: 24 * time.Hour,
		CurrentVersion:     buildinfo.CLI().Version,
		CurrentUpdater:     buildinfo.CLI().Version,
		AutoApply:          !checkOnly,
		Force:              force,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if checkOnly {
		status, err := update.Check(ctx, opts)
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": status, "error": errString(err)})
		}
		if err != nil {
			return err
		}
		if status.UpdateReady {
			fmt.Printf("update available: %s -> %s\n", status.CurrentVersion, status.LatestVersion)
			return nil
		}
		fmt.Println("already up to date")
		return nil
	}
	result, err := update.ForceUpdate(ctx, opts)
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"result": result, "error": errString(err)})
	}
	if err != nil {
		return err
	}
	if result.Updated {
		fmt.Printf("updated: %s -> %s\n", result.FromVersion, result.ToVersion)
		return nil
	}
	fmt.Println("already up to date")
	return nil
}

func fetchDaemonVersion() string {
	port := daemonPort()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://localhost:%d/version", port), nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}
	return strings.TrimSpace(body.Version)
}

func releaseBaseURL() string {
	if value := strings.TrimSpace(os.Getenv("NEXUS_RELEASE_BASE_URL")); value != "" {
		return value
	}
	channel := channelName()
	repo := channelRepo()
	if channel == "prerelease" {
		if prereleaseURL, err := latestPrereleaseBaseURL(repo); err == nil && prereleaseURL != "" {
			return prereleaseURL
		}
	}
	return "https://github.com/inizio/nexus/releases/latest/download"
}

func channelName() string {
	channel := strings.ToLower(strings.TrimSpace(os.Getenv("NEXUS_RELEASE_CHANNEL")))
	if channel == "" {
		return "stable"
	}
	return channel
}

func channelRepo() string {
	repo := strings.TrimSpace(os.Getenv("NEXUS_RELEASE_REPO"))
	if repo == "" {
		return "inizio/nexus"
	}
	return repo
}

func latestPrereleaseBaseURL(repo string) (string, error) {
	url := strings.TrimRight(githubReleaseAPIBaseURL, "/") + "/repos/" + repo + "/releases"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github release lookup failed with status %d", resp.StatusCode)
	}
	var releases []struct {
		TagName    string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", err
	}
	for _, release := range releases {
		if release.Draft || !release.Prerelease {
			continue
		}
		tag := strings.TrimSpace(release.TagName)
		if tag == "" {
			continue
		}
		return "https://github.com/" + repo + "/releases/download/" + tag, nil
	}
	return "", fmt.Errorf("no prerelease found")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  nexus doctor [--report-json path]")
	fmt.Fprintln(os.Stderr, "  nexus init [project-root] [--force]")
	fmt.Fprintln(os.Stderr, "  nexus run [--backend <name>] [--timeout <dur>] -- <command> [args...]")
	fmt.Fprintln(os.Stderr, "  nexus fork <id> <name> [--ref <ref>]")
	fmt.Fprintln(os.Stderr, "  nexus shell <id> [--timeout <dur>]")
	fmt.Fprintln(os.Stderr, "  nexus exec <id> [--timeout <dur>] -- <command> [args...]")
	fmt.Fprintln(os.Stderr, "  nexus <list|create|start|stop|remove|pause|resume|restore|shell|exec|tunnel>")
}

func runRun(backend string, timeout time.Duration, cmdArgs []string) error {
	repoPath, err := normalizeLocalRepoPath(".")
	if err != nil {
		return fmt.Errorf("nexus run: %w", err)
	}
	workspaceName := deriveWorkspaceName(repoPath)

	conn, err := ensureDaemon()
	if err != nil {
		return fmt.Errorf("nexus run: %w", err)
	}
	defer conn.Close()

	configBundle, err := credsbundle.Build()
	if err != nil {
		return fmt.Errorf("nexus run: %w", err)
	}

	spec := workspacemgr.CreateSpec{
		Repo:          repoPath,
		Ref:           "",
		WorkspaceName: workspaceName,
		AgentProfile:  "default",
		Backend:       backend,
		ConfigBundle:  configBundle,
	}
	var createResult struct {
		Workspace workspacemgr.Workspace `json:"workspace"`
	}
	if err := daemonRPC(conn, "workspace.create", map[string]any{"spec": spec}, &createResult); err != nil {
		if renderPreflightCreateError(err) {
			os.Exit(1)
		}
		return fmt.Errorf("nexus run: create failed: %w", err)
	}
	wsID := createResult.Workspace.ID

	defer func() {
		if removeErr := daemonRPC(conn, "workspace.remove", map[string]any{"id": wsID}, nil); removeErr != nil {
			fmt.Fprintf(os.Stderr, "nexus run: cleanup warning: %v\n", removeErr)
		}
	}()

	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("nexus run: timed out waiting for workspace to become ready")
		}
		var readyResult struct {
			Ready bool `json:"ready"`
		}
		if readyErr := daemonRPC(conn, "workspace.ready", map[string]any{
			"workspaceId": wsID,
			"profile":     "default",
		}, &readyResult); readyErr == nil && readyResult.Ready {
			break
		}
		time.Sleep(3 * time.Second)
	}

	cmdLine := formatCommand(cmdArgs[0], cmdArgs[1:])
	payload := "cd /workspace >/dev/null 2>&1 || true\n" + cmdLine + "\nexit\n"
	token := strings.TrimSpace(os.Getenv("NEXUS_AUTH_RELAY_TOKEN"))
	runWorkspacePTYSession("nexus run", wsID, token, "bash", payload, timeout, false)
	return nil
}

func runInit(opts initOptions) error {
	if !filepath.IsAbs(opts.projectRoot) {
		return fmt.Errorf("project root must be absolute: %s", opts.projectRoot)
	}

	nexusDir := filepath.Join(opts.projectRoot, ".nexus")
	if err := os.MkdirAll(nexusDir, 0o755); err != nil {
		return fmt.Errorf("create .nexus directory: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(nexusDir, "lifecycles"), 0o755); err != nil {
		return fmt.Errorf("create .nexus/lifecycles directory: %w", err)
	}

	workspaceCfg := config.WorkspaceConfig{
		Version: 1,
	}

	workspaceJSON, err := json.MarshalIndent(workspaceCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace config: %w", err)
	}
	workspaceJSON = append(workspaceJSON, '\n')

	files := map[string]string{
		filepath.Join(nexusDir, "workspace.json"):            string(workspaceJSON),
		filepath.Join(nexusDir, "lifecycles", "setup.sh"):    "#!/usr/bin/env bash\nset -euo pipefail\necho 'setup: no-op'\n",
		filepath.Join(nexusDir, "lifecycles", "start.sh"):    "#!/usr/bin/env bash\nset -euo pipefail\necho 'start: no-op'\n",
		filepath.Join(nexusDir, "lifecycles", "teardown.sh"): "#!/usr/bin/env bash\nset -euo pipefail\necho 'teardown: no-op'\n",
	}

	for path, content := range files {
		if !opts.force {
			if _, err := os.Stat(path); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("stat %s: %w", path, err)
			}
		}

		mode := os.FileMode(0o644)
		if strings.HasSuffix(path, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}

	if err := initRuntimeBootstrapRunner(opts.projectRoot, "firecracker"); err != nil {
		fmt.Printf("init warning: firecracker bootstrap unavailable, runtime will auto-fallback (%v)\n", err)
	}

	fmt.Printf("initialized nexus workspace metadata at %s\n", nexusDir)
	return nil
}

func runExec(opts execOptions) error {
	if !filepath.IsAbs(opts.projectRoot) {
		return errNotAbsProjectRoot("project root", opts.projectRoot)
	}
	if err := applyRuntimeBackendFromWorkspace(opts.projectRoot); err != nil {
		return err
	}
	if opts.command == "" {
		return errors.New("command is required")
	}
	if opts.timeout <= 0 {
		return fmt.Errorf("timeout must be > 0: %s", opts.timeout)
	}

	defer func() {
		if cleanupErr := runDoctorExecContextCleanup(); cleanupErr != nil {
			fmt.Printf("exec warning: firecracker cleanup failed: %v\n", cleanupErr)
		}
	}()
	execCtx := loadDoctorExecContext()
	if err := execCommandBootstrapRunner(opts.projectRoot); err != nil {
		if shouldReexecExecWithKVMGroup(execCtx.backend, err) {
			cmdPath := setupCommandPath()
			reexecArgs := make([]string, 0, len(opts.args)+8)
			reexecArgs = append(reexecArgs, "run", opts.projectRoot, "--timeout", opts.timeout.String(), "--", opts.command)
			reexecArgs = append(reexecArgs, opts.args...)
			if reexecErr := execKVMGroupReexecRunner(cmdPath, reexecArgs); reexecErr == nil {
				return nil
			} else {
				return fmt.Errorf("%w; sg kvm reexec failed: %v", err, reexecErr)
			}
		}
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	fmt.Printf("exec exec: %s (attempt %d/%d, timeout=%s, context=%s): %s\n", opts.command, 1, 1, opts.timeout, execCtx.backend, formatCommand(opts.command, opts.args))
	out, err := runCheckCommandWithExecContext(ctx, opts.projectRoot, "exec", opts.command, 1, 1, opts.timeout, opts.command, opts.args, execCtx)

	if strings.TrimSpace(out) != "" {
		fmt.Println(strings.TrimSpace(out))
	}
	if err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	return nil
}

func shouldReexecExecWithKVMGroup(backend string, runErr error) bool {
	if runErr == nil {
		return false
	}
	if backend != "firecracker" {
		return false
	}
	if os.Getenv(execKVMGroupReexecEnv) == "1" {
		return false
	}
	if _, err := exec.LookPath("sg"); err != nil {
		return false
	}
	return strings.Contains(runErr.Error(), "/dev/kvm")
}

func runExecWithKVMGroupReexec(commandPath string, args []string) error {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(commandPath))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	inner := strings.Join(parts, " ")

	cmd := exec.Command("sg", "kvm", "-c", inner)
	cmd.Env = append(os.Environ(), execKVMGroupReexecEnv+"=1")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func run(opts options) error {
	if !filepath.IsAbs(opts.projectRoot) {
		return errNotAbsProjectRoot("project root", opts.projectRoot)
	}
	if err := applyRuntimeBackendFromWorkspace(opts.projectRoot); err != nil {
		return err
	}

	execCtx := loadDoctorExecContext()
	fmt.Printf("doctor: runtime backend=%s (cold firecracker: first run may take several minutes before suite probes)\n", execCtx.backend)
	if execCtx.backend == "firecracker" {
		if err := config.ValidateFirecrackerEnv(); err != nil {
			return fmt.Errorf("firecracker configuration error: %w", err)
		}
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

	probesToRun, testsToRun, warnings, err := resolveDoctorChecks(opts.projectRoot)
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
	if err := doctorExecBootstrapRunner(opts.projectRoot); err != nil {
		execCtx := loadDoctorExecContext()
		if shouldReexecExecWithKVMGroup(execCtx.backend, err) {
			cmdPath := setupCommandPath()
			reexecArgs := append([]string(nil), os.Args[1:]...)
			if reexecErr := execKVMGroupReexecRunner(cmdPath, reexecArgs); reexecErr == nil {
				return nil
			} else {
				return fmt.Errorf("%w; sg kvm reexec failed: %v", err, reexecErr)
			}
		}
		return err
	}

	if execCtx.backend == "firecracker" {
		if err := doctorFirecrackerRuntimeVerifier(); err != nil {
			return err
		}
	}

	if err := doctorLifecycleSetupRunner(opts.projectRoot, execCtx); err != nil {
		return err
	}

	if err := doctorLifecycleStartRunner(opts.projectRoot, execCtx); err != nil {
		return err
	}

	publishedPorts := make([]compose.PublishedPort, 0)
	discoverCtx, discoverCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer discoverCancel()
	if ports, discoverErr := compose.DiscoverPublishedPorts(discoverCtx, opts.projectRoot); discoverErr != nil {
		fmt.Printf("doctor warning: compose port discovery failed: %v\n", discoverErr)
	} else {
		publishedPorts = ports
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

	fmt.Printf("doctor passed (discovered %d compose ports)\n", len(publishedPorts))
	return nil
}

func verifyFirecrackerGuestDockerRuntime() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	commandProjectRoot := "/workspace"
	if firecrackerHostGOOS == "darwin" {
		if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
			commandProjectRoot = cwd
		}
	}

	verifyCmd := "if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then exit 0; fi; if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1 && sudo -n docker info >/dev/null 2>&1; then exit 0; fi; if command -v docker >/dev/null 2>&1; then docker info 2>&1 || true; else echo 'docker: not found'; fi; if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then sudo -n docker info 2>&1 || true; fi; exit 1"
	out, err := runFirecrackerCheckCommandForHost(ctx, commandProjectRoot, "sh", []string{"-lc", verifyCmd})
	if err == nil {
		return nil
	}

	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = err.Error()
	}

	return fmt.Errorf("firecracker verification requires docker runtime inside guest workspace; host docker is not used. guest docker check failed: %s", detail)
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
	backend string
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

var firecrackerWorkspaceVerifier = verifyFirecrackerWorkspaceReady

var firecrackerBootstrapRunner = dispatchFirecrackerBootstrap

var containerBootstrapRunner = bootstrapContainerExecContext

var doctorExecBootstrapRunner = bootstrapDoctorExecContext

var execCommandBootstrapRunner = bootstrapExecCommandContext

var doctorFirecrackerRuntimeVerifier = verifyFirecrackerGuestDockerRuntime

var doctorLifecycleStartRunner = runDoctorLifecycleStart

var doctorLifecycleSetupRunner = runDoctorLifecycleSetup

var execKVMGroupReexecRunner = runExecWithKVMGroupReexec

var doctorExecCleanup func() error

var firecrackerDoctorSessionState *firecrackerDoctorSession

var hostDockerSocketStat = os.Stat

var firecrackerHostBinaryLookup = exec.LookPath

var firecrackerHostStat = os.Stat

var firecrackerDefaultAssetStat = os.Stat

var firecrackerDefaultKernelPath = "/var/lib/nexus/vmlinux.bin"

var firecrackerDefaultRootFSPath = "/var/lib/nexus/rootfs.ext4"

var firecrackerHostOpenFile = os.OpenFile

var firecrackerHostGOOS = runtime.GOOS

// firecrackerTapHelperValidator validates the tap helper binary.
// Overridable in tests.
var firecrackerTapHelperValidator = func() error { return validateFirecrackerTapHelper() }

// firecrackerBridgeValidator validates that nexusbr0 exists and is UP.
// Overridable in tests.
var firecrackerBridgeValidator = func() error { return validateFirecrackerBridge() }

func runBootstrapInstallCommand(ctx context.Context, projectRoot string, timeout time.Duration, execCtx doctorExecContext) (string, error) {
	aptOpts := "-o Acquire::Retries=1 -o Acquire::http::Timeout=15 -o Acquire::https::Timeout=15"
	installCmd := "chmod 1777 /tmp; apt-get clean >/dev/null 2>&1 || true; rm -rf /var/lib/apt/lists/*; mkdir -p /var/cache/apt/archives/partial /var/lib/apt/lists/partial; apt-get " + aptOpts + " update && DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confold " + aptOpts + " install -y bash docker.io curl make python3 git nodejs npm iptables docker-compose-v2 docker-buildx-plugin || DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confold " + aptOpts + " install -y bash docker.io curl make python3 git nodejs npm iptables docker-compose-v2 || DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confold " + aptOpts + " install -y bash docker.io curl make python3 git nodejs npm iptables && DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confold " + aptOpts + " install -y docker-compose-v2 docker-buildx-plugin || DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confold " + aptOpts + " install -y docker-compose-v2 || true; command -v make >/dev/null 2>&1 || exit 1"
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

func doctorFirecrackerMachineSpec() (int, int) {
	memoryMiB := 4096
	vcpus := 2

	if raw := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_FIRECRACKER_MEMORY_MIB")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 512 {
			memoryMiB = parsed
		}
	}

	if raw := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_FIRECRACKER_VCPUS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			vcpus = parsed
		}
	}

	return memoryMiB, vcpus
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
			"%s not found in PATH\n\nRun `nexus init --force` to provision host prerequisites",
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
			"%s at %s lacks cap_net_admin\n\nRun `nexus init --force` to refresh host prerequisites",
			tapHelper, path,
		)
	}
	return nil
}

// validateFirecrackerBridge verifies that the nexusbr0 bridge exists and is UP
// so that TAP devices can be attached to it at VM spawn time.
func validateFirecrackerBridge() error {
	const bridge = "nexusbr0"
	const gatewayCIDR = "172.26.0.1/16"
	out, err := exec.Command("ip", "link", "show", bridge).CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"bridge %s not found\n\nRun `nexus init --force` to provision host prerequisites",
			bridge,
		)
	}
	if !strings.Contains(string(out), "UP") {
		return fmt.Errorf(
			"bridge %s exists but is not UP\n\nRun `nexus init --force` to refresh host prerequisites",
			bridge,
		)
	}

	addrOut, addrErr := exec.Command("ip", "-4", "addr", "show", "dev", bridge).CombinedOutput()
	if addrErr != nil {
		return fmt.Errorf("bridge %s IPv4 inspection failed: %w", bridge, addrErr)
	}
	if !strings.Contains(string(addrOut), "172.26.0.1/") {
		return fmt.Errorf(
			"bridge %s is missing gateway IP %s\n\nRun `nexus init --force` to refresh host prerequisites",
			bridge, gatewayCIDR,
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
	doctorMemoryMiB, doctorVCPUs := doctorFirecrackerMachineSpec()

	workspaceID := fmt.Sprintf("doctor-%d", time.Now().UnixNano())
	instance, err := manager.Spawn(ctx, firecracker.SpawnSpec{
		WorkspaceID: workspaceID,
		ProjectRoot: projectRoot,
		MemoryMiB:   doctorMemoryMiB,
		VCPUs:       doctorVCPUs,
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

	if err := firecrackerWorkspaceVerifier(); err != nil {
		return err
	}

	return nil
}

func verifyFirecrackerWorkspaceReady() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	workspacePath := "/workspace"
	if firecrackerHostGOOS == "darwin" {
		if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
			workspacePath = cwd
		}
	}

	probeCmd := fmt.Sprintf("test -d %s", shellQuote(workspacePath))
	out, err := runFirecrackerCheckCommandForHost(ctx, workspacePath, "sh", []string{"-lc", probeCmd})
	if err == nil {
		return nil
	}

	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = err.Error()
	}

	if strings.Contains(detail, "chdir /workspace: no such file or directory") {
		return fmt.Errorf("firecracker guest is missing /workspace; re-run `nexus init --force` to refresh the runtime and then retry")
	}

	return fmt.Errorf("firecracker guest workspace verification failed: %s", detail)
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
	hostUID := os.Getuid()
	hostGID := os.Getgid()
	if hostUID == 0 {
		if raw := strings.TrimSpace(os.Getenv("SUDO_UID")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				hostUID = parsed
			}
		}
	}
	if hostGID == 0 {
		if raw := strings.TrimSpace(os.Getenv("SUDO_GID")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				hostGID = parsed
			}
		}
	}
	env := []string{
		fmt.Sprintf("UID=%d", hostUID),
		fmt.Sprintf("GID=%d", hostGID),
		fmt.Sprintf("HOST_UID=%d", hostUID),
		fmt.Sprintf("HOST_GID=%d", hostGID),
	}
	if backend := strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_BACKEND")); backend != "" {
		env = append(env, "NEXUS_RUNTIME_BACKEND="+backend)
	}
	if mirror := strings.TrimSpace(os.Getenv("NEXUS_DOCKER_REGISTRY_MIRROR")); mirror != "" {
		env = append(env, "NEXUS_DOCKER_REGISTRY_MIRROR="+mirror)
	}
	if username := strings.TrimSpace(os.Getenv("NEXUS_DOCKERHUB_USERNAME")); username != "" {
		env = append(env, "NEXUS_DOCKERHUB_USERNAME="+username)
	}
	if token := strings.TrimSpace(os.Getenv("NEXUS_DOCKERHUB_TOKEN")); token != "" {
		env = append(env, "NEXUS_DOCKERHUB_TOKEN="+token)
	}

	request := firecracker.ExecRequest{
		ID:      firecrackerRequestID(),
		Command: command,
		Args:    args,
		WorkDir: "/workspace",
		Env:     env,
		Stream:  true,
	}
	result, err := agentClient.ExecStreaming(ctx, request, func(_ string, data string) {
		if data == "" {
			return
		}
		fmt.Print(data)
	})
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
	if execCtx.backend != "firecracker" {
		return fmt.Errorf("unsupported runtime backend %q: only firecracker is supported", execCtx.backend)
	}

	timeout := 90 * time.Second
	commandProjectRoot := projectRoot
	if execCtx.backend == "firecracker" {
		commandProjectRoot = "/"
	}
	hostProxyMode := execCtx.backend == "firecracker" && strings.EqualFold(strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE")), "host-proxy")
	collectDockerDiagnostics := func() string {
		diagCmd := "set +e; echo '--- docker binary ---'; command -v docker || true; echo '--- docker version ---'; docker version || true; echo '--- docker info ---'; docker info || true; echo '--- dockerd ps ---'; ps -ef | grep '[d]ockerd' || true; echo '--- dockerd log ---'; cat /tmp/nexus-doctor-dockerd.log || true; if command -v systemctl >/dev/null 2>&1; then echo '--- systemctl status docker ---'; systemctl status docker --no-pager || true; fi"
		diagCtx, diagCancel := context.WithTimeout(context.Background(), 45*time.Second)
		diagOut, _ := doctorCheckCommandRunner(diagCtx, commandProjectRoot, "probe", "runtime-backend-capabilities", 1, 1, 45*time.Second, "sh", []string{"-lc", diagCmd}, execCtx)
		diagCancel()
		return strings.TrimSpace(diagOut)
	}
	capabilityChecks := [][]string{
		{"docker", "info"},
		{"docker", "compose", "version"},
	}
	if hasMakeTarget(projectRoot, "start") {
		capabilityChecks = append(capabilityChecks, []string{"make", "--version"})
	}
	runCapabilityChecks := func() (bool, string) {
		failures := make([]string, 0)
		for _, check := range capabilityChecks {
			checkCtx, checkCancel := context.WithTimeout(context.Background(), timeout)
			out, err := doctorCheckCommandRunner(checkCtx, commandProjectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, check[0], check[1:], execCtx)
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

	startDockerCmd := `mkdir -p /sys/fs/cgroup; if ! grep -q ' /sys/fs/cgroup ' /proc/mounts; then mount -t cgroup2 none /sys/fs/cgroup 2>/dev/null || mount -t tmpfs tmpfs /sys/fs/cgroup || true; fi; if ! grep -q ' /sys/fs/cgroup cgroup2 ' /proc/mounts; then for s in cpuset cpu cpuacct blkio memory devices freezer net_cls perf_event net_prio hugetlb pids; do mkdir -p /sys/fs/cgroup/$s; mount -t cgroup -o $s cgroup /sys/fs/cgroup/$s 2>/dev/null || true; done; fi; ip link set lo up >/dev/null 2>&1 || true; chmod 1777 /tmp >/dev/null 2>&1 || true; if [ -x /usr/sbin/xtables-legacy-multi ]; then ln -sf /usr/sbin/xtables-legacy-multi /usr/sbin/iptables-legacy >/dev/null 2>&1 || true; ln -sf /usr/sbin/xtables-legacy-multi /usr/sbin/ip6tables-legacy >/dev/null 2>&1 || true; fi; if [ -x /usr/bin/xtables-legacy-multi ]; then ln -sf /usr/bin/xtables-legacy-multi /usr/bin/iptables-legacy >/dev/null 2>&1 || true; ln -sf /usr/bin/xtables-legacy-multi /usr/bin/ip6tables-legacy >/dev/null 2>&1 || true; fi; if ! command -v iptables-legacy >/dev/null 2>&1; then apt-get clean >/dev/null 2>&1 || true; rm -rf /var/lib/apt/lists/*; mkdir -p /var/lib/apt/lists/partial /var/cache/apt/archives/partial; apt-get -o Acquire::Retries=1 -o Acquire::http::Timeout=15 -o Acquire::https::Timeout=15 update >/tmp/nexus-iptables-apt.log 2>&1 || true; DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confold -o Acquire::Retries=1 -o Acquire::http::Timeout=15 -o Acquire::https::Timeout=15 install -y iptables >>/tmp/nexus-iptables-apt.log 2>&1 || true; fi; for p in /usr/local/sbin /usr/local/bin /usr/sbin /usr/bin; do [ -x "$p/iptables-legacy" ] && ln -sf "$p/iptables-legacy" "$p/iptables" >/dev/null 2>&1 || true; [ -x "$p/ip6tables-legacy" ] && ln -sf "$p/ip6tables-legacy" "$p/ip6tables" >/dev/null 2>&1 || true; done; if command -v iptables-legacy >/dev/null 2>&1 && command -v update-alternatives >/dev/null 2>&1; then update-alternatives --set iptables /usr/sbin/iptables-legacy >/dev/null 2>&1 || true; update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy >/dev/null 2>&1 || true; fi; iptables_flag='--iptables=false'; if command -v iptables-legacy >/dev/null 2>&1; then iptables_flag='--iptables=true'; fi; if command -v sysctl >/dev/null 2>&1; then sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true; sysctl -w net.ipv4.conf.all.forwarding=1 >/dev/null 2>&1 || true; else echo 1 >/proc/sys/net/ipv4/ip_forward 2>/dev/null || true; echo 1 >/proc/sys/net/ipv4/conf/all/forwarding 2>/dev/null || true; fi; if command -v modprobe >/dev/null 2>&1; then modprobe iptable_raw >/dev/null 2>&1 || true; modprobe iptable_nat >/dev/null 2>&1 || true; modprobe xt_addrtype >/dev/null 2>&1 || true; fi; if command -v systemctl >/dev/null 2>&1; then systemctl enable docker >/dev/null 2>&1 || true; systemctl start docker >/dev/null 2>&1 || true; fi; mirror_flag=''; if [ -n "${NEXUS_DOCKER_REGISTRY_MIRROR:-}" ]; then mirror_flag="--registry-mirror=${NEXUS_DOCKER_REGISTRY_MIRROR}"; else mirror_flag='--registry-mirror=https://mirror.gcr.io'; fi; dns_flags=''; if [ -n "${NEXUS_DOCKER_DNS:-}" ]; then for dns in $(echo "${NEXUS_DOCKER_DNS}" | tr ',' ' '); do [ -n "$dns" ] && dns_flags="$dns_flags --dns=$dns"; done; else for dns in $(awk '/^nameserver[ \t]+/ { print $2 }' /etc/resolv.conf 2>/dev/null); do case "$dns" in 127.*|::1) continue ;; esac; dns_flags="$dns_flags --dns=$dns"; done; for dns in 1.1.1.1 8.8.8.8; do case " $dns_flags " in *" --dns=$dns "*) ;; *) dns_flags="$dns_flags --dns=$dns" ;; esac; done; fi; raw_optout_env=''; if [ "${NEXUS_DOCKER_DISABLE_IPTABLES_RAW:-1}" = "1" ]; then raw_optout_env='DOCKER_INSECURE_NO_IPTABLES_RAW=1'; fi; if ! docker info >/dev/null 2>&1; then mkdir -p /workspace/.nexus-docker; pkill dockerd >/dev/null 2>&1 || true; pkill containerd >/dev/null 2>&1 || true; rm -f /var/run/docker.pid /var/run/docker/containerd/containerd.pid 2>/dev/null || true; rm -rf /var/lib/docker/* /var/lib/containerd/* 2>/dev/null || true; nohup sh -lc "exec env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin ${raw_optout_env} dockerd --host=unix:///var/run/docker.sock --data-root=/workspace/.nexus-docker --exec-root=/workspace/.nexus-docker-exec --storage-driver=overlay2 ${iptables_flag} --allow-direct-routing ${dns_flags} ${mirror_flag}" >/tmp/nexus-doctor-dockerd.log 2>&1 & sleep 5; if ! docker info >/dev/null 2>&1 && grep -q 'unknown flag: --allow-direct-routing' /tmp/nexus-doctor-dockerd.log 2>/dev/null; then nohup sh -lc "exec env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin ${raw_optout_env} dockerd --host=unix:///var/run/docker.sock --data-root=/workspace/.nexus-docker --exec-root=/workspace/.nexus-docker-exec --storage-driver=overlay2 ${iptables_flag} ${dns_flags} ${mirror_flag}" >/tmp/nexus-doctor-dockerd.log 2>&1 & sleep 5; fi; fi; if [ -n "${NEXUS_DOCKERHUB_USERNAME:-}" ] && [ -n "${NEXUS_DOCKERHUB_TOKEN:-}" ]; then echo "$NEXUS_DOCKERHUB_TOKEN" | docker login -u "$NEXUS_DOCKERHUB_USERNAME" --password-stdin docker.io >/tmp/nexus-docker-login.log 2>&1 || true; fi`
	startCtx, startCancel := context.WithTimeout(context.Background(), timeout)
	startOut, startErr := doctorCheckCommandRunner(startCtx, commandProjectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "sh", []string{"-lc", startDockerCmd}, execCtx)
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
	installOut, installErr := bootstrapInstallCommandRunner(installCtx, commandProjectRoot, timeout, execCtx)
	installCancel()
	if installErr != nil {
		trimmedOut := strings.TrimSpace(installOut)
		if trimmedOut != "" {
			fmt.Printf("bootstrap %s tooling: install command failed, continuing with existing runtime packages (%s)\n", backendLabel, trimmedOut)
		} else {
			fmt.Printf("bootstrap %s tooling: install command failed, continuing with existing runtime packages\n", backendLabel)
		}
	}

	startCtx, startCancel = context.WithTimeout(context.Background(), timeout)
	startOut, startErr = doctorCheckCommandRunner(startCtx, commandProjectRoot, "probe", "runtime-backend-capabilities", 1, 1, timeout, "sh", []string{"-lc", startDockerCmd}, execCtx)
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

func dispatchFirecrackerBootstrap(projectRoot string, execCtx doctorExecContext) error {
	if firecrackerHostGOOS == "darwin" {
		return bootstrapFirecrackerExecContextDarwinFn(projectRoot, execCtx)
	}
	return bootstrapFirecrackerExecContextNative(projectRoot, execCtx)
}

func bootstrapFirecrackerExecContext(projectRoot string, execCtx doctorExecContext) error {
	return firecrackerBootstrapRunner(projectRoot, execCtx)
}

func runFirecrackerCheckCommandForHost(ctx context.Context, projectRoot, command string, args []string) (string, error) {
	if firecrackerHostGOOS == "darwin" {
		return runLimaCheckCommandFn(ctx, projectRoot, command, args)
	}
	return firecrackerCheckCommandRunner(ctx, projectRoot, command, args)
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

func runDoctorLifecycleStart(projectRoot string, execCtx doctorExecContext) error {
	command, args, contextLabel, summary, found, err := resolveDoctorLifecycleStartCommand(projectRoot)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	fmt.Printf("doctor lifecycle start selected command: %s\n", summary)

	timeout := 10 * time.Minute
	if rawTimeout := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_START_TIMEOUT_MS")); rawTimeout != "" {
		if ms, err := strconv.Atoi(rawTimeout); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := doctorCheckCommandRunner(ctx, projectRoot, "probe", contextLabel, 1, 1, timeout, command, args, execCtx)
	if err != nil {
		detail := strings.TrimSpace(out)
		if detail == "" {
			detail = err.Error()
		}
		if execCtx.backend == "firecracker" {
			if diag := collectFirecrackerWorkspaceDiagnostics(projectRoot, execCtx); diag != "" {
				return fmt.Errorf("doctor lifecycle start failed: %s\nfirecracker workspace diagnostics:\n%s", detail, diag)
			}
		}
		return fmt.Errorf("doctor lifecycle start failed: %s", detail)
	}

	fmt.Printf("doctor lifecycle started (%s)\n", contextLabel)
	return nil
}

func resolveDoctorLifecycleStartCommand(projectRoot string) (command string, args []string, contextLabel string, summary string, found bool, err error) {
	if hasMakeTarget(projectRoot, "start") {
		makeStartCmd := "export UID=1000; export GID=1000; make start"
		return "sh", []string{"-lc", makeStartCmd}, "lifecycle-start-make", "make start", true, nil
	}

	if hasComposeTarget(projectRoot) {
		composeCmd := "if [ -f Makefile ] && command -v make >/dev/null 2>&1; then if grep -q '^secret:' Makefile; then make secret; fi; fi; export BUILDKIT_PROGRESS=plain; export UID=1000; export GID=1000; docker compose build --progress=plain; docker compose up -d --no-build"
		return "sh", []string{"-lc", composeCmd}, "lifecycle-start-compose", "docker compose build --progress=plain && docker compose up -d --no-build", true, nil
	}

	startPath := filepath.Join(projectRoot, ".nexus", "lifecycles", "start.sh")
	startExists, err := isExecutableFile(startPath)
	if err != nil {
		return "", nil, "", "", false, err
	}
	if startExists {
		return "bash", []string{".nexus/lifecycles/start.sh"}, "lifecycle-start-script", "bash .nexus/lifecycles/start.sh", true, nil
	}

	return "", nil, "", "", false, nil
}

func hasComposeTarget(projectRoot string) bool {
	candidates := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}
	for _, name := range candidates {
		if stat, err := os.Stat(filepath.Join(projectRoot, name)); err == nil && !stat.IsDir() {
			return true
		}
	}
	return false
}

func collectFirecrackerWorkspaceDiagnostics(projectRoot string, execCtx doctorExecContext) string {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	diagCmd := "set +e; echo '--- pwd ---'; pwd; echo '--- ls /workspace ---'; ls -la /workspace 2>&1; echo '--- ls /workspace/.nexus ---'; ls -la /workspace/.nexus 2>&1; echo '--- ls /workspace/.nexus/lifecycles ---'; ls -la /workspace/.nexus/lifecycles 2>&1; echo '--- ls /dev/vd* ---'; ls -la /dev/vd* 2>&1; echo '--- mount output ---'; mount 2>&1; echo '--- mount /workspace ---'; mount | grep ' on /workspace ' || true; echo '--- blkid /dev/vdb ---'; blkid /dev/vdb 2>&1 || true; echo '--- guest resolv.conf ---'; cat /etc/resolv.conf 2>&1 || true; echo '--- ip forwarding ---'; cat /proc/sys/net/ipv4/ip_forward 2>&1 || true; cat /proc/sys/net/ipv4/conf/all/forwarding 2>&1 || true; echo '--- iptables command ---'; command -v iptables 2>&1 || true; readlink -f \"$(command -v iptables 2>/dev/null)\" 2>&1 || true; iptables --version 2>&1 || true; command -v iptables-legacy 2>&1 || true; iptables-legacy --version 2>&1 || true; echo '--- xtables binaries ---'; ls -la /usr/sbin/xtables* /usr/bin/xtables* 2>&1 || true; echo '--- iptables nat ---'; iptables -t nat -S 2>&1 || true; iptables-legacy -t nat -S 2>&1 || true; echo '--- iptables apt log ---'; tail -n 120 /tmp/nexus-iptables-apt.log 2>&1 || true; echo '--- docker info ---'; docker info 2>&1 || true; echo '--- docker network inspect bridge ---'; docker network inspect bridge 2>&1 || true; echo '--- dockerd log tail ---'; tail -n 200 /tmp/nexus-doctor-dockerd.log 2>&1 || true; echo '--- container dns test ---'; docker run --rm busybox sh -lc 'nslookup pypi.org; nslookup files.pythonhosted.org' 2>&1 || true; echo '--- container dns server reachability ---'; docker run --rm busybox sh -lc 'ping -c1 -W2 8.8.8.8; ping -c1 -W2 1.1.1.1' 2>&1 || true; echo '--- container https test ---'; docker run --rm curlimages/curl:8.8.0 -sS -I https://files.pythonhosted.org 2>&1 | head -n 20 || true"
	out, err := doctorCheckCommandRunner(ctx, projectRoot, "probe", "lifecycle-start-workspace-diagnostics", 1, 1, 45*time.Second, "sh", []string{"-lc", diagCmd}, execCtx)
	if err != nil && strings.TrimSpace(out) == "" {
		return strings.TrimSpace(err.Error())
	}
	return strings.TrimSpace(out)
}

func runDoctorLifecycleSetup(projectRoot string, execCtx doctorExecContext) error {
	if hasMakeTarget(projectRoot, "start") {
		fmt.Println("doctor lifecycle setup skipped (startup handled by Makefile target: make start)")
		return nil
	}

	setupPath := filepath.Join(projectRoot, ".nexus", "lifecycles", "setup.sh")
	command := ""
	args := []string(nil)
	contextLabel := "lifecycle-setup"

	if setupExists, err := isExecutableFile(setupPath); err != nil {
		return err
	} else if setupExists {
		command = "bash"
		args = []string{".nexus/lifecycles/setup.sh"}
		contextLabel = "lifecycle-setup-script"
	} else {
		return nil
	}

	timeout := 10 * time.Minute
	if rawTimeout := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_SETUP_TIMEOUT_MS")); rawTimeout != "" {
		if ms, err := strconv.Atoi(rawTimeout); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := doctorCheckCommandRunner(ctx, projectRoot, "probe", contextLabel, 1, 1, timeout, command, args, execCtx)
	if err != nil {
		detail := strings.TrimSpace(out)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("doctor lifecycle setup failed: %s", detail)
	}

	fmt.Printf("doctor lifecycle setup completed (%s)\n", contextLabel)
	return nil
}

func runBuiltInOpencodeSessionCheck(projectRoot string) (checkResult, error) {
	const checkName = "tooling-opencode-session"
	start := time.Now()

	return checkResult{
		Name:       checkName,
		Phase:      "test",
		Status:     "not_run",
		Required:   true,
		Attempts:   0,
		DurationMs: time.Since(start).Milliseconds(),
		SkipReason: "opencode session check is skipped for runtime backend checks",
	}, nil
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

	backend := strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_BACKEND"))
	if backend != "firecracker" && backend != "seatbelt" {
		result.Status = "failed_required"
		result.DurationMs = time.Since(start).Milliseconds()
		result.Error = fmt.Sprintf("unsupported runtime backend %q: doctor command only supports firecracker or seatbelt", backend)
		return result, fmt.Errorf("required probes failed: %s", checkName)
	}

	result.Status = "passed"
	result.DurationMs = time.Since(start).Milliseconds()
	fmt.Printf("probe passed: %s (attempt 1/1)\n", checkName)
	return result, nil
}

func bootstrapDoctorExecContext(projectRoot string) error {
	setDoctorExecContextCleanup(nil)
	execCtx := loadDoctorExecContext()
	switch execCtx.backend {
	case "firecracker":
		if err := bootstrapFirecrackerExecContext(projectRoot, execCtx); err != nil {
			return err
		}
		return containerBootstrapRunner(projectRoot, execCtx, "firecracker", true)
	case "seatbelt":
		return nil
	default:
		return fmt.Errorf("unsupported runtime backend %q: doctor command only supports firecracker or seatbelt", execCtx.backend)
	}
}

func bootstrapExecCommandContext(projectRoot string) error {
	setDoctorExecContextCleanup(nil)
	execCtx := loadDoctorExecContext()
	switch execCtx.backend {
	case "firecracker":
		return bootstrapFirecrackerExecContext(projectRoot, execCtx)
	case "seatbelt":
		return nil
	default:
		return fmt.Errorf("unsupported runtime backend %q: exec command only supports firecracker or seatbelt", execCtx.backend)
	}
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
	if execCtx.backend == "firecracker" {
		fmt.Printf("%s exec: %s (attempt %d/%d, timeout=%s, context=%s): %s\n", phase, name, attempt, attempts, timeout, execCtx.backend, formatCommand(command, args))
		return runFirecrackerCheckCommandForHost(ctx, projectRoot, command, args)
	}

	return runHostCheckCommandWithExecContext(ctx, projectRoot, phase, name, attempt, attempts, timeout, command, args, execCtx, "")
}

func runHostCheckCommandWithExecContext(ctx context.Context, projectRoot, phase, name string, attempt, attempts int, timeout time.Duration, command string, args []string, execCtx doctorExecContext, contextOverride string) (string, error) {
	cmdName, cmdArgs, cmdEnv, contextLabel := resolveCheckCommand(projectRoot, command, args, execCtx)
	if strings.TrimSpace(contextOverride) != "" {
		contextLabel = contextOverride
	}

	fmt.Printf("%s exec: %s (attempt %d/%d, timeout=%s, context=%s): %s\n", phase, name, attempt, attempts, timeout, contextLabel, formatCommand(cmdName, cmdArgs))

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	cmd.Dir = projectRoot
	env := append(os.Environ(), cmdEnv...)
	if !hasEnvKey(env, "UID") {
		env = append(env, fmt.Sprintf("UID=%d", os.Getuid()))
	}
	if !hasEnvKey(env, "GID") {
		env = append(env, fmt.Sprintf("GID=%d", os.Getgid()))
	}
	cmd.Env = env

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

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func loadDoctorExecContext() doctorExecContext {
	backend := strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_BACKEND"))
	if backend == "" {
		backend = selectRuntimeBackend(nil)
		if backend == "" {
			backend = "seatbelt"
		}
	}
	return doctorExecContext{
		backend: backend,
	}
}

func applyRuntimeBackendFromWorkspace(projectRoot string) error {
	if rawBackend := strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_BACKEND")); rawBackend != "" {
		backend, ok := normalizeRuntimeBackend(rawBackend)
		if !ok {
			return fmt.Errorf("unsupported runtime backend %q: doctor command only supports firecracker or seatbelt", rawBackend)
		}
		if err := os.Setenv("NEXUS_RUNTIME_BACKEND", backend); err != nil {
			return fmt.Errorf("set runtime backend env: %w", err)
		}
		if backend == "firecracker" {
			applyFirecrackerAssetDefaults()
		}
		return nil
	}

	if hintedBackend, err := loadRuntimeBackendHint(projectRoot); err != nil {
		return err
	} else if hintedBackend != "" {
		if err := os.Setenv("NEXUS_RUNTIME_BACKEND", hintedBackend); err != nil {
			return fmt.Errorf("set runtime backend env: %w", err)
		}
		if hintedBackend == "firecracker" {
			applyFirecrackerAssetDefaults()
		}
		return nil
	}

	backend := selectRuntimeBackend(nil)
	if backend == "" {
		return fmt.Errorf("no supported runtime found; doctor/exec support firecracker or seatbelt")
	}

	if err := os.Setenv("NEXUS_RUNTIME_BACKEND", backend); err != nil {
		return fmt.Errorf("set runtime backend env: %w", err)
	}

	if backend == "firecracker" {
		applyFirecrackerAssetDefaults()
	}

	return nil
}

func loadRuntimeBackendHint(projectRoot string) (string, error) {
	hintPath := filepath.Join(projectRoot, ".nexus", "run", "nexus-init-env")
	data, err := os.ReadFile(hintPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read init runtime hint: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) != "NEXUS_RUNTIME_BACKEND" {
			continue
		}
		backend, valid := normalizeRuntimeBackend(value)
		if !valid {
			return "", fmt.Errorf("invalid NEXUS_RUNTIME_BACKEND value %q in %s", strings.TrimSpace(value), hintPath)
		}
		return backend, nil
	}

	return "", nil
}

func selectRuntimeBackend(required []string) string {
	if len(required) == 0 {
		required = []string{"darwin", "linux"}
	}

	for _, candidate := range required {
		trimmed := strings.ToLower(strings.TrimSpace(candidate))
		switch trimmed {
		case "darwin":
			if firecrackerHostGOOS == "darwin" {
				if _, err := exec.LookPath("limactl"); err == nil {
					return "firecracker"
				}
				return "seatbelt"
			}
		case "linux":
			if firecrackerHostGOOS == "linux" {
				return "firecracker"
			}
		default:
			if backend, ok := normalizeRuntimeBackend(candidate); ok {
				return backend
			}
		}
	}

	if firecrackerHostGOOS == "darwin" {
		if _, err := exec.LookPath("limactl"); err == nil {
			return "firecracker"
		}
		return "seatbelt"
	}
	if firecrackerHostGOOS == "linux" {
		return "firecracker"
	}

	return ""
}

func normalizeRuntimeBackend(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "firecracker":
		return "firecracker", true
	case "seatbelt":
		return "seatbelt", true
	default:
		return "", false
	}
}

func applyFirecrackerAssetDefaults() {
	if strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_KERNEL")) == "" {
		if _, err := firecrackerDefaultAssetStat(firecrackerDefaultKernelPath); err == nil {
			_ = os.Setenv("NEXUS_FIRECRACKER_KERNEL", firecrackerDefaultKernelPath)
		}
	}

	if strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_ROOTFS")) == "" {
		if _, err := firecrackerDefaultAssetStat(firecrackerDefaultRootFSPath); err == nil {
			_ = os.Setenv("NEXUS_FIRECRACKER_ROOTFS", firecrackerDefaultRootFSPath)
		}
	}
}

func resolveCheckCommand(projectRoot, command string, args []string, execCtx doctorExecContext) (string, []string, []string, string) {
	if execCtx.backend == "firecracker" {
		return command, args, nil, "firecracker"
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

func assertNoManualACP(lifecycleDir string) error {
	entries, err := os.ReadDir(lifecycleDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
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
	if !startExists && !hasComposeTarget(projectRoot) && !hasMakeTarget(projectRoot, "start") {
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

func resolveDoctorChecks(projectRoot string) ([]config.DoctorCommandProbe, []config.DoctorCommandCheck, []string, error) {
	probes, tests, warnings, err := discoverDoctorScripts(projectRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	return probes, tests, warnings, nil
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
