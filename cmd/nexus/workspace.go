package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/coordination"
	"github.com/nexus/nexus/pkg/github"
	"github.com/nexus/nexus/pkg/paths"
	"github.com/spf13/cobra"
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage workspaces",
	Long:  `Create, connect to, and manage isolated development workspaces from GitHub repositories.`,
}

var workspaceCreateCmd = &cobra.Command{
	Use:   "create <repo>",
	Short: "Create a workspace from a GitHub repository",
	Long: `Create a new workspace from a GitHub repository.
You can specify a repo as owner/repo or a full GitHub URL.

Examples:
  nexus workspace create torvalds/linux
  nexus workspace create https://github.com/torvalds/linux`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runWorkspaceCreate(args[0])
	},
}

var workspaceConnectCmd = &cobra.Command{
	Use:   "connect <workspace-name>",
	Short: "Connect to a workspace",
	Long: `Connect to a workspace and show SSH connection info and deep links.
This displays everything needed to access your workspace.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runWorkspaceConnect(args[0])
	},
}

var workspaceShowCmd = &cobra.Command{
	Use:   "show <workspace-name>",
	Short: "Show workspace details",
	Long:  `Display detailed information about a workspace including status and services.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runWorkspaceShow(args[0])
	},
}

var workspaceExecCmd = &cobra.Command{
	Use:   "exec <workspace-name> <command>",
	Short: "Execute a command in a workspace",
	Long:  `Execute a command inside a workspace. Uses LXC container execution.`,
	Args:  cobra.MinimumNArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		workspaceName := args[0]
		command := args[1]
		return runWorkspaceExec(workspaceName, command)
	},
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceConnectCmd)
	workspaceCmd.AddCommand(workspaceShowCmd)
	workspaceCmd.AddCommand(workspaceExecCmd)
}

func runWorkspaceCreate(repoString string) error {
	fmt.Println("🚀 Creating workspace from GitHub repository")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	owner, repo, err := github.ParseRepoURL(repoString)
	if err != nil {
		return fmt.Errorf("invalid repository: %w", err)
	}

	fmt.Printf("Repository: %s/%s\n", owner, repo)

	fmt.Println("")
	fmt.Println("📥 Cloning repository...")

	cloneURL := github.BuildCloneURL(owner, repo)
	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("nexus-workspace-%d", time.Now().Unix()))

	var token string
	userConfigPath := config.GetUserConfigPath()

	if envToken := os.Getenv("GITHUB_TOKEN"); envToken != "" {
		token = envToken
	} else {
		tempUserCfg, cfgErr := config.LoadUserConfig(userConfigPath)
		if cfgErr == nil && tempUserCfg.GitHub.Username != "" {
			projectRoot := paths.GetProjectRoot()
			dbPath := paths.GetDatabasePath(projectRoot)

			db, dbErr := coordination.NewSQLiteRegistry(dbPath)
			if dbErr == nil {
				if installation, instErr := db.GetGitHubInstallation(tempUserCfg.GitHub.Username); instErr == nil {
					token = installation.Token
				}
			}
		}
	}

	if err := github.CloneRepository(cloneURL, tempDir, token); err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	fmt.Println("✅ Repository cloned")

	fmt.Println("")
	fmt.Println("📦 Loading workspace configuration...")

	configPath := filepath.Join(tempDir, ".nexus", "config.yaml")
	var wsConfig *config.Config
	if _, err := os.Stat(configPath); err == nil {
		wsConfig, _ = config.LoadConfig(configPath)
	}

	if wsConfig == nil {
		fmt.Println("⚠️  No .nexus/config.yaml found, using defaults")
		wsConfig = &config.Config{
			Name:     repo,
			Services: make(map[string]config.Service),
		}
	} else {
		fmt.Println("✅ Configuration loaded from repository")
	}

	fmt.Println("")
	fmt.Println("💾 Saving workspace info...")

	userConfigDir := filepath.Dir(userConfigPath)

	if err := config.EnsureConfigDirectory(userConfigDir); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	var userCfg *config.UserConfig
	if _, err := os.Stat(userConfigPath); err == nil {
		userCfg, _ = config.LoadUserConfig(userConfigPath)
		if userCfg == nil {
			userCfg = &config.UserConfig{}
		}
	} else {
		userCfg = &config.UserConfig{}
	}

	workspaceName := fmt.Sprintf("%s-%d", repo, time.Now().Unix())
	if err := config.AddWorkspaceToConfig(userConfigPath, workspaceName, "", "pending"); err != nil {
		return fmt.Errorf("failed to save workspace info: %w", err)
	}

	fmt.Println("✅ Workspace saved")

	fmt.Println("")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✅ Workspace creation initiated!")
	fmt.Println("")
	fmt.Println("📋 Workspace info:")
	fmt.Printf("   Name: %s\n", workspaceName)
	fmt.Printf("   Repository: %s/%s\n", owner, repo)
	fmt.Printf("   Status: pending\n")
	fmt.Println("")
	fmt.Println("📝 Next steps:")
	fmt.Println("  1. Wait for coordination server to allocate resources")
	fmt.Println("  2. Connect to your workspace:")
	fmt.Printf("     nexus workspace connect %s\n", workspaceName)
	fmt.Println("")

	return nil
}

func runWorkspaceConnect(workspaceName string) error {
	fmt.Println("🔗 Workspace Connection Info")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	userConfigPath := config.GetUserConfigPath()
	userCfg, err := config.LoadUserConfig(userConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load user config: %w", err)
	}

	var workspace *struct {
		Name   string `yaml:"name"`
		ID     string `yaml:"id"`
		Status string `yaml:"status"`
	}

	for i := range userCfg.Workspaces {
		if userCfg.Workspaces[i].Name == workspaceName {
			workspace = &userCfg.Workspaces[i]
			break
		}
	}

	if workspace == nil {
		return fmt.Errorf("workspace '%s' not found", workspaceName)
	}

	fmt.Printf("Workspace: %s\n", workspace.Name)
	fmt.Printf("Status: %s\n", workspace.Status)

	if workspace.ID != "" {
		fmt.Printf("ID: %s\n", workspace.ID)
	}

	fmt.Println("")
	fmt.Println("🐚 SSH Access:")
	fmt.Println("  ssh -p <port> dev@localhost")
	fmt.Println("")
	fmt.Println("💻 Deep Links:")
	fmt.Println("  VSCode:  vscode://vscode-remote/ssh-remote+localhost:<port>/workspace")
	fmt.Println("  Cursor:  cursor://ssh/remote?host=localhost&port=<port>&user=dev")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

func runWorkspaceShow(workspaceName string) error {
	ctx := context.Background()

	fmt.Printf("📦 Workspace: %s\n", workspaceName)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	userConfigPath := config.GetUserConfigPath()
	userCfg, err := config.LoadUserConfig(userConfigPath)
	if err != nil {
		fmt.Println("No saved configuration")
		return nil
	}

	for _, ws := range userCfg.Workspaces {
		if ws.Name == workspaceName {
			fmt.Printf("Name: %s\n", ws.Name)
			fmt.Printf("Status: %s\n", ws.Status)
			if ws.ID != "" {
				fmt.Printf("ID: %s\n", ws.ID)
			}
			break
		}
	}

	fmt.Println("")
	fmt.Println("💼 Available commands:")
	fmt.Printf("  nexus workspace connect %s\n", workspaceName)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	_ = ctx
	return nil
}

func runWorkspaceExec(workspaceName string, command string) error {
	lxcContainerName := fmt.Sprintf("nexus-epson-eshop-%s", workspaceName)

	lxcExecCmd := exec.Command("lxc", "exec", lxcContainerName, "--", "sh", "-c", command)
	lxcExecCmd.Stdout = os.Stdout
	lxcExecCmd.Stderr = os.Stderr
	lxcExecCmd.Stdin = os.Stdin

	if err := lxcExecCmd.Run(); err != nil {
		return fmt.Errorf("failed to execute command in workspace: %w", err)
	}

	return nil
}
