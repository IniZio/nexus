package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/nexus/nexus/cmd/internal"
	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/coordination"
	"github.com/nexus/nexus/pkg/ctrl"
	"github.com/nexus/nexus/pkg/metrics"
	"github.com/nexus/nexus/pkg/paths"
	"github.com/nexus/nexus/pkg/provider"
	dockerProvider "github.com/nexus/nexus/pkg/provider/docker"
	lxcProvider "github.com/nexus/nexus/pkg/provider/lxc"
	"github.com/nexus/nexus/pkg/pulse"
	"github.com/nexus/nexus/pkg/templates"
	"github.com/nexus/nexus/pkg/worktree"
	"github.com/spf13/cobra"
	goyaml "gopkg.in/yaml.v3"
)

var (
	version   = "dev"
	goVersion = runtime.Version()
	buildDate = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "nexus",
	Short: "Isolated development environments that work with AI agents",
	Long: `Nexus provides isolated development environments that integrate
seamlessly with AI coding assistants like Cursor, OpenCode, Claude, and others.`,
	Version: version,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of nexus",
	Long:  `Print the version number, Go version, and build information of nexus`,
	RunE: func(_ *cobra.Command, _ []string) error {
		fmt.Printf("Nexus Version: %s\n", version)
		fmt.Printf("Go Version: %s\n", goVersion)
		fmt.Printf("Build Date: %s\n", buildDate)
		return nil
	},
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new nexus project",
	Long:  `Initialize a new nexus project by creating the .nexus directory and default configuration files.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.Init(ctx)
	},
}

var branchCmd = &cobra.Command{
	Use:   "branch",
	Short: "Manage branches",
	Long:  `Create, start, stop, and manage isolated development branches.`,
}

var (
	branchNode string
)

var branchCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new branch",
	Long:  `Create a new branch with the specified name. This will set up a Git worktree and generate AI agent configurations.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.WorkspaceCreate(ctx, args[0])
	},
}

var branchUpCmd = &cobra.Command{
	Use:   "up [name]",
	Short: "Start a branch",
	Long:  `Start the specified branch or auto-detect if no name is provided. This will create and start the isolated environment.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		return controller.WorkspaceUp(ctx, name)
	},
}

var branchDownCmd = &cobra.Command{
	Use:   "down [name]",
	Short: "Stop a branch",
	Long:  `Stop the specified branch or auto-detect if no name is provided.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		return controller.WorkspaceDown(ctx, name)
	},
}

var branchShellCmd = &cobra.Command{
	Use:   "shell [name]",
	Short: "Open shell in branch",
	Long:  `Open an interactive shell in the specified branch or auto-detect if no name is provided.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		return controller.WorkspaceShell(ctx, name)
	},
}

var branchListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all branches",
	Long:  `List all branches, showing their status and provider information.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.WorkspaceList(ctx)
	},
}

var branchRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a branch",
	Long:  `Remove the specified branch, stopping it first if it's running.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.WorkspaceRm(ctx, args[0])
	},
}

var batchCmd = &cobra.Command{
	Use:   "batch",
	Short: "Batch operations on multiple workspaces",
	Long:  `Perform operations on multiple workspaces in parallel.
Useful for managing multiple feature branches simultaneously.`,
}

// Batch commands for parallel workspace operations
var batchUpCmd = &cobra.Command{
	Use:   "up [workspace1] [workspace2] ...",
	Short: "Start multiple workspaces in parallel",
	Long:  `Start multiple workspaces in parallel for feature development.
Useful when working on multiple features simultaneously.`,
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.WorkspaceBatchUp(ctx, args)
	},
}

var batchDownCmd = &cobra.Command{
	Use:   "down [workspace1] [workspace2] ...",
	Short: "Stop multiple workspaces in parallel",
	Long:  `Stop multiple workspaces in parallel.`,
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.WorkspaceBatchDown(ctx, args)
	},
}

var batchExecCmd = &cobra.Command{
	Use:   "exec [workspace1] [workspace2] -- <command>",
	Short: "Execute command in multiple workspaces",
	Long:  `Execute a command in multiple workspaces.
Use -- to separate workspace names from the command.`,
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()

		// Find -- separator
		sepIdx := -1
		for i, arg := range args {
			if arg == "--" {
				sepIdx = i
				break
			}
		}

		if sepIdx == -1 {
			return fmt.Errorf("missing -- separator. Usage: nexus batch exec ws1 ws2 -- <command>")
		}

		workspaces := args[:sepIdx]
		cmd := args[sepIdx+1:]

		if len(workspaces) == 0 {
			return fmt.Errorf("no workspaces specified")
		}
		if len(cmd) == 0 {
			return fmt.Errorf("no command specified")
		}

		return controller.WorkspaceExecAll(ctx, workspaces, cmd, true)
	},
}

var batchStatusCmd = &cobra.Command{
	Use:   "status [workspace]",
	Short: "Show status of workspace(s)",
	Long:  `Show detailed status of one or all workspaces.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		controller := createController()

		if len(args) == 1 {
			return controller.WorkspaceStatus(ctx, args[0])
		}
		return controller.WorkspaceListAll(ctx)
	},
}

// Workspace group commands
var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Manage workspace groups",
	Long:  `Manage workspace groups for organizing related workspaces.
Groups simplify batch operations on related features.`,
}

var groupAddCmd = &cobra.Command{
	Use:   "add <name> <workspace1> [workspace2] ...",
	Short: "Add a new workspace group",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		workspaces := args[1:]
		groupMgr := ctrl.NewWorkspaceGroupManager()
		if err := groupMgr.Load(); err != nil {
			return err
		}
		return groupMgr.AddGroup(name, "", workspaces)
	},
}

var groupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspace groups",
	RunE: func(_ *cobra.Command, _ []string) error {
		groupMgr := ctrl.NewWorkspaceGroupManager()
		if err := groupMgr.Load(); err != nil {
			return err
		}
		groups := groupMgr.ListGroups()
		if len(groups) == 0 {
			fmt.Println("No workspace groups configured.")
			fmt.Println("Create one with: nexus group add <name> <workspace1> <workspace2> ...")
			return nil
		}
		fmt.Println("📦 Workspace Groups")
		fmt.Println(strings.Repeat("─", 50))
		for _, g := range groups {
			fmt.Printf("  %-20s %d workspaces\n", g.Name, len(g.Workspaces))
			if g.Description != "" {
				fmt.Printf("    %s\n", g.Description)
			}
		}
		return nil
	},
}

var groupAliasCmd = &cobra.Command{
	Use:   "alias <alias> <workspace>",
	Short: "Create a quick-access alias for a workspace",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		alias, workspace := args[0], args[1]
		groupMgr := ctrl.NewWorkspaceGroupManager()
		if err := groupMgr.Load(); err != nil {
			return err
		}
		return groupMgr.SetAlias(alias, workspace)
	},
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply latest configuration to agent configs",
	Long:  `Apply the latest nexus configuration to all enabled AI agent configuration files (Cursor, OpenCode, Claude, etc.).`,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.Apply(ctx)
	},
}

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Manage plugins",
	Long:  `Manage plugins: add, remove, update, and list available plugins.`,
}

var pluginUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update all plugins to latest versions",
	Long:  `Update all loaded plugins to their latest versions and refresh the lockfile.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.PluginUpdate(ctx)
	},
}

var pluginListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all loaded plugins",
	Long:  `List all currently loaded plugins with their versions and status.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()
		controller := createController()
		return controller.PluginList(ctx)
	},
}

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Analyze usage metrics and productivity",
	Long:  `Generate reports and insights from usage logs.`,
}

var usageSummaryCmd = &cobra.Command{
	Use:   "summary [date]",
	Short: "Generate daily summary report",
	Long:  `Generate a daily summary of usage metrics and insights. Date format: YYYY-MM-DD (defaults to today).`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runUsageSummary(args)
	},
}

var usageMetricsCmd = &cobra.Command{
	Use:   "metrics [days]",
	Short: "Calculate productivity metrics",
	Long:  `Calculate detailed productivity metrics for the specified number of days (defaults to 7).`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runUsageMetrics(args)
	},
}

var usagePatternsCmd = &cobra.Command{
	Use:   "patterns [days]",
	Short: "Analyze usage patterns",
	Long:  `Analyze usage patterns and trends for the specified number of days (defaults to 7).`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runUsagePatterns(args)
	},
}

var usageBenchmarkCmd = &cobra.Command{
	Use:   "benchmark <baseline-days> <current-days>",
	Short: "Compare baseline and current metrics",
	Long:  `Compare productivity metrics between baseline period and current period.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		return runUsageBenchmark(args)
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage configuration",
	Long:  `Manage nexus configuration files.`,
}

var configExtractCmd = &cobra.Command{
	Use:   "extract <plugin-name>",
	Short: "Extract configuration to plugin",
	Long: `Extract local configuration (rules, skills, commands) into a reusable plugin.
This allows teams to share their coding standards and configurations as distributable plugins.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		pluginName := args[0]

		// Default to extracting all types
		return internal.ExtractConfigToPlugin(pluginName, true, true, true)
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update all extends to latest versions",
	Long: `Fetch the latest versions of all configured extends and update the lockfile.
This ensures you have the most recent templates from remote repositories.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runUpdate()
	},
}

var coordinationCmd = &cobra.Command{
	Use:   "coordination",
	Short: "Manage coordination server",
	Long:  `Start and manage the coordination server for remote node communication.`,
}

var coordinationStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the coordination server",
	Long: `Start the coordination server for remote node communication.
The coordination server manages remote nodes and enables distributed branch execution.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		fmt.Println("Starting coordination server...")
		configPath := coordination.GetConfigPath()
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			configPath = ".nexus/coordination.yaml"
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				fmt.Println("Generating default configuration...")
				if err := coordination.GenerateDefaultConfig(configPath); err != nil {
					return fmt.Errorf("failed to generate config: %w", err)
				}
				fmt.Printf("Configuration written to %s\n", configPath)
			}
		}
		return coordination.StartServer(configPath)
	},
}

var coordinationRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the coordination server",
	Long: `Gracefully restart the coordination server for remote node communication.
Stops any running server, then starts a new instance with the same configuration.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		fmt.Println("=== Restarting Coordination Server ===")

		projectRoot := paths.GetProjectRoot()
		pidFile := paths.GetPIDFilePath(projectRoot)

		if data, err := os.ReadFile(pidFile); err == nil {
			pidStr := strings.TrimSpace(string(data))
			fmt.Printf("Stopping existing server (PID: %s)...\n", pidStr)
			if pid, err := strconv.Atoi(pidStr); err == nil {
				if proc, err := os.FindProcess(pid); err == nil {
					proc.Signal(os.Interrupt)
					time.Sleep(2 * time.Second)
					proc.Kill()
				}
			}
			os.Remove(pidFile)
		}

		fmt.Println("Waiting for cleanup...")
		time.Sleep(1 * time.Second)

		fmt.Println("Starting coordination server in background...")
		configPath := coordination.GetConfigPath()
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			configPath = ".nexus/coordination.yaml"
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				fmt.Println("Generating default configuration...")
				if err := coordination.GenerateDefaultConfig(configPath); err != nil {
					return fmt.Errorf("failed to generate config: %w", err)
				}
				fmt.Printf("Configuration written to %s\n", configPath)
			}
		}

		cmd := exec.Command("nexus", "coordination", "start")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start server: %w", err)
		}

		if err := paths.EnsureDir(filepath.Dir(pidFile)); err != nil {
			return fmt.Errorf("failed to create state directory: %w", err)
		}
		os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644)
		fmt.Printf("Server started (PID: %d)\n", cmd.Process.Pid)

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		fmt.Println("Monitoring startup (15 seconds)...")
		for i := 0; i < 15; i++ {
			resp, err := http.Get("http://localhost:3001/health")
			if err == nil && resp.StatusCode == 200 {
				fmt.Println("✅ Server is healthy!")
				return nil
			}
			if i > 0 {
				fmt.Printf("Attempt %d/15...\n", i+1)
			}
			select {
			case <-time.After(1 * time.Second):
			case <-ctx.Done():
				break
			}
		}

		fmt.Println("⚠️  Server not responding after 15 seconds")
		fmt.Println("Check logs: tail -f .nexus/server.log")
		return nil
	},
}

var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Manage remote nodes",
	Long:  `Add, list, and manage remote nodes for distributed branch execution.`,
}

var nodeAddCmd = &cobra.Command{
	Use:   "add <name> <host>",
	Short: "Add a remote node",
	Long: `Add a remote node to the configuration.
This stores the node configuration for later use with branch creation.`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		host := args[1]
		fmt.Printf("Adding node '%s' at %s...\n", name, host)

		// Auto-generate SSH key if needed
		pubKey, keyPath, err := ensureSSHKey()
		if err != nil {
			return err
		}

		// Store node configuration
		configDir := filepath.Join(os.Getenv("HOME"), ".config", "nexus")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("failed to create config dir: %w", err)
		}
		configPath := filepath.Join(configDir, "nodes.yaml")
		var nodes map[string]map[string]string
		data, err := os.ReadFile(configPath)
		if err == nil {
			goyaml.Unmarshal(data, &nodes)
		}
		if nodes == nil {
			nodes = make(map[string]map[string]string)
		}
		nodes[name] = map[string]string{
			"host":     host,
			"key_path": keyPath,
		}
		output, _ := goyaml.Marshal(nodes)
		if err := os.WriteFile(configPath, output, 0644); err != nil {
			return fmt.Errorf("failed to save node config: %w", err)
		}

		fmt.Printf("\n✅ Node '%s' added successfully!\n\n", name)
		fmt.Println("🔐 SSH Key Setup Required:")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("To enable passwordless SSH access, copy your public key to the remote server:")
		fmt.Println("")
		fmt.Printf("  ssh-copy-id -i %s %s\n", keyPath, host)
		fmt.Println("")
		fmt.Println("Or manually add this key to ~/.ssh/authorized_keys on the remote:")
		fmt.Println("")
		fmt.Println(pubKey)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("\nAfter setting up SSH, you can create branches on this node:")
		fmt.Printf("  nexus branch create my-feature --node %s\n", name)
		return nil
	},
}

var nodeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured nodes",
	Long:  `List all configured remote nodes.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		fmt.Println("Configured nodes:")
		configPath := filepath.Join(os.Getenv("HOME"), ".config", "nexus", "nodes.yaml")
		data, err := os.ReadFile(configPath)
		if err != nil {
			fmt.Println("  No nodes configured")
			return nil
		}
		var nodes map[string]map[string]string
		goyaml.Unmarshal(data, &nodes)
		for name, cfg := range nodes {
			fmt.Printf("  - %s: %s\n", name, cfg["host"])
		}
		return nil
	},
}

var sshCmd = &cobra.Command{
	Use:   "ssh",
	Short: "Manage SSH access for branches",
	Long:  `Generate SSH keys, register with coordination server, and get connection info for branches.`,
}

var sshGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate SSH key for nexus",
	Long: `Generate an Ed25519 SSH key for nexus branch access.
The key is stored in ~/.ssh/id_ed25519_nexus and can be used to access your branches.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		pubKey, keyPath, err := ensureSSHKey()
		if err != nil {
			return err
		}
		fmt.Println("✅ SSH key generated successfully!")
		fmt.Println("")
		fmt.Println("📋 Public Key (share this with your admin):")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println(pubKey)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("")
		fmt.Printf("🔑 Private key location: %s\n", keyPath)
		fmt.Println("")
		fmt.Println("📝 Next steps:")
		fmt.Println("  1. Share the public key above with your admin")
		fmt.Println("  2. Or register with the coordination server:")
		fmt.Println("     nexus ssh register <server-address>")
		return nil
	},
}

var sshRegisterCmd = &cobra.Command{
	Use:   "register <server>",
	Short: "Register your SSH public key with the coordination server",
	Long:  `Register your SSH public key with the coordination server to get access to branches.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		server := args[0]
		pubKey, _, err := ensureSSHKey()
		if err != nil {
			return err
		}

		// Get username from environment or prompt
		username := os.Getenv("USER")
		if username == "" {
			username = "developer"
		}

		// Parse server address (host:port or just host)
		host := server
		port := 3001
		if strings.Contains(server, ":") {
			parts := strings.SplitN(server, ":", 2)
			host = parts[0]
			p, err := strconv.Atoi(parts[1])
			if err == nil {
				port = p
			}
		}

		// Register user via API
		url := fmt.Sprintf("http://%s:%d/api/v1/users", host, port)
		payload := map[string]string{
			"username":   username,
			"public_key": strings.TrimSpace(pubKey),
		}
		jsonPayload, _ := json.Marshal(payload)

		resp, err := http.Post(url, "application/json", strings.NewReader(string(jsonPayload)))
		if err != nil {
			return fmt.Errorf("failed to connect to server: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("registration failed with status: %d", resp.StatusCode)
		}

		fmt.Println("✅ Successfully registered with coordination server!")
		fmt.Println("")
		fmt.Println("📋 Your connection info:")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("  Server: %s\n", host)
		fmt.Printf("  Port:   %d\n", port)
		fmt.Printf("  User:   %s\n", username)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("")
		fmt.Println("📝 When your branch is ready, you'll get SSH access at:")
		fmt.Printf("  ssh -p <branch-port> %s@%s\n", username, host)
		return nil
	},
}

var sshInfoCmd = &cobra.Command{
	Use:   "info <branch>",
	Short: "Show connection info for a branch",
	Long: `Show SSH connection info, deep links, and service URLs for a branch.
This displays all the information you need to access your branch.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		branchName := args[0]
		cfg, err := config.LoadConfig(".nexus/config.yaml")
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Get server config
		configDir := filepath.Join(os.Getenv("HOME"), ".config", "nexus")
		nodesConfigPath := filepath.Join(configDir, "nodes.yaml")
		var nodes map[string]map[string]string
		if data, err := os.ReadFile(nodesConfigPath); err == nil {
			goyaml.Unmarshal(data, &nodes)
		}

		// Determine server address
		serverHost := "localhost"
		serverPort := 3001
		username := os.Getenv("USER")
		if username == "" {
			username = "developer"
		}

		// Look for node config
		for name, nodeCfg := range nodes {
			if name == cfg.Remote.Node || nodeCfg["host"] != "" {
				host := nodeCfg["host"]
				if strings.Contains(host, ":") {
					parts := strings.SplitN(host, ":", 2)
					serverHost = parts[0]
					if p, err := strconv.Atoi(parts[1]); err == nil {
						serverPort = p
					}
				} else {
					serverHost = host
				}
				break
			}
		}

		// For now, generate example info (actual port would come from coordination server)
		fmt.Println("🔗 Workspace Connection Info")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("Workspace: %s\n", branchName)
		fmt.Printf("Server:    %s:%d\n", serverHost, serverPort)
		fmt.Printf("Username:  %s\n", username)
		fmt.Println("")
		fmt.Println("🐚 SSH Access:")
		fmt.Printf("  ssh -p <port> %s@%s\n", username, serverHost)
		fmt.Println("")
		fmt.Println("💻 Deep Links (click to open):")
		fmt.Printf("  VSCode:  vscode://vscode-remote/ssh-remote+%s:<port>/home/dev\n", serverHost)
		fmt.Printf("  Cursor:  cursor://ssh/remote?host=%s&port=<port>&user=%s\n", serverHost, username)
		fmt.Println("")
		fmt.Println("🌐 Service URLs (when branch is running):")
		fmt.Printf("  nexus branch services %s\n", branchName)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		return nil
	},
}

var branchServicesCmd = &cobra.Command{
	Use:   "services <name>",
	Short: "List services and their URLs for a branch",
	Long: `List all services running in a branch with their mapped ports and URLs.
This helps you quickly access your application's endpoints.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		branchName := args[0]
		fmt.Printf("📦 Services for branch '%s'\n", branchName)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  Service     Port     Local Port  URL")
		fmt.Println("  ──────────  ───────  ──────────  ─────────────────────────────────")
		fmt.Println("  (Services will appear here when branch is running)")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("")
		fmt.Println("💡 To get service URLs, ensure the branch is running:")
		fmt.Printf("  nexus branch up %s\n", branchName)
		return nil
	},
}

var branchConnectCmd = &cobra.Command{
	Use:   "connect <name>",
	Short: "Show connection info and deep links for a branch",
	Long: `Show SSH connection info, deep links for editors, and service URLs for a branch.
This is the primary command to get everything you need to access your branch.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return sshInfoCmd.RunE(nil, args)
	},
}

// Pulse integration commands
var pulseCmd = &cobra.Command{
	Use:   "pulse",
	Short: "Manage Pulse issue tracking",
	Long: `Integrate with Pulse issue tracking. Create, list, and update issues
directly from the Nexus CLI.`,
}

var pulseURL string

var pulseIssuesCmd = &cobra.Command{
	Use:   "issues",
	Short: "Manage Pulse issues",
	Long:  `List, create, and update issues in Pulse.`,
}

var pulseIssuesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List issues in workspace",
	Long: `List all issues in the specified workspace.
Defaults to the current or 'default' workspace.`,
	RunE: func(_ *cobra.Command, args []string) error {
		client := pulse.NewClient(pulseURL)

		workspaceID := "default"
		if len(args) > 0 {
			workspaceID = args[0]
		}

		// Check health
		if err := client.Health(); err != nil {
			fmt.Printf("⚠️  Pulse is not running. Start it with: ./bin/pulse start\n")
			return err
		}

		issues, err := client.ListIssues(workspaceID)
		if err != nil {
			return fmt.Errorf("failed to list issues: %w", err)
		}

		fmt.Printf("📋 Issues in workspace '%s'\n", workspaceID)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		if len(issues) == 0 {
			fmt.Println("  No issues found.")
			fmt.Println("")
			fmt.Println("  Create one with: nexus pulse issues create \"Issue title\"")
			return nil
		}

		for _, issue := range issues {
			statusIcon := map[string]string{
				"backlog":     "○",
				"todo":        "◐",
				"in_progress": "◉",
				"done":        "●",
			}[issue.Status]
			if statusIcon == "" {
				statusIcon = "○"
			}

			priorityIcon := ""
			switch issue.Priority {
			case 1:
				priorityIcon = "🔴"
			case 2:
				priorityIcon = "🟠"
			case 3:
				priorityIcon = "🟡"
			case 4:
				priorityIcon = "🟢"
			}

			labelsStr := ""
			if len(issue.Labels) > 0 {
				labelsStr = " [" + joinStrings(issue.Labels, ", ") + "]"
			}

			fmt.Printf("  %s %s %s\n", statusIcon, issue.Title, labelsStr)
			fmt.Printf("     %s Priority: %d | Estimate: %d pts | ID: %s\n", priorityIcon, issue.Priority, issue.Estimate, shortID(issue.ID))
		}
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("  Total: %d issues\n", len(issues))
		return nil
	},
}

var pulseIssuesCreateCmd = &cobra.Command{
	Use:   "create <title>",
	Short: "Create a new issue",
	Long: `Create a new issue in Pulse.
The issue is created in the default workspace unless specified.`,
	Args: cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		client := pulse.NewClient(pulseURL)

		workspaceID, _ := c.Flags().GetString("workspace")
		if workspaceID == "" {
			workspaceID = "default"
		}

		description, _ := c.Flags().GetString("description")
		priority, _ := c.Flags().GetInt("priority")
		estimate, _ := c.Flags().GetInt("estimate")
		labelsStr, _ := c.Flags().GetString("labels")

		// Check health
		if err := client.Health(); err != nil {
			fmt.Printf("⚠️  Pulse is not running. Start it with: ./bin/pulse start\n")
			return err
		}

		// Parse labels
		var labels []string
		if labelsStr != "" {
			labels = splitString(labelsStr, ",")
		}

		issue, err := client.CreateIssue(workspaceID, args[0], description, priority, estimate, labels)
		if err != nil {
			return fmt.Errorf("failed to create issue: %w", err)
		}

		fmt.Printf("✅ Created issue: %s\n", issue.Title)
		fmt.Printf("   ID: %s\n", shortID(issue.ID))
		fmt.Printf("   Status: %s | Priority: %d | Estimate: %d pts\n", issue.Status, issue.Priority, issue.Estimate)

		return nil
	},
}

var pulseIssuesMoveCmd = &cobra.Command{
	Use:   "move <issue-id> <status>",
	Short: "Move issue to different status",
	Long: `Move an issue to a different status.
Valid statuses: backlog, todo, in_progress, done`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		client := pulse.NewClient(pulseURL)

		issueID := args[0]
		status := args[1]

		validStatuses := map[string]bool{
			"backlog":     true,
			"todo":        true,
			"in_progress": true,
			"done":        true,
		}
		if !validStatuses[status] {
			return fmt.Errorf("invalid status: %s. Valid: backlog, todo, in_progress, done", status)
		}

		// Check health
		if err := client.Health(); err != nil {
			fmt.Printf("⚠️  Pulse is not running. Start it with: ./bin/pulse start\n")
			return err
		}

		if err := client.UpdateIssueStatus(issueID, status); err != nil {
			return fmt.Errorf("failed to update issue: %w", err)
		}

		fmt.Printf("✅ Moved issue %s to '%s'\n", shortID(issueID), status)
		return nil
	},
}

var pulseWebCmd = &cobra.Command{
	Use:   "web",
	Short: "Open Pulse web UI",
	Long:  `Open the Pulse web interface in your default browser.`,
	RunE: func(_ *cobra.Command, args []string) error {
		url := pulseURL
		if url == "" {
			url = "http://localhost:3002"
		}

		fmt.Printf("🌐 Opening %s\n", url)

		// Try to open in browser
		cmd := exec.Command("xdg-open", url)
		if err := cmd.Start(); err != nil {
			fmt.Printf("⚠️  Could not open browser automatically\n")
			fmt.Printf("   Open this URL manually: %s\n", url)
		}

		return nil
	},
}

// Helper functions
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

func splitString(s, sep string) []string {
	if s == "" {
		return nil
	}
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep[0] {
			if i > start {
				result = append(result, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}

// ensureSSHKey generates an SSH key for nexus if one doesn't exist
func ensureSSHKey() (string, string, error) {
	sshDir := filepath.Join(os.Getenv("HOME"), ".ssh")
	keyPath := filepath.Join(sshDir, "id_ed25519_nexus")
	pubPath := keyPath + ".pub"

	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return "", "", fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		fmt.Println("🔑 Generating SSH key for nexus remote access...")
		cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-C", "nexus@localhost")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", "", fmt.Errorf("failed to generate SSH key: %w, output: %s", err, output)
		}
		fmt.Println("✅ SSH key generated")
	}

	pubKey, err := os.ReadFile(pubPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read public key: %w", err)
	}

	return string(pubKey), keyPath, nil
}

func init() {
	// Add subcommands
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(applyCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(pluginCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(usageCmd)
	rootCmd.AddCommand(branchCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(coordinationCmd)
	rootCmd.AddCommand(nodeCmd)

	// Plugin subcommands
	pluginCmd.AddCommand(pluginUpdateCmd)
	pluginCmd.AddCommand(pluginListCmd)

	// Config subcommands
	configCmd.AddCommand(configExtractCmd)

	// Usage subcommands
	usageCmd.AddCommand(usageSummaryCmd)
	usageCmd.AddCommand(usageMetricsCmd)
	usageCmd.AddCommand(usagePatternsCmd)
	usageCmd.AddCommand(usageBenchmarkCmd)

	// Workspace subcommands
	branchCmd.AddCommand(branchCreateCmd)
	branchCmd.AddCommand(branchUpCmd)
	branchCmd.AddCommand(branchDownCmd)
	branchCmd.AddCommand(branchListCmd)
	branchCmd.AddCommand(branchRmCmd)
	branchCmd.AddCommand(branchShellCmd)

	// Batch commands for parallel operations
	rootCmd.AddCommand(batchCmd)
	batchCmd.AddCommand(batchUpCmd)
	batchCmd.AddCommand(batchDownCmd)
	batchCmd.AddCommand(batchExecCmd)
	batchCmd.AddCommand(batchStatusCmd)

	// Workspace group commands
	rootCmd.AddCommand(groupCmd)
	groupCmd.AddCommand(groupAddCmd)
	groupCmd.AddCommand(groupListCmd)
	groupCmd.AddCommand(groupAliasCmd)

	// Add --node flag to branch commands
	branchCreateCmd.Flags().StringVarP(&branchNode, "node", "n", "", "Remote node to create branch on")
	branchUpCmd.Flags().StringVarP(&branchNode, "node", "n", "", "Remote node to start branch on")
	branchDownCmd.Flags().StringVarP(&branchNode, "node", "n", "", "Remote node to stop branch on")
	branchShellCmd.Flags().StringVarP(&branchNode, "node", "n", "", "Remote node to shell into")

	// Coordination subcommands
	coordinationCmd.AddCommand(coordinationStartCmd)
	coordinationCmd.AddCommand(coordinationRestartCmd)

	// Node subcommands
	nodeCmd.AddCommand(nodeAddCmd)
	nodeCmd.AddCommand(nodeListCmd)

	// SSH management commands
	rootCmd.AddCommand(sshCmd)
	sshCmd.AddCommand(sshGenerateCmd)
	sshCmd.AddCommand(sshRegisterCmd)
	sshCmd.AddCommand(sshInfoCmd)

	// Workspace connection commands
	branchCmd.AddCommand(branchConnectCmd)
	branchCmd.AddCommand(branchServicesCmd)

	// Pulse integration commands
	rootCmd.AddCommand(pulseCmd)
	pulseCmd.AddCommand(pulseIssuesCmd)
	pulseCmd.AddCommand(pulseWebCmd)
	pulseIssuesCmd.AddCommand(pulseIssuesListCmd)
	pulseIssuesCmd.AddCommand(pulseIssuesCreateCmd)
	pulseIssuesCmd.AddCommand(pulseIssuesMoveCmd)

	// Flags for pulse commands
	pulseIssuesCreateCmd.Flags().StringP("workspace", "w", "", "Workspace ID (default: default)")
	pulseIssuesCreateCmd.Flags().StringP("description", "d", "", "Issue description")
	pulseIssuesCreateCmd.Flags().IntP("priority", "p", 3, "Priority (1=Urgent, 2=High, 3=Medium, 4=Low)")
	pulseIssuesCreateCmd.Flags().IntP("estimate", "e", 0, "Estimate in story points")
	pulseIssuesCreateCmd.Flags().StringP("labels", "l", "", "Labels (comma-separated)")
}

func createController() ctrl.Controller {
	// Create providers
	dockerProv, err := dockerProvider.NewDockerProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Docker provider: %v\n", err)
		os.Exit(1)
	}

	lxcProv, err := lxcProvider.NewLXCProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create LXC provider: %v\n", err)
		os.Exit(1)
	}

	providers := []provider.Provider{dockerProv, lxcProv}

	// Create worktree manager
	wtManager := worktree.NewManager(".", paths.GetWorktreesDir("."))

	// Create controller
	return ctrl.NewBaseController(providers, wtManager)
}

// runUpdate updates all extends to their latest versions
func runUpdate() error {
	fmt.Println("📦 Updating extends to latest versions...")

	// Load config
	cfg, err := config.LoadConfig(".nexus/config.yaml")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Create templates manager
	templateManager := templates.NewManager(".")

	// Update each extend
	updated := 0
	for _, ext := range cfg.Extends {
		extStr, ok := ext.(string)
		if !ok {
			continue
		}
		parts := strings.Split(extStr, "/")
		if len(parts) != 2 {
			continue
		}

		owner, repo := parts[0], parts[1]

		// Parse optional branch
		branch := ""
		if strings.Contains(repo, "@") {
			repoParts := strings.SplitN(repo, "@", 2)
			repo = repoParts[0]
			branch = repoParts[1]
		}

		repoURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)
		repoTemplate := templates.TemplateRepo{
			URL:    repoURL,
			Branch: branch,
		}

		fmt.Printf("  Updating %s...\n", ext)
		if err := templateManager.PullWithUpdate(repoTemplate); err != nil {
			fmt.Printf("  ⚠️  Failed to update %s: %v\n", ext, err)
			continue
		}

		// Get new SHA
		sha, err := templateManager.GetRepoSHA(repoTemplate)
		if err != nil {
			fmt.Printf("  ⚠️  Failed to get SHA for %s: %v\n", ext, err)
			continue
		}

		fmt.Printf("  ✅ %s (SHA: %s)\n", ext, sha[:7])
		updated++
	}

	if updated == 0 {
		fmt.Println("No extends to update")
	} else {
		fmt.Printf("✅ Updated %d extends\n", updated)
	}

	return nil
}

func runUsageSummary(args []string) error {
	logger := metrics.NewLogger(".")
	reporter := metrics.NewReporter()

	var date time.Time
	if len(args) > 0 {
		var err error
		date, err = time.Parse("2006-01-02", args[0])
		if err != nil {
			return fmt.Errorf("invalid date format: %w (use YYYY-MM-DD)", err)
		}
	} else {
		date = time.Now()
	}

	summary, err := reporter.GenerateDailySummary(logger, date)
	if err != nil {
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal summary: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

func runUsageMetrics(args []string) error {
	days := 7
	if len(args) > 0 {
		parsed, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid days value: %w", err)
		}
		days = parsed
	}

	logger := metrics.NewLogger(".")
	reporter := metrics.NewReporter()

	m, summary, patterns, err := reporter.GenerateReport(logger, days)
	if err != nil {
		return fmt.Errorf("failed to generate report: %w", err)
	}

	result := map[string]interface{}{
		"summary":  summary,
		"metrics":  m,
		"patterns": patterns,
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal report: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

func runUsagePatterns(args []string) error {
	days := 7
	if len(args) > 0 {
		parsed, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid days value: %w", err)
		}
		days = parsed
	}

	logger := metrics.NewLogger(".")
	analyzer := metrics.NewAnalyzer()

	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -days)

	logs, err := logger.Query(metrics.Filter{
		StartTime: startDate,
		EndTime:   endDate,
	})

	if err != nil {
		return fmt.Errorf("failed to query logs: %w", err)
	}

	patterns, err := analyzer.AnalyzePatterns(logs)
	if err != nil {
		return fmt.Errorf("failed to analyze patterns: %w", err)
	}

	data, err := json.MarshalIndent(patterns, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal patterns: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

func runUsageBenchmark(args []string) error {
	baselineDays, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid baseline days: %w", err)
	}

	currentDays, err := strconv.Atoi(args[1])
	if err != nil {
		return fmt.Errorf("invalid current days: %w", err)
	}

	logger := metrics.NewLogger(".")
	reporter := metrics.NewReporter()

	comparison, err := reporter.GenerateBenchmark(logger, baselineDays, currentDays)
	if err != nil {
		return fmt.Errorf("failed to generate benchmark: %w", err)
	}

	data, err := json.MarshalIndent(comparison, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal benchmark: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

func main() {
	// Load .env files in order of precedence
	// 1. .env in current directory (project root)
	// 2. .env.local in current directory (local overrides)
	// 3. .env in deploy/envs/[env]/ (environment-specific)
	// 4. .env.local in deploy/envs/[env]/ (environment-specific local)
	envFiles := []string{
		".env",
		".env.local",
	}

	// Also check for staging environment
	stagingEnvFiles := []string{
		"deploy/envs/staging/.env",
		"deploy/envs/staging/.env.local",
	}

	for _, f := range append(envFiles, stagingEnvFiles...) {
		if _, err := os.Stat(f); err == nil {
			_ = godotenv.Load(f)
		}
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
