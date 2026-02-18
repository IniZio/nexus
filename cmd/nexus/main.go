package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"nexus/internal/docker"
	"nexus/internal/workspace"
	"nexus/pkg/coordination"
	"nexus/pkg/template"
)

var rootCmd = &cobra.Command{
	Use:   "nexus",
	Short: "Nexus workspace manager",
	Long:  `Docker-based workspace manager for AI-assisted development.`,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize nexus configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}

		configDir := filepath.Join(homeDir, ".nexus")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}

		sshDir := filepath.Join(homeDir, ".ssh")
		if err := os.MkdirAll(sshDir, 0700); err != nil {
			return fmt.Errorf("failed to create .ssh directory: %w", err)
		}

		keyPath := filepath.Join(sshDir, "id_ed25519_nexus")
		if _, err := os.Stat(keyPath); os.IsNotExist(err) {
			fmt.Println("Generating SSH key...")
			if err := generateSSHKey(keyPath); err != nil {
				return fmt.Errorf("failed to generate SSH key: %w", err)
			}
		}

		projectDir := ".nexus"
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			return fmt.Errorf("failed to create .nexus directory: %w", err)
		}

		configPath := filepath.Join(projectDir, "config.yaml")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			config := `name: nexus-project
docker:
  image: ubuntu:22.04
  dind: false
`
			if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}
		}

		templatesDir := filepath.Join(projectDir, "templates")
		os.MkdirAll(templatesDir, 0755)

		hooksDir := filepath.Join(projectDir, "hooks")
		os.MkdirAll(hooksDir, 0755)

		upHookPath := filepath.Join(hooksDir, "up.sh")
		if _, err := os.Stat(upHookPath); os.IsNotExist(err) {
			upHook := `#!/bin/bash
echo "Workspace started!"
`
			if err := os.WriteFile(upHookPath, []byte(upHook), 0755); err != nil {
				return fmt.Errorf("failed to write up hook: %w", err)
			}
		}

		fmt.Println("Nexus initialized successfully!")
		fmt.Println("")
		fmt.Println("Next steps:")
		fmt.Println("  nexus workspace create <name>  - Create workspace")
		fmt.Println("  nexus workspace up <name>      - Start workspace")
		fmt.Println("  nexus workspace shell <name>   - Enter workspace")
		return nil
	},
}

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage workspaces",
}

var workspaceUpCmd = &cobra.Command{
	Use:   "up [name]",
	Short: "Start a workspace",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}

		mgr := workspace.NewManager(provider)
		return mgr.Up(name)
	},
}

var workspaceDownCmd = &cobra.Command{
	Use:   "down [name]",
	Short: "Stop a workspace",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}

		mgr := workspace.NewManager(provider)
		return mgr.Down(name)
	},
}

var workspaceShellCmd = &cobra.Command{
	Use:   "shell [name]",
	Short: "Open shell in workspace",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}

		mgr := workspace.NewManager(provider)
		return mgr.Shell(name)
	},
}

var workspaceExecCmd = &cobra.Command{
	Use:   "exec [name] -- [command]",
	Short: "Execute command in workspace",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		command := args[2:]

		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}

		mgr := workspace.NewManager(provider)
		return mgr.Exec(name, command)
	},
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces",
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}

		mgr := workspace.NewManager(provider)
		return mgr.List()
	},
}

var workspaceDestroyCmd = &cobra.Command{
	Use:   "destroy [name]",
	Short: "Destroy a workspace",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}

		mgr := workspace.NewManager(provider)
		return mgr.Destroy(name)
	},
}

var workspaceSyncCmd = &cobra.Command{
	Use:   "sync [name]",
	Short: "Sync workspace changes with main branch",
	Long: `Sync workspace changes with the main branch.

Examples:
  nexus workspace sync           - Sync current workspace
  nexus workspace sync myworkspace - Sync specific workspace`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}

		mgr := workspace.NewManager(provider)
		return mgr.Sync(name)
	},
}

var workspacePortsCmd = &cobra.Command{
	Use:   "ports [name]",
	Short: "List port mappings for a workspace",
	Long: `List all port mappings for a workspace.

Examples:
  nexus workspace ports           - List ports for auto-detected workspace
  nexus workspace ports myworkspace - List ports for specific workspace`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		if name == "" {
			name = detectWorkspaceName()
			if name == "" {
				return fmt.Errorf("no workspace specified and could not auto-detect")
			}
			fmt.Printf("Auto-detected workspace: %s\n", name)
		}

		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}
		defer provider.Close()

		ctx := context.Background()
		mappings, err := provider.GetPortMappings(ctx, name)
		if err != nil {
			return fmt.Errorf("failed to get port mappings: %w", err)
		}

		if len(mappings) == 0 {
			fmt.Printf("No port mappings found for workspace '%s'\n", name)
			return nil
		}

		fmt.Printf("Port mappings for workspace '%s':\n", name)
		fmt.Println("────────────────────────────────────────────────")
		fmt.Printf("%-15s %-15s %-10s %-10s\n", "SERVICE", "CONTAINER", "HOST", "PROTOCOL")
		fmt.Println("────────────────────────────────────────────────")
		for _, m := range mappings {
			fmt.Printf("%-15s %-15d %-10d %-10s\n", m.ServiceName, m.ContainerPort, m.HostPort, m.Protocol)
		}
		fmt.Println("────────────────────────────────────────────────")

		return nil
	},
}

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Manage tasks",
	Long:  `Manage tasks in the current workspace using pulse toolkit.`,
}

var taskCreateCmd = &cobra.Command{
	Use:   "create <title>",
	Short: "Create a new task",
	Long: `Create a new task in the current workspace.

Examples:
  nexus task create "Implement feature X"
  nexus task create "Fix bug Y" -p 5
  nexus task create "Update docs" -d "Update API docs" --depends task-123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		req := coordination.CreateTaskRequest{
			Title:       args[0],
			Description: taskDescription,
			Priority:    taskPriority,
			DependsOn:   taskDependsOn,
		}

		task, err := mgr.CreateTask(context.Background(), req)
		if err != nil {
			return fmt.Errorf("failed to create task: %w", err)
		}

		fmt.Printf("Created task: %s\n", task.ID)
		fmt.Printf("  Title: %s\n", task.Title)
		fmt.Printf("  Status: %s\n", task.Status)
		return nil
	},
}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all tasks",
	Long: `List all tasks in the current workspace.

Examples:
  nexus task list
  nexus task list --status pending
  nexus task list --status in_progress`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		var status coordination.TaskStatus
		if taskStatus != "" {
			status = coordination.TaskStatus(taskStatus)
		}

		tasks, err := mgr.ListTasks(context.Background(), status)
		if err != nil {
			return fmt.Errorf("failed to list tasks: %w", err)
		}

		if len(tasks) == 0 {
			fmt.Println("No tasks found")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTITLE\tSTATUS\tASSIGNEE\tPRIORITY\tCREATED")
		fmt.Fprintln(w, "----\t-----\t------\t--------\t--------\t--------")

		for _, task := range tasks {
			assignee := task.Assignee
			if assignee == "" {
				assignee = "-"
			}
			priority := fmt.Sprintf("%d", task.Priority)
			if task.Priority == 0 {
				priority = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				task.ID, truncate(task.Title, 25),
				task.Status, assignee, priority,
				task.CreatedAt.Format("2006-01-02 15:04"),
			)
		}
		w.Flush()

		return nil
	},
}

var taskAssignCmd = &cobra.Command{
	Use:   "assign <task-id> <agent>",
	Short: "Assign task to agent",
	Long: `Assign a task to an agent.

Examples:
  nexus task assign task-123 agent-1`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		taskID := args[0]
		agentID := args[1]

		task, err := mgr.AssignTask(context.Background(), taskID, agentID)
		if err != nil {
			return fmt.Errorf("failed to assign task: %w", err)
		}

		fmt.Printf("Assigned task: %s\n", task.ID)
		fmt.Printf("  Title: %s\n", task.Title)
		fmt.Printf("  Assignee: %s\n", task.Assignee)
		return nil
	},
}

var taskCompleteCmd = &cobra.Command{
	Use:   "complete <task-id>",
	Short: "Mark task as completed",
	Long: `Mark a task as completed.

Examples:
  nexus task complete task-123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		taskID := args[0]

		task, err := mgr.CompleteTask(context.Background(), taskID)
		if err != nil {
			return fmt.Errorf("failed to complete task: %w", err)
		}

		fmt.Printf("Completed task: %s\n", task.ID)
		fmt.Printf("  Title: %s\n", task.Title)
		return nil
	},
}

var taskStartCmd = &cobra.Command{
	Use:   "start <task-id>",
	Short: "Start working on task",
	Long: `Mark a task as in progress.

Examples:
  nexus task start task-123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		taskID := args[0]

		task, err := mgr.StartTask(context.Background(), taskID)
		if err != nil {
			return fmt.Errorf("failed to start task: %w", err)
		}

		fmt.Printf("Started task: %s\n", task.ID)
		fmt.Printf("  Title: %s\n", task.Title)
		fmt.Printf("  Status: %s\n", task.Status)
		return nil
	},
}

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agents",
	Long:  `Manage agents in the current workspace.`,
}

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active agents",
	Long: `List all active agents in the current workspace.

Examples:
  nexus agent list`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		agents, err := mgr.ListAgents(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list agents: %w", err)
		}

		if len(agents) == 0 {
			fmt.Println("No active agents")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCURRENT TASK\tCAPABILITIES\tLAST SEEN")
		fmt.Fprintln(w, "----\t-----\t------\t------------\t------------\t---------")

		for _, agent := range agents {
			task := agent.CurrentTask
			if task == "" {
				task = "-"
			}
			caps := agent.Capabilities
			if len(caps) == 0 {
				caps = []string{"-"}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				agent.ID, agent.Name, agent.Status, task,
				truncateSlice(caps, 15),
				agent.LastSeenAt.Format("2006-01-02 15:04"),
			)
		}
		w.Flush()

		return nil
	},
}

var agentRegisterCmd = &cobra.Command{
	Use:   "register <name>",
	Short: "Register a new agent",
	Long: `Register a new agent in the current workspace.

Examples:
  nexus agent register executor
  nexus agent register coder --capabilities go,python`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getTaskManager()
		if err != nil {
			return fmt.Errorf("failed to get task manager: %w", err)
		}
		defer mgr.Close()

		agent, err := mgr.RegisterAgent(context.Background(), args[0], agentCapabilities)
		if err != nil {
			return fmt.Errorf("failed to register agent: %w", err)
		}

		fmt.Printf("Registered agent: %s\n", agent.ID)
		fmt.Printf("  Name: %s\n", agent.Name)
		fmt.Printf("  Status: %s\n", agent.Status)
		return nil
	},
}

var (
	taskDescription   string
	taskPriority      int
	taskStatus        string
	taskDependsOn     []string
	agentCapabilities []string
	templateName      string
	templateVars      []string
)

func generateSSHKey(keyPath string) error {
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-C", "nexus@localhost")
	return cmd.Run()
}

func getTaskManager() (*coordination.TaskManager, error) {
	workspaceDir := getWorkspaceDir()
	return coordination.NewTaskManager(workspaceDir)
}

func getWorkspaceDir() string {
	dir := os.Getenv("NEXUS_WORKSPACE_DIR")
	if dir == "" {
		dir = "."
	}
	return dir
}

func detectWorkspaceName() string {
	if data, err := os.ReadFile(".nexus/current"); err == nil {
		return string(data)
	}
	if dir, err := os.Getwd(); err == nil {
		return filepath.Base(dir)
	}
	return ""
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func truncateSlice(s []string, maxLen int) string {
	if len(s) == 0 {
		return "-"
	}
	result := ""
	for i, str := range s {
		if i > 0 {
			result += ","
		}
		result += str
	}
	if len(result) <= maxLen {
		return result
	}
	return result[:maxLen-3] + "..."
}

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage templates",
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available templates",
	Long: `List all available templates for workspace creation.

Examples:
  nexus template list`,
	RunE: func(cmd *cobra.Command, args []string) error {
		engine := template.NewEngine()
		templates := engine.ListTemplates()

		fmt.Println("📦 Available Templates:")
		fmt.Println("────────────────────────────────────────────────")
		for _, t := range templates {
			fmt.Printf("  %-20s %s\n", t.Name, t.Description)
			if len(t.Files) > 0 {
				fmt.Printf("    Files: %s\n", strings.Join(t.Files, ", "))
			}
		}
		fmt.Println("────────────────────────────────────────────────")
		return nil
	},
}

var workspaceCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new workspace",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		if templateName != "" {
			vars := make(map[string]string)
			for _, v := range templateVars {
				parts := strings.SplitN(v, "=", 2)
				if len(parts) == 2 {
					vars[parts[0]] = parts[1]
				}
			}

			provider, err := docker.NewProvider()
			if err != nil {
				return fmt.Errorf("failed to create docker provider: %w", err)
			}

			mgr := workspace.NewManager(provider)
			return mgr.CreateWithTemplate(name, templateName, vars)
		}

		provider, err := docker.NewProvider()
		if err != nil {
			return fmt.Errorf("failed to create docker provider: %w", err)
		}

		mgr := workspace.NewManager(provider)
		return mgr.Create(name)
	},
}

func main() {
	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceUpCmd)
	workspaceCmd.AddCommand(workspaceDownCmd)
	workspaceCmd.AddCommand(workspaceShellCmd)
	workspaceCmd.AddCommand(workspaceExecCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceDestroyCmd)
	workspaceCmd.AddCommand(workspaceSyncCmd)
	workspaceCmd.AddCommand(workspacePortsCmd)

	taskCmd.AddCommand(taskCreateCmd)
	taskCmd.AddCommand(taskListCmd)
	taskCmd.AddCommand(taskAssignCmd)
	taskCmd.AddCommand(taskCompleteCmd)
	taskCmd.AddCommand(taskStartCmd)

	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentRegisterCmd)

	taskCreateCmd.Flags().StringVarP(&taskDescription, "description", "d", "", "Task description")
	taskCreateCmd.Flags().IntVarP(&taskPriority, "priority", "p", 0, "Task priority (0-10)")
	taskCreateCmd.Flags().StringSliceVar(&taskDependsOn, "depends", []string{}, "Task dependencies")

	taskListCmd.Flags().StringVarP(&taskStatus, "status", "s", "", "Filter by status (pending, assigned, in_progress, completed)")

	agentRegisterCmd.Flags().StringSliceVarP(&agentCapabilities, "capabilities", "c", []string{}, "Agent capabilities")

	workspaceCreateCmd.Flags().StringVarP(&templateName, "template", "t", "", "Template to use (node-postgres, python-postgres, go-postgres)")
	workspaceCreateCmd.Flags().StringSliceVar(&templateVars, "var", []string{}, "Template variables (key=value)")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(taskCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(templateCmd)
	rootCmd.AddCommand(docCmd)

	templateCmd.AddCommand(templateListCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
