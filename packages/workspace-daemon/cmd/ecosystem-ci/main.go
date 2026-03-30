package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
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
	requiredHostPorts []int
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
	suite := fs.String("suite", "", "ecosystem suite name")
	requiredPorts := fs.String("required-host-ports", "5173,5174,8000", "comma-separated required published host ports")
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

	if err := run(options{
		projectRoot:       *projectRoot,
		suite:             *suite,
		requiredHostPorts: ports,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  ecosystem-ci doctor --project-root <abs-path> --suite <name> [--required-host-ports 5173,5174,8000]")
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
		filepath.Join(opts.projectRoot, "docker-compose.yml"),
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

	fmt.Printf("ecosystem suite passed: %s (discovered %d compose ports)\n", opts.suite, len(publishedPorts))
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
