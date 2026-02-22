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
	templateFlag    string
	fromFlag        string
	cpuFlag         int
	memoryFlag      int
	forceFlag       bool
	formatFlag      string
	allFlag         bool
	clearFlag       bool
)

const (
	sessionDirName     = ".nexus/session"
	activeWorkspaceFile = "active-workspace"
)

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
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		req := CreateWorkspaceRequest{
			Name: name,
		}

		if templateFlag != "" {
			req.Labels = map[string]string{"template": templateFlag}
		}
		if fromFlag != "" {
			req.WorktreePath = fromFlag
		}
		if cpuFlag > 0 {
			req.Labels = mergeLabels(req.Labels, "cpu", fmt.Sprintf("%d", cpuFlag))
		}
		if memoryFlag > 0 {
			req.Labels = mergeLabels(req.Labels, "memory", fmt.Sprintf("%d", memoryFlag))
		}

		ws, err := client.CreateWorkspace(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating workspace: %v\n", err)
			os.Exit(4)
		}

		if jsonOutput {
			printJSON(ws)
		} else {
			fmt.Printf("Created workspace %s (ID: %s)\n", ws.Name, ws.ID)
			fmt.Printf("Status: %s\n", ws.Status)
			if ws.WorktreePath != "" {
				fmt.Printf("Worktree: %s\n", ws.WorktreePath)
			}
		}
	},
}

var workspaceStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a stopped workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.StartWorkspace(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error starting workspace: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		if jsonOutput {
			printJSON(ws)
		} else {
			fmt.Printf("Started workspace %s\n", ws.Name)
			fmt.Printf("Status: %s\n", ws.Status)
		}
	},
}

var workspaceStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop a running workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		timeout := 30
		if forceFlag {
			timeout = 5
		}

		ws, err := client.StopWorkspace(name, timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error stopping workspace: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		if jsonOutput {
			printJSON(ws)
		} else {
			fmt.Printf("Stopped workspace %s\n", ws.Name)
			fmt.Printf("Status: %s\n", ws.Status)
		}
	},
}

var workspaceDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a workspace permanently",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
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
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting workspace: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		if jsonOutput {
			printJSON(map[string]string{"name": name, "status": "deleted"})
		} else {
			fmt.Printf("Deleted workspace %s\n", name)
		}
	},
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces",
	Aliases: []string{"ls"},
	Run: func(cmd *cobra.Command, args []string) {
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")
		result, err := client.ListWorkspaces()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(result.Workspaces) == 0 {
			fmt.Println("No workspaces found")
			return
		}

		if jsonOutput || formatFlag == "json" {
			printJSON(result)
			return
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "NAME\tSTATUS\tCPU\tMEM\tDISK\tCREATED\n")
		for _, ws := range result.Workspaces {
			created := ws.CreatedAt.Format("2006-01-02 15:04")
			if ws.CreatedAt.IsZero() {
				created = "-"
			}
			cpu := "-"
			mem := "-"
			if ws.Labels != nil {
				if c, ok := ws.Labels["cpu"]; ok {
					cpu = c + " CPU"
				}
				if m, ok := ws.Labels["memory"]; ok {
					mem = m + "GB"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				ws.Name,
				colorStatus(ws.Status),
				cpu,
				mem,
				"-",
				created,
			)
		}
		w.Flush()
		fmt.Fprintf(os.Stderr, "\nTotal: %d workspace(s)\n", result.Total)
	},
}

var workspaceSSHCmd = &cobra.Command{
	Use:   "ssh <name>",
	Short: "SSH into workspace interactively",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		err := client.Shell(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}
	},
}

var workspaceExecCmd = &cobra.Command{
	Use:   "exec <name> -- <command>",
	Short: "Execute command in workspace",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		command := args[1:]
		if len(command) == 0 {
			fmt.Fprintf(os.Stderr, "Error: specify command after --\n")
			os.Exit(1)
		}

		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		output, err := client.Exec(name, command)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		fmt.Print(output)
	},
}

var workspaceStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show detailed workspace status",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.GetWorkspace(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		if jsonOutput {
			printJSON(ws)
			return
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
	},
}

var workspaceLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show workspace logs",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		logs, err := client.GetLogs(name, 100)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				os.Exit(3)
			}
			os.Exit(1)
		}

		fmt.Print(logs)
	},
}

var workspaceUseCmd = &cobra.Command{
	Use:   "use [name]",
	Short: "Set active workspace for current session",
	Long: `Set the active workspace so subsequent commands run in that workspace context.
	  
Use 'nexus workspace use -' or 'nexus workspace use --clear' to deactivate and run on host.`,
	Args: cobra.RangeArgs(0, 1),
	Run: func(cmd *cobra.Command, args []string) {
		if clearFlag {
			err := clearActiveWorkspace()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error clearing active workspace: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Cleared active workspace. Commands will run on host.")
			return
		}

		var name string
		if len(args) > 0 {
			name = args[0]
		}

		if name == "-" {
			err := clearActiveWorkspace()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error clearing active workspace: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Cleared active workspace. Commands will run on host.")
			return
		}

		if name == "" {
			fmt.Fprintf(os.Stderr, "Error: specify a workspace name or use --clear\n")
			os.Exit(1)
		}

		cfg := getConfig()
		client := NewClient(fmt.Sprintf("http://%s:%d", cfg.Daemon.Host, cfg.Daemon.Port), "")

		ws, err := client.GetWorkspace(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if containsString(err.Error(), "not found") || containsString(err.Error(), "404") {
				fmt.Fprintf(os.Stderr, "Workspace '%s' not found. Run 'nexus workspace list' to see available workspaces.\n", name)
				os.Exit(3)
			}
			os.Exit(1)
		}

		err = setActiveWorkspace(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error saving active workspace: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Switched to workspace '%s'. Subsequent commands will run in this workspace.\n", ws.Name)
		fmt.Printf("Workspaces commands will auto-intercept: docker, docker-compose, npm, ./scripts/*.sh, etc.\n")
		fmt.Printf("\nTo run on host: 'nexus workspace use --clear' or 'HOST: <command>'\n")
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
	workspaceCmd.AddCommand(workspaceStatusCmd)
	workspaceCmd.AddCommand(workspaceLogsCmd)
	workspaceCmd.AddCommand(workspaceUseCmd)

	workspaceCreateCmd.Flags().StringVarP(&templateFlag, "template", "t", "", "Template (node, python, go, rust, blank)")
	workspaceCreateCmd.Flags().StringVar(&fromFlag, "from", "", "Import from existing project path")
	workspaceCreateCmd.Flags().IntVar(&cpuFlag, "cpu", 2, "CPU limit")
	workspaceCreateCmd.Flags().IntVar(&memoryFlag, "memory", 4, "Memory limit (GB)")

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
