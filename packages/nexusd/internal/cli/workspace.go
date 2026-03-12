package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var (
	// Template support removed - users provide Dockerfile directly
	fromFlag    string
	cpuFlag     int
	memoryFlag  int
	diskFlag    int
	forceFlag   bool
	formatFlag  string
	allFlag     bool
	clearFlag   bool
	backendFlag string
)

const (
	sessionDirName      = ".nexus/session"
	activeWorkspaceFile = "active-workspace"
)

func validateWorkspaceName(name string) error {
	if name == "" {
		return fmt.Errorf("workspace name cannot be empty")
	}
	if len(name) > 63 {
		return fmt.Errorf("workspace name too long (max 63 characters)")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("workspace name can only contain letters, numbers, hyphens, and underscores")
		}
	}
	return nil
}

func getSessionDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, sessionDirName)
}

func getActiveWorkspacePath() string {
	return filepath.Join(getSessionDir(), activeWorkspaceFile)
}

func getActiveWorkspace() (string, error) {
	activePath := getActiveWorkspacePath()
	data, err := os.ReadFile(activePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func setActiveWorkspace(name string) error {
	sessionDir := getSessionDir()
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return err
	}
	activePath := getActiveWorkspacePath()
	if name == "" {
		return os.Remove(activePath)
	}
	return os.WriteFile(activePath, []byte(name+"\n"), 0644)
}

func clearActiveWorkspace() error {
	return setActiveWorkspace("")
}

var workspaceCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new workspace",
	Long: `Create a new workspace for development.

Examples:
  nexus workspace create myproject
  nexus workspace create myproject --backend docker
  nexus workspace create myproject --from ./existing-project --cpu 4`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]
		if err := validateWorkspaceName(name); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		req := CreateWorkspaceRequest{
			Name:    name,
			Backend: backendFlag,
		}

		// Template support removed - users provide Dockerfile directly
		if fromFlag != "" {
			req.WorktreePath = fromFlag
		}
		if cpuFlag > 0 {
			req.Labels = mergeLabels(req.Labels, "cpu", fmt.Sprintf("%d", cpuFlag))
		}
		if memoryFlag > 0 {
			req.Labels = mergeLabels(req.Labels, "memory", fmt.Sprintf("%d", memoryFlag))
		}
		if diskFlag > 0 {
			req.Labels = mergeLabels(req.Labels, "disk", fmt.Sprintf("%d", diskFlag))
		}

		ws, err := client.CreateWorkspace(req)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace create", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error creating workspace: %v\n", err)
			os.Exit(4)
		}

		recordCommand("workspace create", args, duration, true, nil)

		if jsonOutput {
			printJSON(ws)
		} else {
			fmt.Printf("Created workspace %s (ID: %s)\n", ws.Name, ws.ID)
			fmt.Printf("Status: %s\n", ws.Status)
			fmt.Printf("Backend: %s\n", ws.Backend)
			if ws.WorktreePath != "" {
				fmt.Printf("Worktree: %s\n", ws.WorktreePath)
			}
		}
		return nil
	},
}

var workspaceStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a stopped workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.StartWorkspace(name)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace start", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error starting workspace: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "\nTip: Run 'nexus workspace list' to see available workspaces.\n")
				os.Exit(3)
			}
			os.Exit(1)
		}

		recordCommand("workspace start", args, duration, true, nil)

		if jsonOutput {
			printJSON(ws)
		} else {
			fmt.Printf("Started workspace %s\n", ws.Name)
			fmt.Printf("Status: %s\n", ws.Status)
		}
		return nil
	},
}

var workspaceStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop a running workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		timeout := 30
		if forceFlag {
			timeout = 5
		}

		ws, err := client.StopWorkspace(name, timeout)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace stop", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error stopping workspace: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "\nTip: Run 'nexus workspace list' to see available workspaces.\n")
				os.Exit(3)
			}
			os.Exit(1)
		}

		recordCommand("workspace stop", args, duration, true, nil)

		if jsonOutput {
			printJSON(ws)
		} else {
			fmt.Printf("Stopped workspace %s\n", ws.Name)
			fmt.Printf("Status: %s\n", ws.Status)
		}
		return nil
	},
}

var workspaceDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a workspace permanently",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		if !forceFlag {
			fmt.Printf("Are you sure you want to delete workspace %s? This cannot be undone. [y/N]: ", name)
			var confirm string
			fmt.Scanln(&confirm)
			if confirm != "y" && confirm != "Y" {
				fmt.Println("Cancelled")
				os.Exit(0)
			}
		}

		err := client.DeleteWorkspace(name)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace delete", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error deleting workspace: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "\nTip: Run 'nexus workspace list' to see available workspaces.\n")
				os.Exit(3)
			}
			os.Exit(1)
		}

		recordCommand("workspace delete", args, duration, true, nil)

		if jsonOutput {
			printJSON(map[string]string{"name": name, "status": "deleted"})
		} else {
			fmt.Printf("Deleted workspace %s\n", name)
		}
		return nil
	},
}

var workspaceListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List all workspaces",
	Long:    "List all workspaces with their status, backend, and creation time.",
	Aliases: []string{"ls"},
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")
		result, err := client.ListWorkspaces()
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace list", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		recordCommand("workspace list", args, duration, true, nil)

		if len(result.Workspaces) == 0 {
			fmt.Println("No workspaces found")
			return nil
		}

		if jsonOutput || formatFlag == "json" {
			printJSON(result)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "NAME\tSTATUS\tBACKEND\tCREATED\n")
		for _, ws := range result.Workspaces {
			created := ws.CreatedAt.Format("2006-01-02 15:04")
			if ws.CreatedAt.IsZero() {
				created = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				ws.Name,
				colorStatus(ws.Status),
				ws.Backend,
				created,
			)
		}
		w.Flush()
		fmt.Fprintf(os.Stderr, "\nTotal: %d workspace(s)\n", result.Total)
		return nil
	},
}

var workspaceSSHCmd = &cobra.Command{
	Use:   "ssh <name>",
	Short: "SSH into workspace interactively",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		err := client.Shell(name)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace ssh", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "\nTip: Run 'nexus workspace list' to see available workspaces.\n")
				os.Exit(3)
			}
			os.Exit(1)
		}

		recordCommand("workspace ssh", args, duration, true, nil)
		return nil
	},
}

var workspaceExecCmd = &cobra.Command{
	Use:   "exec <name> -- <command>",
	Short: "Execute command in workspace",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]

		command := args[1:]
		if len(command) == 0 {
			fmt.Fprintf(os.Stderr, "Error: specify command after --\n")
			os.Exit(1)
		}

		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		output, err := client.Exec(name, command)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace exec", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "\nTip: Run 'nexus workspace list' to see available workspaces.\n")
				os.Exit(3)
			}
			os.Exit(1)
		}

		recordCommand("workspace exec", args, duration, true, nil)
		fmt.Print(output)
		return nil
	},
}

var workspaceInjectKeyCmd = &cobra.Command{
	Use:   "inject-key <name>",
	Short: "Inject SSH key into workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]

		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		output, err := client.InjectSSHKey(name)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace inject-key", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "\nTip: Run 'nexus workspace list' to see available workspaces.\n")
				os.Exit(3)
			}
			os.Exit(1)
		}

		recordCommand("workspace inject-key", args, duration, true, nil)
		fmt.Print(output)
		return nil
	},
}

var workspaceStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show detailed workspace status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.GetWorkspace(name)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace status", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "\nTip: Run 'nexus workspace list' to see available workspaces.\n")
				os.Exit(3)
			}
			os.Exit(1)
		}

		recordCommand("workspace status", args, duration, true, nil)

		if jsonOutput {
			printJSON(ws)
			return nil
		}

		fmt.Printf("Workspace: %s\n", ws.Name)
		fmt.Printf("ID: %s\n", ws.ID)
		fmt.Printf("Status: %s\n", colorStatus(ws.Status))
		fmt.Printf("Backend: %s\n", ws.Backend)
		if ws.WorktreePath != "" {
			fmt.Printf("Worktree: %s\n", ws.WorktreePath)
		}
		if ws.Branch != "" {
			fmt.Printf("Branch: %s\n", ws.Branch)
		}
		fmt.Printf("Created: %s\n", ws.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("Updated: %s\n", ws.UpdatedAt.Format("2006-01-02 15:04:05"))

		if len(ws.Ports) > 0 {
			fmt.Printf("\nPorts:\n")
			for _, port := range ws.Ports {
				url := ""
				if port.URL != "" {
					url = fmt.Sprintf(" -> %s", port.URL)
				}
				fmt.Printf("  %s: %d/%s%s\n", port.Name, port.HostPort, port.Protocol, url)
			}
		}

		if ws.Health != nil {
			fmt.Printf("\nHealth:\n")
			fmt.Printf("  Healthy: %v\n", ws.Health.Healthy)
			if len(ws.Health.Checks) > 0 {
				for _, check := range ws.Health.Checks {
					status := "OK"
					if !check.Healthy {
						status = "FAIL"
					}
					fmt.Printf("  - %s: %s", check.Name, status)
					if check.Error != "" {
						fmt.Printf(" (%s)", check.Error)
					}
					fmt.Println()
				}
			}
		}

		if ws.Labels != nil && len(ws.Labels) > 0 {
			fmt.Printf("\nLabels:\n")
			for k, v := range ws.Labels {
				fmt.Printf("  %s: %s\n", k, v)
			}
		}
		return nil
	},
}

var workspaceLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show workspace logs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		logs, err := client.GetLogs(name, 100)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace logs", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "\nTip: Run 'nexus workspace list' to see available workspaces.\n")
				os.Exit(3)
			}
			os.Exit(1)
		}

		recordCommand("workspace logs", args, duration, true, nil)
		fmt.Print(logs)
		return nil
	},
}

var workspaceUseCmd = &cobra.Command{
	Use:   "use [name]",
	Short: "Set active workspace for current session",
	Long: `Set the active workspace so subsequent commands run in that workspace context.
	  
Use 'nexus workspace use -' or 'nexus workspace use --clear' to deactivate and run on host.`,
	Args: cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		if clearFlag {
			err := clearActiveWorkspace()
			duration := time.Since(start)
			if err != nil {
				recordCommand("workspace use", args, duration, false, err)
				fmt.Fprintf(os.Stderr, "Error clearing active workspace: %v\n", err)
				os.Exit(1)
			}
			recordCommand("workspace use", args, duration, true, nil)
			fmt.Println("Cleared active workspace. Commands will run on host.")
			return nil
		}

		var name string
		if len(args) > 0 {
			name = args[0]
		}

		if name == "-" {
			err := clearActiveWorkspace()
			duration := time.Since(start)
			if err != nil {
				recordCommand("workspace use", args, duration, false, err)
				fmt.Fprintf(os.Stderr, "Error clearing active workspace: %v\n", err)
				os.Exit(1)
			}
			recordCommand("workspace use", args, duration, true, nil)
			fmt.Println("Cleared active workspace. Commands will run on host.")
			return nil
		}

		if name == "" {
			fmt.Fprintf(os.Stderr, "Error: specify a workspace name or use --clear\n")
			os.Exit(1)
		}

		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.GetWorkspace(name)
		duration := time.Since(start)

		if err != nil {
			recordCommand("workspace use", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "Workspace '%s' not found. Run 'nexus workspace list' to see available workspaces.\n", name)
				os.Exit(3)
			}
			os.Exit(1)
		}

		err = setActiveWorkspace(name)
		if err != nil {
			recordCommand("workspace use", args, duration, false, err)
			fmt.Fprintf(os.Stderr, "Error saving active workspace: %v\n", err)
			os.Exit(1)
		}

		recordCommand("workspace use", args, duration, true, nil)
		fmt.Printf("Switched to workspace '%s'. Subsequent commands will run in this workspace.\n", ws.Name)
		fmt.Printf("Workspaces commands will auto-intercept: docker, docker-compose, npm, ./scripts/*.sh, etc.\n")
		fmt.Printf("\nTo run on host: 'nexus workspace use --clear' or 'HOST: <command>'\n")
		return nil
	},
}

func init() {
	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceStartCmd)
	workspaceCmd.AddCommand(workspaceStopCmd)
	workspaceCmd.AddCommand(workspaceDeleteCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceSSHCmd)
	workspaceCmd.AddCommand(workspaceExecCmd)
	workspaceCmd.AddCommand(workspaceInjectKeyCmd)
	workspaceCmd.AddCommand(workspaceStatusCmd)
	workspaceCmd.AddCommand(workspaceLogsCmd)
	workspaceCmd.AddCommand(workspaceUseCmd)
	workspaceCmd.AddCommand(workspaceCheckpointCmd)
	workspaceCheckpointCmd.AddCommand(workspaceCheckpointCreateCmd)
	workspaceCheckpointCmd.AddCommand(workspaceCheckpointListCmd)
	workspaceCheckpointCmd.AddCommand(workspaceCheckpointRestoreCmd)
	workspaceCheckpointCmd.AddCommand(workspaceCheckpointDeleteCmd)

	// Template support removed - users provide Dockerfile directly
	workspaceCreateCmd.Flags().StringVar(&fromFlag, "from", "", "Import from existing project path")
	workspaceCreateCmd.Flags().StringVar(&backendFlag, "backend", "", "Backend to use (docker, daytona)")
	workspaceCreateCmd.Flags().IntVar(&cpuFlag, "cpu", 2, "CPU limit")
	workspaceCreateCmd.Flags().IntVar(&memoryFlag, "memory", 4, "Memory limit (GB)")
	workspaceCreateCmd.Flags().IntVar(&diskFlag, "disk", 20, "Disk space (GB)")

	workspaceStopCmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "Force stop")
	workspaceDeleteCmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "Force delete without confirmation")

	workspaceListCmd.Flags().BoolVar(&allFlag, "all", false, "Show all workspaces including stopped")
	workspaceListCmd.Flags().StringVar(&formatFlag, "format", "table", "Output format (table, json)")

	workspaceUseCmd.Flags().BoolVarP(&clearFlag, "clear", "c", false, "Clear active workspace")
}

func colorStatus(status string) string {
	switch status {
	case "running":
		return "\033[32m" + status + "\033[0m"
	case "stopped":
		return "\033[31m" + status + "\033[0m"
	case "creating":
		return "\033[33m" + status + "\033[0m"
	case "sleeping":
		return "\033[34m" + status + "\033[0m"
	case "paused":
		return "\033[35m" + status + "\033[0m"
	case "error":
		return "\033[31;1m" + status + "\033[0m"
	default:
		return status
	}
}

var (
	checkpointNameFlag string
)

var workspaceCheckpointCmd = &cobra.Command{
	Use:   "checkpoint",
	Short: "Manage workspace checkpoints",
}

var workspaceCheckpointCreateCmd = &cobra.Command{
	Use:   "create <workspace>",
	Short: "Create a checkpoint of a workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		workspaceName := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.GetWorkspace(workspaceName)
		if err != nil {
			recordCommand("workspace checkpoint create", args, time.Since(start), false, err)
			fmt.Fprintf(os.Stderr, "Error: workspace %q not found\n", workspaceName)
			os.Exit(3)
		}

		cp, err := client.CreateCheckpoint(ws.ID, checkpointNameFlag)

		if err != nil {
			recordCommand("workspace checkpoint create", args, time.Since(start), false, err)
			fmt.Fprintf(os.Stderr, "Error creating checkpoint: %v\n", err)
			os.Exit(1)
		}

		recordCommand("workspace checkpoint create", args, time.Since(start), true, nil)

		fmt.Printf("Created checkpoint %s for workspace %s\n", cp.ID, workspaceName)
		return nil
	},
}

var workspaceCheckpointListCmd = &cobra.Command{
	Use:   "list <workspace>",
	Short: "List checkpoints for a workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		workspaceName := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.GetWorkspace(workspaceName)
		if err != nil {
			recordCommand("workspace checkpoint list", args, time.Since(start), false, err)
			fmt.Fprintf(os.Stderr, "Error: workspace %q not found\n", workspaceName)
			os.Exit(3)
		}

		checkpoints, err := client.ListCheckpoints(ws.ID)

		if err != nil {
			recordCommand("workspace checkpoint list", args, time.Since(start), false, err)
			fmt.Fprintf(os.Stderr, "Error listing checkpoints: %v\n", err)
			os.Exit(1)
		}

		recordCommand("workspace checkpoint list", args, time.Since(start), true, nil)

		if len(checkpoints) == 0 {
			fmt.Printf("No checkpoints found for workspace %s\n", workspaceName)
			return nil
		}

		fmt.Printf("Checkpoints for workspace %s:\n", workspaceName)
		fmt.Println("────────────────────────────────────────────────")
		for _, cp := range checkpoints {
			fmt.Printf("  %s - %s (created: %s)\n", cp.ID, cp.Name, cp.CreatedAt.Format("2006-01-02 15:04"))
		}
		return nil
	},
}

var workspaceCheckpointRestoreCmd = &cobra.Command{
	Use:   "restore <workspace> <checkpoint-id>",
	Short: "Restore a workspace from a checkpoint",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		workspaceName := args[0]
		checkpointID := args[1]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.GetWorkspace(workspaceName)
		if err != nil {
			recordCommand("workspace checkpoint restore", args, time.Since(start), false, err)
			fmt.Fprintf(os.Stderr, "Error: workspace %q not found\n", workspaceName)
			os.Exit(3)
		}

		_, err = client.RestoreCheckpoint(ws.ID, checkpointID)

		if err != nil {
			recordCommand("workspace checkpoint restore", args, time.Since(start), false, err)
			fmt.Fprintf(os.Stderr, "Error restoring checkpoint: %v\n", err)
			os.Exit(1)
		}

		recordCommand("workspace checkpoint restore", args, time.Since(start), true, nil)

		fmt.Printf("Restored workspace %s from checkpoint %s\n", workspaceName, checkpointID)
		return nil
	},
}

var workspaceCheckpointDeleteCmd = &cobra.Command{
	Use:   "delete <workspace> <checkpoint-id>",
	Short: "Delete a checkpoint",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		initTelemetry()

		workspaceName := args[0]
		checkpointID := args[1]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.GetWorkspace(workspaceName)
		if err != nil {
			recordCommand("workspace checkpoint delete", args, time.Since(start), false, err)
			fmt.Fprintf(os.Stderr, "Error: workspace %q not found\n", workspaceName)
			os.Exit(3)
		}

		err = client.DeleteCheckpoint(ws.ID, checkpointID)

		if err != nil {
			recordCommand("workspace checkpoint delete", args, time.Since(start), false, err)
			fmt.Fprintf(os.Stderr, "Error deleting checkpoint: %v\n", err)
			os.Exit(1)
		}

		recordCommand("workspace checkpoint delete", args, time.Since(start), true, nil)

		fmt.Printf("Deleted checkpoint %s from workspace %s\n", checkpointID, workspaceName)
		return nil
	},
}

func printJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

func mergeLabels(labels map[string]string, key, value string) map[string]string {
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[key] = value
	return labels
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

var _ time.Duration
