package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/git"
)

var createCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new workspace",
	Args:  cobra.RangeArgs(0, 1),
	Run: func(cmd *cobra.Command, args []string) {
		name := fmt.Sprintf("workspace-%d", os.Getpid())
		if len(args) > 0 {
			name = args[0]
		}

		displayName, _ := cmd.Flags().GetString("display-name")
		repoURL, _ := cmd.Flags().GetString("repo")
		branch, _ := cmd.Flags().GetString("branch")
		fromBranch, _ := cmd.Flags().GetString("from-branch")
		backend, _ := cmd.Flags().GetString("backend")
		noWorktree, _ := cmd.Flags().GetBool("no-worktree")
		dind, _ := cmd.Flags().GetBool("dind")
		forwardSSH := os.Getenv("SSH_AUTH_SOCK") != ""

		var worktreePath string

		if !noWorktree {
			projectRoot, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Could not determine project root: %v\n", err)
			} else {
				worktreeManager := git.NewWorktreeManager(projectRoot)
				worktreePath, err = worktreeManager.CreateWorktree(name, fromBranch)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error creating worktree: %v\n", err)
					fmt.Println("Creating workspace without worktree...")
					worktreePath = ""
				} else {
					fmt.Printf("Created worktree: %s\n", worktreePath)
				}
			}
		}

		client := getClient()
		ws, err := client.CreateWorkspace(CreateWorkspaceRequest{
			Name:          name,
			DisplayName:   displayName,
			RepositoryURL: repoURL,
			Branch:        branch,
			Backend:       backend,
			ForwardSSH:    forwardSSH,
			WorktreePath:  worktreePath,
			DinD:          dind,
		})
		exitOnError(err)

		fmt.Printf("Workspace created: %s (%s)\n", ws.Name, ws.ID)
		fmt.Printf("Status: %s\n", ws.Status)

		if worktreePath != "" {
			fmt.Printf("Worktree: %s\n", worktreePath)
		}

		if forwardSSH {
			err = client.ForwardSSHAgent(ws.ID)
			exitOnError(err)
		}
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces",
	Aliases: []string{"ls"},
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		result, err := client.ListWorkspaces()
		exitOnError(err)

		if len(result.Workspaces) == 0 {
			fmt.Println("No workspaces found")
			return
		}

		fmt.Printf("Workspaces (%d):\n\n", result.Total)
		for _, ws := range result.Workspaces {
			fmt.Printf("  %s  %s  %s\n", ws.ID, ws.Name, colorStatus(ws.Status))
		}
	},
}

var statusCmd = &cobra.Command{
	Use:   "status <workspace>",
	Short: "Show workspace status",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		ws, err := client.GetWorkspace(args[0])
		exitOnError(err)

		fmt.Printf("Workspace: %s\n", ws.Name)
		fmt.Printf("ID: %s\n", ws.ID)
		fmt.Printf("Status: %s\n", colorStatus(ws.Status))
		fmt.Printf("Backend: %s\n", ws.Backend)
		if ws.Repository != nil {
			fmt.Printf("Repository: %s\n", ws.Repository.URL)
		}
		if ws.Branch != "" {
			fmt.Printf("Branch: %s\n", ws.Branch)
		}
		if ws.WorktreePath != "" {
			fmt.Printf("Worktree: %s\n", ws.WorktreePath)
		}
		fmt.Printf("Created: %s\n", ws.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("Updated: %s\n", ws.UpdatedAt.Format("2006-01-02 15:04:05"))
	},
}

var startCmd = &cobra.Command{
	Use:   "start <workspace>",
	Short: "Start a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		ws, err := client.StartWorkspace(args[0])
		exitOnError(err)

		fmt.Printf("Workspace started: %s\n", ws.Status)
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop <workspace>",
	Short: "Stop a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		timeout, _ := cmd.Flags().GetInt("timeout")

		client := getClient()
		ws, err := client.StopWorkspace(args[0], timeout)
		exitOnError(err)

		fmt.Printf("Workspace stopped: %s\n", ws.Status)
	},
}

var pauseCmd = &cobra.Command{
	Use:   "pause <workspace>",
	Short: "Pause a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		err := client.PauseWorkspace(args[0])
		exitOnError(err)

		fmt.Printf("Workspace paused: %s\n", args[0])
	},
}

var resumeCmd = &cobra.Command{
	Use:   "resume <workspace>",
	Short: "Resume a paused workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		ws, err := client.ResumeWorkspace(args[0])
		exitOnError(err)

		fmt.Printf("Workspace resumed: %s (%s)\n", ws.Name, ws.Status)
	},
}

var destroyCmd = &cobra.Command{
	Use:   "destroy <workspace>",
	Short: "Delete a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			fmt.Printf("Are you sure you want to delete workspace %s? (y/N): ", args[0])
			var input string
			fmt.Scanln(&input)
			if strings.ToLower(input) != "y" {
				fmt.Println("Cancelled")
				return
			}
		}

		client := getClient()
		err := client.DeleteWorkspace(args[0])
		exitOnError(err)

		fmt.Println("Workspace deleted")
	},
}

var execCmd = &cobra.Command{
	Use:   "exec <workspace> -- <command>",
	Short: "Execute a command in a workspace",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]
		var command []string

		if len(args) > 2 && args[1] == "--" {
			command = args[2:]
		} else {
			command = args[1:]
		}

		client := getClient()
		output, err := client.Exec(workspace, command)
		exitOnError(err)

		fmt.Print(output)
	},
}

var consoleCmd = &cobra.Command{
	Use:   "console <workspace>",
	Short: "Interactive shell (SSH)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]

		client := getClient()
		err := client.Shell(workspace)
		exitOnError(err)
	},
}

var urlCmd = &cobra.Command{
	Use:   "url <workspace>",
	Short: "Get workspace URL",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		ws, err := client.GetWorkspace(args[0])
		exitOnError(err)

		if len(ws.Ports) > 0 {
			for _, port := range ws.Ports {
				if port.URL != "" {
					fmt.Printf("%s: %s\n", port.Name, port.URL)
				}
			}
		} else {
			fmt.Printf("No services running for workspace %s\n", args[0])
		}
	},
}

var useCmd = &cobra.Command{
	Use:   "use <workspace>",
	Short: "Set active workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		ws, err := client.GetWorkspace(args[0])
		exitOnError(err)

		fmt.Printf("Active workspace set to: %s (%s)\n", ws.Name, ws.ID)
	},
}

var proxyCmd = &cobra.Command{
	Use:   "proxy <workspace> <port>",
	Short: "Port forwarding",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]
		port := args[1]

		client := getClient()
		err := client.ForwardPort(workspace, port)
		exitOnError(err)

		fmt.Printf("Forwarding port %s for workspace %s\n", port, workspace)
		fmt.Println("Press Ctrl+C to stop")
		select {}
	},
}

var servicesCmd = &cobra.Command{
	Use:   "services <workspace>",
	Short: "List services",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		services, err := client.ListServices(args[0])
		exitOnError(err)

		if len(services) == 0 {
			fmt.Println("No services found")
			return
		}

		fmt.Printf("Services for workspace %s:\n\n", args[0])
		for _, svc := range services {
			fmt.Printf("  %s  %s  :%d -> %d\n", svc.Name, svc.Status, svc.ContainerPort, svc.HostPort)
		}
	},
}

var servicesListCmd = &cobra.Command{
	Use:   "list <workspace>",
	Short: "List services in workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		services, err := client.ListServices(args[0])
		exitOnError(err)

		if len(services) == 0 {
			fmt.Println("No services found")
			return
		}

		for _, svc := range services {
			fmt.Printf("  %s  %s  :%d -> %d\n", svc.Name, svc.Status, svc.ContainerPort, svc.HostPort)
		}
	},
}

var servicesLogsCmd = &cobra.Command{
	Use:   "logs <workspace> <service>",
	Short: "Get service logs",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]
		service := args[1]
		tail, _ := cmd.Flags().GetInt("tail")

		client := getClient()
		logs, err := client.GetServiceLogs(workspace, service, tail)
		exitOnError(err)

		fmt.Print(logs)
	},
}

var healthCmd = &cobra.Command{
	Use:   "health <workspace> [service]",
	Short: "Check workspace health",
	Args:  cobra.RangeArgs(1, 2),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]
		var service string
		if len(args) > 1 {
			service = args[1]
		}

		client := getClient()
		health, err := client.GetHealth(workspace, service)
		exitOnError(err)

		if health.Healthy {
			fmt.Printf("✓ Workspace %s is healthy\n", workspace)
		} else {
			fmt.Printf("✗ Workspace %s is unhealthy\n", workspace)
		}

		if len(health.Checks) > 0 {
			fmt.Println("\nHealth Checks:")
			for _, check := range health.Checks {
				status := "✓"
				if !check.Healthy {
					status = "✗"
				}
				fmt.Printf("  %s %s (%v)\n", status, check.Name, check.Latency)
				if check.Error != "" {
					fmt.Printf("    Error: %s\n", check.Error)
				}
			}
		}

		if !health.LastCheck.IsZero() {
			fmt.Printf("\nLast check: %s\n", health.LastCheck.Format("2006-01-02 15:04:05"))
		}
	},
}

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Session management",
}

var sessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active sessions",
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		sessions, err := client.ListSessions()
		exitOnError(err)

		if len(sessions) == 0 {
			fmt.Println("No active sessions")
			return
		}

		fmt.Println("Active sessions:")
		for _, sess := range sessions {
			fmt.Printf("  %s  %s  %s\n", sess.ID, sess.WorkspaceID, sess.Status)
		}
	},
}

var sessionsAttachCmd = &cobra.Command{
	Use:   "attach <id>",
	Short: "Attach to a session",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		err := client.AttachSession(args[0])
		exitOnError(err)
	},
}

var sessionsKillCmd = &cobra.Command{
	Use:   "kill <id>",
	Short: "Kill a session",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		err := client.KillSession(args[0])
		exitOnError(err)

		fmt.Printf("Session %s killed\n", args[0])
	},
}

var checkpointCmd = &cobra.Command{
	Use:   "checkpoint",
	Short: "Snapshot management",
}

var checkpointCreateCmd = &cobra.Command{
	Use:   "create <workspace>",
	Short: "Create a checkpoint",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			name = fmt.Sprintf("checkpoint-%d", os.Getpid())
		}

		client := getClient()
		cp, err := client.CreateCheckpoint(args[0], name)
		exitOnError(err)

		fmt.Printf("Checkpoint created: %s\n", cp.ID)
	},
}

var checkpointListCmd = &cobra.Command{
	Use:   "list <workspace>",
	Short: "List checkpoints",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		checkpoints, err := client.ListCheckpoints(args[0])
		exitOnError(err)

		if len(checkpoints) == 0 {
			fmt.Println("No checkpoints found")
			return
		}

		fmt.Printf("Checkpoints for workspace %s:\n\n", args[0])
		for _, cp := range checkpoints {
			fmt.Printf("  %s  %s  %s\n", cp.ID, cp.Name, cp.CreatedAt.Format("2006-01-02 15:04"))
		}
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore <workspace> <checkpoint-id>",
	Short: "Restore from checkpoint",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		workspace := args[0]
		checkpointID := args[1]

		client := getClient()
		ws, err := client.RestoreCheckpoint(workspace, checkpointID)
		exitOnError(err)

		fmt.Printf("Workspace restored: %s (%s)\n", ws.Name, ws.Status)
	},
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manage file sync",
}

var syncStatusCmd = &cobra.Command{
	Use:   "status <workspace>",
	Short: "Show sync status for a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		status, err := client.GetSyncStatus(args[0])
		exitOnError(err)

		fmt.Printf("Sync Status for workspace %s:\n", args[0])
		fmt.Printf("  State: %s\n", status.State)
		if !status.LastSync.IsZero() {
			fmt.Printf("  Last Sync: %s\n", status.LastSync.Format("2006-01-02 15:04:05"))
		}
		if len(status.Conflicts) > 0 {
			fmt.Printf("  Conflicts (%d):\n", len(status.Conflicts))
			for _, c := range status.Conflicts {
				fmt.Printf("    - %s\n", c.Path)
			}
		}
	},
}

var syncPauseCmd = &cobra.Command{
	Use:   "pause <workspace>",
	Short: "Pause sync for a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		err := client.PauseSync(args[0])
		exitOnError(err)
		fmt.Printf("Sync paused for workspace %s\n", args[0])
	},
}

var syncResumeCmd = &cobra.Command{
	Use:   "resume <workspace>",
	Short: "Resume sync for a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		err := client.ResumeSync(args[0])
		exitOnError(err)
		fmt.Printf("Sync resumed for workspace %s\n", args[0])
	},
}

var syncFlushCmd = &cobra.Command{
	Use:   "flush <workspace>",
	Short: "Flush sync for a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		err := client.FlushSync(args[0])
		exitOnError(err)
		fmt.Printf("Sync flushed for workspace %s\n", args[0])
	},
}

func init() {
	createCmd.Flags().StringP("display-name", "d", "", "Display name")
	createCmd.Flags().StringP("repo", "r", "", "Repository URL")
	createCmd.Flags().StringP("branch", "b", "", "Branch name")
	createCmd.Flags().StringP("from-branch", "", "", "Base branch to create worktree from (default: main)")
	createCmd.Flags().StringP("backend", "", "docker", "Backend (docker, sprite, kubernetes)")
	createCmd.Flags().BoolP("no-worktree", "", false, "Skip git worktree creation")
	createCmd.Flags().Bool("dind", false, "Enable Docker-in-Docker support")

	stopCmd.Flags().Int("timeout", 30, "Timeout in seconds")

	destroyCmd.Flags().BoolP("force", "f", false, "Force delete without confirmation")

	servicesLogsCmd.Flags().Int("tail", 100, "Number of lines to show")

	checkpointCreateCmd.Flags().StringP("name", "n", "", "Checkpoint name")
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
