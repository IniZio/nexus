package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	probeRuntime      bool
	probeURLs         []string
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
	requiredPorts := fs.String("required-host-ports", "5173,5174,8000", "comma-separated required published host ports")
	probeRuntime := fs.Bool("probe-runtime", false, "start compose stack and probe key app/auth endpoints")
	probeURLs := fs.String("probe-urls", "", "comma-separated URLs to probe when --probe-runtime is enabled")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *projectRoot == "" || *suite == "" {
		fmt.Fprintln(os.Stderr, "--project-root and --suite are required")
		os.Exit(2)
	}

	ports, err := parseRequiredPorts(*requiredPorts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	urls := parseURLs(*probeURLs)

	if err := run(options{
		projectRoot:       *projectRoot,
		suite:             *suite,
		composeFile:       *composeFile,
		requiredHostPorts: ports,
		probeRuntime:      *probeRuntime,
		probeURLs:         urls,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  nexus doctor --project-root <abs-path> --suite <name> [--compose-file docker-compose.yml] [--required-host-ports 5173,5174,8000] [--probe-runtime]")
}

func run(opts options) error {
	if opts.suite != "hanlun-root" {
		return fmt.Errorf("unknown suite: %s", opts.suite)
	}

	if !filepath.IsAbs(opts.projectRoot) {
		return fmt.Errorf("project root must be absolute: %s", opts.projectRoot)
	}

	requiredFiles := []string{
		filepath.Join(opts.projectRoot, ".nexus", "workspace.json"),
		filepath.Join(opts.projectRoot, ".nexus", "lifecycles", "setup.sh"),
		filepath.Join(opts.projectRoot, ".nexus", "lifecycles", "start.sh"),
		filepath.Join(opts.projectRoot, ".nexus", "lifecycles", "teardown.sh"),
		filepath.Join(opts.projectRoot, opts.composeFile),
	}

	for _, p := range requiredFiles {
		if _, err := os.Stat(p); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("missing required file: %s", p)
			}
			return fmt.Errorf("stat %s: %w", p, err)
		}
	}

	for _, p := range []string{
		filepath.Join(opts.projectRoot, ".nexus", "lifecycles", "setup.sh"),
		filepath.Join(opts.projectRoot, ".nexus", "lifecycles", "start.sh"),
		filepath.Join(opts.projectRoot, ".nexus", "lifecycles", "teardown.sh"),
	} {
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("lifecycle script is not executable: %s", p)
		}
	}

	if err := assertNoManualACP(filepath.Join(opts.projectRoot, ".nexus", "lifecycles")); err != nil {
		return err
	}

	if _, _, err := config.LoadWorkspaceConfig(opts.projectRoot); err != nil {
		return fmt.Errorf("invalid workspace config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	publishedPorts, err := compose.DiscoverPublishedPorts(ctx, opts.projectRoot)
	if err != nil {
		return fmt.Errorf("compose discovery failed: %w", err)
	}
	if len(publishedPorts) == 0 {
		return fmt.Errorf("no compose published ports discovered")
	}

	missing := missingRequiredPorts(opts.requiredHostPorts, publishedPorts)
	if len(missing) > 0 {
		return fmt.Errorf("missing required host ports: %v", missing)
	}

	if opts.probeRuntime {
		if len(opts.probeURLs) == 0 {
			opts.probeURLs = defaultProbeURLs(opts.suite)
		}
		if err := runRuntimeProbes(opts); err != nil {
			return err
		}
	}

	fmt.Printf("doctor suite passed: %s (discovered %d compose ports)\n", opts.suite, len(publishedPorts))
	return nil
}

func parseURLs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	urls := make([]string, 0, len(parts))
	for _, part := range parts {
		u := strings.TrimSpace(part)
		if u == "" {
			continue
		}
		urls = append(urls, u)
	}
	return urls
}

func defaultProbeURLs(suite string) []string {
	switch suite {
	case "hanlun-root":
		return []string{
			"http://localhost:3001/oauth2/.well-known/openid-configuration",
			"http://localhost:4001/oauth2/.well-known/openid-configuration",
			"http://localhost:5173",
			"http://localhost:5174",
			"http://localhost:6006",
			"http://localhost:6007",
			"http://localhost:8001",
		}
	default:
		return nil
	}
}

func runRuntimeProbes(opts options) error {
	composePath := filepath.Join(opts.projectRoot, opts.composeFile)
	upCtx, upCancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer upCancel()

	upCmd := exec.CommandContext(upCtx, "docker", "compose", "-f", composePath, "up", "-d")
	upCmd.Dir = opts.projectRoot
	upCmd.Env = os.Environ()
	if os.Getenv("GLITCHTIP_DSN") == "" {
		upCmd.Env = append(upCmd.Env, "GLITCHTIP_DSN=placeholder")
	}
	if out, err := upCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose up failed: %w: %s", err, string(out))
	}

	defer func() {
		downCtx, downCancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer downCancel()
		downCmd := exec.CommandContext(downCtx, "docker", "compose", "-f", composePath, "down", "--remove-orphans")
		downCmd.Dir = opts.projectRoot
		downCmd.Env = os.Environ()
		if os.Getenv("GLITCHTIP_DSN") == "" {
			downCmd.Env = append(downCmd.Env, "GLITCHTIP_DSN=placeholder")
		}
		_, _ = downCmd.CombinedOutput()
	}()

	for _, u := range opts.probeURLs {
		if err := waitForURL(u, 4*time.Minute); err != nil {
			return fmt.Errorf("runtime probe failed for %s: %w", u, err)
		}
	}

	return nil
}

func waitForURL(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(3 * time.Second)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return lastErr
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
