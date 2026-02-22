package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/nexus/nexus/packages/workspace-daemon/internal/git"
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage workspaces",
	Aliases: []string{"ws"},
}

var workspaceCreateCmd = &cobra.Command{
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

var workspaceListCmd = &cobra.Command{
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

var workspaceStatusCmd = &cobra.Command{
	Use:   "status <id>",
	Short: "Get workspace status",
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

var workspaceStartCmd = &cobra.Command{
	Use:   "start <id>",
	Short: "Start a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		ws, err := client.StartWorkspace(args[0])
		exitOnError(err)

		fmt.Printf("Workspace started: %s\n", ws.Status)
	},
}

var workspaceStopCmd = &cobra.Command{
	Use:   "stop <id>",
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

var workspaceDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
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

var workspaceExecCmd = &cobra.Command{
	Use:   "exec <id> <command...>",
	Short: "Execute a command in a workspace",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]
		command := args[1:]

		client := getClient()
		output, err := client.Exec(id, command)
		exitOnError(err)

		fmt.Print(output)
	},
}

var workspaceLogsCmd = &cobra.Command{
	Use:   "logs <id>",
	Short: "Get workspace logs",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		tail, _ := cmd.Flags().GetInt("tail")

		client := getClient()
		logs, err := client.GetLogs(args[0], tail)
		exitOnError(err)

		fmt.Print(logs)
	},
}

var workspaceUseCmd = &cobra.Command{
	Use:   "use <id>",
	Short: "Set active workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		ws, err := client.GetWorkspace(args[0])
		exitOnError(err)

		fmt.Printf("Active workspace set to: %s (%s)\n", ws.Name, ws.ID)
	},
}

func init() {
	workspaceCreateCmd.Flags().StringP("display-name", "d", "", "Display name")
	workspaceCreateCmd.Flags().StringP("repo", "r", "", "Repository URL")
	workspaceCreateCmd.Flags().StringP("branch", "b", "", "Branch name")
	workspaceCreateCmd.Flags().StringP("from-branch", "", "", "Base branch to create worktree from (default: main)")
	workspaceCreateCmd.Flags().StringP("backend", "", "docker", "Backend (docker, sprite, kubernetes)")
	workspaceCreateCmd.Flags().BoolP("no-worktree", "", false, "Skip git worktree creation")

	workspaceStopCmd.Flags().Int("timeout", 30, "Timeout in seconds")

	workspaceDeleteCmd.Flags().BoolP("force", "f", false, "Force delete without confirmation")

	workspaceLogsCmd.Flags().Int("tail", 100, "Number of lines to show")
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
	case "error":
		return "\033[31;1m" + status + "\033[0m"
	default:
		return status
	}
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manage file sync",
}

var syncStatusCmd = &cobra.Command{
	Use:   "status <id>",
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
	Use:   "pause <id>",
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
	Use:   "resume <id>",
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
	Use:   "flush <id>",
	Short: "Flush sync for a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := getClient()
		err := client.FlushSync(args[0])
		exitOnError(err)
		fmt.Printf("Sync flushed for workspace %s\n", args[0])
	},
}
